package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	alpacastream "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/rs/zerolog/log"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// BarFetcher is a function that retrieves historical bars from the REST API.
// It is injected into WSClient so the REST poller can use it during ghost windows.
type BarFetcher func(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)

// connectFn is the signature for the underlying WebSocket connect call.
// Injected at construction time so tests can replace it with a fake.
type connectFn func(ctx context.Context) error

// WSClient handles WebSocket connections for Alpaca market data.
type WSClient struct {
	dataURL      string
	apiKey       string
	apiSecret    string
	feed         string
	fetcher      BarFetcher // REST poller fallback; nil disables polling
	tradeHandler ports.TradeHandler
	closeOnce    sync.Once
	cancel       context.CancelFunc
	mu           sync.Mutex
	metrics      *metrics.Metrics
	tracker      *feedTracker

	// connectFactory builds a real alpacastream.StocksClient and returns its Connect func.
	// Overridable in tests.
	connectFactory func(symStrs []string, barHandler func(alpacastream.Bar), tradeHandler func(alpacastream.Trade)) connectFn
}

// NewWSClient creates a new WSClient instance.
// fetcher is optional: if non-nil, it is used to poll historical bars during
// ghost-session windows so live data continues flowing while WS is blocked.
func NewWSClient(dataURL string, apiKey string, apiSecret string, feed string, fetcher BarFetcher) *WSClient {
	if feed == "" {
		feed = "iex"
	}
	ws := &WSClient{
		dataURL:   dataURL,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		feed:      feed,
		fetcher:   fetcher,
		tracker:   newFeedTracker(),
	}
	ws.connectFactory = ws.defaultConnectFactory
	return ws
}

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (w *WSClient) SetMetrics(m *metrics.Metrics) { w.metrics = m }

// SetTradeHandler sets the callback for forwarding raw trade ticks.
func (w *WSClient) SetTradeHandler(h ports.TradeHandler) { w.tradeHandler = h }

// FeedHealth returns a point-in-time snapshot of WebSocket feed status.
func (w *WSClient) FeedHealth() FeedHealth { return w.tracker.Snapshot() }

// defaultConnectFactory builds a real Alpaca SDK StocksClient and returns its Connect method.
// The returned connectFn blocks until the stream is fully terminated (not just established).
// Connect() on the SDK returns nil once the connection is *established*; actual disconnect
// is signalled via sc.Terminated(). We block on Terminated() so the caller sees a return
// only when the stream has truly ended.
func (w *WSClient) defaultConnectFactory(symStrs []string, barHandler func(alpacastream.Bar), tradeHandler func(alpacastream.Trade)) connectFn {
	wsBaseURL := deriveStreamURL(w.dataURL)
	sc := alpacastream.NewStocksClient(
		w.feed,
		alpacastream.WithCredentials(w.apiKey, w.apiSecret),
		alpacastream.WithBaseURL(wsBaseURL),
		alpacastream.WithBars(barHandler, symStrs...),
		alpacastream.WithTrades(tradeHandler, symStrs...),     // keeps connection alive
		alpacastream.WithReconnectSettings(1, 90*time.Second), // single SDK retry at 90s; outer loop owns all reconnect logic
	)
	return func(ctx context.Context) error {
		if err := sc.Connect(ctx); err != nil {
			// Connection failed to establish — return immediately.
			return err
		}
		// Connection established successfully. Block until the stream terminates.
		// sc.Terminated() receives when the SDK's internal goroutines exit.
		select {
		case err := <-sc.Terminated():
			return err // nil = clean close; non-nil = error
		case <-ctx.Done():
			return nil // caller cancelled — clean shutdown
		}
	}
}

// ParseBarMessage converts raw Alpaca bar JSON into a domain.MarketBar.
func (w *WSClient) ParseBarMessage(data []byte) (domain.MarketBar, error) {
	var ab struct {
		T    string  `json:"T"`
		S    string  `json:"S"`
		O    float64 `json:"o"`
		H    float64 `json:"h"`
		L    float64 `json:"l"`
		C    float64 `json:"c"`
		V    float64 `json:"v"`
		Time string  `json:"t"`
	}
	if err := json.Unmarshal(data, &ab); err != nil {
		return domain.MarketBar{}, err
	}

	t, err := time.Parse(time.RFC3339, ab.Time)
	if err != nil {
		return domain.MarketBar{}, err
	}

	sym, err := domain.NewSymbol(ab.S)
	if err != nil {
		return domain.MarketBar{}, err
	}

	tf, _ := domain.NewTimeframe("1m")

	return domain.NewMarketBar(t, sym, tf, ab.O, ab.H, ab.L, ab.C, ab.V)
}

