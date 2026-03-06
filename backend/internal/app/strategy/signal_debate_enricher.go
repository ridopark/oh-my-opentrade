package strategy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

type SignalDebateEnricher struct {
	eventBus      ports.EventBusPort
	aiAdvisor     ports.AIAdvisorPort
	repo          ports.RepositoryPort
	debateTimeout time.Duration

	makeDebateOpts func(sig strat.Signal) []ports.DebateOption
	logger         *slog.Logger
}

type EnricherOption func(*SignalDebateEnricher)

func WithDebateTimeout(d time.Duration) EnricherOption {
	return func(e *SignalDebateEnricher) {
		if d > 0 {
			e.debateTimeout = d
		}
	}
}

func WithDebateOptionFactory(fn func(strat.Signal) []ports.DebateOption) EnricherOption {
	return func(e *SignalDebateEnricher) {
		e.makeDebateOpts = fn
	}
}

func WithRepository(repo ports.RepositoryPort) EnricherOption {
	return func(e *SignalDebateEnricher) {
		e.repo = repo
	}
}

func NewSignalDebateEnricher(eventBus ports.EventBusPort, aiAdvisor ports.AIAdvisorPort, logger *slog.Logger, opts ...EnricherOption) *SignalDebateEnricher {
	if logger == nil {
		logger = slog.Default()
	}
	e := &SignalDebateEnricher{
		eventBus:      eventBus,
		aiAdvisor:     aiAdvisor,
		debateTimeout: 5 * time.Second,
		logger:        logger.With("component", "signal_debate_enricher"),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

func (e *SignalDebateEnricher) Start(ctx context.Context) error {
	if err := e.eventBus.Subscribe(ctx, domain.EventSignalCreated, e.handleSignal); err != nil {
		return fmt.Errorf("signal debate enricher: failed to subscribe to SignalCreated: %w", err)
	}
	e.logger.Info("signal debate enricher subscribed to SignalCreated events")
	return nil
}

func (e *SignalDebateEnricher) handleSignal(ctx context.Context, event domain.Event) error {
	sig, ok := event.Payload.(strat.Signal)
	if !ok {
		return nil
	}
	if !sig.Type.IsActionable() {
		return nil
	}

	ref := domain.SignalRef{
		StrategyInstanceID: string(sig.StrategyInstanceID),
		Symbol:             sig.Symbol,
		SignalType:         sig.Type.String(),
		Side:               sig.Side.String(),
		Strength:           sig.Strength,
		Tags:               sig.Tags,
	}

	if sig.Type == strat.SignalExit {
		direction := domain.DirectionLong
		if sig.Side == strat.SideSell {
			direction = domain.DirectionCloseLong
		}
		enrichment := domain.SignalEnrichment{
			Signal:     ref,
			Status:     domain.EnrichmentSkipped,
			Confidence: sig.Strength,
			Direction:  direction,
			Rationale:  fmt.Sprintf("exit signal: %s strength=%.2f", sig.Side, sig.Strength),
		}
		e.emit(ctx, domain.EventSignalEnriched, event.TenantID, event.EnvMode, event.IdempotencyKey+"-enriched", enrichment)
		e.saveThoughtLog(ctx, event, enrichment)
		return nil
	}

	direction := domain.DirectionLong
	if sig.Side == strat.SideSell {
		direction = domain.DirectionShort
	}

	advCtx, cancel := context.WithTimeout(ctx, e.debateTimeout)
	defer cancel()

	var debateOpts []ports.DebateOption
	if e.makeDebateOpts != nil {
		debateOpts = e.makeDebateOpts(sig)
	}

	decision, err := e.aiAdvisor.RequestDebate(
		advCtx,
		domain.Symbol(sig.Symbol),
		domain.MarketRegime{},
		domain.IndicatorSnapshot{},
		debateOpts...,
	)

	if err == nil && decision != nil {
		enrichment := domain.SignalEnrichment{
			Signal:         ref,
			Status:         domain.EnrichmentOK,
			Confidence:     decision.Confidence,
			Rationale:      decision.Rationale,
			Direction:      decision.Direction,
			BullArgument:   decision.BullArgument,
			BearArgument:   decision.BearArgument,
			JudgeReasoning: decision.JudgeReasoning,
		}
		e.emit(ctx, domain.EventSignalEnriched, event.TenantID, event.EnvMode, event.IdempotencyKey+"-enriched", enrichment)
		e.saveThoughtLog(ctx, event, enrichment)
		return nil
	}

	status := domain.EnrichmentError
	if errors.Is(err, context.DeadlineExceeded) {
		status = domain.EnrichmentTimeout
	}

	enrichment := domain.SignalEnrichment{
		Signal:     ref,
		Status:     status,
		Confidence: sig.Strength,
		Rationale:  fmt.Sprintf("signal: %s %s strength=%.2f (AI %s)", sig.Type, sig.Side, sig.Strength, status),
		Direction:  direction,
	}
	if err == nil && decision == nil {
		enrichment.Rationale = fmt.Sprintf("signal: %s %s strength=%.2f (AI %s)", sig.Type, sig.Side, sig.Strength, domain.EnrichmentError)
	}

	e.emit(ctx, domain.EventSignalEnriched, event.TenantID, event.EnvMode, event.IdempotencyKey+"-enriched", enrichment)
	e.saveThoughtLog(ctx, event, enrichment)
	return nil
}

func (e *SignalDebateEnricher) saveThoughtLog(ctx context.Context, event domain.Event, enrichment domain.SignalEnrichment) {
	if e.repo == nil {
		return
	}
	tl := domain.ThoughtLog{
		Time:           time.Now().UTC(),
		TenantID:       event.TenantID,
		EnvMode:        event.EnvMode,
		Symbol:         domain.Symbol(enrichment.Signal.Symbol),
		EventType:      "SignalEnriched",
		Direction:      string(enrichment.Direction),
		Confidence:     enrichment.Confidence,
		BullArgument:   enrichment.BullArgument,
		BearArgument:   enrichment.BearArgument,
		JudgeReasoning: enrichment.JudgeReasoning,
		Rationale:      enrichment.Rationale,
	}
	if err := e.repo.SaveThoughtLog(ctx, tl); err != nil {
		e.logger.Error("failed to save thought log", "error", err)
	}
}

func (e *SignalDebateEnricher) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = e.eventBus.Publish(ctx, *ev)
}
