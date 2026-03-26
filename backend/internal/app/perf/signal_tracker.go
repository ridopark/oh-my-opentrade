package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type signalCorr struct {
	signalID string
	kind     string
	side     string
}

type SignalTracker struct {
	eventBus ports.EventBusPort
	pnlRepo  ports.PnLPort
	log      zerolog.Logger

	mu sync.Mutex

	byInstanceKey map[string]string
	latestByScope map[string]signalCorr
}

func NewSignalTracker(eventBus ports.EventBusPort, pnlRepo ports.PnLPort, log zerolog.Logger) *SignalTracker {
	return &SignalTracker{
		eventBus:      eventBus,
		pnlRepo:       pnlRepo,
		log:           log,
		byInstanceKey: make(map[string]string),
		latestByScope: make(map[string]signalCorr),
	}
}

func (st *SignalTracker) Start(ctx context.Context) error {
	if err := st.eventBus.Subscribe(ctx, domain.EventSignalCreated, st.handleSignalCreated); err != nil {
		return fmt.Errorf("perf: signal tracker failed to subscribe to SignalCreated: %w", err)
	}
	if err := st.eventBus.Subscribe(ctx, domain.EventOrderIntentValidated, st.handleIntentValidated); err != nil {
		return fmt.Errorf("perf: signal tracker failed to subscribe to OrderIntentValidated: %w", err)
	}
	if err := st.eventBus.Subscribe(ctx, domain.EventOrderIntentRejected, st.handleIntentRejected); err != nil {
		return fmt.Errorf("perf: signal tracker failed to subscribe to OrderIntentRejected: %w", err)
	}
	if err := st.eventBus.SubscribeAsync(ctx, domain.EventFillReceived, st.handleFill); err != nil {
		return fmt.Errorf("perf: signal tracker failed to subscribe to FillReceived: %w", err)
	}
	st.log.Info().Msg("signal tracker subscribed to signal lifecycle events")
	return nil
}

func (st *SignalTracker) handleSignalCreated(ctx context.Context, event domain.Event) error {
	sig, ok := event.Payload.(start.Signal)
	if !ok {
		return nil
	}

	strategy, ok := parseStrategyIDFromInstanceLocal(sig.StrategyInstanceID)
	if !ok {
		return nil
	}
	if sig.Symbol == "" {
		return nil
	}

	signalID := uuid.NewString()
	kind := sig.Type.String()
	side := strings.ToUpper(sig.Side.String())

	instanceKey := fmt.Sprintf("%s:%s:%s:%s", sig.StrategyInstanceID.String(), sig.Symbol, kind, sig.Side.String())
	scopeKey := st.scopeKey(event.TenantID, event.EnvMode, strategy, sig.Symbol)

	st.mu.Lock()
	st.byInstanceKey[instanceKey] = signalID
	st.latestByScope[scopeKey] = signalCorr{signalID: signalID, kind: kind, side: side}
	st.mu.Unlock()

	payload := mustJSON(sig)

	evt, err := domain.NewStrategySignalEvent(
		event.OccurredAt.UTC(),
		event.TenantID,
		event.EnvMode,
		strategy,
		signalID,
		sig.Symbol,
		kind,
		side,
		domain.SignalStatusGenerated,
		"",
		sig.Strength,
		payload,
	)
	if err != nil {
		st.log.Error().Err(err).Msg("signal tracker: invalid StrategySignalEvent")
		return nil
	}

	if err := st.pnlRepo.SaveStrategySignalEvent(ctx, evt); err != nil {
		st.log.Error().Err(err).Msg("signal tracker: failed to save StrategySignalEvent")
		return nil
	}
	st.publishLifecycle(ctx, evt)
	return nil
}

func (st *SignalTracker) handleIntentValidated(ctx context.Context, event domain.Event) error {
	p, ok := event.Payload.(domain.OrderIntentEventPayload)
	if !ok {
		return nil
	}
	if p.Strategy == "" || p.Symbol == "" {
		return nil
	}

	corr := st.getOrDeriveCorr(event.TenantID, event.EnvMode, p.Strategy, p.Symbol, p.Direction)
	payload := mustJSON(p)

	evt, err := domain.NewStrategySignalEvent(
		event.OccurredAt.UTC(),
		event.TenantID,
		event.EnvMode,
		p.Strategy,
		corr.signalID,
		p.Symbol,
		corr.kind,
		corr.side,
		domain.SignalStatusValidated,
		"",
		p.Confidence,
		payload,
	)
	if err != nil {
		st.log.Error().Err(err).Msg("signal tracker: invalid StrategySignalEvent")
		return nil
	}

	if err := st.pnlRepo.SaveStrategySignalEvent(ctx, evt); err != nil {
		st.log.Error().Err(err).Msg("signal tracker: failed to save StrategySignalEvent")
		return nil
	}
	st.publishLifecycle(ctx, evt)
	return nil
}

