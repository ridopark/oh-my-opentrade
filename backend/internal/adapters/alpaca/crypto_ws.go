package alpaca

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	alpacastream "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

const cryptoStaleThreshold = 30 * time.Minute

// cryptoConnectFn is the signature for the underlying crypto WebSocket connect call.
// Injected at construction time so tests can replace it with a fake.
type cryptoConnectFn func(ctx context.Context) error

// CryptoWSClient handles WebSocket connections for Alpaca crypto market data.
// It uses a separate stream.CryptoClient (not the StocksClient used for equities)
// and includes reconnect, watchdog, and circuit breaker hardening.
type CryptoWSClient struct {
	cryptoDataURL        string
	apiKey               string
	apiSecret            string
	feed                 string     // e.g. "us"
	fetcher              BarFetcher // REST polling fallback; nil disables polling
	tradeHandler         ports.TradeHandler
	pipelineHealth       ports.PipelineHealthReporter
	onDegraded           func(reason string)
	onCircuitBreakerOpen func(consecutiveFails int, blockedFor time.Duration)
	log                  zerolog.Logger
	tracker              *feedTracker
	closeOnce            sync.Once
	cancel               context.CancelFunc
	mu                   sync.Mutex

	connectFactory func(symStrs []string, barHandler func(alpacastream.CryptoBar), tradeHandler func(alpacastream.CryptoTrade)) cryptoConnectFn
}

// NewCryptoWSClient creates a new CryptoWSClient.
// cryptoDataURL is the base WebSocket URL (e.g. "wss://stream.data.alpaca.markets").
// feed is the crypto feed name (e.g. "us").
func NewCryptoWSClient(cryptoDataURL, apiKey, apiSecret, feed string, fetcher BarFetcher, log zerolog.Logger) (*CryptoWSClient, error) {
	if apiKey == "" {
		return nil, ErrCryptoWSMissingCredentials
	}
	if apiSecret == "" {
		return nil, ErrCryptoWSMissingCredentials
	}
	if feed == "" {
		feed = "us"
	}
	if cryptoDataURL == "" {
		cryptoDataURL = "wss://stream.data.alpaca.markets"
	}
	c := &CryptoWSClient{
		cryptoDataURL: cryptoDataURL,
		apiKey:        apiKey,
		apiSecret:     apiSecret,
		feed:          feed,
		fetcher:       fetcher,
		log:           log.With().Str("component", "crypto_ws").Logger(),
		tracker:       newFeedTracker(),
	}
	c.connectFactory = c.defaultConnectFactory
	return c, nil
}

func (c *CryptoWSClient) SetTradeHandler(h ports.TradeHandler) { c.tradeHandler = h }

// SetPipelineHealth injects pipeline liveness reporter for dual-track watchdog.
func (c *CryptoWSClient) SetPipelineHealth(ph ports.PipelineHealthReporter) { c.pipelineHealth = ph }

func (c *CryptoWSClient) SetDegradedCallback(fn func(reason string)) { c.onDegraded = fn }

func (c *CryptoWSClient) SetCircuitBreakerCallback(fn func(consecutiveFails int, blockedFor time.Duration)) {
	c.onCircuitBreakerOpen = fn
}

// FeedHealth returns a point-in-time snapshot of crypto WebSocket feed status.
func (c *CryptoWSClient) FeedHealth() FeedHealth {
	fh := c.tracker.Snapshot()
	if c.pipelineHealth != nil {
		last := c.pipelineHealth.LastProcessedAt("crypto")
		if !last.IsZero() {
			fh.PipelineLastBarAge = time.Since(last)
			fh.PipelineHealthy = !IsPipelineDeadlocked(fh.LastBarAge, fh.PipelineLastBarAge)
		}
	}
	return fh
}

// ErrCryptoWSMissingCredentials is returned when API key or secret is empty.
var ErrCryptoWSMissingCredentials = errors.New("crypto websocket requires API key and secret")

