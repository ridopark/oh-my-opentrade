package perf_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type signalTrackerEventBus struct {
	mu       sync.Mutex
	handlers map[string][]ports.EventHandler
}

func newSignalTrackerEventBus() *signalTrackerEventBus {
	return &signalTrackerEventBus{handlers: make(map[string][]ports.EventHandler)}
}

func (m *signalTrackerEventBus) Publish(ctx context.Context, event domain.Event) error {
	m.mu.Lock()
	handlers := append([]ports.EventHandler(nil), m.handlers[event.Type]...)
	m.mu.Unlock()
	for _, h := range handlers {
		if err := h(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (m *signalTrackerEventBus) Subscribe(_ context.Context, eventType domain.EventType, handler ports.EventHandler) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[eventType] = append(m.handlers[eventType], handler)
	return nil
}

func (m *signalTrackerEventBus) Unsubscribe(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}

type signalTrackerPnLRepo struct {
	mu     sync.Mutex
	events []domain.StrategySignalEvent
}

func (m *signalTrackerPnLRepo) SaveStrategySignalEvent(_ context.Context, evt domain.StrategySignalEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, evt)
	return nil
}

func (m *signalTrackerPnLRepo) UpsertDailyPnL(_ context.Context, _ domain.DailyPnL) error { return nil }
func (m *signalTrackerPnLRepo) GetDailyPnL(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.DailyPnL, error) {
	return nil, nil
}
func (m *signalTrackerPnLRepo) SaveEquityPoint(_ context.Context, _ domain.EquityPoint) error {
	return nil
}
func (m *signalTrackerPnLRepo) GetEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.EquityPoint, error) {
	return nil, nil
}
func (m *signalTrackerPnLRepo) GetDailyRealizedPnL(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) (float64, error) {
	return 0, nil
}
func (m *signalTrackerPnLRepo) GetBucketedEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time, _ string) ([]domain.EquityPoint, error) {
	return nil, nil
}
func (m *signalTrackerPnLRepo) GetMaxDrawdown(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (float64, error) {
	return 0, nil
}
func (m *signalTrackerPnLRepo) GetSharpe(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (*float64, error) {
	return nil, nil
}
func (m *signalTrackerPnLRepo) UpsertStrategyDailyPnL(_ context.Context, _ domain.StrategyDailyPnL) error {
	return nil
}
func (m *signalTrackerPnLRepo) GetStrategyDailyPnL(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) ([]domain.StrategyDailyPnL, error) {
	return nil, nil
}
func (m *signalTrackerPnLRepo) SaveStrategyEquityPoint(_ context.Context, _ domain.StrategyEquityPoint) error {
	return nil
}
func (m *signalTrackerPnLRepo) GetStrategyEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) ([]domain.StrategyEquityPoint, error) {
	return nil, nil
}
func (m *signalTrackerPnLRepo) GetStrategySignalEvents(_ context.Context, _ ports.StrategySignalQuery) (ports.StrategySignalPage, error) {
	return ports.StrategySignalPage{}, nil
}
func (m *signalTrackerPnLRepo) GetStrategyDashboard(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) (domain.StrategyDashboard, error) {
	return domain.StrategyDashboard{}, nil
}

func signalTrackerMakeSignalEvent(t *testing.T, instanceID string, symbol string, signalType strat.SignalType, side strat.Side, strength float64) domain.Event {
	t.Helper()

	iid, err := strat.NewInstanceID(instanceID)
	require.NoError(t, err)
	sig := strat.Signal{
		StrategyInstanceID: iid,
		Symbol:             symbol,
		Type:               signalType,
		Side:               side,
		Strength:           strength,
		Tags:               map[string]string{"ref_price": "100"},
	}
	ev, err := domain.NewEvent(domain.EventSignalCreated, "default", domain.EnvModePaper, "sig-evt-1", sig)
	require.NoError(t, err)
	return *ev
}

func signalTrackerMakeIntentEvent(t *testing.T, eventType domain.EventType, strategy, symbol, direction, status, reason string, confidence float64) domain.Event {
	t.Helper()
	payload := domain.OrderIntentEventPayload{
		ID:         "intent-1",
		Symbol:     symbol,
		Direction:  direction,
		Strategy:   strategy,
		Confidence: confidence,
		Status:     status,
		Reason:     reason,
	}
	ev, err := domain.NewEvent(eventType, "default", domain.EnvModePaper, "intent-evt-1"+string(eventType), payload)
	require.NoError(t, err)
	return *ev
}

func signalTrackerMakeFillEvent(t *testing.T, strategy, symbol, side string) domain.Event {
	t.Helper()
	ev, err := domain.NewEvent(domain.EventFillReceived, "default", domain.EnvModePaper, "fill-evt-1", map[string]any{
		"broker_order_id": "order-1",
		"intent_id":       "intent-1",
		"symbol":          symbol,
		"side":            side,
		"quantity":        1.0,
		"price":           123.45,
		"filled_at":       time.Now().UTC(),
		"strategy":        strategy,
	})
	require.NoError(t, err)
	return *ev
}

func TestSignalTracker_SignalCreated_PersistsGeneratedAndEmitsLifecycle(t *testing.T) {
	bus := newSignalTrackerEventBus()
	repo := &signalTrackerPnLRepo{}
	st := perf.NewSignalTracker(bus, repo, zerolog.Nop())
	require.NoError(t, st.Start(context.Background()))

	var lifecycle []domain.StrategySignalEvent
	require.NoError(t, bus.Subscribe(context.Background(), domain.EventStrategySignalLifecycle, func(_ context.Context, ev domain.Event) error {
		sse, ok := ev.Payload.(domain.StrategySignalEvent)
		require.True(t, ok)
		lifecycle = append(lifecycle, sse)
		return nil
	}))

	err := bus.Publish(context.Background(), signalTrackerMakeSignalEvent(t, "orb_break_retest:1:AAPL", "AAPL", strat.SignalEntry, strat.SideBuy, 0.88))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.events, 1)

	got := repo.events[0]
	assert.Equal(t, "orb_break_retest", got.Strategy)
	assert.Equal(t, "AAPL", got.Symbol)
	assert.Equal(t, "entry", got.Kind)
	assert.Equal(t, "BUY", got.Side)
	assert.Equal(t, domain.SignalStatusGenerated, got.Status)
	assert.NotEmpty(t, got.SignalID)
	assert.InDelta(t, 0.88, got.Confidence, 0.0001)

	require.Len(t, lifecycle, 1)
	assert.Equal(t, got.SignalID, lifecycle[0].SignalID)
	assert.Equal(t, domain.SignalStatusGenerated, lifecycle[0].Status)
}

