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
	mu           sync.Mutex
	upserts      []domain.DailyPnL
	points       []domain.EquityPoint
	stratUpserts []domain.StrategyDailyPnL
	stratPoints  []domain.StrategyEquityPoint
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
func (m *mockPnLRepo) UpsertStrategyDailyPnL(_ context.Context, pnl domain.StrategyDailyPnL) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stratUpserts = append(m.stratUpserts, pnl)
	return nil
}
func (m *mockPnLRepo) GetStrategyDailyPnL(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) ([]domain.StrategyDailyPnL, error) {
	return nil, nil
}
func (m *mockPnLRepo) SaveStrategyEquityPoint(_ context.Context, pt domain.StrategyEquityPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stratPoints = append(m.stratPoints, pt)
	return nil
}
func (m *mockPnLRepo) GetStrategyEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) ([]domain.StrategyEquityPoint, error) {
	return nil, nil
}
func (m *mockPnLRepo) SaveStrategySignalEvent(_ context.Context, _ domain.StrategySignalEvent) error {
	return nil
}
func (m *mockPnLRepo) GetStrategySignalEvents(_ context.Context, _ ports.StrategySignalQuery) (ports.StrategySignalPage, error) {
	return ports.StrategySignalPage{}, nil
}
func (m *mockPnLRepo) GetStrategyDashboard(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) (domain.StrategyDashboard, error) {
	return domain.StrategyDashboard{}, nil
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
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	// Buy 10 shares at $150 to establish position
	evt := makeFillEvent(t, "AAPL", "buy", 10, 150.0)
	err = bus.Publish(context.Background(), evt)
	require.NoError(t, err)

	// Sell 10 shares at $160 to close position (realized P&L = $100)
	evt2 := makeFillEvent(t, "AAPL", "sell", 10, 160.0)
	err = bus.Publish(context.Background(), evt2)
	require.NoError(t, err)

	// Verify daily P&L was upserted
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.upserts, 2)
	assert.Equal(t, 2, repo.upserts[1].TradeCount)
	assert.InDelta(t, 100.0, repo.upserts[1].RealizedPnL, 0.01) // (160-150)*10

	// Verify equity points were saved (one per fill)
	require.Len(t, repo.points, 2)
	assert.Equal(t, 100000.0, repo.points[0].Equity)         // buy: no P&L change
	assert.InDelta(t, 100100.0, repo.points[1].Equity, 0.01) // sell: 100k + 100 realized
}

func TestLedgerWriter_AccumulatesMultipleFills(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	ctx := context.Background()

	// Buy 10 @ $500 (no realized P&L)
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "buy", 10, 500.0))
	require.NoError(t, err)

	// Sell 10 @ $600 (realized P&L = (600-500)*10 = $1000)
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "sell", 10, 600.0))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.upserts, 2)

	// Buy should have zero P&L
	assert.InDelta(t, 0.0, repo.upserts[0].RealizedPnL, 0.01)

	// Sell should have cumulative realized P&L of $1000
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
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	// Initially zero
	pnl := lw.GetDailyRealizedPnL("default", domain.EnvModePaper)
	assert.Equal(t, 0.0, pnl)

	// Buy 5 @ $200 — no realized P&L
	err = bus.Publish(context.Background(), makeFillEvent(t, "AAPL", "buy", 5, 200.0))
	require.NoError(t, err)

	pnl = lw.GetDailyRealizedPnL("default", domain.EnvModePaper)
	assert.Equal(t, 0.0, pnl) // buy = no realized P&L

	// Sell 5 @ $220 — realized P&L = (220-200)*5 = $100
	err = bus.Publish(context.Background(), makeFillEvent(t, "AAPL", "sell", 5, 220.0))
	require.NoError(t, err)

	pnl = lw.GetDailyRealizedPnL("default", domain.EnvModePaper)
	assert.InDelta(t, 100.0, pnl, 0.01)
}

func TestLedgerWriter_SetAccountEquity(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 50000.0, log)

	// Update equity
	lw.SetAccountEquity(75000.0)

	// Start and process a buy+sell to see the new equity reflected
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	// Buy 1 @ $100
	err = bus.Publish(context.Background(), makeFillEvent(t, "AAPL", "buy", 1, 100.0))
	require.NoError(t, err)

	// Sell 1 @ $110 (realized P&L = $10)
	err = bus.Publish(context.Background(), makeFillEvent(t, "AAPL", "sell", 1, 110.0))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.points, 2)
	// Buy: equity = 75000 + 0 (no P&L) = 75000
	assert.Equal(t, 75000.0, repo.points[0].Equity)
	// Sell: equity = 75000 + 10 (realized P&L) = 75010
	assert.InDelta(t, 75010.0, repo.points[1].Equity, 0.01)
}

