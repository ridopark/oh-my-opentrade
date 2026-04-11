package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/gate"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/screener"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)
// DNAGateChecker decides whether a strategy's DNA is approved for trading.
// If no checker is set on the service, all setups pass through (backward compat).
type DNAGateChecker interface {
	IsDNAApproved(ctx context.Context, strategyKey string) (bool, error)
}
const settlingBars = 5
var anchorTimeframes = []domain.Timeframe{"5m", "15m", "1h"}
// Service is the monitor application service.
// It subscribes to MarketBarSanitized events, computes technical indicators,
// detects market regime shifts, and identifies trade setups.
type Service struct {
	eventBus         ports.EventBusPort
	repo             ports.RepositoryPort
	calculator       *IndicatorCalculator
	regimeDetector   *RegimeDetector
	orbTracker       *ORBTracker
	orbCfg           ORBConfig
	mu               sync.Mutex
	baseSymbols      map[string]struct{}
	effectiveSymbols map[string]struct{}
	lastSnaps        map[string]domain.IndicatorSnapshot
	liveBars         map[string]int
	aggregators      map[string]*domain.BarAggregator
	// aggKeysBySym caches the per-symbol aggregator key strings ("sym:tf"),
	// parallel to anchorTimeframes. Populated by InitAggregators so the hot
	// HandleMarketBar loop can iterate without re-concatenating strings on
	// every bar (was ~880k allocs per backtest).
	aggKeysBySym map[string][]string
	// anchorRegimeMaps holds a reusable AnchorRegimes map per symbol so
	// HandleMarketBar doesn't allocate a fresh map on every bar. Was ~320MB
	// of bucket allocations per backtest. Safe because each symbol owns its
	// slot — lastSnaps[symX] just aliases the same reference and is
	// consistent with the "latest snap" semantics.
	anchorRegimeMaps map[string]map[domain.Timeframe]domain.MarketRegime
	// htfDataMaps is the same idea for snap.HTF maps built by buildHTFMap.
	htfDataMaps map[string]map[domain.Timeframe]domain.HTFData
	orbAggregators map[string]*domain.BarAggregator // per-symbol 5m aggregators for ORB tracker
	orbTimeframe     domain.Timeframe                 // timeframe for ORB bar delivery (default "5m")
	anchorRegimes    map[string]domain.MarketRegime
	lastHTFSnaps     map[string]domain.IndicatorSnapshot
	htfStatic        map[string]domain.HTFData
	readySymbols     map[string]struct{}
	log              zerolog.Logger
	dnaGate          DNAGateChecker
	strategyKey      string
	vixLevel           float64  // latest VIX close; 0 = unknown (allow all)
	vixSkipAbove       float64  // skip ORB when VIX > this (0 = disabled)
	vixWidenAbove      float64  // widen stops when VIX > this (0 = disabled)
	orbAllowedRegimes  []string // regime gate for ORB (empty = allow all)
	orbHTFBiasEnabled  bool     // block entries against daily EMA50 bias
	orbMinATRPct       float64  // skip symbols with daily ATR% below this
	avwapFn            func(symbol string) map[string]float64
	monitorGateChain   *gate.MonitorGateChain
	tideTracker        *gate.IndexTideTracker

	// Standalone AVWAP computation for all streaming symbols (independent of strategy assignment)
	avwapCalcs       map[string]*start.AnchoredVWAPCalc
	avwapAnchors     []string // e.g. ["session_open", "pd_high", "pd_low"]
	avwapLastSession    map[string]string // symbol → last resolved session date (ET) — legacy
	avwapLastSessionInt map[string]int    // symbol → YYYYMMDD int for cheap date comparison

	// directDispatch flips HandleMarketBar into collect-only mode for the
	// backtest Pipeline. When set, the hot path fills pendingStrict and
	// pendingBestEffort instead of calling eventBus.Publish, and the caller
	// drains them via DrainPending{Strict,BestEffort}. Single-goroutine use
	// only (Pipeline.ProcessBar is serial).
	directDispatch     bool
	pendingStrict      []domain.Event
	pendingBestEffort  []domain.Event
	anchorResolverFn func(symbol string, barTime time.Time, anchors []string) map[string]time.Time
	prevDayBarsFn    func(symbol string, since time.Time) []start.Bar
	nyLoc            *time.Location // cached America/New_York location
}
// SetAVWAPFn installs a function that returns current anchored VWAP values
// for a symbol. The enriched bar event will include these values when set.
func (s *Service) SetAVWAPFn(fn func(symbol string) map[string]float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.avwapFn = fn
}

// SetDirectDispatch flips HandleMarketBar into collect-only mode. When
// enabled, events that would have been published to the bus are appended
// to internal slices; the caller must retrieve them via DrainPending.
// Single-goroutine use only — safe because replay's Pipeline serializes
// monitor access through one goroutine.
func (s *Service) SetDirectDispatch(v bool) {
	s.directDispatch = v
}

// DrainPending returns the pending strict and best-effort events collected
// during the most recent direct-dispatch HandleMarketBar call, and clears
// the internal slices. Only meaningful when SetDirectDispatch(true).
func (s *Service) DrainPending() (strict, bestEffort []domain.Event) {
	strict = s.pendingStrict
	bestEffort = s.pendingBestEffort
	s.pendingStrict = s.pendingStrict[:0]
	s.pendingBestEffort = s.pendingBestEffort[:0]
	return strict, bestEffort
}

// SetAnchorResolverFn installs a function that resolves anchor times (pd_high, pd_low,
// session_open) from session data. Used for standalone AVWAP computation.
func (s *Service) SetAnchorResolverFn(fn func(symbol string, barTime time.Time, anchors []string) map[string]time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.anchorResolverFn = fn
}