func TestSignalTracker_IntentValidated_PersistsValidatedAndCorrelatesSignalID(t *testing.T) {
	bus := newSignalTrackerEventBus()
	repo := &signalTrackerPnLRepo{}
	st := perf.NewSignalTracker(bus, repo, zerolog.Nop())
	require.NoError(t, st.Start(context.Background()))

	var lifecycle []domain.StrategySignalEvent
	_ = bus.Subscribe(context.Background(), domain.EventStrategySignalLifecycle, func(_ context.Context, ev domain.Event) error {
		sse, ok := ev.Payload.(domain.StrategySignalEvent)
		if ok {
			lifecycle = append(lifecycle, sse)
		}
		return nil
	})

	require.NoError(t, bus.Publish(context.Background(), signalTrackerMakeSignalEvent(t, "orb_break_retest:1:AAPL", "AAPL", strat.SignalEntry, strat.SideBuy, 0.91)))
	require.NoError(t, bus.Publish(context.Background(), signalTrackerMakeIntentEvent(t, domain.EventOrderIntentValidated, "orb_break_retest", "AAPL", "LONG", "validated", "", 0.91)))

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.events, 2)
	created := repo.events[0]
	validated := repo.events[1]
	assert.Equal(t, created.SignalID, validated.SignalID)
	assert.Equal(t, domain.SignalStatusValidated, validated.Status)
	assert.Equal(t, "entry", validated.Kind)
	assert.Equal(t, "BUY", validated.Side)
	assert.Equal(t, "orb_break_retest", validated.Strategy)
	assert.Equal(t, "AAPL", validated.Symbol)

	require.Len(t, lifecycle, 2)
	assert.Equal(t, domain.SignalStatusValidated, lifecycle[1].Status)
}

func TestSignalTracker_IntentRejected_PersistsRejectedWithReason(t *testing.T) {
	bus := newSignalTrackerEventBus()
	repo := &signalTrackerPnLRepo{}
	st := perf.NewSignalTracker(bus, repo, zerolog.Nop())
	require.NoError(t, st.Start(context.Background()))

	require.NoError(t, bus.Publish(context.Background(), signalTrackerMakeSignalEvent(t, "orb_break_retest:1:AAPL", "AAPL", strat.SignalEntry, strat.SideBuy, 0.77)))
	require.NoError(t, bus.Publish(context.Background(), signalTrackerMakeIntentEvent(t, domain.EventOrderIntentRejected, "orb_break_retest", "AAPL", "LONG", "rejected", "risk_gate", 0.77)))

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.events, 2)

	rejected := repo.events[1]
	assert.Equal(t, domain.SignalStatusRejected, rejected.Status)
	assert.Equal(t, "risk_gate", rejected.Reason)
	assert.NotEmpty(t, rejected.SignalID)
}

func TestSignalTracker_FillReceived_PersistsExecutedAndCorrelatesSignalID(t *testing.T) {
	bus := newSignalTrackerEventBus()
	repo := &signalTrackerPnLRepo{}
	st := perf.NewSignalTracker(bus, repo, zerolog.Nop())
	require.NoError(t, st.Start(context.Background()))

	require.NoError(t, bus.Publish(context.Background(), signalTrackerMakeSignalEvent(t, "orb_break_retest:1:AAPL", "AAPL", strat.SignalEntry, strat.SideBuy, 0.66)))
	require.NoError(t, bus.Publish(context.Background(), signalTrackerMakeFillEvent(t, "orb_break_retest", "AAPL", "BUY")))

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.events, 2)
	created := repo.events[0]
	executed := repo.events[1]
	assert.Equal(t, created.SignalID, executed.SignalID)
	assert.Equal(t, domain.SignalStatusExecuted, executed.Status)
	assert.Equal(t, "orb_break_retest", executed.Strategy)
	assert.Equal(t, "AAPL", executed.Symbol)
	assert.Equal(t, "BUY", executed.Side)
}

func TestSignalTracker_UnknownPayloadTypes_AreSilentlySkipped(t *testing.T) {
	bus := newSignalTrackerEventBus()
	repo := &signalTrackerPnLRepo{}
	st := perf.NewSignalTracker(bus, repo, zerolog.Nop())
	require.NoError(t, st.Start(context.Background()))

	var lifecycleCount int
	_ = bus.Subscribe(context.Background(), domain.EventStrategySignalLifecycle, func(_ context.Context, _ domain.Event) error {
		lifecycleCount++
		return nil
	})

	bad, err := domain.NewEvent(domain.EventSignalCreated, "default", domain.EnvModePaper, "bad-sig", "not-a-signal")
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), *bad))

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Len(t, repo.events, 0)
	assert.Equal(t, 0, lifecycleCount)
}