func (c *CryptoWSClient) defaultConnectFactory(symStrs []string, barHandler func(alpacastream.CryptoBar), tradeHandler func(alpacastream.CryptoTrade)) cryptoConnectFn {
	return func(ctx context.Context) error {
		sc := alpacastream.NewCryptoClient(
			c.feed,
			alpacastream.WithCredentials(c.apiKey, c.apiSecret),
			alpacastream.WithCryptoBars(barHandler, symStrs...),
			alpacastream.WithCryptoTrades(tradeHandler, symStrs...),
			alpacastream.WithReconnectSettings(1, 0),
		)
		if err := sc.Connect(ctx); err != nil {
			return err
		}
		select {
		case err := <-sc.Terminated():
			return err
		case <-ctx.Done():
			return nil
		}
	}
}

// StreamBars connects to the Alpaca crypto WebSocket and streams bars for the
// requested symbols. It reconnects with exponential backoff on failure and uses
// the shared stale feed watchdog to detect zombie connections.
func (c *CryptoWSClient) StreamBars(ctx context.Context, symbols []domain.Symbol, _ domain.Timeframe, handler ports.BarHandler) error {
	if len(symbols) == 0 {
		return nil
	}

	symStrs := make([]string, len(symbols))
	for i, s := range symbols {
		symStrs[i] = string(s)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()

	const cryptoRestPollInterval = 60 * time.Second

	var dedupMu sync.Mutex
	dedup := make(map[string]struct{})

	lastBarTime := make(map[domain.Symbol]time.Time)
	var lastBarMu sync.Mutex

	var restPollMu sync.Mutex
	var restPollCancel context.CancelFunc
	restPollerWasStarted := false

	stopRestPoller := func() {
		restPollMu.Lock()
		defer restPollMu.Unlock()
		wasActive := restPollCancel != nil
		if restPollCancel != nil {
			restPollCancel()
			restPollCancel = nil
		}
		if wasActive {
			c.log.Info().Msg("crypto REST poller: STOPPED")
		}
	}

	callHandler := func(bCtx context.Context, bar domain.MarketBar, fromREST bool) error {
		key := barKey(bar)
		dedupMu.Lock()
		if _, seen := dedup[key]; seen {
			dedupMu.Unlock()
			return nil
		}
		dedup[key] = struct{}{}
		if len(dedup) > maxDedupEntries {
			dedup = make(map[string]struct{})
		}
		dedupMu.Unlock()

		lastBarMu.Lock()
		if bar.Time.After(lastBarTime[bar.Symbol]) {
			lastBarTime[bar.Symbol] = bar.Time
		}
		lastBarMu.Unlock()

		return handler(bCtx, bar)
	}

	startRestPoller := func() {
		pollCtx, pCancel := context.WithCancel(streamCtx)
		restPollMu.Lock()
		restPollCancel = pCancel
		restPollMu.Unlock()
		restPollerWasStarted = true
		c.log.Info().Int("symbols", len(symbols)).Dur("interval", cryptoRestPollInterval).
			Msg("crypto REST poller: STARTED")
		go c.cryptoRestPoller(pollCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler, cryptoRestPollInterval)
	}

	c.tracker.setState("reconnecting")
	attempt := 0
	consecutiveFails := 0

	for {
		if streamCtx.Err() != nil {
			c.tracker.setState("stopped")
			return nil
		}

		if ok, wait := CheckCircuitBreaker(c.tracker, c.log); !ok {
			if c.onCircuitBreakerOpen != nil {
				c.onCircuitBreakerOpen(c.tracker.cb.ConsecutiveFails(), wait)
			}
			select {
			case <-time.After(wait):
			case <-streamCtx.Done():
				c.tracker.setState("stopped")
				return nil
			}
		}

		if CheckMaxConsecutiveFails(consecutiveFails) {
			c.tracker.setState("stopped")
			return fmt.Errorf("crypto ws: %d consecutive connect failures — giving up", consecutiveFails)
		}

		if IncrementAttempt(&attempt, consecutiveFails, symStrs, c.log) {
			c.tracker.incReconnect()
			c.tracker.setState("reconnecting")
		}

		connCtx, connCancel := context.WithCancel(streamCtx)
		var staleCancelMu sync.Mutex
		staleCancelFn := connCancel

		c.tracker.resetBarTimer()

		watchdogDone := make(chan struct{})
		go func() {
			defer close(watchdogDone)
			staleFeedWatchdog(connCtx, c.tracker, &staleCancelMu, &staleCancelFn, cryptoStaleThreshold, func() bool { return true }, c.pipelineHealth, "crypto")
		}()

		c.tracker.setConnected(true)
		c.tracker.setState("streaming")
		connectedAt := time.Now()
		var cryptoBarCount atomic.Int64

		// Buffered channel decouples SDK callback from downstream processing.
		// SDK's connReader goroutine never blocks on slow handlers (LLM, DB, broker).
		barCh := make(chan alpacastream.CryptoBar, 100)
		barProcessDone := make(chan struct{})
		go func() {
			defer close(barProcessDone)
			for cb := range barCh {
				bar, err := CryptoBarToMarketBar(cb)
				if err != nil {
					continue
				}
				_ = callHandler(streamCtx, bar, false)
			}
		}()

		barHandler := func(cb alpacastream.CryptoBar) {
			c.tracker.recordBar()
			if cryptoBarCount.Add(1) == 1 {
				bar, err := CryptoBarToMarketBar(cb)
				if err == nil {
					c.log.Info().Str("symbol", string(bar.Symbol)).Time("bar_time", bar.Time).
						Float64("close", bar.Close).Float64("volume", bar.Volume).
						Msg("crypto WS: first bar received after connect")
				}
			}
			c.tracker.clearStaleAlert()
			stopRestPoller()
			select {
			case <-connCtx.Done():
			case barCh <- cb:
			default:
				c.log.Warn().Str("symbol", cb.Symbol).Msg("crypto WS: bar channel full, dropping bar")
			}
		}

		tradeHandler := func(ct alpacastream.CryptoTrade) {
			if c.tradeHandler == nil {
				return
			}
			sym, err := domain.NewSymbol(ct.Symbol)
			if err != nil {
				return
			}
			sym = sym.ToSlashFormat()
			mt := domain.MarketTrade{
				Time:   ct.Timestamp,
				Symbol: sym,
				Price:  ct.Price,
				Size:   ct.Size,
			}
			_ = c.tradeHandler(connCtx, mt)
		}

		connect := c.connectFactory(symStrs, barHandler, tradeHandler)
		c.log.Info().Strs("symbols", symStrs).Int("attempt", attempt).
			Msg("crypto WS: connecting")
		connErr := connect(connCtx)

		close(barCh)
		select {
		case <-barProcessDone:
		case <-time.After(5 * time.Second):
			c.log.Warn().Msg("crypto WS: bar processing drain timed out — abandoning goroutine")
		}

		barsReceived := cryptoBarCount.Load()
		c.log.Info().Err(connErr).Int64("bars_received", barsReceived).
			Dur("connected_for", time.Since(connectedAt)).
			Bool("ctx_canceled", connCtx.Err() != nil).
			Msg("crypto WS: connection ended")

		connCancel()
		<-watchdogDone
		staleCancelMu.Lock()
		staleCancelFn = nil
		staleCancelMu.Unlock()

		c.tracker.setConnected(false)

		stopRestPoller()
		hadPoller := restPollerWasStarted
		restPollerWasStarted = false

		if streamCtx.Err() != nil {
			c.tracker.setState("stopped")
			return nil
		}

		errClass := ClassifyAndRecordError(connErr, c.tracker)

		wasStaleReset := HandleStaleReset(connCtx, streamCtx, connErr, c.tracker, c.log)

		if hadPoller && c.fetcher != nil {
			c.cryptoGapFill(streamCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler)
		}

		if errClass == ErrFatal {
			c.log.Error().Err(connErr).Int("attempt", attempt).
				Msg("crypto stream fatal error — circuit breaker will gate retries")
			// Keep REST poller alive during fatal WS errors so bars
			// continue flowing while the circuit breaker gates retries.
			if hadPoller && c.fetcher != nil {
				startRestPoller()
			}
			consecutiveFails++
			continue
		}

		// Determine if this is a ghost-session / connection-limit scenario.
		isConnLimit := errClass == ErrGhost

		if !isConnLimit {
			// Non-ghost path: stale reset handling + normal backoff.
			if wasStaleReset && c.fetcher != nil {
				dedupMu.Lock()
				dedup = make(map[string]struct{})
				dedupMu.Unlock()

				if c.onDegraded != nil && c.tracker.tryMarkStaleAlert() {
					c.onDegraded("crypto WebSocket stale — falling back to REST polling")
				}

				startRestPoller()
			}

			// BUG FIX: When a REST poller was running but the WS reconnect
			// failed (non-stale), the poller was killed at cleanup (above)
			// and never restarted — causing silent data loss. Restart it
			// so bars keep flowing while WS retries.
			if !wasStaleReset && hadPoller && c.fetcher != nil {
				startRestPoller()
			}

			wait, resetCounter := CalculateCryptoBackoff(errClass, connErr, connectedAt, consecutiveFails, attempt, c.log)
			if resetCounter {
				consecutiveFails = 0
			} else {
				consecutiveFails++
			}

			select {
			case <-time.After(wait):
			case <-streamCtx.Done():
				stopRestPoller()
				c.tracker.setState("stopped")
				return nil
			}
			continue
		}

		// ── Ghost-session scenario — probe reconnect with REST bridge ──
		stopRestPoller() // stop any stale-triggered REST poller before ghost takes over
		c.tracker.incGhostWindow()
		c.tracker.setState("ghost_probe")
		ghostStart := time.Now()

		c.log.Warn().Int("attempt", attempt).
			Msg("crypto WebSocket: connection limit exceeded — probing reconnect with REST bridge")

		// Clear dedup set so next WS session starts fresh.
		dedupMu.Lock()
		dedup = make(map[string]struct{})
		dedupMu.Unlock()

		// Start REST bridge poller if a fetcher is available.
		var ghostPollCancel context.CancelFunc
		if c.fetcher != nil {
			pollCtx, pCancel := context.WithCancel(streamCtx)
			ghostPollCancel = pCancel
			go c.cryptoRestPoller(pollCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler, cryptoRestPollInterval)
		}

		// Probe reconnect loop.
		probeIdx := 0
		reconnected := false
		for !reconnected {
			wait := ghostWait(probeIdx)
			probeIdx++
			c.log.Info().Dur("retry_in", wait).Int("probe", probeIdx).Msg("crypto ghost session: probing reconnect")

			select {
			case <-time.After(wait):
			case <-streamCtx.Done():
				if ghostPollCancel != nil {
					ghostPollCancel()
				}
				c.tracker.setState("stopped")
				return nil
			}

			// Probe: try connecting and watch for a real bar to arrive.
			var probeGotBar atomic.Bool
			probeBarHandler := func(cb alpacastream.CryptoBar) {
				probeGotBar.Store(true)
				bar, err := CryptoBarToMarketBar(cb)
				if err != nil {
					return
				}
				_ = callHandler(streamCtx, bar, false)
			}
			probeTradeHandler := func(ct alpacastream.CryptoTrade) {} // no-op during probe
			probeConnect := c.connectFactory(symStrs, probeBarHandler, probeTradeHandler)
			probeCtx, probeCancel := context.WithTimeout(streamCtx, 8*time.Second)
			probeErr := probeConnect(probeCtx)
			probeCancel()

			if streamCtx.Err() != nil {
				if ghostPollCancel != nil {
					ghostPollCancel()
				}
				c.tracker.setState("stopped")
				return nil
			}

			if probeGotBar.Load() {
				c.log.Info().Int("probe", probeIdx).Msg("crypto ghost session: bar received during probe — ghost cleared")
				reconnected = true
				continue
			}

			// No bar received. Classify by error.
			isStillGhost := probeErr != nil && (strings.Contains(probeErr.Error(), "connection limit exceeded") ||
				strings.Contains(probeErr.Error(), "406") ||
				strings.Contains(probeErr.Error(), "max reconnect limit"))
			switch {
			case probeErr == nil:
				c.log.Info().Int("probe", probeIdx).Msg("crypto ghost session: clean probe close — ghost cleared")
				reconnected = true
			case probeCtx.Err() == context.DeadlineExceeded:
				c.log.Info().Int("probe", probeIdx).Msg("crypto ghost session: probe timeout with no bar — still alive")
			case isStillGhost:
				c.log.Info().Int("probe", probeIdx).Err(probeErr).Msg("crypto ghost session: still alive, continuing probes")
			default:
				c.log.Info().Int("probe", probeIdx).Err(probeErr).Msg("crypto ghost session: non-406 error — ghost cleared")
				reconnected = true
			}
		} // end for !reconnected

		// Record ghost window duration.
		ghostDur := time.Since(ghostStart)
		c.log.Info().Dur("ghost_duration", ghostDur).Msg("crypto ghost session resolved")

		// Stop REST poller.
		if ghostPollCancel != nil {
			ghostPollCancel()
		}

		// Gap-fill: fetch any bars missed between last REST poll and WS resume.
		if c.fetcher != nil {
			c.cryptoGapFill(streamCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler)
		}

		// Reset counters — we've successfully probed through the ghost.
		attempt = 0
		consecutiveFails = 0
	}
}

func (c *CryptoWSClient) cryptoRestPoller(
	ctx context.Context,
	symbols []domain.Symbol,
	timeframe domain.Timeframe,
	lastBarTime map[domain.Symbol]time.Time,
	lastBarMu *sync.Mutex,
	dedupMu *sync.Mutex,
	dedup map[string]struct{},
	callHandler func(context.Context, domain.MarketBar, bool) error,
	pollInterval time.Duration,
) {
	c.log.Info().Int("symbols", len(symbols)).Dur("interval", pollInterval).
		Msg("crypto REST poller goroutine: entered")
	defer c.log.Info().Msg("crypto REST poller goroutine: exiting")

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	pollOnce := func() {
		now := time.Now()
		totalBars := 0
		for _, sym := range symbols {
			lastBarMu.Lock()
			from := lastBarTime[sym]
			lastBarMu.Unlock()

			if from.IsZero() {
				from = now.Add(-2 * pollInterval)
			} else {
				from = from.Add(time.Second)
			}

			bars, err := c.fetcher(ctx, sym, timeframe, from, now)
			if err != nil {
				if ctx.Err() == nil {
					c.log.Warn().Err(err).Str("symbol", string(sym)).Msg("crypto REST poller: fetch failed")
				}
				continue
			}

			totalBars += len(bars)
			for _, bar := range bars {
				if err := callHandler(ctx, bar, true); err != nil {
					if ctx.Err() == nil {
						c.log.Warn().Err(err).Str("symbol", string(sym)).Msg("crypto REST poller: handler error")
					}
				}
			}
		}
		if ctx.Err() == nil {
			c.log.Info().Int("bars_delivered", totalBars).Int("symbols_polled", len(symbols)).
				Msg("crypto REST poller: poll cycle complete")
		}
	}

	pollOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollOnce()
		}
	}
}