// probeSchedule is the ordered list of wait durations used during ghost-session
// probe reconnect. Each entry includes ±10% random jitter.
var probeSchedule = []time.Duration{
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
	45 * time.Second,
	60 * time.Second,
	75 * time.Second,
	95 * time.Second,
}

// ghostWait returns the next probe-reconnect wait duration.
// probeIdx is the current index into probeSchedule (clamped to last entry beyond the end).
func ghostWait(probeIdx int) time.Duration {
	idx := probeIdx
	if idx >= len(probeSchedule) {
		idx = len(probeSchedule) - 1
	}
	base := probeSchedule[idx]
	// ±10% jitter
	jitter := time.Duration(float64(base) * 0.1 * (rand.Float64()*2 - 1)) //nolint:gosec
	return base + jitter
}

// barKey returns a unique deduplication key for a bar: "SYMBOL@RFC3339timestamp".
func barKey(bar domain.MarketBar) string {
	return fmt.Sprintf("%s@%s", bar.Symbol.String(), bar.Time.UTC().Format(time.RFC3339))
}

// maxDedupEntries caps the dedup map to prevent unbounded memory growth.
const maxDedupEntries = 10_000

// maxConsecutiveFailsBeforeError is the hard limit on consecutive connect
// failures (without any successful streaming) before StreamBars returns an error.
const maxConsecutiveFailsBeforeError = 50

// staleFeedThresholdRTH is how long we wait without a bar during RTH
// before the watchdog forces a reconnect. 1-min bars + generous slack.
const staleFeedThresholdRTH = 90 * time.Second

// staleFeedThresholdOffHours is the stale feed threshold outside RTH.
// Crypto trades 24/7 but volume is sparse on weekends — 10 min allows
// for legitimate quiet periods while catching dead connections.
const staleFeedThresholdOffHours = 10 * time.Minute

// staleFeedCheckInterval is how often the watchdog checks for stale feed.
const staleFeedCheckInterval = 15 * time.Second