func TestLedgerWriter_IgnoresInvalidPayload(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	// Publish event with wrong payload type
	evt, _ := domain.NewEvent(domain.EventFillReceived, "default", domain.EnvModePaper, "bad-payload", "not-a-map")
	err = bus.Publish(context.Background(), *evt)
	assert.NoError(t, err) // should not error, just skip

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Len(t, repo.upserts, 0)
}

func TestLedgerWriter_PartialSellRealizesPartialPnL(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	ctx := context.Background()

	// Buy 10 @ $100
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "buy", 10, 100.0))
	require.NoError(t, err)

	// Sell only 5 @ $120 (partial close — realized = (120-100)*5 = $100)
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "sell", 5, 120.0))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.upserts, 2)
	assert.InDelta(t, 100.0, repo.upserts[1].RealizedPnL, 0.01)
}

func TestLedgerWriter_MultipleBuysAverageEntry(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	ctx := context.Background()

	// Buy 10 @ $100, then buy 10 @ $200 → avg entry = $150
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "buy", 10, 100.0))
	require.NoError(t, err)
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "buy", 10, 200.0))
	require.NoError(t, err)

	// Sell all 20 @ $180 → realized = (180-150)*20 = $600
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "sell", 20, 180.0))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.upserts, 3)
	assert.InDelta(t, 600.0, repo.upserts[2].RealizedPnL, 0.01)
}

func TestLedgerWriter_SellWithoutPositionRecordsZero(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	// Sell without any prior buy — should record zero P&L (not a crash)
	err = bus.Publish(context.Background(), makeFillEvent(t, "AAPL", "sell", 10, 150.0))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.upserts, 1)
	assert.Equal(t, 0.0, repo.upserts[0].RealizedPnL)
	assert.Equal(t, 100000.0, repo.points[0].Equity) // equity unchanged
}

func TestLedgerWriter_BuyProducesZeroPnL(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	// Buy fill should not affect realized P&L at all
	err = bus.Publish(context.Background(), makeFillEvent(t, "AAPL", "buy", 100, 500.0))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.upserts, 1)
	assert.Equal(t, 0.0, repo.upserts[0].RealizedPnL) // buy = zero realized P&L
	assert.Equal(t, 100000.0, repo.points[0].Equity)  // equity unchanged on buy
}

func makeStrategyFillEvent(t *testing.T, symbol, side string, quantity, price float64, strategy string) domain.Event {
	t.Helper()
	evt, err := domain.NewEvent(domain.EventFillReceived, "default", domain.EnvModePaper, "idem-"+symbol+"-"+strategy, map[string]any{
		"broker_order_id": "order-123",
		"intent_id":       "intent-123",
		"symbol":          symbol,
		"side":            side,
		"quantity":        quantity,
		"price":           price,
		"strategy":        strategy,
		"filled_at":       time.Now(),
	})
	require.NoError(t, err)
	return *evt
}

// --- per-strategy dual-write tests ---

func TestLedgerWriter_StrategyDualWrite_BuySellRecordsPnL(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	ctx := context.Background()

	// Buy 10 AAPL @ $150 via orb_break_retest
	err = bus.Publish(ctx, makeStrategyFillEvent(t, "AAPL", "buy", 10, 150.0, "orb_break_retest"))
	require.NoError(t, err)

	// Sell 10 AAPL @ $160 via orb_break_retest (realized = $100)
	err = bus.Publish(ctx, makeStrategyFillEvent(t, "AAPL", "sell", 10, 160.0, "orb_break_retest"))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	// Global P&L still works
	require.Len(t, repo.upserts, 2)
	assert.InDelta(t, 100.0, repo.upserts[1].RealizedPnL, 0.01)

	// Per-strategy P&L was dual-written
	require.Len(t, repo.stratUpserts, 2)
	assert.Equal(t, "orb_break_retest", repo.stratUpserts[0].Strategy)
	assert.Equal(t, 0.0, repo.stratUpserts[0].RealizedPnL) // buy = zero
	assert.Equal(t, 1, repo.stratUpserts[0].TradeCount)

	assert.Equal(t, "orb_break_retest", repo.stratUpserts[1].Strategy)
	assert.InDelta(t, 100.0, repo.stratUpserts[1].RealizedPnL, 0.01) // sell
	assert.Equal(t, 2, repo.stratUpserts[1].TradeCount)
	assert.Equal(t, 1, repo.stratUpserts[1].WinCount)
	assert.Equal(t, 0, repo.stratUpserts[1].LossCount)
	assert.InDelta(t, 100.0, repo.stratUpserts[1].GrossProfit, 0.01)

	// Per-strategy equity points were dual-written
	require.Len(t, repo.stratPoints, 2)
	assert.Equal(t, "orb_break_retest", repo.stratPoints[0].Strategy)
	assert.Equal(t, "orb_break_retest", repo.stratPoints[1].Strategy)
	assert.InDelta(t, 100.0, repo.stratPoints[1].RealizedPnLToDate, 0.01)
	assert.Equal(t, 2, repo.stratPoints[1].TradeCountToDate)
}