// SetPrevDayBarsFn installs a function that returns previous-day 1m bars from a given
// time for replaying into AVWAP anchors (pd_high, pd_low).
func (s *Service) SetPrevDayBarsFn(fn func(symbol string, since time.Time) []start.Bar) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prevDayBarsFn = fn
}

// SetAVWAPAnchors configures which anchors the standalone AVWAP calculator uses.
func (s *Service) SetAVWAPAnchors(anchors []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.avwapAnchors = anchors
}

// GetLastSnapshot returns the most recently cached IndicatorSnapshot for the given symbol.
// Returns false if no snapshot has been cached yet.
func (s *Service) GetLastSnapshot(symbol string) (domain.IndicatorSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.lastSnaps[symbol]
	return snap, ok
}
// BarSnapshot pairs a market bar with its computed indicator snapshot.
type BarSnapshot struct {
	Bar      domain.MarketBar
	Snapshot domain.IndicatorSnapshot
}
// WarmUpAndCollect processes historical bars through the indicator calculator
// and returns per-bar indicator snapshots. It does NOT emit events or persist data.
// Returns a slice of (MarketBar, IndicatorSnapshot) pairs for use by downstream warmup consumers.
func (s *Service) WarmUpAndCollect(bars []domain.MarketBar) []BarSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]BarSnapshot, 0, len(bars))
	for _, bar := range bars {
		snap := s.calculator.Update(bar)
		result = append(result, BarSnapshot{Bar: bar, Snapshot: snap})
	}
	return result
}
func (s *Service) SetStaticHTFData(sym string, tf domain.Timeframe, data domain.HTFData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.htfStatic == nil {
		s.htfStatic = make(map[string]domain.HTFData)
	}
	s.htfStatic[sym+":"+tf.String()] = data
}
// SeedHTFSnapshot seeds the calculator state and stores an HTF snapshot
// from pre-computed EMA values. This avoids replaying bars through Update().
func (s *Service) SeedHTFSnapshot(sym string, tf domain.Timeframe, snap domain.IndicatorSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calculator.SeedState(sym, tf.String(), snap.EMA9, snap.EMA21, snap.EMA50)
	if s.lastHTFSnaps == nil {
		s.lastHTFSnaps = make(map[string]domain.IndicatorSnapshot)
	}
	s.lastHTFSnaps[sym+":"+tf.String()] = snap
}

// GetHTFSnapshot returns the stored HTF snapshot for a symbol:timeframe key.
func (s *Service) GetHTFSnapshot(sym string, tf domain.Timeframe) (domain.IndicatorSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.lastHTFSnaps[sym+":"+tf.String()]
	return snap, ok
}

func (s *Service) WarmUpHTF(bars []domain.MarketBar) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lastSnap domain.IndicatorSnapshot
	for _, bar := range bars {
		lastSnap = s.calculator.Update(bar)
	}
	if len(bars) > 0 {
		sym := bars[0].Symbol.String()
		tf := bars[0].Timeframe.String()
		key := sym + ":" + tf
		if s.lastHTFSnaps == nil {
			s.lastHTFSnaps = make(map[string]domain.IndicatorSnapshot)
		}
		s.lastHTFSnaps[key] = lastSnap
	}
	return len(bars)
}
// NewService creates a new monitor Service.
func NewService(eventBus ports.EventBusPort, repo ports.RepositoryPort, log zerolog.Logger) *Service {
	nyLoc, _ := time.LoadLocation("America/New_York")
	return &Service{
		eventBus:         eventBus,
		repo:             repo,
		calculator:       NewIndicatorCalculator(),
		regimeDetector:   NewRegimeDetector(),
		orbTracker:       NewORBTrackerWithSource("monitor"),
		orbCfg:           DefaultORBConfig(),
		lastSnaps:        make(map[string]domain.IndicatorSnapshot),
		liveBars:         make(map[string]int),
		aggregators:      make(map[string]*domain.BarAggregator),
		aggKeysBySym:     make(map[string][]string),
		anchorRegimeMaps: make(map[string]map[domain.Timeframe]domain.MarketRegime),
		htfDataMaps:      make(map[string]map[domain.Timeframe]domain.HTFData),
		orbAggregators:   make(map[string]*domain.BarAggregator),
		orbTimeframe:     "5m",
		anchorRegimes:    make(map[string]domain.MarketRegime),
		avwapCalcs:       make(map[string]*start.AnchoredVWAPCalc),
		avwapLastSession:    make(map[string]string),
		avwapLastSessionInt: make(map[string]int),
		nyLoc:            nyLoc,
		log:              log,
	}
}
// TagBacktest annotates the ORB tracker's slog logger with backtest_id so
// that backtest ORB log lines are distinguishable from live ones.
func (s *Service) TagBacktest(backtestID string) {
	l := slog.Default().With("source", "monitor", "backtest_id", backtestID)
	s.orbTracker.SetLogger(l)
}