// StreamBars connects to the Alpaca WebSocket market data feed and streams
// minute bars for the requested symbols until ctx is cancelled.
//
// Hardening features:
//   - Exponential backoff with full jitter (aggressive RTH / relaxed off-hours)
//   - Circuit breaker for fatal errors (auth, permission)
//   - Stale feed watchdog: forces reconnect if no bars during RTH
//   - Bounded dedup map (10k entries max)
//   - Max consecutive fail limit (50 attempts → error)
//   - Ghost-session handling with REST bridge and probe schedule
func (w *WSClient) StreamBars(ctx context.Context, symbols []domain.Symbol, _ domain.Timeframe, handler ports.BarHandler) error {
	if len(symbols) == 0 {
		return nil
	}

	symStrs := make([]string, len(symbols))
	for i, s := range symbols {
		symStrs[i] = string(s)
	}

	// Create a child context so we can cancel on Close().
	streamCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()
	defer cancel()

	const restPollInterval = 5 * time.Second

	attempt := 0
	consecutiveFails := 0 // counts attempts without successful streaming

	// dedup tracks bar keys emitted by the REST poller so WS resume can skip duplicates.
	// Protected by dedupMu.
	var dedupMu sync.Mutex
	dedup := make(map[string]struct{})

	// lastBarTime tracks the most-recently-seen bar timestamp per symbol, used
	// to window REST poll requests.
	lastBarTime := make(map[domain.Symbol]time.Time)
	var lastBarMu sync.Mutex

	// staleCancelFn cancels the current connection when the watchdog detects stale feed.
	// Set per-connection; nil when no watchdog is active.
	var staleCancelFn context.CancelFunc
	var staleCancelMu sync.Mutex

	// callHandler deduplicates by (symbol, timestamp) and forwards new bars to handler.
	// Both REST and WS paths record seen keys; either skips if already seen.
	callHandler := func(bCtx context.Context, bar domain.MarketBar, fromREST bool) error {
		start := time.Now()
		key := barKey(bar)
		dedupMu.Lock()
		if _, seen := dedup[key]; seen {
			dedupMu.Unlock()
			return nil
		}
		dedup[key] = struct{}{}
		// Bound dedup map: if it grows too large, reset it.
		if len(dedup) > maxDedupEntries {
			dedup = make(map[string]struct{})
		}
		dedupMu.Unlock()

		lastBarMu.Lock()
		if bar.Time.After(lastBarTime[bar.Symbol]) {
			lastBarTime[bar.Symbol] = bar.Time
		}
		lastBarMu.Unlock()

		// Record bar in health tracker.
		w.tracker.recordBar()

		source := "ws"
		if fromREST {
			source = "rest"
		}
		err := handler(bCtx, bar)
		if w.metrics != nil {
			w.metrics.WS.MessagesTotal.WithLabelValues(w.feed, "bar").Inc()
			w.metrics.WS.LastMsgTimestamp.WithLabelValues(w.feed).Set(float64(time.Now().Unix()))
			result := "ok"
			if err != nil {
				result = "error"
			}
			w.metrics.WS.MsgProcDuration.WithLabelValues(w.feed, "bar", result).Observe(time.Since(start).Seconds())
			_ = source // source available for future per-source breakdown
		}
		return err
	}

	w.tracker.setState("reconnecting")

	for {
		if streamCtx.Err() != nil {
			w.tracker.setState("stopped")
			return nil // context cancelled — clean shutdown
		}

		// Circuit breaker check.
		if ok, wait := w.tracker.cb.Allow(); !ok {
			w.tracker.setState("circuit_open")
			log.Warn().Dur("wait", wait).Msg("circuit breaker open — waiting before retry")
			if w.metrics != nil {
				w.metrics.WS.ReconnectsTotal.WithLabelValues(w.feed, "circuit_open").Inc()
			}
			select {
			case <-time.After(wait):
			case <-streamCtx.Done():
				w.tracker.setState("stopped")
				return nil
			}
		}

		// Max consecutive failures check.
		if consecutiveFails >= maxConsecutiveFailsBeforeError {
			w.tracker.setState("stopped")
			return fmt.Errorf("alpaca ws: %d consecutive connect failures without successful streaming — giving up", consecutiveFails)
		}

		attempt++
		if attempt > 1 {
			w.tracker.incReconnect()
			w.tracker.setState("reconnecting")
			if w.metrics != nil {
				reason := "transient"
				w.metrics.WS.ReconnectsTotal.WithLabelValues(w.feed, reason).Inc()
			}
			log.Info().
				Int("attempt", attempt).
				Int("consecutive_fails", consecutiveFails).
				Strs("symbols", symStrs).
				Msg("reconnecting to Alpaca WebSocket stream")
		}

		barHandler := func(bar alpacastream.Bar) {
			sym, err := domain.NewSymbol(bar.Symbol)
			if err != nil {
				log.Warn().Err(err).Str("symbol", bar.Symbol).Msg("alpaca stream: invalid symbol")
				return
			}
			tf, _ := domain.NewTimeframe("1m")
			domainBar, err := domain.NewMarketBar(bar.Timestamp, sym, tf, bar.Open, bar.High, bar.Low, bar.Close, float64(bar.Volume))
			if err != nil {
				log.Warn().Err(err).Str("symbol", bar.Symbol).Msg("alpaca stream: invalid bar")
				return
			}
			domainBar.TradeCount = bar.TradeCount
			if err := callHandler(streamCtx, domainBar, false); err != nil {
				log.Warn().Err(err).Str("symbol", bar.Symbol).Msg("alpaca stream: bar handler error")
			}
		}

		tradeHandler := func(t alpacastream.Trade) {
			if w.tradeHandler == nil {
				return
			}
			sym, err := domain.NewSymbol(t.Symbol)
			if err != nil {
				return
			}
			mt := domain.MarketTrade{
				Time:   t.Timestamp,
				Symbol: sym,
				Price:  t.Price,
				Size:   float64(t.Size),
			}
			_ = w.tradeHandler(streamCtx, mt)
		}

		connect := w.connectFactory(symStrs, barHandler, tradeHandler)

		// Create a per-connection context that the stale watchdog can cancel.
		connCtx, connCancel := context.WithCancel(streamCtx)
		staleCancelMu.Lock()
		staleCancelFn = connCancel
		staleCancelMu.Unlock()

		w.tracker.resetBarTimer()

		// Start stale feed watchdog.
		watchdogDone := make(chan struct{})
		go func() {
			defer close(watchdogDone)
			staleFeedWatchdog(connCtx, w.tracker, &staleCancelMu, &staleCancelFn)
		}()

		w.tracker.setConnected(true)
		w.tracker.setState("streaming")
		if w.metrics != nil {
			w.metrics.WS.Connected.WithLabelValues(w.feed).Set(1)
		}
		connectedAt := time.Now()
		connErr := connect(connCtx)

		// Stop watchdog.
		connCancel()
		<-watchdogDone
		staleCancelMu.Lock()
		staleCancelFn = nil
		staleCancelMu.Unlock()

		w.tracker.setConnected(false)
		if w.metrics != nil {
			w.metrics.WS.Connected.WithLabelValues(w.feed).Set(0)
		}

		// If top-level context was cancelled, this is an intentional shutdown.
		if streamCtx.Err() != nil {
			w.tracker.setState("stopped")
			return nil
		}

		// Record error for health tracking.
		w.tracker.recordError(connErr)

		// Classify error.
		errClass := classifyError(connErr)

		// Check for stale-triggered reconnect (connCtx cancelled but streamCtx still alive).
		wasStaleReset := connCtx.Err() != nil && streamCtx.Err() == nil && connErr == nil
		if wasStaleReset {
			w.tracker.incStaleReset()
			if w.metrics != nil {
				w.metrics.WS.ReconnectsTotal.WithLabelValues(w.feed, "stale").Inc()
			}
			log.Warn().Msg("stale feed watchdog triggered reconnect")
			// Use aggressive backoff for stale resets.
			errClass = ErrTransient
		}

		// Record in circuit breaker.
		w.tracker.cb.Record(errClass)

		// Determine if this is a ghost-session / connection-limit scenario.
		isConnLimit := false
		if errClass == ErrGhost {
			isConnLimit = true
		} else if connErr == nil && !wasStaleReset {
			// nil = clean close from Alpaca.
			// Only retry fast if we're in core market hours AND the stream was genuinely
			// live (≥10s). A flash-close (<10s) means the previous session is still
			// alive on Alpaca's side regardless of market hours — wait to drain.
			wasLive := time.Since(connectedAt) >= 10*time.Second
			if !isCoreMarketHours() || !wasLive {
				isConnLimit = true // treat as ghost-session scenario
			}
		}

		if errClass == ErrFatal {
			// Fatal error — circuit breaker handles backoff; log clearly.
			log.Error().Err(connErr).Int("attempt", attempt).
				Int("cb_fails", w.tracker.cb.ConsecutiveFails()).
				Msg("Alpaca stream fatal error (auth/permission) — circuit breaker will gate retries")
			consecutiveFails++
			continue
		}

		if !isConnLimit {
			// Normal transient error — policy-based backoff.
			consecutiveFails++
			policy := selectPolicy()
			wait := policy.backoff(consecutiveFails - 1)
			if connErr != nil {
				log.Error().Err(connErr).Int("attempt", attempt).Dur("retry_in", wait).Msg("Alpaca stream disconnected with error, reconnecting")
			} else {
				log.Warn().Int("attempt", attempt).Dur("retry_in", wait).Msg("Alpaca stream clean close during core market hours, reconnecting")
			}
			select {
			case <-time.After(wait):
			case <-streamCtx.Done():
				w.tracker.setState("stopped")
				return nil
			}
			continue
		}

		// Ghost-session scenario — probe reconnect with REST bridge.
		w.tracker.incGhostWindow()
		w.tracker.setState("ghost_probe")
		consecutiveFails++ // count ghost windows as a fail until streaming resumes
		ghostStart := time.Now()
		if w.metrics != nil {
			w.metrics.WS.ReconnectsTotal.WithLabelValues(w.feed, "ghost").Inc()
		}
		if connErr != nil {
			log.Warn().Int("attempt", attempt).
				Msg("Alpaca WebSocket: connection limit exceeded — probing reconnect with REST bridge")
		} else {
			log.Warn().Int("attempt", attempt).
				Msg("Alpaca stream nil close (flash or off-hours) — probing reconnect with REST bridge")
		}

		// Clear dedup set so next WS session starts fresh.
		dedupMu.Lock()
		dedup = make(map[string]struct{})
		dedupMu.Unlock()

		// Start REST bridge poller if a fetcher is available.
		var pollCancel context.CancelFunc
		if w.fetcher != nil {
			pollCtx, pCancel := context.WithCancel(streamCtx)
			pollCancel = pCancel
			go w.restPoller(pollCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler, restPollInterval)
		}

		// Probe reconnect loop.
		probeIdx := 0
		reconnected := false
		for !reconnected {
			wait := ghostWait(probeIdx)
			probeIdx++
			log.Info().Dur("retry_in", wait).Int("probe", probeIdx).Msg("ghost session: probing reconnect")

			select {
			case <-time.After(wait):
			case <-streamCtx.Done():
				if pollCancel != nil {
					pollCancel()
				}
				w.tracker.setState("stopped")
				return nil
			}

			// Probe: try connecting and watch for a real bar to arrive.
			// Ghost is cleared only if at least one bar fires during the probe window.
			// DeadlineExceeded alone is NOT sufficient — the SDK may be mid-retry-loop for 406.
			var probeGotBar atomic.Bool
			probeBarHandler := func(bar alpacastream.Bar) {
				probeGotBar.Store(true)
				barHandler(bar) // forward to real handler
			}
			probeConnect := w.connectFactory(symStrs, probeBarHandler, tradeHandler)
			probeCtx, probeCancel := context.WithTimeout(streamCtx, 8*time.Second)
			probeErr := probeConnect(probeCtx)
			probeCancel()

			if streamCtx.Err() != nil {
				if pollCancel != nil {
					pollCancel()
				}
				w.tracker.setState("stopped")
				return nil
			}

			if probeGotBar.Load() {
				// At least one bar received — ghost is definitely gone.
				log.Info().Int("probe", probeIdx).Msg("ghost session: bar received during probe — ghost cleared")
				reconnected = true
				continue
			}

			// No bar received. Classify by error.
			isStillGhost := probeErr != nil && (strings.Contains(probeErr.Error(), "connection limit exceeded") ||
				strings.Contains(probeErr.Error(), "406") ||
				strings.Contains(probeErr.Error(), "max reconnect limit"))
			if probeErr == nil || probeCtx.Err() == context.DeadlineExceeded {
				// nil = clean close (ghost gone); DeadlineExceeded with no bar = ghost still alive.
				// Only treat nil as cleared.
				if probeErr == nil {
					log.Info().Int("probe", probeIdx).Msg("ghost session: clean probe close — ghost cleared")
					reconnected = true
				} else {
					// DeadlineExceeded, no bar — SDK was mid-retry, ghost still alive.
					log.Info().Int("probe", probeIdx).Msg("ghost session: probe timeout with no bar — still alive")
				}
			} else if isStillGhost {
				log.Info().Int("probe", probeIdx).Err(probeErr).Msg("ghost session: still alive, continuing probes")
			} else {
				// Some other error — treat as ghost gone.
				log.Info().Int("probe", probeIdx).Err(probeErr).Msg("ghost session: non-406 error — ghost cleared")
				reconnected = true
			}
		} // end for !reconnected

		// Record ghost window duration.
		ghostDur := time.Since(ghostStart)
		log.Info().Dur("ghost_duration", ghostDur).Msg("ghost session resolved")

		// Stop REST poller.
		if pollCancel != nil {
			pollCancel()
		}

		// Gap-fill: fetch any bars missed between last REST poll and WS resume.
		if w.fetcher != nil {
			w.gapFill(streamCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler)
		}

		// Reset counters — we've successfully reconnected.
		attempt = 0
		consecutiveFails = 0
	}
}

