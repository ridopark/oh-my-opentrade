package positionmonitor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockEventBus struct {
	mu          sync.Mutex
	subscribers map[string][]ports.EventHandler
	published   []domain.Event
}

func (m *mockEventBus) Publish(ctx context.Context, event domain.Event) error {
	m.mu.Lock()
	m.published = append(m.published, event)
	handlers := append([]ports.EventHandler(nil), m.subscribers[event.Type]...)
	m.mu.Unlock()

	for _, h := range handlers {
		_ = h(ctx, event)
	}
	return nil
}

func (m *mockEventBus) Subscribe(ctx context.Context, eventType domain.EventType, handler ports.EventHandler) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subscribers == nil {
		m.subscribers = make(map[string][]ports.EventHandler)
	}
	m.subscribers[eventType] = append(m.subscribers[eventType], handler)
	return nil
}

func (m *mockEventBus) Unsubscribe(context.Context, domain.EventType, ports.EventHandler) error {
	return nil
}

func (m *mockEventBus) publishedCount(eventType domain.EventType) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, ev := range m.published {
		if ev.Type == eventType {
			count++
		}
	}
	return count
}

func (m *mockEventBus) totalPublished() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.published)
}

type mockBroker struct{}

func (m *mockBroker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	return "", nil
}
func (m *mockBroker) CancelOrder(ctx context.Context, orderID string) error { return nil }
func (m *mockBroker) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	return "", nil
}
func (m *mockBroker) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}

func mustExitRule(t *testing.T, ruleType domain.ExitRuleType, params map[string]float64) domain.ExitRule {
	t.Helper()
	r, err := domain.NewExitRule(ruleType, params)
	require.NoError(t, err)
	return r
}

func TestService_processFill_FillAddsPosition(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(), WithNowFunc(func() time.Time {
		return time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	}))

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   time.Date(2026, 3, 6, 9, 31, 0, 0, time.UTC),
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	assert.Equal(t, 1, svc.PositionCount())
}

func TestService_processFill_ExitFillRemovesPosition(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   time.Date(2026, 3, 6, 9, 31, 0, 0, time.UTC),
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})
	require.Equal(t, 1, svc.PositionCount())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "SELL",
		Price:      155,
		Quantity:   10,
		FilledAt:   time.Date(2026, 3, 6, 9, 45, 0, 0, time.UTC),
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	assert.Equal(t, 0, svc.PositionCount())
}

func TestService_processFill_ScaleInUpdatesAverageEntry(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	tenantID := "tenant-1"
	envMode := domain.EnvModePaper
	svc := NewService(bus, pc, pg, tenantID, envMode, zerolog.Nop())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      100,
		Quantity:   1,
		FilledAt:   time.Date(2026, 3, 6, 9, 31, 0, 0, time.UTC),
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      110,
		Quantity:   1,
		FilledAt:   time.Date(2026, 3, 6, 9, 32, 0, 0, time.UTC),
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	key := "tenant-1:Paper:AAPL"
	pos, ok := svc.positions[key]
	require.True(t, ok)
	assert.InEpsilon(t, 105.0, pos.EntryPrice, 0.000001)
	assert.Equal(t, 2.0, pos.Quantity)
}

func TestService_Start_PublishFillReceived_ActorProcessesFill(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithTickInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	payload := map[string]any{
		"symbol":    "AAPL",
		"side":      "BUY",
		"price":     float64(150.0),
		"quantity":  float64(10.0),
		"filled_at": time.Date(2026, 3, 6, 9, 31, 0, 0, time.UTC),
		"strategy":  "orb_break_retest",
	}

	ev, err := domain.NewEvent(domain.EventFillReceived, "tenant-1", domain.EnvModePaper, "fill-1", payload)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), *ev))

	require.Eventually(t, func() bool { return svc.PositionCount() == 1 }, 500*time.Millisecond, 10*time.Millisecond)
}

func TestService_tick_TrailingStop_EmitsOutboxAndPublishesEvents(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithTickInterval(10*time.Millisecond),
	)

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   now.Add(-5 * time.Minute),
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules: []domain.ExitRule{
			mustExitRule(t, domain.ExitRuleTrailingStop, map[string]float64{"pct": 0.05}),
		},
	})
	require.Equal(t, 1, svc.PositionCount())

	pos := svc.positions["tenant-1:Paper:AAPL"]
	require.NotNil(t, pos)
	pos.HighWaterMark = 200
	pc.UpdatePrice(domain.Symbol("AAPL"), 180, now)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	require.Eventually(t, func() bool {
		return bus.publishedCount(domain.EventExitTriggered) == 1 &&
			bus.publishedCount(domain.EventOrderIntentCreated) == 1
	}, 500*time.Millisecond, 10*time.Millisecond)

	require.True(t, pos.ExitPending)
}

func TestService_tick_ExitPendingPreventsDoubleExit(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithTickInterval(10*time.Millisecond),
	)

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   now.Add(-5 * time.Minute),
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules: []domain.ExitRule{
			mustExitRule(t, domain.ExitRuleTrailingStop, map[string]float64{"pct": 0.05}),
		},
	})

	pos := svc.positions["tenant-1:Paper:AAPL"]
	require.NotNil(t, pos)
	pos.HighWaterMark = 200
	pc.UpdatePrice(domain.Symbol("AAPL"), 180, now)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	require.Eventually(t, func() bool { return bus.totalPublished() == 2 }, 500*time.Millisecond, 10*time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 2, bus.totalPublished())
}

func TestService_tick_ExitPendingTimeoutClearsLock(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(), WithNowFunc(func() time.Time { return now }))

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   now.Add(-5 * time.Minute),
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})

	pos := svc.positions["tenant-1:Paper:AAPL"]
	require.NotNil(t, pos)
	pos.ExitPending = true
	pos.ExitPendingAt = now.Add(-31 * time.Second)

	svc.tick()
	assert.False(t, pos.ExitPending)
}
