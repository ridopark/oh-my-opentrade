package positionmonitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type IndicatorSnapshotFunc func(symbol string) (domain.IndicatorSnapshot, bool)
type RegimeFunc func(symbol string) (domain.MarketRegime, bool)

type Revaluator struct {
	posMonitor *Service
	assessor   ports.RiskAssessorPort
	eventBus   ports.EventBusPort
	snapshotFn IndicatorSnapshotFunc
	regimeFn   RegimeFunc
	interval   time.Duration
	tenantID   string
	envMode    domain.EnvMode
	log        zerolog.Logger

	pendingTheses sync.Map // symbol → *domain.EntryThesis (cached until fill creates the position)
}

func NewRevaluator(
	posMonitor *Service,
	assessor ports.RiskAssessorPort,
	eventBus ports.EventBusPort,
	snapshotFn IndicatorSnapshotFunc,
	regimeFn RegimeFunc,
	interval time.Duration,
	tenantID string,
	envMode domain.EnvMode,
	log zerolog.Logger,
) *Revaluator {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Revaluator{
		posMonitor: posMonitor,
		assessor:   assessor,
		eventBus:   eventBus,
		snapshotFn: snapshotFn,
		regimeFn:   regimeFn,
		interval:   interval,
		tenantID:   tenantID,
		envMode:    envMode,
		log:        log.With().Str("service", "position_revaluator").Logger(),
	}
}

func (r *Revaluator) Start(ctx context.Context) error {
	if err := r.eventBus.Subscribe(ctx, domain.EventSignalEnriched, r.handleSignalEnriched); err != nil {
		return fmt.Errorf("position revaluator: failed to subscribe to SignalEnriched: %w", err)
	}
	if err := r.eventBus.Subscribe(ctx, domain.EventFillReceived, r.handleFillReceived); err != nil {
		return fmt.Errorf("position revaluator: failed to subscribe to FillReceived: %w", err)
	}

	go r.runLoop(ctx)
	r.log.Info().
		Dur("interval", r.interval).
		Msg("position revaluator started")
	return nil
}

// handleSignalEnriched caches the AI thesis per symbol so it can be attached to the
// position once the corresponding fill arrives (two-phase: enrich → fill → attach).
func (r *Revaluator) handleSignalEnriched(_ context.Context, event domain.Event) error {
	enrichment, ok := event.Payload.(domain.SignalEnrichment)
	if !ok {
		return nil
	}

	if enrichment.Signal.SignalType != "entry" || enrichment.Status != domain.EnrichmentOK {
		return nil
	}

	thesis := &domain.EntryThesis{
		BullArgument:   enrichment.BullArgument,
		BearArgument:   enrichment.BearArgument,
		JudgeReasoning: enrichment.JudgeReasoning,
		Rationale:      enrichment.Rationale,
		Confidence:     enrichment.Confidence,
		RiskModifier:   enrichment.RiskModifier,
		Direction:      enrichment.Direction,
	}

	r.pendingTheses.Store(enrichment.Signal.Symbol, thesis)

	r.log.Debug().
		Str("symbol", enrichment.Signal.Symbol).
		Float64("confidence", enrichment.Confidence).
		Str("risk_modifier", string(enrichment.RiskModifier)).
		Msg("entry thesis cached for pending fill")

	return nil
}

func (r *Revaluator) handleFillReceived(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return nil
	}

	symbol, _ := payload["symbol"].(string)
	side, _ := payload["side"].(string)

	if symbol == "" || side != "BUY" {
		return nil
	}

	raw, ok := r.pendingTheses.LoadAndDelete(symbol)
	if !ok {
		return nil
	}
	thesis, ok := raw.(*domain.EntryThesis)
	if !ok {
		return nil
	}

	key := fmt.Sprintf("%s:%s:%s", r.tenantID, r.envMode, symbol)
	r.posMonitor.SetEntryThesis(key, thesis)

	r.log.Info().
		Str("symbol", symbol).
		Float64("confidence", thesis.Confidence).
		Str("direction", string(thesis.Direction)).
		Msg("entry thesis attached to monitored position")

	return nil
}

func (r *Revaluator) runLoop(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.evaluateAll(ctx)
		}
	}
}

func (r *Revaluator) evaluateAll(ctx context.Context) {
	positions := r.posMonitor.ListPositions()
	if len(positions) == 0 {
		return
	}

	r.log.Debug().Int("positions", len(positions)).Msg("starting risk re-evaluation cycle")

	for _, pos := range positions {
		if ctx.Err() != nil {
			return
		}

		if pos.EntryThesis == nil {
			r.log.Info().Str("symbol", string(pos.Symbol)).Msg("skipping re-evaluation — no entry thesis attached")
			continue
		}

		if pos.ExitPending {
			continue
		}

		dedupWindow := time.Duration(float64(r.interval) * 0.8)
		if !pos.LastRevaluationAt.IsZero() && time.Since(pos.LastRevaluationAt) < dedupWindow {
			continue
		}

		r.evaluatePosition(ctx, pos)
	}
}