// staleFeedWatchdog monitors the feed and cancels the connection context if no
// bars arrive within the adaptive threshold (tight during RTH, relaxed off-hours).
// This forces a reconnect for zombie connections where TCP is alive but no data flows.
// Shared by both equity WSClient and CryptoWSClient.
func staleFeedWatchdog(ctx context.Context, tracker *feedTracker, cancelMu *sync.Mutex, cancelFn *context.CancelFunc) {
	ticker := time.NewTicker(staleFeedCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			threshold := staleFeedThresholdOffHours
			if isCoreMarketHours() {
				threshold = staleFeedThresholdRTH
			}

			tracker.mu.Lock()
			lastBar := tracker.lastBarAt
			tracker.mu.Unlock()

			if lastBar.IsZero() {
				continue
			}

			age := time.Since(lastBar)
			if age > threshold {
				log.Warn().Dur("bar_age", age).Dur("threshold", threshold).
					Msg("stale feed watchdog: no bars received within threshold — forcing reconnect")

				cancelMu.Lock()
				if *cancelFn != nil {
					(*cancelFn)()
				}
				cancelMu.Unlock()
				return
			}
		}
	}
}

// restPoller polls GetHistoricalBars every pollInterval during a ghost window,
// forwarding new bars to callHandler (with fromREST=true for dedup tracking).
func (w *WSClient) restPoller(
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
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// pollOnce performs a single poll cycle for all symbols.
	pollOnce := func() {
		now := time.Now()
		for _, sym := range symbols {
			lastBarMu.Lock()
			from := lastBarTime[sym]
			lastBarMu.Unlock()

			if from.IsZero() {
				from = now.Add(-2 * pollInterval) // sensible default if no bars seen yet
			} else {
				from = from.Add(time.Second) // fetch from 1s after last seen bar
			}

			bars, err := w.fetcher(ctx, sym, timeframe, from, now)
			if err != nil {
				if ctx.Err() == nil {
					log.Warn().Err(err).Str("symbol", string(sym)).Msg("REST poller: fetch failed")
				}
				continue
			}

			for _, bar := range bars {
				if err := callHandler(ctx, bar, true); err != nil {
					if ctx.Err() == nil {
						log.Warn().Err(err).Str("symbol", string(sym)).Msg("REST poller: handler error")
					}
				}
			}
		}
	}

	// Fire immediately on entry, then on each tick.
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

// gapFill performs a one-shot REST fetch after WS reconnect to catch any bars
// that fell between the last REST poll and the WS resuming.
func (w *WSClient) gapFill(
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
			continue // no reference point; skip
		}
		from = from.Add(time.Second)

		bars, err := w.fetcher(ctx, sym, timeframe, from, now)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn().Err(err).Str("symbol", string(sym)).Msg("gap-fill: fetch failed")
			}
			continue
		}
		for _, bar := range bars {
			if err := callHandler(ctx, bar, true); err != nil {
				if ctx.Err() == nil {
					log.Warn().Err(err).Str("symbol", string(sym)).Msg("gap-fill: handler error")
				}
			}
		}
	}
}