func (c *CryptoWSClient) cryptoGapFill(
	ctx context.Context,
	symbols []domain.Symbol,
	timeframe domain.Timeframe,
	lastBarTime map[domain.Symbol]time.Time,
	lastBarMu *sync.Mutex,
	dedupMu *sync.Mutex,
	dedup map[string]struct{},
	callHandler func(context.Context, domain.MarketBar, bool) error,
) {
	now := time.Now()
	for _, sym := range symbols {
		lastBarMu.Lock()
		from := lastBarTime[sym]
		lastBarMu.Unlock()

		if from.IsZero() {
			continue
		}
		from = from.Add(time.Second)

		bars, err := c.fetcher(ctx, sym, timeframe, from, now)
		if err != nil {
			if ctx.Err() == nil {
				c.log.Warn().Err(err).Str("symbol", string(sym)).Msg("crypto gap-fill: fetch failed")
			}
			continue
		}
		for _, bar := range bars {
			if err := callHandler(ctx, bar, true); err != nil {
				if ctx.Err() == nil {
					c.log.Warn().Err(err).Str("symbol", string(sym)).Msg("crypto gap-fill: handler error")
				}
			}
		}
	}
}

func CryptoBarToMarketBar(cb alpacastream.CryptoBar) (domain.MarketBar, error) {
	sym, err := domain.NewSymbol(cb.Symbol)
	if err != nil {
		return domain.MarketBar{}, err
	}
	sym = sym.ToSlashFormat()
	tf, _ := domain.NewTimeframe("1m")
	bar, err := domain.NewMarketBar(cb.Timestamp, sym, tf, cb.Open, cb.High, cb.Low, cb.Close, cb.Volume)
	if err != nil {
		return bar, err
	}
	bar.TradeCount = cb.TradeCount
	return bar, nil
}

// Close terminates the crypto WebSocket connection.
func (c *CryptoWSClient) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		cancel := c.cancel
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
	c.tracker.setState("stopped")
	return nil
}
