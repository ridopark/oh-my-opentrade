package strategy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

type MarketDataProvider func(symbol string) (domain.IndicatorSnapshot, bool)

type PositionLookup func(symbol string) (domain.MonitoredPosition, bool)

type SignalDebateEnricher struct {
	eventBus      ports.EventBusPort
	aiAdvisor     ports.AIAdvisorPort
	repo          ports.RepositoryPort
	stratPerf     ports.StrategyPerformancePort
	debateTimeout time.Duration
	marketData    MarketDataProvider
	posLookup     PositionLookup

	makeDebateOpts func(sig start.Signal) []ports.DebateOption
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

func WithDebateOptionFactory(fn func(start.Signal) []ports.DebateOption) EnricherOption {
	return func(e *SignalDebateEnricher) {
		e.makeDebateOpts = fn
	}
}

func WithRepository(repo ports.RepositoryPort) EnricherOption {
	return func(e *SignalDebateEnricher) {
		e.repo = repo
	}
}

func WithMarketDataProvider(fn MarketDataProvider) EnricherOption {
	return func(e *SignalDebateEnricher) {
		e.marketData = fn
	}
}

func WithPositionLookup(fn PositionLookup) EnricherOption {
	return func(e *SignalDebateEnricher) {
		e.posLookup = fn
	}
}

func WithStrategyPerformance(port ports.StrategyPerformancePort) EnricherOption {
	return func(e *SignalDebateEnricher) {
		e.stratPerf = port
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
	if err := e.eventBus.SubscribeAsync(ctx, domain.EventSignalCreated, e.handleSignal); err != nil {
		return fmt.Errorf("signal debate enricher: failed to subscribe to SignalCreated: %w", err)
	}
	e.logger.Info("signal debate enricher subscribed to SignalCreated events")
	return nil
}

func (e *SignalDebateEnricher) handleSignal(ctx context.Context, event domain.Event) error {
	sig, ok := event.Payload.(start.Signal)
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

	if sig.Type == start.SignalExit {
		direction := domain.DirectionLong
		if sig.Side == start.SideSell {
			direction = domain.DirectionCloseLong
		}
		enrichment := domain.SignalEnrichment{
			Signal:     ref,
			Status:     domain.EnrichmentSkipped,
			Confidence: sig.Strength,
			Direction:  direction,
			Rationale:  fmt.Sprintf("exit signal: %s strength=%.2f", sig.Side, sig.Strength),
		}
		if e.posLookup != nil {
			if pos, ok := e.posLookup(sig.Symbol); ok {
				refPrice, _ := strconv.ParseFloat(sig.Tags["ref_price"], 64)
				if refPrice > 0 && pos.EntryPrice > 0 {
					enrichment.EntryPrice = pos.EntryPrice
					enrichment.UnrealizedPnLPct = pos.UnrealizedPnLPct(refPrice)
					enrichment.UnrealizedPnLUSD = (refPrice - pos.EntryPrice) * pos.Quantity
					enrichment.HasPnL = true
				}
			}
		}
		e.emit(ctx, domain.EventSignalEnriched, event.TenantID, event.EnvMode, event.IdempotencyKey+"-enriched", enrichment)
		e.saveThoughtLog(ctx, event, enrichment)
		return nil
	}

	direction := domain.DirectionLong
	if sig.Side == start.SideSell {
		direction = domain.DirectionShort
	}

	advCtx, cancel := context.WithTimeout(ctx, e.debateTimeout)
	defer cancel()

	var debateOpts []ports.DebateOption
	if e.makeDebateOpts != nil {
		debateOpts = e.makeDebateOpts(sig)
	}

	// Fetch real-time market data for the AI prompt.
	var indicators domain.IndicatorSnapshot
	var regime domain.MarketRegime
	if e.marketData != nil {
		if snap, ok := e.marketData(sig.Symbol); ok {
			indicators = snap
			// Use the best available anchor regime (prefer 5m, then 15m).
			if r, ok := snap.AnchorRegimes["5m"]; ok {
				regime = r
			} else if r, ok := snap.AnchorRegimes["15m"]; ok {
				regime = r
			}
		}
	}

	if e.stratPerf != nil {
		strategyName := extractStrategyName(sig.StrategyInstanceID)
		summary, perfErr := e.stratPerf.GetPerformanceSummary(
			ctx, event.TenantID, event.EnvMode,
			strategyName, sig.Symbol, 30*24*time.Hour,
		)
		if perfErr != nil {
			e.logger.Warn("strategy perf lookup failed", "symbol", sig.Symbol, "error", perfErr)
		} else if summary != nil {
			debateOpts = append(debateOpts, ports.WithStrategyPerformance(summary))

			const minTradesForVeto = 5
			if summary.HasNegativeExpectancy(regime.Type, minTradesForVeto) {
				e.logger.Warn("pre-LLM veto: negative expectancy",
					"symbol", sig.Symbol,
					"regime", regime.Type,
					"expectancy", summary.Overall.Expectancy,
					"trades", summary.Overall.TradeCount,
				)
				enrichment := domain.SignalEnrichment{
					Signal:     ref,
					Status:     domain.EnrichmentSkipped,
					Confidence: 0.1,
					Rationale:  fmt.Sprintf("pre-LLM veto: negative expectancy $%.2f/trade in %s (%d trades)", summary.Overall.Expectancy, regime.Type, summary.Overall.TradeCount),
					Direction:  direction,
				}
				e.emit(ctx, domain.EventSignalEnriched, event.TenantID, event.EnvMode, event.IdempotencyKey+"-enriched", enrichment)
				e.saveThoughtLog(ctx, event, enrichment)
				return nil
			}
		}
	}

	decision, err := e.aiAdvisor.RequestDebate(
		advCtx,
		domain.Symbol(sig.Symbol),
		regime,
		indicators,
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
			RiskModifier:   decision.RiskModifier,
		}
		e.emit(ctx, domain.EventSignalEnriched, event.TenantID, event.EnvMode, event.IdempotencyKey+"-enriched", enrichment)
		e.saveThoughtLog(ctx, event, enrichment)
		return nil
	}

	// AI returned NEUTRAL or no decision — treat as skipped, proceed with original signal.
	if err == nil && decision == nil {
		e.logger.Info("AI debate neutral — proceeding with original signal", "symbol", sig.Symbol)
		enrichment := domain.SignalEnrichment{
			Signal:     ref,
			Status:     domain.EnrichmentSkipped,
			Confidence: sig.Strength,
			Rationale:  fmt.Sprintf("signal: %s %s strength=%.2f (AI neutral)", sig.Type, sig.Side, sig.Strength),
			Direction:  direction,
		}
		e.emit(ctx, domain.EventSignalEnriched, event.TenantID, event.EnvMode, event.IdempotencyKey+"-enriched", enrichment)
		e.saveThoughtLog(ctx, event, enrichment)
		return nil
	}

	status := domain.EnrichmentError
	if errors.Is(err, context.DeadlineExceeded) {
		status = domain.EnrichmentTimeout
	}
	e.logger.Warn("AI debate failed", "symbol", sig.Symbol, "error", err)

	enrichment := domain.SignalEnrichment{
		Signal:     ref,
		Status:     status,
		Confidence: sig.Strength,
		Rationale:  fmt.Sprintf("signal: %s %s strength=%.2f (AI %s)", sig.Type, sig.Side, sig.Strength, status),
		Direction:  direction,
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

func extractStrategyName(instanceID start.InstanceID) string {
	parts := strings.SplitN(string(instanceID), ":", 3)
	if len(parts) >= 1 {
		return parts[0]
	}
	return string(instanceID)
}

func (e *SignalDebateEnricher) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = e.eventBus.Publish(ctx, *ev)
}