// SetORBConfig overrides the default ORB configuration with values from
// strategy DNA parameters. This must be called before Start() to ensure
// the ORB tracker uses DNA-configured thresholds (min_rvol, min_confidence, etc.)
// instead of hardcoded defaults.
func (s *Service) SetORBConfig(params map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orbCfg = NewORBConfigFromDNA(params)
	s.vixSkipAbove = s.orbCfg.VIXSkipAbove
	s.vixWidenAbove = s.orbCfg.VIXWidenAbove
	s.orbAllowedRegimes = s.orbCfg.AllowedRegimes
	s.orbHTFBiasEnabled = s.orbCfg.HTFBiasEnabled
	s.orbMinATRPct = s.orbCfg.MinATRPct
	s.log.Info().
		Float64("min_rvol", s.orbCfg.MinRVOL).
		Float64("min_confidence", s.orbCfg.MinConfidence).
		Int("orb_window_minutes", s.orbCfg.WindowMinutes).
		Float64("vix_skip_above", s.vixSkipAbove).
		Float64("vix_widen_above", s.vixWidenAbove).
		Msg("ORB config set from DNA parameters")
}
// SetORBTimeframe configures the bar timeframe for the ORB tracker.
// When set to "5m" (the default), the ORB tracker receives aggregated 5m bars
// so entries align to 5-minute boundaries. Call before Start().
func (s *Service) SetORBTimeframe(tf string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tf == "" {
		tf = "5m"
	}
	s.orbTimeframe = domain.Timeframe(tf)
	s.log.Info().Str("orb_timeframe", tf).Msg("ORB timeframe set")
}
// SetVIXLevel sets the current VIX level for ORB regime gating.
func (s *Service) SetVIXLevel(level float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vixLevel = level
	s.log.Info().Float64("vix", level).Msg("VIX level set")
}
// SetVIXThresholds configures VIX-based gating. skipAbove: skip ORB entirely.
// widenAbove: signal debate service to widen stops.
func (s *Service) SetVIXThresholds(skipAbove, widenAbove float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vixSkipAbove = skipAbove
	s.vixWidenAbove = widenAbove
	s.log.Info().Float64("skip_above", skipAbove).Float64("widen_above", widenAbove).Msg("VIX thresholds set")
}
// SetDNAGate installs a gate checker that blocks SetupDetected events when the
// active DNA version for strategyKey is not approved. If checker is nil the gate
// is disabled and all setups pass through.
func (s *Service) RegisterEMAConfig(symbols []string, timeframes []string, params map[string]any) {
	fast, slow := extractIntParam(params, "ema_fast", 0), extractIntParam(params, "ema_slow", 0)
	threshold := extractFloat64Param(params, "ema_divergence_threshold_pct", 0) / 100.0
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sym := range symbols {
		for _, tf := range timeframes {
			if fast > 0 && slow > 0 && fast < slow {
				s.calculator.RegisterEMAConfig(sym, tf, fast, slow)
			}
			if threshold > 0 {
				s.regimeDetector.RegisterDivergenceThreshold(sym, tf, threshold)
			}
		}
	}
}
func extractIntParam(params map[string]any, key string, def int) int {
	if v, ok := params[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return def
}
func extractFloat64Param(params map[string]any, key string, def float64) float64 {
	if v, ok := params[key]; ok {
		if n, ok := v.(float64); ok {
			return n
		}
	}
	return def
}
func (s *Service) SetDNAGate(checker DNAGateChecker, strategyKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dnaGate = checker
	s.strategyKey = strategyKey
	s.log.Info().
		Str("strategy_key", strategyKey).
		Bool("gate_enabled", checker != nil).
		Msg("DNA gate configured")
}
// SetMonitorGateChain installs a gate chain that replaces the hardcoded gate
// if-blocks. When set, detected setups are evaluated through the chain; when
// nil, the legacy inline gates are used (backward compat).
func (s *Service) SetMonitorGateChain(chain *gate.MonitorGateChain) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.monitorGateChain = chain
	if chain != nil {
		s.log.Info().Strs("gates", chain.Names()).Msg("monitor gate chain installed")
	}
}

// SetTideTracker installs an IndexTideTracker that is fed every 1m bar to
// maintain running VWAP for SPY/QQQ. The market_tide gate reads from this.
func (s *Service) SetTideTracker(tracker *gate.IndexTideTracker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tideTracker = tracker
	s.log.Info().Bool("enabled", tracker != nil).Msg("tide tracker configured")
}

func (s *Service) SetBaseSymbols(symbols []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseSymbols = make(map[string]struct{}, len(symbols))
	for _, sym := range symbols {
		s.baseSymbols[sym] = struct{}{}
	}
	s.effectiveSymbols = nil
	if s.readySymbols == nil {
		s.readySymbols = make(map[string]struct{})
	}
	s.log.Info().Strs("symbols", symbols).Msg("base symbols configured")
}
func (s *Service) isAllowedSymbolLocked(sym string) bool {
	if s.baseSymbols == nil {
		return true
	}
	if s.effectiveSymbols != nil {
		_, ok := s.effectiveSymbols[sym]
		if !ok {
			return false
		}
		if s.readySymbols != nil {
			_, ready := s.readySymbols[sym]
			return ready
		}
		return true
	}
	_, ok := s.baseSymbols[sym]
	if !ok {
		return false
	}
	if s.readySymbols != nil {
		_, ready := s.readySymbols[sym]
		return ready
	}
	return true
}
func (s *Service) MarkReady(symbols ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readySymbols == nil {
		s.readySymbols = make(map[string]struct{})
	}
	for _, sym := range symbols {
		s.readySymbols[sym] = struct{}{}
	}
	s.log.Info().Strs("symbols", symbols).Int("total_ready", len(s.readySymbols)).Msg("symbols marked ready")
}
func (s *Service) IsReady(sym string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readySymbols == nil {
		return false
	}
	_, ok := s.readySymbols[sym]
	return ok
}
func (s *Service) ResetSessionIndicators(symbol string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calculator.ResetSession(symbol, "1m")
	for _, tf := range anchorTimeframes {
		s.calculator.ResetSession(symbol, tf.String())
	}
}
func (s *Service) InitAggregators(symbols []domain.Symbol, sessionOpen time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sym := range symbols {
		symStr := sym.String()
		keys := make([]string, len(anchorTimeframes))
		for i, tf := range anchorTimeframes {
			key := symStr + ":" + tf.String()
			keys[i] = key
			agg, err := domain.NewBarAggregator(sym, tf, sessionOpen)
			if err != nil {
				continue
			}
			s.aggregators[key] = agg
		}
		s.aggKeysBySym[symStr] = keys
		// ORB aggregator for the configured timeframe
		if s.orbTimeframe != "" && s.orbTimeframe != "1m" {
			orbAgg, err := domain.NewBarAggregator(sym, s.orbTimeframe, sessionOpen)
			if err == nil {
				s.orbAggregators[symStr] = orbAgg
			}
		}
	}
}
func (s *Service) ResetAggregators(sessionOpen time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, agg := range s.aggregators {
		agg.Reset(sessionOpen)
	}
	for _, agg := range s.orbAggregators {
		agg.Reset(sessionOpen)
	}
}
// Start subscribes the service to incoming sanitized market data events.
func (s *Service) Start(ctx context.Context) error {
	err := s.eventBus.Subscribe(ctx, domain.EventMarketBarSanitized, s.HandleMarketBar)
	if err != nil {
		return fmt.Errorf("monitor: failed to subscribe to MarketBarSanitized: %w", err)
	}
	if err := s.eventBus.Subscribe(ctx, domain.EventEffectiveSymbolsUpdated, s.handleEffectiveSymbolsUpdated); err != nil {
		return fmt.Errorf("monitor: failed to subscribe to EffectiveSymbolsUpdated: %w", err)
	}
	s.log.Info().Msg("subscribed to MarketBarSanitized and EffectiveSymbolsUpdated events")
	return nil
}