func (r *Revaluator) evaluatePosition(ctx context.Context, pos domain.MonitoredPosition) {
	indicators, ok := r.snapshotFn(string(pos.Symbol))
	if !ok {
		r.log.Debug().Str("symbol", string(pos.Symbol)).Msg("skipping — no indicator snapshot available")
		return
	}

	regime := domain.MarketRegime{
		Symbol: pos.Symbol,
		Type:   domain.RegimeBalance,
	}
	if r.regimeFn != nil {
		if r, ok := r.regimeFn(string(pos.Symbol)); ok {
			regime = r
		}
	}

	result, err := r.assessor.AssessPosition(ctx, pos, indicators, regime)
	if err != nil {
		r.log.Warn().Err(err).Str("symbol", string(pos.Symbol)).Msg("risk assessment failed")
		return
	}
	if result == nil {
		return
	}

	key := pos.PositionKey()
	r.posMonitor.ApplyRevaluation(key, result)

	currentPrice := indicators.EMA9
	if currentPrice == 0 {
		currentPrice = pos.EntryPrice
	}

	revalEvent := domain.RiskRevaluationEvent{
		RiskRevaluation: *result,
		Strategy:        pos.Strategy,
		EntryPrice:      pos.EntryPrice,
		CurrentPrice:    currentPrice,
		UnrealizedPnL:   pos.UnrealizedPnLPct(currentPrice),
		HoldDuration:    formatDuration(time.Since(pos.EntryTime)),
		TenantID:        pos.TenantID,
		EnvMode:         pos.EnvMode,
	}

	idempotencyKey := fmt.Sprintf("REVAL:%s:%s:%s:%d", pos.TenantID, pos.EnvMode, pos.Symbol, time.Now().Unix())
	r.emit(ctx, domain.EventRiskRevaluated, pos.TenantID, pos.EnvMode, idempotencyKey, revalEvent)

	r.log.Info().
		Str("symbol", string(pos.Symbol)).
		Str("thesis_status", string(result.ThesisStatus)).
		Str("action", string(result.Action)).
		Float64("confidence", result.Confidence).
		Msg("risk re-evaluation complete")

	switch result.Action {
	case domain.RiskActionTighten:
		r.log.Info().
			Str("symbol", string(pos.Symbol)).
			Str("updated_modifier", string(result.UpdatedModifier)).
			Float64("confidence", result.Confidence).
			Msg("TIGHTEN applied — exit rules adjusted, new entries blocked")
	case domain.RiskActionExit:
		r.triggerExit(ctx, pos, result, currentPrice)
	case domain.RiskActionScaleOut:
		r.triggerScaleOut(ctx, pos, result, currentPrice)
	}
}

func (r *Revaluator) triggerExit(ctx context.Context, pos domain.MonitoredPosition, result *domain.RiskRevaluation, currentPrice float64) {
	idempotencyKey := fmt.Sprintf("REVAL_EXIT:%s:%s:%s:%d",
		pos.TenantID, pos.EnvMode, pos.Symbol, pos.EntryTime.Unix())

	exitPrice := currentPrice * 0.98 // IOC aggressive: 2% buffer for sells
	intent, err := domain.NewOrderIntent(
		uuid.New(),
		pos.TenantID,
		pos.EnvMode,
		pos.Symbol,
		domain.DirectionCloseLong,
		exitPrice,
		0,
		0,
		pos.Quantity,
		pos.Strategy,
		fmt.Sprintf("risk_revaluation:EXIT:%s", result.Reasoning),
		result.Confidence,
		idempotencyKey,
	)
	if err != nil {
		r.log.Error().Err(err).Str("symbol", string(pos.Symbol)).Msg("failed to create revaluation exit intent")
		return
	}
	intent.OrderType = "limit"
	intent.TimeInForce = "ioc"

	r.emit(ctx, domain.EventOrderIntentCreated, pos.TenantID, pos.EnvMode, idempotencyKey, intent)

	r.log.Warn().
		Str("symbol", string(pos.Symbol)).
		Str("reason", result.Reasoning).
		Msg("risk revaluator triggered full exit")
}

func (r *Revaluator) triggerScaleOut(ctx context.Context, pos domain.MonitoredPosition, result *domain.RiskRevaluation, currentPrice float64) {
	scaleOutPct := result.ScaleOutPct
	if scaleOutPct <= 0 || scaleOutPct > 1 {
		scaleOutPct = 0.50
	}

	sellQty := pos.Quantity * scaleOutPct
	if sellQty < 0.01 {
		return
	}

	idempotencyKey := fmt.Sprintf("REVAL_SCALE:%s:%s:%s:%d",
		pos.TenantID, pos.EnvMode, pos.Symbol, time.Now().Unix())

	intent, err := domain.NewOrderIntent(
		uuid.New(),
		pos.TenantID,
		pos.EnvMode,
		pos.Symbol,
		domain.DirectionCloseLong,
		currentPrice,
		0,
		0,
		sellQty,
		pos.Strategy,
		fmt.Sprintf("risk_revaluation:SCALE_OUT:%.0f%%:%s", scaleOutPct*100, result.Reasoning),
		result.Confidence,
		idempotencyKey,
	)
	if err != nil {
		r.log.Error().Err(err).Str("symbol", string(pos.Symbol)).Msg("failed to create scale-out intent")
		return
	}

	r.emit(ctx, domain.EventOrderIntentCreated, pos.TenantID, pos.EnvMode, idempotencyKey, intent)

	r.log.Info().
		Str("symbol", string(pos.Symbol)).
		Float64("scale_out_pct", scaleOutPct).
		Float64("sell_qty", sellQty).
		Msg("risk revaluator triggered scale-out")
}

func (r *Revaluator) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = r.eventBus.Publish(ctx, *ev)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	totalMin := int(d.Minutes())
	if totalMin < 60 {
		return fmt.Sprintf("%dm", totalMin)
	}
	h := totalMin / 60
	m := totalMin % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
