package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime/pprof"
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
	dataURL        string
	apiKey         string
	apiSecret      string
	feed           string
	fetcher        BarFetcher // REST poller fallback; nil disables polling
	tradeHandler   ports.TradeHandler
	pipelineHealth ports.PipelineHealthReporter
	closeOnce      sync.Once
	cancel         context.CancelFunc
	mu             sync.Mutex
	metrics        *metrics.Metrics
	tracker        *feedTracker

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

// SetPipelineHealth injects pipeline liveness reporter for dual-track watchdog.
func (w *WSClient) SetPipelineHealth(ph ports.PipelineHealthReporter) { w.pipelineHealth = ph }

// FeedHealth returns a point-in-time snapshot of WebSocket feed status.
func (w *WSClient) FeedHealth() FeedHealth {
	fh := w.tracker.Snapshot()
	if w.pipelineHealth != nil {
		last := w.pipelineHealth.LastProcessedAt("equity")
		if !last.IsZero() {
			fh.PipelineLastBarAge = time.Since(last)
			fh.PipelineHealthy = !IsPipelineDeadlocked(fh.LastBarAge, fh.PipelineLastBarAge)
		}
	}
	return fh
}

// defaultConnectFactory builds a real Alpaca SDK StocksClient and returns its Connect method.
// The returned connectFn blocks until the stream is fully terminated (not just established).
// Connect() on the SDK returns nil once the connection is *established*; actual disconnect
// is signaled via sc.Terminated(). We block on Terminated() so the caller sees a return
// only when the stream has truly ended.
func (w *WSClient) defaultConnectFactory(symStrs []string, barHandler func(alpacastream.Bar), tradeHandler func(alpacastream.Trade)) connectFn {
	wsBaseURL := deriveStreamURL(w.dataURL)
	return func(ctx context.Context) error {
		sc := alpacastream.NewStocksClient(
			w.feed,
			alpacastream.WithCredentials(w.apiKey, w.apiSecret),
			alpacastream.WithBaseURL(wsBaseURL),
			alpacastream.WithBars(barHandler, symStrs...),
			alpacastream.WithTrades(tradeHandler, symStrs...),
			alpacastream.WithReconnectSettings(1, 0),
		)
		if err := sc.Connect(ctx); err != nil {
			return err
		}
		select {
		case err := <-sc.Terminated():
			return err
		case <-ctx.Done():
			// Wait for SDK to send RFC6455 close frame before returning.
			select {
			case <-sc.Terminated():
			case <-time.After(3 * time.Second):
			}
			return nil
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

// staleFeedCheckInterval is how often the watchdog checks for stale feed.
const staleFeedCheckInterval = 15 * time.Second

// StreamBars connects to the Alpaca WebSocket market data feed and streams
// minute bars for the requested symbols until ctx is canceled.
//
// Hardening features:
//   - Exponential backoff with full jitter (aggressive RTH / relaxed off-hours)
//   - Circuit breaker for fatal errors (auth, permission)
//   - Stale feed watchdog: forces reconnect if no bars during RTH
//   - Bounded dedup map (10k entries max)
//   - Max consecutive fail limit (50 attempts → error)
//   - Ghost-session handling with REST bridge and probe schedule
//   - REST polling fallback during stale feed resets
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
			log.Info().Str("feed", w.feed).Msg("equity REST poller: STOPPED")
		}
	}

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

	startRestPoller := func() {
		if w.fetcher == nil {
			return
		}
		pollCtx, pCancel := context.WithCancel(streamCtx)
		restPollMu.Lock()
		restPollCancel = pCancel
		restPollMu.Unlock()
		restPollerWasStarted = true
		log.Info().Str("feed", w.feed).Int("symbols", len(symbols)).Dur("interval", restPollInterval).
			Msg("equity REST poller: STARTED")
		go w.restPoller(pollCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler, restPollInterval)
	}

	w.tracker.setState("reconnecting")

	for {
		if streamCtx.Err() != nil {
			w.tracker.setState("stopped")
			return nil // context canceled — clean shutdown
		}

		// Circuit breaker check.
		if ok, wait := CheckCircuitBreaker(w.tracker, log.Logger); !ok {
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
		if CheckMaxConsecutiveFails(consecutiveFails) {
			w.tracker.setState("stopped")
			return fmt.Errorf("alpaca ws: %d consecutive connect failures without successful streaming — giving up", consecutiveFails)
		}

		if IncrementAttempt(&attempt, consecutiveFails, symStrs, log.Logger) {
			w.tracker.incReconnect()
			w.tracker.setState("reconnecting")
			if w.metrics != nil {
				reason := "transient"
				w.metrics.WS.ReconnectsTotal.WithLabelValues(w.feed, reason).Inc()
			}
		}

		// Create a per-connection context that the stale watchdog can cancel.
		connCtx, connCancel := context.WithCancel(streamCtx)

		var wsBarCount atomic.Int64
		const wsMinutesToTrust = 2
		var wsMinuteMu sync.Mutex
		wsMinutesSeen := make(map[int64]struct{})

		// Buffered channel decouples SDK callback from downstream processing.
		// SDK's connReader goroutine never blocks on slow handlers (LLM, DB, broker).
		barCh := make(chan alpacastream.Bar, 100)
		barProcessDone := make(chan struct{})
		go func() {
			defer close(barProcessDone)
			for bar := range barCh {
				sym, err := domain.NewSymbol(bar.Symbol)
				if err != nil {
					log.Warn().Err(err).Str("symbol", bar.Symbol).Msg("alpaca stream: invalid symbol")
					continue
				}
				tf, _ := domain.NewTimeframe("1m")
				domainBar, err := domain.NewMarketBar(bar.Timestamp, sym, tf, bar.Open, bar.High, bar.Low, bar.Close, float64(bar.Volume))
				if err != nil {
					log.Warn().Err(err).Str("symbol", bar.Symbol).Msg("alpaca stream: invalid bar")
					continue
				}
				domainBar.TradeCount = bar.TradeCount
				if err := callHandler(streamCtx, domainBar, false); err != nil {
					log.Warn().Err(err).Str("symbol", bar.Symbol).Msg("alpaca stream: bar handler error")
				}
			}
		}()

		barHandler := func(bar alpacastream.Bar) {
			w.tracker.recordBar()
			n := wsBarCount.Add(1)
			if n == 1 {
				log.Info().Str("symbol", bar.Symbol).Time("bar_time", bar.Timestamp).
					Float64("close", bar.Close).Uint64("volume", bar.Volume).
					Msg("equity WS: first bar received after connect")
			}
			minuteKey := bar.Timestamp.Truncate(time.Minute).Unix()
			wsMinuteMu.Lock()
			prevLen := len(wsMinutesSeen)
			wsMinutesSeen[minuteKey] = struct{}{}
			newLen := len(wsMinutesSeen)
			wsMinuteMu.Unlock()
			if newLen >= wsMinutesToTrust && prevLen < wsMinutesToTrust {
				log.Info().Int("distinct_minutes", newLen).Int64("ws_bars", n).
					Msg("equity WS: stream trusted — stopping REST poller")
				stopRestPoller()
			}
			select {
			case <-connCtx.Done():
			case barCh <- bar:
			default:
				log.Warn().Str("symbol", bar.Symbol).Msg("equity WS: bar channel full, dropping bar")
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

		staleCancelMu.Lock()
		staleCancelFn = connCancel
		staleCancelMu.Unlock()

		w.tracker.resetBarTimer()

		watchdogDone := make(chan struct{})
		go func() {
			defer close(watchdogDone)
			staleFeedWatchdog(connCtx, w.tracker, &staleCancelMu, &staleCancelFn, staleFeedThresholdRTH, isCoreMarketHours, w.pipelineHealth, "equity")
		}()

		w.tracker.setConnected(true)
		w.tracker.setState("streaming")
		if w.metrics != nil {
			w.metrics.WS.Connected.WithLabelValues(w.feed).Set(1)
		}
		connectedAt := time.Now()
		wsBarCount.Store(0)
		log.Info().Strs("symbols", symStrs).Int("attempt", attempt).
			Msg("equity WS: connected, waiting for bars")
		connErr := connect(connCtx)

		close(barCh)
		select {
		case <-barProcessDone:
		case <-time.After(5 * time.Second):
			log.Warn().Msg("equity WS: bar processing drain timed out — abandoning goroutine")
		}

		barsReceived := wsBarCount.Load()
		connDur := time.Since(connectedAt)
		log.Info().Err(connErr).Int64("bars_received", barsReceived).
			Dur("connected_for", connDur).
			Bool("ctx_canceled", connCtx.Err() != nil).
			Msg("equity WS: connection ended")

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

		stopRestPoller()
		hadPoller := restPollerWasStarted
		restPollerWasStarted = false

		// If top-level context was canceled, this is an intentional shutdown.
		if streamCtx.Err() != nil {
			stopRestPoller()
			w.tracker.setState("stopped")
			return nil
		}

		// Classify error and record in circuit breaker.
		errClass := ClassifyAndRecordError(connErr, w.tracker)

		// Check for stale-triggered reconnect (connCtx canceled but streamCtx still alive).
		wasStaleReset := HandleStaleReset(connCtx, streamCtx, connErr, w.tracker, log.Logger)
		errClassName := [...]string{"transient", "ghost", "fatal"}
		ecName := "unknown"
		if int(errClass) < len(errClassName) {
			ecName = errClassName[errClass]
		}
		log.Info().Str("feed", w.feed).Bool("was_stale_reset", wasStaleReset).
			Bool("has_fetcher", w.fetcher != nil).Str("err_class", ecName).
			Int64("ws_bars", wsBarCount.Load()).
			Msg("equity WS: reconnect decision point")
		if wasStaleReset && w.metrics != nil {
			w.metrics.WS.ReconnectsTotal.WithLabelValues(w.feed, "stale").Inc()
		}

		// Gap-fill after REST poller was active (async with timeout to avoid
		// blocking the reconnect loop — gapFill calls callHandler which can
		// block on downstream processing).
		if hadPoller && w.fetcher != nil {
			go func() {
				gfCtx, gfCancel := context.WithTimeout(streamCtx, 30*time.Second)
				defer gfCancel()
				w.gapFill(gfCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler)
			}()
		}

		// Start REST poller on stale reset to bridge data during reconnect.
		if wasStaleReset && w.fetcher != nil {
			dedupMu.Lock()
			dedup = make(map[string]struct{})
			dedupMu.Unlock()
			startRestPoller()
		}

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
			if hadPoller && w.fetcher != nil {
				startRestPoller()
			}
			consecutiveFails++
			continue
		}

		if !isConnLimit {
			// Normal transient error — policy-based backoff.
			consecutiveFails++
			// BUG FIX: restart REST poller if one was running before the failed reconnect
			if !wasStaleReset && hadPoller && w.fetcher != nil {
				startRestPoller()
			}
			wait, shouldContinue := CalculateBackoff(errClass, wasStaleReset, connErr, connectedAt, consecutiveFails, attempt, log.Logger)
			if !shouldContinue {
				consecutiveFails++
				continue
			}
			select {
			case <-time.After(wait):
			case <-streamCtx.Done():
				stopRestPoller()
				w.tracker.setState("stopped")
				return nil
			}
			continue
		}

		// Ghost-session scenario — probe reconnect with REST bridge.
		stopRestPoller() // stop any stale-triggered REST poller before ghost takes over
		w.tracker.incGhostWindow()
		w.tracker.setState("ghost_probe")
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

		// Probe reconnect loop — capped at maxGhostProbes to prevent infinite loops.
		probeIdx := 0
		reconnected := false
		for !reconnected && probeIdx < maxGhostProbes {
			wait := ghostWait(probeIdx)
			probeIdx++
			log.Info().Dur("retry_in", wait).Int("probe", probeIdx).Int("max", maxGhostProbes).
				Msg("ghost session: probing reconnect")

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
				sym, err := domain.NewSymbol(bar.Symbol)
				if err != nil {
					return
				}
				tf, _ := domain.NewTimeframe("1m")
				domainBar, err := domain.NewMarketBar(bar.Timestamp, sym, tf, bar.Open, bar.High, bar.Low, bar.Close, float64(bar.Volume))
				if err != nil {
					return
				}
				domainBar.TradeCount = bar.TradeCount
				_ = callHandler(streamCtx, domainBar, false)
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

			// No bar received. Classify by error using SDK typed errors.
			isStillGhost := probeErr != nil && errors.Is(probeErr, alpacastream.ErrConnectionLimitExceeded)
			switch {
			case probeErr == nil:
				// nil = clean close (ghost gone).
				log.Info().Int("probe", probeIdx).Msg("ghost session: clean probe close — ghost cleared")
				reconnected = true
			case probeCtx.Err() == context.DeadlineExceeded:
				// DeadlineExceeded, no bar — SDK was mid-retry, ghost still alive.
				log.Info().Int("probe", probeIdx).Msg("ghost session: probe timeout with no bar — still alive")
			case isStillGhost:
				log.Info().Int("probe", probeIdx).Err(probeErr).Msg("ghost session: 406 — still alive")
			default:
				// Some other error — treat as ghost gone.
				log.Info().Int("probe", probeIdx).Err(probeErr).Msg("ghost session: non-406 error — ghost cleared")
				reconnected = true
			}
		}

		// Stop REST poller.
		if pollCancel != nil {
			pollCancel()
		}

		ghostDur := time.Since(ghostStart)

		// If max probes exhausted without reconnection, enter cold turkey silence
		// to let Alpaca's server-side session timeout expire naturally (~60-90s).
		if !reconnected {
			log.Warn().Int("probes", probeIdx).Dur("cold_turkey", ghostColdTurkeyPeriod).
				Msg("ghost session: max probes reached — entering cold turkey silence to let Alpaca session expire")
			w.tracker.setState("cold_turkey")
			select {
			case <-time.After(ghostColdTurkeyPeriod):
			case <-streamCtx.Done():
				w.tracker.setState("stopped")
				return nil
			}
			log.Info().Dur("ghost_duration", ghostDur+ghostColdTurkeyPeriod).
				Msg("ghost session: cold turkey complete — resuming reconnect loop")
		} else {
			log.Info().Dur("ghost_duration", ghostDur).Msg("ghost session resolved")
		}

		// Gap-fill: fetch any bars missed between last REST poll and WS resume
		// (async to avoid blocking the reconnect loop).
		if w.fetcher != nil {
			go func() {
				gfCtx, gfCancel := context.WithTimeout(streamCtx, 30*time.Second)
				defer gfCancel()
				w.gapFill(gfCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler)
			}()
		}

		// Reset counters — we've successfully reconnected.
		attempt = 0
		consecutiveFails = 0
	}
}

const (
	pipelineStaleThreshold  = 30 * time.Second
	networkHealthyThreshold = 10 * time.Second
)

// IsPipelineDeadlocked returns true when the network is actively delivering
// bars but the processing pipeline has stalled — indicating a deadlock.
func IsPipelineDeadlocked(networkAge, pipelineAge time.Duration) bool {
	return networkAge < networkHealthyThreshold && pipelineAge > pipelineStaleThreshold
}

func dumpGoroutineProfile() string {
	ts := time.Now().Format("20060102-150405")
	path := fmt.Sprintf("/tmp/omo-pipeline-deadlock-%s.prof", ts)
	f, err := os.Create(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	_ = pprof.Lookup("goroutine").WriteTo(f, 1)
	return path
}

func staleFeedWatchdog(ctx context.Context, tracker *feedTracker, cancelMu *sync.Mutex, cancelFn *context.CancelFunc, threshold time.Duration, shouldMonitor func() bool, pipeline ports.PipelineHealthReporter, feedType string) {
	ticker := time.NewTicker(staleFeedCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !shouldMonitor() {
				continue
			}

			tracker.mu.Lock()
			lastBar := tracker.lastBarAt
			tracker.mu.Unlock()

			if lastBar.IsZero() {
				continue
			}

			networkAge := time.Since(lastBar)

			if pipeline != nil {
				pipelineLast := pipeline.LastProcessedAt(feedType)
				if !pipelineLast.IsZero() {
					pipelineAge := time.Since(pipelineLast)
					if IsPipelineDeadlocked(networkAge, pipelineAge) {
						profPath := dumpGoroutineProfile()
						log.Fatal().
							Dur("network_age", networkAge).
							Dur("pipeline_age", pipelineAge).
							Str("feed_type", feedType).
							Str("goroutine_dump", profPath).
							Msg("pipeline deadlock detected: network healthy but pipeline stalled — forcing restart")
						return
					}
				}
			}

			if networkAge > threshold {
				log.Warn().Dur("bar_age", networkAge).Dur("threshold", threshold).
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
	log.Info().Str("feed", w.feed).Int("symbols", len(symbols)).Dur("interval", pollInterval).
		Msg("equity REST poller goroutine: entered")
	defer log.Info().Str("feed", w.feed).Msg("equity REST poller goroutine: exiting")

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// pollOnce performs a single poll cycle for all symbols.
	pollOnce := func() {
		now := time.Now()
		totalBars := 0
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

			totalBars += len(bars)
			for _, bar := range bars {
				if err := callHandler(ctx, bar, true); err != nil {
					if ctx.Err() == nil {
						log.Warn().Err(err).Str("symbol", string(sym)).Msg("REST poller: handler error")
					}
				}
			}
		}
		if ctx.Err() == nil {
			log.Info().Str("feed", w.feed).Int("bars_delivered", totalBars).Int("symbols_polled", len(symbols)).
				Msg("equity REST poller: poll cycle complete")
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

// isCoreMarketHours reports whether the current time falls within US equity core
// market hours in the America/Chicago timezone: 08:30–15:00 local.
//
// When the Go timezone database is available (normal images), time.LoadLocation
// handles DST automatically. For distroless images without tzdata, we fall back
// to computing the offset manually: UTC-5 during CDT, UTC-6 during CST.
func isCoreMarketHours() bool {
	cst, err := time.LoadLocation("America/Chicago")
	if err != nil {
		// Distroless fallback: compute UTC offset based on DST rules.
		// US DST: second Sunday of March 02:00 → first Sunday of November 02:00.
		cst = chicagoFallbackZone()
	}
	now := time.Now().In(cst)
	h, m, _ := now.Clock()
	minutes := h*60 + m
	return minutes >= 8*60+30 && minutes < 15*60 // 08:30–15:00 Chicago local
}

// chicagoFallbackZone returns a fixed time.Location for America/Chicago
// that accounts for US Daylight Saving Time. Used only when the system
// timezone database is unavailable (e.g. distroless containers).
func chicagoFallbackZone() *time.Location {
	now := time.Now().UTC()
	if isDST(now) {
		return time.FixedZone("CDT", -5*60*60)
	}
	return time.FixedZone("CST", -6*60*60)
}

// isDST reports whether the given UTC time falls within US Daylight Saving Time.
// DST starts: second Sunday of March at 02:00 CST (08:00 UTC).
// DST ends: first Sunday of November at 02:00 CDT (07:00 UTC).
func isDST(utcNow time.Time) bool {
	year := utcNow.Year()

	// Find second Sunday of March.
	march1 := time.Date(year, time.March, 1, 0, 0, 0, 0, time.UTC)
	daysToSunday := (7 - int(march1.Weekday())) % 7
	secondSunday := march1.AddDate(0, 0, daysToSunday+7)
	// DST starts at 02:00 CST = 08:00 UTC on that Sunday.
	dstStart := time.Date(year, time.March, secondSunday.Day(), 8, 0, 0, 0, time.UTC)

	// Find first Sunday of November.
	nov1 := time.Date(year, time.November, 1, 0, 0, 0, 0, time.UTC)
	daysToSunday = (7 - int(nov1.Weekday())) % 7
	if daysToSunday == 0 {
		daysToSunday = 0 // Nov 1 is already Sunday
	}
	firstSunday := nov1.AddDate(0, 0, daysToSunday)
	// DST ends at 02:00 CDT = 07:00 UTC on that Sunday.
	dstEnd := time.Date(year, time.November, firstSunday.Day(), 7, 0, 0, 0, time.UTC)

	return utcNow.After(dstStart) && utcNow.Before(dstEnd)
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