// StartEnrichedBarPublisher subscribes a SECOND handler to MarketBarSanitized
// that publishes EnrichedBar events. This MUST be called AFTER the strategy
// runner has started, so that the runner's handleBar (which updates the AVWAP
// calculator) runs before this handler reads AVWAP values. The event bus
// dispatches synchronous handlers in subscription order.
func (s *Service) StartEnrichedBarPublisher(ctx context.Context) error {
	err := s.eventBus.Subscribe(ctx, domain.EventMarketBarSanitized, s.publishEnrichedBar)
	if err != nil {
		return fmt.Errorf("monitor: failed to subscribe enriched bar publisher: %w", err)
	}
	s.log.Info().Msg("enriched bar publisher subscribed (runs after strategy runner)")
	return nil
}

// publishEnrichedBar is a MarketBarSanitized handler that builds and publishes
// an EnrichedBar event combining bar OHLCV, cached indicator snapshot, and
// current AVWAP values. Because it is subscribed after the strategy runner,
// GetAVWAPValues returns values that include the current bar.
func (s *Service) publishEnrichedBar(ctx context.Context, event domain.Event) error {
	bar, ok := event.Payload.(domain.MarketBar)
	if !ok {
		return nil
	}
	symStr := bar.Symbol.String()

	s.mu.Lock()
	snap, hasSnap := s.lastSnaps[symStr]
	avwapFn := s.avwapFn
	calc, hasCalc := s.avwapCalcs[symStr]
	s.mu.Unlock()

	if !hasSnap {
		return nil
	}

	enriched := domain.EnrichedBarPayload{
		Time:      bar.Time.Unix(),
		Symbol:    symStr,
		Timeframe: string(bar.Timeframe),
		Open:      bar.Open,
		High:      bar.High,
		Low:       bar.Low,
		Close:     bar.Close,
		Volume:    bar.Volume,
		EMA9:      snap.EMA9,
		EMA21:     snap.EMA21,
	}
	if snap.EMA50 > 0 {
		enriched.EMA50 = snap.EMA50
	}
	if snap.EMA200 > 0 {
		enriched.EMA200 = snap.EMA200
	}

	// Standalone AVWAP values from monitor's own calculator (covers all streaming symbols).
	if hasCalc {
		if vals := calc.Values(); len(vals) > 0 {
			avwaps := make(map[string]float64, len(vals))
			for k, v := range vals {
				if v > 0 {
					avwaps[k] = v
				}
			}
			if len(avwaps) > 0 {
				enriched.AVWAPs = avwaps
			}
		}
	}
	// Fall back to strategy runner's AVWAP for symbols with dynamic anchors (swing_high, etc.)
	if enriched.AVWAPs == nil && avwapFn != nil {
		if vals := avwapFn(symStr); len(vals) > 0 {
			avwaps := make(map[string]float64, len(vals))
			for k, v := range vals {
				if v > 0 {
					avwaps[k] = v
				}
			}
			if len(avwaps) > 0 {
				enriched.AVWAPs = avwaps
			}
		}
	}

	ev, err := domain.NewEvent(
		domain.EventEnrichedBar,
		event.TenantID,
		event.EnvMode,
		event.IdempotencyKey+"-enriched",
		enriched,
	)
	if err != nil {
		return nil
	}
	_ = s.eventBus.Publish(ctx, *ev)
	return nil
}

