package monitor

import (
	"context"
	"fmt"
	"sync"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const settlingBars = 5

// Service is the monitor application service.
// It subscribes to MarketBarSanitized events, computes technical indicators,
// detects market regime shifts, and identifies trade setups.
type Service struct {
	eventBus       ports.EventBusPort
	repo           ports.RepositoryPort
	calculator     *IndicatorCalculator
	regimeDetector *RegimeDetector
	orbTracker     *ORBTracker
	mu             sync.Mutex
	lastSnaps      map[string]domain.IndicatorSnapshot
	liveBars       map[string]int
	log            zerolog.Logger
}

// NewService creates a new monitor Service.
func NewService(eventBus ports.EventBusPort, repo ports.RepositoryPort, log zerolog.Logger) *Service {
	return &Service{
		eventBus:       eventBus,
		repo:           repo,
		calculator:     NewIndicatorCalculator(),
		regimeDetector: NewRegimeDetector(),
		orbTracker:     NewORBTracker(),
		lastSnaps:      make(map[string]domain.IndicatorSnapshot),
		liveBars:       make(map[string]int),
		log:            log,
	}
}

func (s *Service) ResetSessionIndicators(symbol string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calculator.ResetSession(symbol)
}

// Start subscribes the service to incoming sanitized market data events.
func (s *Service) Start(ctx context.Context) error {
	err := s.eventBus.Subscribe(ctx, domain.EventMarketBarSanitized, s.HandleMarketBar)
	if err != nil {
		return fmt.Errorf("monitor: failed to subscribe to MarketBarSanitized: %w", err)
	}
	s.log.Info().Msg("subscribed to MarketBarSanitized events")
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

	l := s.log.With().
		Str("symbol", string(bar.Symbol)).
		Str("timeframe", string(bar.Timeframe)).
		Logger()

	s.mu.Lock()
	defer s.mu.Unlock()

	snap := s.calculator.Update(bar)
	symStr := bar.Symbol.String()

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
		return fmt.Errorf("monitor: failed to create state updated event: %w", err)
	}

	if err := s.eventBus.Publish(ctx, *stateUpdatedEv); err != nil {
		return fmt.Errorf("monitor: failed to publish state updated event: %w", err)
	}

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
			return fmt.Errorf("monitor: failed to create regime shifted event: %w", err)
		}
		if err := s.eventBus.Publish(ctx, *regimeShiftedEv); err != nil {
			return fmt.Errorf("monitor: failed to publish regime shifted event: %w", err)
		}
	}

	if s.liveBars[symStr] < settlingBars {
		cfg := DefaultORBConfig()
		s.orbTracker.OnBar(bar, snap, cfg, true)
		l.Debug().Msg(fmt.Sprintf("settling: %d/%d bars, suppressing setup detection", s.liveBars[symStr], settlingBars))
		s.lastSnaps[symStr] = snap
		return nil
	}

	lastSnap, hasLast := s.lastSnaps[symStr]
	_ = hasLast
	_ = lastSnap

	orbCfg := DefaultORBConfig()
	setup, detected := s.orbTracker.OnBar(bar, snap, orbCfg, false)
	if detected && setup != nil {
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
			return fmt.Errorf("monitor: failed to create setup detected event: %w", err)
		}
		if err := s.eventBus.Publish(ctx, *setupEv); err != nil {
			return fmt.Errorf("monitor: failed to publish setup detected event: %w", err)
		}
	}

	s.lastSnaps[symStr] = snap

	return nil
}

func (s *Service) WarmUpORB(bars []domain.MarketBar) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := DefaultORBConfig()
	for _, bar := range bars {
		snap := s.calculator.Update(bar)
		s.orbTracker.OnBar(bar, snap, cfg, true)
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
