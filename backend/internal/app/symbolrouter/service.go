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
	if err := s.bus.Subscribe(ctx, domain.EventAIScreenerCompleted, s.handleAIScreenerCompleted); err != nil {
		return fmt.Errorf("symbolrouter: subscribe to AIScreenerCompleted: %w", err)
	}
	s.log.Info().Int("strategies", len(s.strategies)).Msg("symbol router started")
	return nil
}

func (s *Service) handleAIScreenerCompleted(ctx context.Context, evt domain.Event) error {
	payload, ok := evt.Payload.(screener.AIScreenerCompletedPayload)
	if !ok {
		return fmt.Errorf("symbolrouter: payload is not AIScreenerCompletedPayload, got %T", evt.Payload)
	}

	// Find the strategy matching this AI screener result.
	var spec *StrategySpec
	for i := range s.strategies {
		if s.strategies[i].Key == payload.StrategyKey {
			spec = &s.strategies[i]
			break
		}
	}
	if spec == nil {
		s.log.Warn().Str("strategy", payload.StrategyKey).Msg("ai screener: no matching strategy in router")
		return nil
	}

	// Convert AIRankedSymbol → RankedSymbol for the existing resolver.
	ranked := make([]screener.RankedSymbol, len(payload.Ranked))
	for i, r := range payload.Ranked {
		score := float64(r.Score)
		ranked[i] = screener.RankedSymbol{
			Symbol:     r.Symbol,
			TotalScore: score,
		}
	}

	effective, source := ResolveEffectiveSymbols(spec.WatchlistMode, spec.BaseSymbols, ranked)
	source = "ai:" + source

	s.log.Info().
		Str("strategy", spec.Key).
		Str("mode", spec.WatchlistMode).
		Str("source", source).
		Str("model", payload.Model).
		Int("effective_count", len(effective)).
		Strs("symbols", effective).
		Msg("ai screener: effective symbols resolved")

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
		fmt.Sprintf("%s-%s-ai-effective", payload.RunID, spec.Key),
		outPayload,
	)
	if err != nil {
		return fmt.Errorf("symbolrouter: create ai event for %s: %w", spec.Key, err)
	}
	if err := s.bus.Publish(ctx, *outEvt); err != nil {
		return fmt.Errorf("symbolrouter: publish ai effective symbols for %s: %w", spec.Key, err)
	}
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