func (s *Service) handleEffectiveSymbolsUpdated(ctx context.Context, evt domain.Event) error {
	payload, ok := evt.Payload.(screener.EffectiveSymbolsUpdatedPayload)
	if !ok {
		return fmt.Errorf("monitor: effective symbols payload is not EffectiveSymbolsUpdatedPayload, got %T", evt.Payload)
	}
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	s.effectiveSymbols = make(map[string]struct{}, len(payload.Symbols))
	for _, sym := range payload.Symbols {
		s.effectiveSymbols[sym] = struct{}{}
	}
	s.log.Info().
		Str("strategy", payload.StrategyKey).
		Str("source", payload.Source).
		Int("count", len(payload.Symbols)).
		Strs("symbols", payload.Symbols).
		Msg("effective symbols updated")
	return nil
}
// HandleMarketBar processes a single market bar event.
// It computes an indicator snapshot, detects regime changes,
// and checks for trade setup conditions. Emits StateUpdated,
// RegimeShifted (on regime change), and SetupDetected (on valid entry) events.
func (s *Service) HandleMarketBar(ctx context.Context, event domain.Event) error {
	bar, ok := event.Payload.(domain.MarketBar)
	if !ok {
		return fmt.Errorf("monitor: payload is not a MarketBar, got %T", event.Payload)
	}
	if bar.Timeframe != "1m" {
		return nil
	}
	// Skip building a child logger per bar when debug is disabled — the
	// downstream l.Debug() calls would already drop their work at the event
	// level, but .With().Str().Str().Logger() still allocates a new logger
	// context on every bar (~840k/backtest per pprof). Materialize the child
	// only when the parent logger is below info.
	var l zerolog.Logger
	if s.log.GetLevel() <= zerolog.DebugLevel {
		l = s.log.With().
			Str("symbol", string(bar.Symbol)).
			Str("timeframe", string(bar.Timeframe)).
			Logger()
	} else {
		l = s.log
	}
	var publishStrict []domain.Event
	var publishBestEffort []domain.Event
	s.mu.Lock()
	if !s.isAllowedSymbolLocked(string(bar.Symbol)) {
		s.mu.Unlock()
		return nil
	}
	snap := s.calculator.Update(bar)
	symStr := bar.Symbol.String()

	// Standalone AVWAP: resolve anchors on new session day, then update calculator.
	if s.anchorResolverFn != nil && len(s.avwapAnchors) > 0 {
		y, m, d := bar.Time.In(s.nyLoc).Date()
		barDateInt := y*10000 + int(m)*100 + d
		if s.avwapLastSessionInt[symStr] != barDateInt {
			s.avwapLastSessionInt[symStr] = barDateInt
			resolved := s.anchorResolverFn(symStr, bar.Time, s.avwapAnchors)
			if len(resolved) > 0 {
				calc := start.NewAnchoredVWAPCalc()
				for name, t := range resolved {
					calc.AddAnchor(start.AnchorPoint{Name: name, AnchorTime: t})
				}
				s.avwapCalcs[symStr] = calc

				// Replay prev-day bars for non-session_open anchors so they
				// accumulate volume from their actual anchor time.
				if s.prevDayBarsFn != nil {
					sortedNames := make([]string, 0, len(resolved))
					for name := range resolved {
						if name != "session_open" {
							sortedNames = append(sortedNames, name)
						}
					}
					sort.Strings(sortedNames)
					for _, name := range sortedNames {
						anchorTime := resolved[name]
						prevBars := s.prevDayBarsFn(symStr, anchorTime)
						for _, b := range prevBars {
							calc.UpdateSingleAnchor(name, b.Time, b.High, b.Low, b.Close, b.Volume)
						}
					}
				}
				s.log.Debug().
					Str("symbol", symStr).
					Int("anchors", len(resolved)).
					Msg("standalone AVWAP anchors resolved for new session")
			}
		}
		if calc, ok := s.avwapCalcs[symStr]; ok {
			calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
		}
	}

	aggKeys := s.aggKeysBySym[symStr]
	for i, tf := range anchorTimeframes {
		var aggKey string
		if i < len(aggKeys) {
			aggKey = aggKeys[i]
		} else {
			aggKey = symStr + ":" + tf.String()
		}
		agg, exists := s.aggregators[aggKey]
		if !exists {
			continue
		}
		closed, ok := agg.Push(bar)
		if !ok {
			continue
		}
		barEv := domain.NewBacktestEvent(
			domain.EventMarketBarSanitized,
			event.TenantID,
			event.EnvMode,
			event.IdempotencyKey+"-"+tf.String()+"-htf-bar",
			closed,
			event.OccurredAt,
		)
		publishBestEffort = append(publishBestEffort, barEv)
		htfSnap := s.calculator.Update(closed)
		reg, changedAnchor := s.regimeDetector.Detect(htfSnap)
		s.anchorRegimes[aggKey] = reg
		if changedAnchor {
			regimeShiftedEv := domain.NewBacktestEvent(
				domain.EventRegimeShifted,
				event.TenantID,
				event.EnvMode,
				event.IdempotencyKey+"-"+tf.String()+"-regime-shifted",
				reg,
				event.OccurredAt,
			)
			publishBestEffort = append(publishBestEffort, regimeShiftedEv)
		}
		if tf == "1h" {
			if s.lastHTFSnaps == nil {
				s.lastHTFSnaps = make(map[string]domain.IndicatorSnapshot)
			}
			s.lastHTFSnaps[aggKey] = htfSnap
		}
	}
	// Reuse the per-symbol map across bars. The map is stable for this
	// symbol (cleared + refilled on every call), so lastSnaps[symStr]
	// continues to reflect the latest snap correctly.
	regimeMap, ok := s.anchorRegimeMaps[symStr]
	if !ok {
		regimeMap = make(map[domain.Timeframe]domain.MarketRegime, len(anchorTimeframes)+1)
		s.anchorRegimeMaps[symStr] = regimeMap
	} else {
		clear(regimeMap)
	}
	snap.AnchorRegimes = regimeMap
	for i, tf := range anchorTimeframes {
		var aggKey string
		if i < len(aggKeys) {
			aggKey = aggKeys[i]
		} else {
			aggKey = symStr + ":" + tf.String()
		}
		if reg, ok := s.anchorRegimes[aggKey]; ok {
			snap.AnchorRegimes[tf] = reg
		}
	}
	snap.HTF = s.buildHTFMap(symStr, bar.Close)
	s.liveBars[symStr]++
	regime, changed := s.regimeDetector.Detect(snap)
	snap.AnchorRegimes[bar.Timeframe] = regime
	l.Debug().
		Float64("close", bar.Close).
		Float64("volume", bar.Volume).
		Float64("rsi", snap.RSI).
		Float64("stoch_k", snap.StochK).
		Float64("stoch_d", snap.StochD).
		Float64("ema9", snap.EMA9).
		Float64("ema21", snap.EMA21).
		Float64("vwap", snap.VWAP).
		Float64("volume_sma", snap.VolumeSMA).
		Str("regime", string(regime.Type)).
		Float64("regime_strength", regime.Strength).
		Msg("indicator snapshot")
	stateUpdatedEv := domain.NewBacktestEvent(
		domain.EventStateUpdated,
		event.TenantID,
		event.EnvMode,
		event.IdempotencyKey+"-state-updated",
		snap,
		event.OccurredAt,
	)
	publishStrict = append(publishStrict, stateUpdatedEv)
	l.Debug().
		Str("regime", string(regime.Type)).
		Float64("strength", regime.Strength).
		Bool("changed", changed).
		Msg("regime classification")
	if changed {
		l.Info().Str("regime", string(regime.Type)).Msg("market regime shifted")
		regimeShiftedEv := domain.NewBacktestEvent(
			domain.EventRegimeShifted,
			event.TenantID,
			event.EnvMode,
			event.IdempotencyKey+"-regime-shifted",
			regime,
			event.OccurredAt,
		)
		publishStrict = append(publishStrict, regimeShiftedEv)
	}
	if s.liveBars[symStr] < settlingBars {
		s.feedORBBar(bar, snap, true)
		// Mark ORB range as already notified during settling so we don't
		// re-emit the notification when the first live bar arrives.
		if sess := s.orbTracker.GetSession(symStr); sess != nil &&
			sess.State == ORBStateRangeSet && !sess.RangeNotified {
			sess.RangeNotified = true
		}
		l.Debug().Msg(fmt.Sprintf("settling: %d/%d bars, suppressing setup detection", s.liveBars[symStr], settlingBars))
		s.lastSnaps[symStr] = snap

		s.mu.Unlock()

		if s.directDispatch {
			s.pendingStrict = append(s.pendingStrict, publishStrict...)
			s.pendingBestEffort = append(s.pendingBestEffort, publishBestEffort...)
			return nil
		}

		for _, ev := range publishStrict {
			if err := s.eventBus.Publish(ctx, ev); err != nil {
				return fmt.Errorf("monitor: failed to publish event %s: %w", ev.Type, err)
			}
		}
		for _, ev := range publishBestEffort {
			_ = s.eventBus.Publish(ctx, ev)
		}
		return nil
	}
	lastSnap, hasLast := s.lastSnaps[symStr]
	_ = hasLast
	_ = lastSnap
	// Update index tide tracker (SPY/QQQ VWAP) before setup gating.
	if s.tideTracker != nil {
		s.tideTracker.OnBar(bar)
	}

	setup, detected := s.feedORBBar(bar, snap, false)

	// Emit ORBRangeSet notification once per session when opening range locks.
	// Only notify if we're within a few minutes of the ORB window — skip stale
	// ranges from warmup replay, settling bars, or tracker cycling mid-session.
	orbNotifyCutoff := RTHOpenUTC(bar.Time).Add(time.Duration(s.orbCfg.WindowMinutes+10) * time.Minute)
	if sess := s.orbTracker.GetSession(symStr); sess != nil &&
		sess.State == ORBStateRangeSet && !sess.RangeNotified && !sess.RangeInvalid &&
		bar.Time.Before(orbNotifyCutoff) {
		sess.RangeNotified = true
		htfBias := ""
		atrPct := 0.0
		nr7 := false
		if htf, ok := snap.HTF[domain.Timeframe("1d")]; ok {
			htfBias = htf.Bias
			if htf.DailyATR > 0 && bar.Close > 0 {
				atrPct = htf.DailyATR / bar.Close * 100
			}
			nr7 = htf.NR7
		}
		orbRangeEv, err := domain.NewEvent(
			domain.EventORBRangeSet,
			event.TenantID,
			event.EnvMode,
			event.IdempotencyKey+"-orb-range-set",
			domain.ORBRangeSetPayload{
				Symbol:  bar.Symbol,
				High:    sess.OrbHigh,
				Low:     sess.OrbLow,
				Bars:    sess.RangeBarCount,
				HTFBias: htfBias,
				ATRPct:  atrPct,
				NR7:     nr7,
			},
		)
		if err == nil {
			publishBestEffort = append(publishBestEffort, *orbRangeEv)
		}
	}

	if detected && setup != nil {
		if s.monitorGateChain != nil {
			// New gate chain path: evaluate all gates in sequence.
			gctx := &gate.MonitorGateContext{
				Setup: gate.SetupInput{
					Direction:  setup.Direction,
					Confidence: setup.Confidence,
					RVOL:       setup.RVOL,
					Trigger:    setup.Trigger,
					ORBHigh:    setup.ORBHigh,
					ORBLow:     setup.ORBLow,
					Symbol:     bar.Symbol,
				},
				Bar:           bar,
				Snapshot:      snap,
				Regime:        regime,
				VIXLevel:      s.vixLevel,
				AnchorRegimes: s.anchorRegimes,
				ORBTimeframe:  s.orbTimeframe,
				StrategyKey:   s.strategyKey,
			}
			if result := s.monitorGateChain.Run(ctx, gctx); result != nil {
				l.Warn().
					Str("gate", result.GateName).
					Str("reason", result.Reason).
					Str("direction", string(setup.Direction)).
					Msg("setup blocked by gate chain")
				detected = false
			}
		} else {
			// Legacy inline gates (backward compat when no gate chain is configured).
			// DNA approval gate: suppress setup if DNA is not approved.
			if s.dnaGate != nil {
				approved, gateErr := s.dnaGate.IsDNAApproved(ctx, s.strategyKey)
				if gateErr != nil {
					l.Warn().Err(gateErr).Msg("DNA gate check failed, allowing setup")
				} else if !approved {
					l.Warn().
						Str("strategy_key", s.strategyKey).
						Str("direction", string(setup.Direction)).
						Float64("confidence", setup.Confidence).
						Msg("setup blocked: DNA version not approved")
					detected = false
				}
			}
			// VIX gate: skip ORB when VIX is too high.
			if detected && s.vixSkipAbove > 0 && s.vixLevel > s.vixSkipAbove {
				l.Warn().
					Float64("vix", s.vixLevel).
					Float64("threshold", s.vixSkipAbove).
					Str("direction", string(setup.Direction)).
					Msg("setup blocked: VIX too high")
				detected = false
			}
			// Regime gate: block ORB in disallowed regimes (uses strategy timeframe anchor if available).
			if detected && len(s.orbAllowedRegimes) > 0 {
				gateRegime := regime // fallback to 1m
				if s.orbTimeframe != "" && s.orbTimeframe != "1m" {
					if ar, ok := s.anchorRegimes[symStr+":"+string(s.orbTimeframe)]; ok && ar.Type != "" {
						gateRegime = ar
					}
				}
				regimeStr := string(gateRegime.Type)
				allowed := false
				for _, r := range s.orbAllowedRegimes {
					if r == regimeStr {
						allowed = true
						break
					}
				}
				if !allowed {
					l.Warn().
						Str("regime", regimeStr).
						Strs("allowed", s.orbAllowedRegimes).
						Str("direction", string(setup.Direction)).
						Msg("setup blocked: regime not allowed")
					detected = false
				}
			}
			// HTF bias gate: block entries against daily EMA50 trend direction.
			if detected && s.orbHTFBiasEnabled {
				if htf, ok := snap.HTF[domain.Timeframe("1d")]; ok && htf.Bias != "" && htf.Bias != "NEUTRAL" {
					blocked := false
					if setup.Direction == domain.DirectionLong && htf.Bias == "BEARISH" {
						blocked = true
					} else if setup.Direction == domain.DirectionShort && htf.Bias == "BULLISH" {
						blocked = true
					}
					if blocked {
						l.Warn().
							Str("direction", string(setup.Direction)).
							Str("daily_bias", htf.Bias).
							Msg("setup blocked: HTF bias against trade direction")
						detected = false
					}
				}
			}
			// Min ATR% gate: skip low-volatility symbols that don't move enough for ORB.
			if detected && s.orbMinATRPct > 0 {
				if dailyATR := snap.HTFDailyATR(); dailyATR > 0 && bar.Close > 0 {
					atrPct := dailyATR / bar.Close * 100
					if atrPct < s.orbMinATRPct {
						l.Warn().
							Float64("atr_pct", atrPct).
							Float64("min_atr_pct", s.orbMinATRPct).
							Str("symbol", symStr).
							Msg("setup blocked: symbol ATR% too low")
						detected = false
					}
				}
			}
		}
		if detected {
			// Use strategy-timeframe anchor regime (more stable than 1m) if available.
			anchorRegime := regime // fallback to 1m
			if s.orbTimeframe != "" && s.orbTimeframe != "1m" {
				if ar, ok := s.anchorRegimes[symStr+":"+string(s.orbTimeframe)]; ok && ar.Type != "" {
					anchorRegime = ar
				}
			}
			setup.Regime = anchorRegime
			// Tag VIX adjustment for downstream (debate service)
			if s.vixWidenAbove > 0 && s.vixLevel > s.vixWidenAbove {
				setup.VIXAdjust = "widen_stops"
			}
			// Populate regime labels for display
			setup.EMARegime = string(anchorRegime.Type)
			// VIX bucket
			switch {
			case s.vixLevel <= 0:
				setup.VIXBucket = "N/A"
			case s.vixLevel < 15:
				setup.VIXBucket = "LOW_VOL"
			case s.vixLevel <= 25:
				setup.VIXBucket = "NORMAL"
			default:
				setup.VIXBucket = "HIGH_VOL"
			}
			// Composite market context: VIX bucket + per-symbol ATR + NR7 + VWAP alignment
			ctx := setup.VIXBucket
			if dailyATR := snap.HTFDailyATR(); dailyATR > 0 && bar.Close > 0 {
				// Show per-symbol daily ATR as % of price for comparability
				atrPct := dailyATR / bar.Close * 100
				ctx += fmt.Sprintf(" | ATR%.1f%%", atrPct)
			}
			if htf, ok := snap.HTF[domain.Timeframe("1d")]; ok && htf.NR7 {
				ctx += " | NR7"
			}
			if snap.VWAP > 0 {
				switch {
				case setup.Direction == domain.DirectionLong && bar.Close > snap.VWAP:
					ctx += " | VWAP+"
				case setup.Direction == domain.DirectionShort && bar.Close < snap.VWAP:
					ctx += " | VWAP+"
				default:
					ctx += " | VWAP-"
				}
			}
			setup.MarketContext = ctx
			l.Info().
				Str("direction", string(setup.Direction)).
				Str("trigger", setup.Trigger).
				Float64("orb_high", setup.ORBHigh).
				Float64("orb_low", setup.ORBLow).
				Float64("rvol", setup.RVOL).
				Float64("confidence", setup.Confidence).
				Float64("vix", s.vixLevel).
				Str("ema_regime", setup.EMARegime).
				Str("vix_bucket", setup.VIXBucket).
				Str("market_context", setup.MarketContext).
				Msg("ORB setup detected")
			setupEv, err := domain.NewEvent(
				domain.EventSetupDetected,
				event.TenantID,
				event.EnvMode,
				event.IdempotencyKey+"-setup-detected",
				*setup,
			)
			if err != nil {
				s.mu.Unlock()
				return fmt.Errorf("monitor: failed to create setup detected event: %w", err)
			}
			publishStrict = append(publishStrict, *setupEv)
		}
	}
	s.lastSnaps[symStr] = snap
	s.mu.Unlock()

	if s.directDispatch {
		// Stash for the caller to drain. Pipeline will dispatch them
		// directly to the downstream handlers without the bus hop.
		s.pendingStrict = append(s.pendingStrict, publishStrict...)
		s.pendingBestEffort = append(s.pendingBestEffort, publishBestEffort...)
		return nil
	}

	for _, ev := range publishStrict {
		if err := s.eventBus.Publish(ctx, ev); err != nil {
			return fmt.Errorf("monitor: failed to publish event %s: %w", ev.Type, err)
		}
	}
	for _, ev := range publishBestEffort {
		_ = s.eventBus.Publish(ctx, ev)
	}
	return nil
}
// feedORBBar routes a 1m bar through the ORB aggregator (if configured) so the
// ORB tracker receives completed bars at the strategy timeframe (e.g. 5m).
// When orbTimeframe is "1m" or empty, bars pass through directly.
// Must be called with s.mu held.
func (s *Service) feedORBBar(bar domain.MarketBar, snap domain.IndicatorSnapshot, replay bool) (*SetupCondition, bool) {
	symStr := bar.Symbol.String()
	// No aggregation needed for 1m timeframe
	if s.orbTimeframe == "" || s.orbTimeframe == "1m" {
		return s.orbTracker.OnBar(bar, snap, s.orbCfg, replay)
	}
	agg, ok := s.orbAggregators[symStr]
	if !ok {
		// No aggregator yet (InitAggregators not called) — pass through directly
		return s.orbTracker.OnBar(bar, snap, s.orbCfg, replay)
	}
	closed, emitted := agg.Push(bar)
	if !emitted {
		return nil, false
	}
	// Use the completed aggregated bar with the latest 1m indicator snapshot
	return s.orbTracker.OnBar(closed, snap, s.orbCfg, replay)
}
func (s *Service) buildHTFMap(sym string, currentClose float64) map[domain.Timeframe]domain.HTFData {
	// Per-symbol reusable map — same pattern as anchorRegimeMaps. Saves
	// ~185MB of per-bar map allocations on a 3-month backtest.
	htf, ok := s.htfDataMaps[sym]
	if !ok {
		htf = make(map[domain.Timeframe]domain.HTFData, 2)
		s.htfDataMaps[sym] = htf
	} else {
		clear(htf)
	}
	hourlyKey := sym + ":1h"
	if hSnap, ok := s.lastHTFSnaps[hourlyKey]; ok && hSnap.EMA50 > 0 {
		htf[domain.Timeframe("1h")] = domain.HTFData{EMA50: hSnap.EMA50}
	}
	dailyKey := sym + ":1d"
	if dStatic, ok := s.htfStatic[dailyKey]; ok && dStatic.EMA200 > 0 {
		bias := "NEUTRAL"
		if currentClose > dStatic.EMA200*1.005 {
			bias = "BULLISH"
		} else if currentClose < dStatic.EMA200*0.995 {
			bias = "BEARISH"
		}
		htf[domain.Timeframe("1d")] = domain.HTFData{
			EMA200:   dStatic.EMA200,
			Bias:     bias,
			NR7:      dStatic.NR7,
			DailyATR: dStatic.DailyATR,
		}
	}
	if len(htf) == 0 {
		return nil
	}
	return htf
}
func (s *Service) WarmUpORB(bars []domain.MarketBar) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{})
	for _, bar := range bars {
		snap := s.calculator.Update(bar)
		s.feedORBBar(bar, snap, true)
		seen[string(bar.Symbol)] = struct{}{}
	}
	// Mark all recovered ORB ranges as already notified so live bars
	// don't re-emit stale ORBRangeSet notifications.
	for sym := range seen {
		if sess := s.orbTracker.GetSession(sym); sess != nil &&
			sess.State == ORBStateRangeSet && !sess.RangeNotified {
			sess.RangeNotified = true
		}
	}
}
func (s *Service) GetORBSession(symbol string) *ORBSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orbTracker.GetSession(symbol)
}
func (s *Service) WarmUp(bars []domain.MarketBar) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lastSnap domain.IndicatorSnapshot
	var lastBar domain.MarketBar
	for _, bar := range bars {
		lastSnap = s.calculator.Update(bar)
		lastBar = bar
		symStr := bar.Symbol.String()
		for _, tf := range anchorTimeframes {
			aggKey := symStr + ":" + tf.String()
			agg, exists := s.aggregators[aggKey]
			if !exists {
				continue
			}
			closed, ok := agg.Push(bar)
			if !ok {
				continue
			}
			htfSnap := s.calculator.Update(closed)
			reg, _ := s.regimeDetector.Detect(htfSnap)
			s.anchorRegimes[aggKey] = reg
			if tf == "1h" {
				if s.lastHTFSnaps == nil {
					s.lastHTFSnaps = make(map[string]domain.IndicatorSnapshot)
				}
				s.lastHTFSnaps[aggKey] = htfSnap
			}
		}
	}
	if len(bars) > 0 {
		symStr := lastBar.Symbol.String()
		regime, _ := s.regimeDetector.Detect(lastSnap)
		lastSnap.AnchorRegimes = map[domain.Timeframe]domain.MarketRegime{
			lastBar.Timeframe: regime,
		}
		for _, tf := range anchorTimeframes {
			aggKey := symStr + ":" + tf.String()
			if reg, ok := s.anchorRegimes[aggKey]; ok {
				lastSnap.AnchorRegimes[tf] = reg
			}
		}
		lastSnap.HTF = s.buildHTFMap(symStr, lastBar.Close)
		s.lastSnaps[symStr] = lastSnap
	}
	return len(bars)
}
