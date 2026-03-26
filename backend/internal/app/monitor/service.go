package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/screener"
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
	orbAggregators   map[string]*domain.BarAggregator // per-symbol 5m aggregators for ORB tracker
	orbTimeframe     domain.Timeframe                 // timeframe for ORB bar delivery (default "5m")
	anchorRegimes    map[string]domain.MarketRegime
	lastHTFSnaps     map[string]domain.IndicatorSnapshot
	htfStatic        map[string]domain.HTFData
	readySymbols     map[string]struct{}
	log              zerolog.Logger
	dnaGate          DNAGateChecker
	strategyKey      string
	vixLevel         float64 // latest VIX close; 0 = unknown (allow all)
	vixSkipAbove     float64 // skip ORB when VIX > this (0 = disabled)
	vixWidenAbove    float64 // widen stops when VIX > this (0 = disabled)
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
	return &Service{
		eventBus:       eventBus,
		repo:           repo,
		calculator:     NewIndicatorCalculator(),
		regimeDetector: NewRegimeDetector(),
		orbTracker:     NewORBTrackerWithSource("monitor"),
		orbCfg:         DefaultORBConfig(),
		lastSnaps:      make(map[string]domain.IndicatorSnapshot),
		liveBars:       make(map[string]int),
		aggregators:    make(map[string]*domain.BarAggregator),
		orbAggregators: make(map[string]*domain.BarAggregator),
		orbTimeframe:   "5m",
		anchorRegimes:  make(map[string]domain.MarketRegime),
		log:            log,
	}
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
		for _, tf := range anchorTimeframes {
			key := symStr + ":" + tf.String()
			agg, err := domain.NewBarAggregator(sym, tf, sessionOpen)
			if err != nil {
				continue
			}
			s.aggregators[key] = agg
		}
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

	l := s.log.With().
		Str("symbol", string(bar.Symbol)).
		Str("timeframe", string(bar.Timeframe)).
		Logger()

	var publishStrict []domain.Event
	var publishBestEffort []domain.Event

	s.mu.Lock()

	if !s.isAllowedSymbolLocked(string(bar.Symbol)) {
		s.mu.Unlock()
		return nil
	}

	snap := s.calculator.Update(bar)
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

		barEv, err := domain.NewEvent(
			domain.EventMarketBarSanitized,
			event.TenantID,
			event.EnvMode,
			event.IdempotencyKey+"-"+tf.String()+"-htf-bar",
			closed,
		)
		if err == nil {
			publishBestEffort = append(publishBestEffort, *barEv)
		}

		htfSnap := s.calculator.Update(closed)
		reg, changedAnchor := s.regimeDetector.Detect(htfSnap)
		s.anchorRegimes[aggKey] = reg
		if changedAnchor {
			regimeShiftedEv, err := domain.NewEvent(
				domain.EventRegimeShifted,
				event.TenantID,
				event.EnvMode,
				event.IdempotencyKey+"-"+tf.String()+"-regime-shifted",
				reg,
			)
			if err == nil {
				publishBestEffort = append(publishBestEffort, *regimeShiftedEv)
			}
		}

		if tf == "1h" {
			if s.lastHTFSnaps == nil {
				s.lastHTFSnaps = make(map[string]domain.IndicatorSnapshot)
			}
			s.lastHTFSnaps[aggKey] = htfSnap
		}
	}

	snap.AnchorRegimes = make(map[domain.Timeframe]domain.MarketRegime)
	for _, tf := range anchorTimeframes {
		aggKey := symStr + ":" + tf.String()
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

	stateUpdatedEv, err := domain.NewEvent(
		domain.EventStateUpdated,
		event.TenantID,
		event.EnvMode,
		event.IdempotencyKey+"-state-updated",
		snap,
	)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("monitor: failed to create state updated event: %w", err)
	}
	publishStrict = append(publishStrict, *stateUpdatedEv)

	l.Debug().
		Str("regime", string(regime.Type)).
		Float64("strength", regime.Strength).
		Bool("changed", changed).
		Msg("regime classification")
	if changed {
		l.Info().Str("regime", string(regime.Type)).Msg("market regime shifted")
		regimeShiftedEv, err := domain.NewEvent(
			domain.EventRegimeShifted,
			event.TenantID,
			event.EnvMode,
			event.IdempotencyKey+"-regime-shifted",
			regime,
		)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("monitor: failed to create regime shifted event: %w", err)
		}
		publishStrict = append(publishStrict, *regimeShiftedEv)
	}

	if s.liveBars[symStr] < settlingBars {
		s.feedORBBar(bar, snap, true)
		l.Debug().Msg(fmt.Sprintf("settling: %d/%d bars, suppressing setup detection", s.liveBars[symStr], settlingBars))
		s.lastSnaps[symStr] = snap
		s.mu.Unlock()
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

	setup, detected := s.feedORBBar(bar, snap, false)
	if detected && setup != nil {
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
		if detected {
			setup.Regime = regime
			// Tag VIX adjustment for downstream (debate service)
			if s.vixWidenAbove > 0 && s.vixLevel > s.vixWidenAbove {
				setup.VIXAdjust = "widen_stops"
			}
			l.Info().
				Str("direction", string(setup.Direction)).
				Str("trigger", setup.Trigger).
				Float64("orb_high", setup.ORBHigh).
				Float64("orb_low", setup.ORBLow).
				Float64("rvol", setup.RVOL).
				Float64("confidence", setup.Confidence).
				Float64("vix", s.vixLevel).
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
	htf := make(map[domain.Timeframe]domain.HTFData)

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
			EMA200: dStatic.EMA200,
			Bias:   bias,
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
	for _, bar := range bars {
		snap := s.calculator.Update(bar)
		s.feedORBBar(bar, snap, true)
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