// isCoreMarketHours reports whether the current time falls within IEX core market
// hours in CST (America/Chicago): 08:30–15:00.
// Only during this window is the stream rock-solid; pre/post-market and off-hours
// all produce frequent nil closes that risk ghost sessions on Alpaca's side.
func isCoreMarketHours() bool {
	cst, err := time.LoadLocation("America/Chicago")
	if err != nil {
		// If the timezone DB is unavailable (distroless image), fall back to UTC-6.
		cst = time.FixedZone("CST", -6*60*60)
	}
	now := time.Now().In(cst)
	h, m, _ := now.Clock()
	minutes := h*60 + m
	return minutes >= 8*60+30 && minutes < 15*60 // 08:30–15:00 CST
}

// Close safely cancels any active WebSocket stream.
func (w *WSClient) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		if w.cancel != nil {
			w.cancel()
		}
		w.mu.Unlock()
	})
	w.tracker.setState("stopped")
	return nil
}

// deriveStreamURL converts a REST data base URL to a WebSocket stream URL.
//
// The Alpaca stream SDK appends "/<feed>" to the base URL internally, so we
// must only return the path up to and including "/v2".
//
//	https://data.alpaca.markets         → wss://stream.data.alpaca.markets/v2
//	https://data.sandbox.alpaca.markets  → wss://stream.data.sandbox.alpaca.markets/v2
func deriveStreamURL(dataURL string) string {
	u := strings.TrimRight(dataURL, "/")
	u = strings.Replace(u, "https://", "wss://stream.", 1)
	u = strings.Replace(u, "http://", "wss://stream.", 1)
	return u + "/v2"
}
