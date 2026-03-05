package symbolrouter

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type StrategySpec struct {
	Key           string
	BaseSymbols   []string
	WatchlistMode string
}

type Service struct {
	log        zerolog.Logger
	bus        ports.EventBusPort
	strategies []StrategySpec
	tenantID   string
	envMode    domain.EnvMode
}

func NewService(
	bus ports.EventBusPort,
	strategies []StrategySpec,
	tenantID string,
	envMode domain.EnvMode,
	log zerolog.Logger,
) *Service {
	return &Service{
		log:        log,
		bus:        bus,
		strategies: strategies,
		tenantID:   tenantID,
		envMode:    envMode,
	}
}

func (s *Service) Start(ctx context.Context) error {
	if err := s.bus.Subscribe(ctx, domain.EventScreenerCompleted, s.handleScreenerCompleted); err != nil {
		return fmt.Errorf("symbolrouter: subscribe to ScreenerCompleted: %w", err)
	}
	s.log.Info().Int("strategies", len(s.strategies)).Msg("symbol router started")
	return nil
}

func (s *Service) handleScreenerCompleted(ctx context.Context, evt domain.Event) error {
	payload, ok := evt.Payload.(screener.CompletedPayload)
	if !ok {
		return fmt.Errorf("symbolrouter: payload is not CompletedPayload, got %T", evt.Payload)
	}

	for _, spec := range s.strategies {
		effective, source := ResolveEffectiveSymbols(spec.WatchlistMode, spec.BaseSymbols, payload.Ranked)

		s.log.Info().
			Str("strategy", spec.Key).
			Str("mode", spec.WatchlistMode).
			Str("source", source).
			Int("effective_count", len(effective)).
			Strs("symbols", effective).
			Msg("effective symbols resolved")

		outPayload := screener.EffectiveSymbolsUpdatedPayload{
			StrategyKey: spec.Key,
			RunID:       payload.RunID,
			AsOf:        payload.AsOf,
			Mode:        spec.WatchlistMode,
			Source:      source,
			Symbols:     effective,
		}
		outEvt, err := domain.NewEvent(
			domain.EventEffectiveSymbolsUpdated,
			s.tenantID,
			s.envMode,
			fmt.Sprintf("%s-%s-effective", payload.RunID, spec.Key),
			outPayload,
		)
		if err != nil {
			return fmt.Errorf("symbolrouter: create event for %s: %w", spec.Key, err)
		}
		if err := s.bus.Publish(ctx, *outEvt); err != nil {
			return fmt.Errorf("symbolrouter: publish effective symbols for %s: %w", spec.Key, err)
		}
	}
	return nil
}