func (st *SignalTracker) handleIntentRejected(ctx context.Context, event domain.Event) error {
	p, ok := event.Payload.(domain.OrderIntentEventPayload)
	if !ok {
		return nil
	}
	if p.Strategy == "" || p.Symbol == "" {
		return nil
	}

	corr := st.getOrDeriveCorr(event.TenantID, event.EnvMode, p.Strategy, p.Symbol, p.Direction)
	payload := mustJSON(p)

	evt, err := domain.NewStrategySignalEvent(
		event.OccurredAt.UTC(),
		event.TenantID,
		event.EnvMode,
		p.Strategy,
		corr.signalID,
		p.Symbol,
		corr.kind,
		corr.side,
		domain.SignalStatusRejected,
		p.Reason,
		p.Confidence,
		payload,
	)
	if err != nil {
		st.log.Error().Err(err).Msg("signal tracker: invalid StrategySignalEvent")
		return nil
	}

	if err := st.pnlRepo.SaveStrategySignalEvent(ctx, evt); err != nil {
		st.log.Error().Err(err).Msg("signal tracker: failed to save StrategySignalEvent")
		return nil
	}
	st.publishLifecycle(ctx, evt)
	return nil
}

func (st *SignalTracker) handleFill(ctx context.Context, event domain.Event) error {
	p, ok := event.Payload.(map[string]any)
	if !ok {
		return nil
	}

	strategy, _ := p["strategy"].(string)
	symbol, _ := p["symbol"].(string)
	sideAny, _ := p["side"].(string)
	filledAt, _ := p["filled_at"].(time.Time)
	if strategy == "" || symbol == "" {
		return nil
	}

	side := strings.ToUpper(sideAny)
	corr := st.getOrDeriveCorr(event.TenantID, event.EnvMode, strategy, symbol, "")
	if side != "" {
		corr.side = side
	}

	ts := event.OccurredAt.UTC()
	if !filledAt.IsZero() {
		ts = filledAt.UTC()
	}

	payload := mustJSON(p)
	evt, err := domain.NewStrategySignalEvent(
		ts,
		event.TenantID,
		event.EnvMode,
		strategy,
		corr.signalID,
		symbol,
		corr.kind,
		corr.side,
		domain.SignalStatusExecuted,
		"",
		0,
		payload,
	)
	if err != nil {
		st.log.Error().Err(err).Msg("signal tracker: invalid StrategySignalEvent")
		return nil
	}

	if err := st.pnlRepo.SaveStrategySignalEvent(ctx, evt); err != nil {
		st.log.Error().Err(err).Msg("signal tracker: failed to save StrategySignalEvent")
		return nil
	}
	st.publishLifecycle(ctx, evt)
	return nil
}

func (st *SignalTracker) publishLifecycle(ctx context.Context, evt domain.StrategySignalEvent) {
	idem := fmt.Sprintf("%s:%s:%s", evt.SignalID, evt.Strategy, evt.Status)
	ev, err := domain.NewEvent(domain.EventStrategySignalLifecycle, evt.TenantID, evt.EnvMode, idem, evt)
	if err != nil {
		return
	}
	_ = st.eventBus.Publish(ctx, *ev)
}

func (st *SignalTracker) scopeKey(tenantID string, envMode domain.EnvMode, strategy string, symbol string) string {
	return fmt.Sprintf("%s:%s:%s:%s", tenantID, string(envMode), strategy, symbol)
}

func (st *SignalTracker) getOrDeriveCorr(tenantID string, envMode domain.EnvMode, strategy string, symbol string, direction string) signalCorr {
	scopeKey := st.scopeKey(tenantID, envMode, strategy, symbol)

	st.mu.Lock()
	ref, ok := st.latestByScope[scopeKey]
	st.mu.Unlock()
	if ok && ref.signalID != "" {
		return ref
	}

	ref = signalCorr{signalID: uuid.NewString(), kind: "", side: ""}
	kind, side := deriveKindSideFromDirection(direction)
	ref.kind = kind
	ref.side = side

	st.mu.Lock()
	st.latestByScope[scopeKey] = ref
	st.mu.Unlock()

	return ref
}

func deriveKindSideFromDirection(direction string) (string, string) {
	switch strings.ToUpper(direction) {
	case string(domain.DirectionCloseLong):
		return "exit", "SELL"
	case string(domain.DirectionCloseShort):
		return "exit", "BUY"
	case string(domain.DirectionShort):
		return "entry", "SELL"
	case string(domain.DirectionLong):
		return "entry", "BUY"
	default:
		return "", ""
	}
}

func parseStrategyIDFromInstanceLocal(instanceID start.InstanceID) (string, bool) {
	parts := strings.SplitN(instanceID.String(), ":", 3)
	if len(parts) < 1 {
		return "", false
	}
	strategy := strings.TrimSpace(parts[0])
	if strategy == "" {
		return "", false
	}
	return strategy, true
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}