func TestLedgerWriter_StrategyDualWrite_NoStrategySkipsDualWrite(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	ctx := context.Background()

	// Fill without strategy field (legacy behavior)
	err = bus.Publish(ctx, makeFillEvent(t, "AAPL", "buy", 10, 150.0))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	// Global P&L should be written
	require.Len(t, repo.upserts, 1)

	// Per-strategy should NOT be written
	assert.Len(t, repo.stratUpserts, 0)
	assert.Len(t, repo.stratPoints, 0)
}

func TestLedgerWriter_StrategyDualWrite_MultipleStrategiesSameSymbol(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	ctx := context.Background()

	// ORB buys 10 AAPL @ $100
	err = bus.Publish(ctx, makeStrategyFillEvent(t, "AAPL", "buy", 10, 100.0, "orb_break_retest"))
	require.NoError(t, err)

	// AVWAP buys 5 AAPL @ $105
	err = bus.Publish(ctx, makeStrategyFillEvent(t, "AAPL", "buy", 5, 105.0, "avwap_v1"))
	require.NoError(t, err)

	// ORB sells 10 AAPL @ $110 (realized = (110-100)*10 = $100)
	err = bus.Publish(ctx, makeStrategyFillEvent(t, "AAPL", "sell", 10, 110.0, "orb_break_retest"))
	require.NoError(t, err)

	// AVWAP sells 5 AAPL @ $108 (realized = (108-105)*5 = $15)
	err = bus.Publish(ctx, makeStrategyFillEvent(t, "AAPL", "sell", 5, 108.0, "avwap_v1"))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	// Global: 4 fills total, cumulative P&L = 100 + 15 = 115
	// Note: global position tracking sees 15 shares bought then 15 sold
	// Buy 10@100 → avg=100, Buy 5@105 → avg=(1000+525)/15=101.67
	// Sell 10@110 → PnL = (110-101.67)*10 = 83.33, remaining 5@101.67
	// Sell 5@108 → PnL = (108-101.67)*5 = 31.67
	// Total global PnL = 83.33 + 31.67 = 115.0
	require.Len(t, repo.upserts, 4)
	assert.InDelta(t, 115.0, repo.upserts[3].RealizedPnL, 0.01)

	// Per-strategy: 4 fills (2 per strategy)
	require.Len(t, repo.stratUpserts, 4)

	// Find the last ORB and AVWAP upserts
	var lastORB, lastAVWAP domain.StrategyDailyPnL
	for _, u := range repo.stratUpserts {
		switch u.Strategy {
		case "orb_break_retest":
			lastORB = u
		case "avwap_v1":
			lastAVWAP = u
		}
	}

	assert.InDelta(t, 100.0, lastORB.RealizedPnL, 0.01)
	assert.Equal(t, 2, lastORB.TradeCount)
	assert.Equal(t, 1, lastORB.WinCount)

	assert.InDelta(t, 15.0, lastAVWAP.RealizedPnL, 0.01)
	assert.Equal(t, 2, lastAVWAP.TradeCount)
	assert.Equal(t, 1, lastAVWAP.WinCount)
}

func TestLedgerWriter_StrategyDualWrite_LossTracking(t *testing.T) {
	bus := newMockEventBus()
	repo := &mockPnLRepo{}
	broker := &mockBroker{}
	log := zerolog.Nop()

	lw := perf.NewLedgerWriter(bus, repo, broker, 100000.0, log)
	err := lw.Start(context.Background(), "default", domain.EnvModePaper)
	require.NoError(t, err)

	ctx := context.Background()

	// Buy 10 @ $150
	err = bus.Publish(ctx, makeStrategyFillEvent(t, "AAPL", "buy", 10, 150.0, "orb_break_retest"))
	require.NoError(t, err)

	// Sell 10 @ $140 — loss of $100
	err = bus.Publish(ctx, makeStrategyFillEvent(t, "AAPL", "sell", 10, 140.0, "orb_break_retest"))
	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	require.Len(t, repo.stratUpserts, 2)
	last := repo.stratUpserts[1]
	assert.InDelta(t, -100.0, last.RealizedPnL, 0.01)
	assert.Equal(t, 0, last.WinCount)
	assert.Equal(t, 1, last.LossCount)
	assert.Equal(t, 0.0, last.GrossProfit)
	assert.InDelta(t, -100.0, last.GrossLoss, 0.01)
}
