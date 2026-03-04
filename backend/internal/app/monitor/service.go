package monitor

import (
	"context"
	"fmt"
	"sync"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Service is the monitor application service.
// It subscribes to MarketBarSanitized events, computes technical indicators,
// detects market regime shifts, and identifies trade setups.
type Service struct {
	eventBus       ports.EventBusPort
	repo           ports.RepositoryPort
	calculator     *IndicatorCalculator
	regimeDetector *RegimeDetector
	mu             sync.Mutex
	lastSnaps      map[string]domain.IndicatorSnapshot
	log            zerolog.Logger
}

// NewService creates a new monitor Service.
func NewService(eventBus ports.EventBusPort, repo ports.RepositoryPort, log zerolog.Logger) *Service {
	return &Service{
		eventBus:       eventBus,
		repo:           repo,
		calculator:     NewIndicatorCalculator(),
		regimeDetector: NewRegimeDetector(),
		lastSnaps:      make(map[string]domain.IndicatorSnapshot),
		log:            log,
	}
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

	lastSnap, hasLast := s.lastSnaps[symStr]
	if hasLast && lastSnap.RSI > 0 { // Ensure RSI is actually initialized

	// Debug: log setup detection evaluation criteria
	l.Debug().
		Bool("has_prev", hasLast).
		Float64("rsi_prev", lastSnap.RSI).
		Float64("rsi_curr", snap.RSI).
		Float64("ema9", snap.EMA9).
		Float64("ema21", snap.EMA21).
		Bool("long_rsi_cross", hasLast && lastSnap.RSI < 40 && snap.RSI >= 40).
		Bool("long_ema_bullish", snap.EMA9 > snap.EMA21).
		Bool("short_rsi_cross", hasLast && lastSnap.RSI > 60 && snap.RSI <= 60).
		Bool("short_ema_bearish", snap.EMA9 < snap.EMA21).
		Msg("setup evaluation")
		// Detect Setup
		// Recovering from oversold and bullish EMA
		if lastSnap.RSI < 40 && snap.RSI >= 40 && snap.EMA9 > snap.EMA21 {
			setup := SetupCondition{
				Symbol:    bar.Symbol,
				Timeframe: bar.Timeframe,
				Direction: domain.DirectionLong,
				Trigger:   "Bullish EMA Crossover from Oversold",
				Snapshot:  snap,
				Regime:    regime,
				BarClose:  bar.Close,
			}
			l.Info().
				Str("direction", string(domain.DirectionLong)).
				Str("trigger", setup.Trigger).
				Float64("rsi_prev", lastSnap.RSI).
				Float64("rsi_curr", snap.RSI).
				Msg("setup detected")
			setupEv, err := domain.NewEvent(
				domain.EventSetupDetected,
				event.TenantID,
				event.EnvMode,
				event.IdempotencyKey+"-setup-detected",
				setup,
			)
			if err != nil {
				return fmt.Errorf("monitor: failed to create setup detected event: %w", err)
			}
			if err := s.eventBus.Publish(ctx, *setupEv); err != nil {
				return fmt.Errorf("monitor: failed to publish setup detected event: %w", err)
			}
		} else if lastSnap.RSI > 60 && snap.RSI <= 60 && snap.EMA9 < snap.EMA21 {
			setup := SetupCondition{
				Symbol:    bar.Symbol,
				Timeframe: bar.Timeframe,
				Direction: domain.DirectionShort,
				Trigger:   "Bearish EMA Crossover from Overbought",
				Snapshot:  snap,
				Regime:    regime,
				BarClose:  bar.Close,
			}
			l.Info().
				Str("direction", string(domain.DirectionShort)).
				Str("trigger", setup.Trigger).
				Float64("rsi_prev", lastSnap.RSI).
				Float64("rsi_curr", snap.RSI).
				Msg("setup detected")
			setupEv, err := domain.NewEvent(
				domain.EventSetupDetected,
				event.TenantID,
				event.EnvMode,
				event.IdempotencyKey+"-setup-detected",
				setup,
			)
			if err != nil {
				return fmt.Errorf("monitor: failed to create setup detected event: %w", err)
			}
			if err := s.eventBus.Publish(ctx, *setupEv); err != nil {
				return fmt.Errorf("monitor: failed to publish setup detected event: %w", err)
			}
		}
	}

	s.lastSnaps[symStr] = snap

	return nil
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
