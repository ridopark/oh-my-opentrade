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
	anchorRegimes    map[string]domain.MarketRegime
	log              zerolog.Logger
	dnaGate          DNAGateChecker
	strategyKey      string
}

// NewService creates a new monitor Service.
func NewService(eventBus ports.EventBusPort, repo ports.RepositoryPort, log zerolog.Logger) *Service {
	return &Service{
		eventBus:       eventBus,
		repo:           repo,
		calculator:     NewIndicatorCalculator(),
		regimeDetector: NewRegimeDetector(),
		orbTracker:     NewORBTracker(),
		orbCfg:         DefaultORBConfig(),
		lastSnaps:      make(map[string]domain.IndicatorSnapshot),
		liveBars:       make(map[string]int),
		aggregators:    make(map[string]*domain.BarAggregator),
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
	s.log.Info().
		Float64("min_rvol", s.orbCfg.MinRVOL).
		Float64("min_confidence", s.orbCfg.MinConfidence).
		Int("orb_window_minutes", s.orbCfg.WindowMinutes).
		Msg("ORB config set from DNA parameters")
}

// SetDNAGate installs a gate checker that blocks SetupDetected events when the
// active DNA version for strategyKey is not approved. If checker is nil the gate
// is disabled and all setups pass through.
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
	s.log.Info().Strs("symbols", symbols).Msg("base symbols configured")
}

func (s *Service) isAllowedSymbolLocked(sym string) bool {
	if s.baseSymbols == nil {
		return true
	}
	if s.effectiveSymbols != nil {
		_, ok := s.effectiveSymbols[sym]
		return ok
	}
	_, ok := s.baseSymbols[sym]
	return ok
}

func (s *Service) ResetSessionIndicators(symbol string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calculator.ResetSession(symbol, "1m")
	s.calculator.ResetSession(symbol, "5m")
	s.calculator.ResetSession(symbol, "15m")
}

func (s *Service) InitAggregators(symbols []domain.Symbol, sessionOpen time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sym := range symbols {
		symStr := sym.String()
		for _, tf := range []domain.Timeframe{"5m", "15m"} {
			key := symStr + ":" + tf.String()
			agg, err := domain.NewBarAggregator(sym, tf, sessionOpen)
			if err != nil {
				continue
			}
			s.aggregators[key] = agg
		}
	}
}

func (s *Service) ResetAggregators(sessionOpen time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, agg := range s.aggregators {
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

	for _, tf := range []domain.Timeframe{"5m", "15m"} {
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
	}

	snap.AnchorRegimes = make(map[domain.Timeframe]domain.MarketRegime)
	for _, tf := range []domain.Timeframe{"5m", "15m"} {
		aggKey := symStr + ":" + tf.String()
		if reg, ok := s.anchorRegimes[aggKey]; ok {
			snap.AnchorRegimes[tf] = reg
		}
	}

	s.liveBars[symStr]++

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

	regime, changed := s.regimeDetector.Detect(snap)
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
		s.orbTracker.OnBar(bar, snap, s.orbCfg, true)
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

	setup, detected := s.orbTracker.OnBar(bar, snap, s.orbCfg, false)
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
		if detected {
			setup.Regime = regime
			l.Info().
				Str("direction", string(setup.Direction)).
				Str("trigger", setup.Trigger).
				Float64("orb_high", setup.ORBHigh).
				Float64("orb_low", setup.ORBLow).
				Float64("rvol", setup.RVOL).
				Float64("confidence", setup.Confidence).
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

func (s *Service) WarmUpORB(bars []domain.MarketBar) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bar := range bars {
		snap := s.calculator.Update(bar)
		s.orbTracker.OnBar(bar, snap, s.orbCfg, true)
	}
}

func (s *Service) GetORBSession(symbol string) *ORBSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orbTracker.GetSession(symbol)
}

// WarmUp seeds the indicator calculator with historical bars without emitting
// any events or persisting data. It must be called before streaming begins.
// Returns the number of bars processed.
func (s *Service) WarmUp(bars []domain.MarketBar) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bar := range bars {
		s.calculator.Update(bar)
	}
	return len(bars)
}
