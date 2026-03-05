package perf_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mocks ---

type mockEventBus struct {
	mu       sync.Mutex
	handlers map[string][]ports.EventHandler
}

func newMockEventBus() *mockEventBus {
	return &mockEventBus{handlers: make(map[string][]ports.EventHandler)}
}

func (m *mockEventBus) Publish(ctx context.Context, event domain.Event) error {
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

func (m *mockEventBus) Subscribe(_ context.Context, eventType domain.EventType, handler ports.EventHandler) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[eventType] = append(m.handlers[eventType], handler)
	return nil
}

func (m *mockEventBus) Unsubscribe(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}

type mockPnLRepo struct {
	mu      sync.Mutex
	upserts []domain.DailyPnL
	points  []domain.EquityPoint
}

func (m *mockPnLRepo) UpsertDailyPnL(_ context.Context, pnl domain.DailyPnL) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserts = append(m.upserts, pnl)
	return nil
}

func (m *mockPnLRepo) GetDailyPnL(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.DailyPnL, error) {
	return nil, nil
}

func (m *mockPnLRepo) SaveEquityPoint(_ context.Context, pt domain.EquityPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.points = append(m.points, pt)
	return nil
}

func (m *mockPnLRepo) GetEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.EquityPoint, error) {
	return nil, nil
}

func (m *mockPnLRepo) GetDailyRealizedPnL(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) (float64, error) {
	return 0, nil
}

func (m *mockPnLRepo) GetBucketedEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time, _ string) ([]domain.EquityPoint, error) {
	return nil, nil
}

func (m *mockPnLRepo) GetMaxDrawdown(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (float64, error) {
	return 0, nil
}

func (m *mockPnLRepo) GetSharpe(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (*float64, error) {
	return nil, nil
}

type mockBroker struct{}

func (m *mockBroker) SubmitOrder(_ context.Context, _ domain.OrderIntent) (string, error) {
	return "", nil
}
func (m *mockBroker) CancelOrder(_ context.Context, _ string) error { return nil }
func (m *mockBroker) GetOrderStatus(_ context.Context, _ string) (string, error) {
	return "filled", nil
}
func (m *mockBroker) GetPositions(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}

// --- helpers ---

func makeFillEvent(t *testing.T, symbol, side string, quantity, price float64) domain.Event {
	t.Helper()
	evt, err := domain.NewEvent(domain.EventFillReceived, "default", domain.EnvModePaper, "idem-"+symbol, map[string]any{
		"broker_order_id": "order-123",
		"intent_id":       "intent-123",
		"symbol":          symbol,
		"side":            side,
		"quantity":        quantity,
		"price":           price,
		"filled_at":       time.Now(),
	})
	require.NoError(t, err)
	return *evt
}

// --- tests ---

func TestLedgerWriter_HandlesFillAndPersists(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background())
	require.NoError(t, err)

	// Publish a sell fill event
	evt := makeFillEvent(t, "AAPL", "sell", 10, 150.0)
	err = bus.Publish(context.Background(), evt)
	require.NoError(t, err)

	// Verify daily P&L was upserted
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.upserts, 1)
	assert.Equal(t, 1, repo.upserts[0].TradeCount)
	assert.Equal(t, 1500.0, repo.upserts[0].RealizedPnL) // 10 * 150 sell proceeds

	// Verify equity point was saved
	require.Len(t, repo.points, 1)
	assert.Equal(t, 101500.0, repo.points[0].Equity) // 100k + 1500
}

func TestLedgerWriter_AccumulatesMultipleFills(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background())
	require.NoError(t, err)

	ctx := context.Background()

	// Buy fill (cost: -5000)
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "buy", 10, 500.0))
	require.NoError(t, err)

	// Sell fill (proceeds: +6000)
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "sell", 10, 600.0))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.upserts, 2)

	// Last upsert should have cumulative: -5000 + 6000 = 1000
	lastPnL := repo.upserts[1]
	assert.Equal(t, 2, lastPnL.TradeCount)
	assert.InDelta(t, 1000.0, lastPnL.RealizedPnL, 0.01)
}

func TestLedgerWriter_GetDailyRealizedPnL(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background())
	require.NoError(t, err)

	// Initially zero
	pnl := lw.GetDailyRealizedPnL("default", domain.EnvModePaper)
	assert.Equal(t, 0.0, pnl)

	// After a fill
	err = bus.Publish(context.Background(), makeFillEvent(t, "AAPL", "sell", 5, 200.0))
	require.NoError(t, err)

	pnl = lw.GetDailyRealizedPnL("default", domain.EnvModePaper)
	assert.Equal(t, 1000.0, pnl) // 5 * 200
}

func TestLedgerWriter_SetAccountEquity(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 50000.0, log)

	// Update equity
	lw.SetAccountEquity(75000.0)

	// Start and process a fill to see the new equity reflected
	err := lw.Start(context.Background())
	require.NoError(t, err)

	err = bus.Publish(context.Background(), makeFillEvent(t, "AAPL", "sell", 1, 100.0))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.points, 1)
	// Equity should be 75000 (updated base) + 100 (fill) = 75100
	assert.Equal(t, 75100.0, repo.points[0].Equity)
}

func TestLedgerWriter_IgnoresInvalidPayload(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background())
	require.NoError(t, err)

	// Publish event with wrong payload type
	evt, _ := domain.NewEvent(domain.EventFillReceived, "default", domain.EnvModePaper, "bad-payload", "not-a-map")
	err = bus.Publish(context.Background(), *evt)
	assert.NoError(t, err) // should not error, just skip

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Len(t, repo.upserts, 0)
}
