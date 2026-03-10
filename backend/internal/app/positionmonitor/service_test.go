package positionmonitor

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
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

func (m *mockEventBus) SubscribeAsync(ctx context.Context, et domain.EventType, h ports.EventHandler) error {
	return m.Subscribe(ctx, et, h)
}

func (m *mockEventBus) Unsubscribe(context.Context, domain.EventType, ports.EventHandler) error {
	return nil
}

func (m *mockEventBus) Close() {}

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

type mockBroker struct {
	positions []domain.Trade
	posErr    error
}

func (m *mockBroker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	return "", nil
}
func (m *mockBroker) CancelOrder(ctx context.Context, orderID string) error { return nil }
func (m *mockBroker) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	return "", nil
}
func (m *mockBroker) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	return m.positions, m.posErr
}
func (m *mockBroker) CancelOpenOrders(_ context.Context, _ domain.Symbol, _ string) (int, error) {
	return 0, nil
}

type mockBrokerWithCancel struct {
	mockBroker
	lastCancelledID string
}

func (m *mockBrokerWithCancel) CancelOrder(_ context.Context, orderID string) error {
	m.lastCancelledID = orderID
	return nil
}

type mockRepo struct {
	trades    []domain.Trade
	tradesErr error
}

func (m *mockRepo) SaveMarketBar(ctx context.Context, bar domain.MarketBar) error { return nil }
func (m *mockRepo) GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (m *mockRepo) SaveTrade(ctx context.Context, trade domain.Trade) error { return nil }
func (m *mockRepo) GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error) {
	return m.trades, m.tradesErr
}
func (m *mockRepo) SaveStrategyDNA(ctx context.Context, dna domain.StrategyDNA) error { return nil }
func (m *mockRepo) GetLatestStrategyDNA(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (m *mockRepo) SaveOrder(ctx context.Context, order domain.BrokerOrder) error { return nil }
func (m *mockRepo) UpdateOrderFill(ctx context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64) error {
	return nil
}
func (m *mockRepo) ListTrades(ctx context.Context, q ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (m *mockRepo) ListOrders(ctx context.Context, q ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (m *mockRepo) SaveThoughtLog(ctx context.Context, tl domain.ThoughtLog) error { return nil }
func (m *mockRepo) GetThoughtLogsByIntentID(ctx context.Context, intentID string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (m *mockRepo) UpdateTradeThesis(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ json.RawMessage) error {
	return nil
}
func (m *mockRepo) GetMaxBarHighSince(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time) (float64, error) {
	return 0, nil
}

type mockSpecStore struct {
	specs map[string]*portstrategy.Spec
}

func (m *mockSpecStore) List(ctx context.Context, filter *portstrategy.SpecFilter) ([]portstrategy.Spec, error) {
	var result []portstrategy.Spec
	for _, sp := range m.specs {
		if sp != nil {
			result = append(result, *sp)
		}
	}
	return result, nil
}
func (m *mockSpecStore) Get(ctx context.Context, id domstrategy.StrategyID, version domstrategy.Version) (*portstrategy.Spec, error) {
	return nil, nil
}
func (m *mockSpecStore) GetLatest(ctx context.Context, id domstrategy.StrategyID) (*portstrategy.Spec, error) {
	if m.specs == nil {
		return nil, assert.AnError
	}
	if spec, ok := m.specs[id.String()]; ok {
		return spec, nil
	}
	return nil, assert.AnError
}
func (m *mockSpecStore) Save(ctx context.Context, spec portstrategy.Spec) error { return nil }
func (m *mockSpecStore) Watch(ctx context.Context) (<-chan domstrategy.StrategyID, error) {
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
	pos.ExitPendingAt = now.Add(-11 * time.Second)

	svc.tick()
	assert.False(t, pos.ExitPending)
}

func TestService_tick_ExitTimeoutIncrementsRetryCount(t *testing.T) {
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
	pos.ExitPendingAt = now.Add(-11 * time.Second)
	pos.ExitRetryCount = 0

	svc.tick()
	assert.False(t, pos.ExitPending)
	assert.Equal(t, 1, pos.ExitRetryCount)
}

func TestService_tick_ExitTimeoutCancelsStaleOrder(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	broker := &mockBrokerWithCancel{}
	pg := execution.NewPositionGate(broker, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithBroker(broker),
	)

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("BTC/USD"),
		Side:       "BUY",
		Price:      67000,
		Quantity:   0.15,
		FilledAt:   now.Add(-5 * time.Minute),
		Strategy:   "avwap_v1",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})

	pos := svc.positions["tenant-1:Paper:BTC/USD"]
	require.NotNil(t, pos)
	pos.ExitPending = true
	pos.ExitPendingAt = now.Add(-11 * time.Second)
	pos.ExitOrderID = "stale-order-123"

	svc.tick()
	assert.False(t, pos.ExitPending)
	assert.Equal(t, "", pos.ExitOrderID)
	assert.Equal(t, 1, pos.ExitRetryCount)
	assert.Equal(t, "stale-order-123", broker.lastCancelledID)
}

func TestService_partialFill_KeepsExitPending(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("BTC/USD"),
		Side:       "BUY",
		Price:      67000,
		Quantity:   0.15,
		FilledAt:   time.Now(),
		Strategy:   "avwap_v1",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})

	pos := svc.positions["tenant-1:Paper:BTC/USD"]
	require.NotNil(t, pos)
	pos.ExitPending = true
	pos.ExitOrderID = "exit-order-123"

	svc.processFill(fillMsg{
		Symbol:   domain.Symbol("BTC/USD"),
		Side:     "SELL",
		Price:    66900,
		Quantity: 0.05,
	})

	assert.Equal(t, 1, svc.PositionCount())
	assert.True(t, pos.ExitPending, "partial fill must NOT clear ExitPending")
	assert.InEpsilon(t, 0.10, pos.Quantity, 0.001)
}

func TestExitOrderParams_Escalation(t *testing.T) {
	price := 67000.0

	t.Run("retry 0: 2% buffer IOC", func(t *testing.T) {
		p, ot, tif := exitOrderParams(domain.ExitRuleMaxHoldingTime, price, 0)
		assert.InEpsilon(t, price*0.98, p, 0.01)
		assert.Equal(t, "limit", ot)
		assert.Equal(t, "ioc", tif)
	})

	t.Run("retry 1: 3% buffer IOC", func(t *testing.T) {
		p, ot, tif := exitOrderParams(domain.ExitRuleMaxHoldingTime, price, 1)
		assert.InEpsilon(t, price*0.97, p, 0.01)
		assert.Equal(t, "limit", ot)
		assert.Equal(t, "ioc", tif)
	})

	t.Run("retry 2: 5% buffer IOC", func(t *testing.T) {
		p, ot, tif := exitOrderParams(domain.ExitRuleMaxHoldingTime, price, 2)
		assert.InEpsilon(t, price*0.95, p, 0.01)
		assert.Equal(t, "limit", ot)
		assert.Equal(t, "ioc", tif)
	})

	t.Run("retry 3+: market order", func(t *testing.T) {
		_, ot, tif := exitOrderParams(domain.ExitRuleMaxHoldingTime, price, 3)
		assert.Equal(t, "market", ot)
		assert.Equal(t, "ioc", tif)
	})

	t.Run("non-forced exit: regular limit", func(t *testing.T) {
		p, ot, tif := exitOrderParams(domain.ExitRuleTrailingStop, price, 0)
		assert.Equal(t, price, p)
		assert.Equal(t, "limit", ot)
		assert.Equal(t, "", tif)
	})
}

func TestService_processExitSubmitted_TracksOrderIDAndSetsExitPending(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 7, 22, 35, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
	)

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("BTC/USD"),
		Side:       "BUY",
		Price:      67000,
		Quantity:   0.15,
		FilledAt:   now,
		Strategy:   "avwap_v1",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})

	svc.processExitSubmitted(exitOrderSubmittedMsg{
		Symbol:        domain.Symbol("BTC/USD"),
		BrokerOrderID: "broker-exit-456",
		Direction:     "CLOSE_LONG",
	})

	pos := svc.positions["tenant-1:Paper:BTC/USD"]
	require.NotNil(t, pos)
	assert.Equal(t, "broker-exit-456", pos.ExitOrderID)
	assert.True(t, pos.ExitPending, "processExitSubmitted must set ExitPending=true")
	assert.Equal(t, now, pos.ExitPendingAt)
}

func TestService_processExitSubmitted_DoesNotResetExitPendingAt(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 7, 22, 35, 0, 0, time.UTC)
	earlier := now.Add(-5 * time.Second)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
	)

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("BTC/USD"),
		Side:       "BUY",
		Price:      67000,
		Quantity:   0.15,
		FilledAt:   now,
		Strategy:   "avwap_v1",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})

	pos := svc.positions["tenant-1:Paper:BTC/USD"]
	pos.ExitPending = true
	pos.ExitPendingAt = earlier

	svc.processExitSubmitted(exitOrderSubmittedMsg{
		Symbol:        domain.Symbol("BTC/USD"),
		BrokerOrderID: "broker-exit-789",
		Direction:     "CLOSE_LONG",
	})

	assert.True(t, pos.ExitPending)
	assert.Equal(t, earlier, pos.ExitPendingAt, "must not reset ExitPendingAt if already pending")
}

func TestService_processExitTerminal_ClearsExitPending(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("BTC/USD"),
		Side:       "BUY",
		Price:      67000,
		Quantity:   0.15,
		FilledAt:   time.Now(),
		Strategy:   "avwap_v1",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})

	pos := svc.positions["tenant-1:Paper:BTC/USD"]
	pos.ExitPending = true
	pos.ExitOrderID = "exit-order-ABC"
	pos.ExitRetryCount = 0

	svc.processExitTerminal(exitOrderTerminalMsg{
		Symbol:        domain.Symbol("BTC/USD"),
		BrokerOrderID: "exit-order-ABC",
	})

	assert.False(t, pos.ExitPending, "terminal event must clear ExitPending")
	assert.Equal(t, "", pos.ExitOrderID)
	assert.Equal(t, 1, pos.ExitRetryCount)
}

func TestService_processExitTerminal_IgnoresMismatchedOrderID(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("BTC/USD"),
		Side:       "BUY",
		Price:      67000,
		Quantity:   0.15,
		FilledAt:   time.Now(),
		Strategy:   "avwap_v1",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})

	pos := svc.positions["tenant-1:Paper:BTC/USD"]
	pos.ExitPending = true
	pos.ExitOrderID = "current-exit-order"

	svc.processExitTerminal(exitOrderTerminalMsg{
		Symbol:        domain.Symbol("BTC/USD"),
		BrokerOrderID: "stale-old-order",
	})

	assert.True(t, pos.ExitPending, "must NOT clear ExitPending for mismatched order ID")
	assert.Equal(t, "current-exit-order", pos.ExitOrderID)
}

func TestService_bootstrapPositions_RestoresOMOPositionThatExistsOnBroker(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())

	broker := &mockBroker{positions: []domain.Trade{{
		Symbol:     domain.Symbol("AAPL"),
		Quantity:   10,
		Price:      150,
		AssetClass: domain.AssetClassEquity,
		TenantID:   "tenant-1",
		EnvMode:    domain.EnvModePaper,
	}}}
	pg := execution.NewPositionGate(broker, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	repo := &mockRepo{trades: []domain.Trade{{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		Time:       now.Add(-10 * time.Minute),
		TenantID:   "tenant-1",
		EnvMode:    domain.EnvModePaper,
	}}}

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
		WithRepo(repo),
		WithNowFunc(func() time.Time { return now }),
	)

	svc.bootstrapPositions(context.Background())

	require.Equal(t, 1, svc.PositionCount())
	pos, ok := svc.positions["tenant-1:Paper:AAPL"]
	require.True(t, ok)
	assert.Equal(t, 150.0, pos.EntryPrice)
	assert.Equal(t, 10.0, pos.Quantity)
	assert.Equal(t, "orb_break_retest", pos.Strategy)
}

func TestService_bootstrapPositions_SkipsPositionsNotOnBroker(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	broker := &mockBroker{positions: nil}
	pg := execution.NewPositionGate(broker, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	repo := &mockRepo{trades: []domain.Trade{{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		Time:       now.Add(-10 * time.Minute),
		TenantID:   "tenant-1",
		EnvMode:    domain.EnvModePaper,
	}}}

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
		WithRepo(repo),
		WithNowFunc(func() time.Time { return now }),
	)

	svc.bootstrapPositions(context.Background())
	assert.Equal(t, 0, svc.PositionCount())
}

func TestService_bootstrapPositions_SkipsManuallyOpenedBrokerPositions(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	broker := &mockBroker{positions: []domain.Trade{{
		Symbol:     domain.Symbol("TSLA"),
		Quantity:   5,
		Price:      200,
		AssetClass: domain.AssetClassEquity,
		TenantID:   "tenant-1",
		EnvMode:    domain.EnvModePaper,
	}}}
	pg := execution.NewPositionGate(broker, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	repo := &mockRepo{trades: []domain.Trade{{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		Time:       now.Add(-10 * time.Minute),
		TenantID:   "tenant-1",
		EnvMode:    domain.EnvModePaper,
	}}}

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
		WithRepo(repo),
		WithNowFunc(func() time.Time { return now }),
	)

	svc.bootstrapPositions(context.Background())
	assert.Equal(t, 0, svc.PositionCount())
}

func TestService_bootstrapPositions_HandlesClosedPositionsCorrectly(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	broker := &mockBroker{positions: nil}
	pg := execution.NewPositionGate(broker, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	repo := &mockRepo{trades: []domain.Trade{
		{
			Symbol:     domain.Symbol("AAPL"),
			Side:       "BUY",
			Price:      150,
			Quantity:   10,
			Strategy:   "orb_break_retest",
			AssetClass: domain.AssetClassEquity,
			Time:       now.Add(-20 * time.Minute),
			TenantID:   "tenant-1",
			EnvMode:    domain.EnvModePaper,
		},
		{
			Symbol:     domain.Symbol("AAPL"),
			Side:       "SELL",
			Price:      155,
			Quantity:   10,
			Strategy:   "orb_break_retest",
			AssetClass: domain.AssetClassEquity,
			Time:       now.Add(-10 * time.Minute),
			TenantID:   "tenant-1",
			EnvMode:    domain.EnvModePaper,
		},
	}}

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
		WithRepo(repo),
		WithNowFunc(func() time.Time { return now }),
	)

	svc.bootstrapPositions(context.Background())
	assert.Equal(t, 0, svc.PositionCount())
}

func TestService_bootstrapPositions_HandlesPartialFills(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())

	broker := &mockBroker{positions: []domain.Trade{{
		Symbol:     domain.Symbol("AAPL"),
		Quantity:   5,
		Price:      155,
		AssetClass: domain.AssetClassEquity,
		TenantID:   "tenant-1",
		EnvMode:    domain.EnvModePaper,
	}}}
	pg := execution.NewPositionGate(broker, zerolog.Nop())

	now := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	repo := &mockRepo{trades: []domain.Trade{
		{
			Symbol:     domain.Symbol("AAPL"),
			Side:       "BUY",
			Price:      150,
			Quantity:   10,
			Strategy:   "orb_break_retest",
			AssetClass: domain.AssetClassEquity,
			Time:       now.Add(-20 * time.Minute),
			TenantID:   "tenant-1",
			EnvMode:    domain.EnvModePaper,
		},
		{
			Symbol:     domain.Symbol("AAPL"),
			Side:       "SELL",
			Price:      152,
			Quantity:   5,
			Strategy:   "orb_break_retest",
			AssetClass: domain.AssetClassEquity,
			Time:       now.Add(-10 * time.Minute),
			TenantID:   "tenant-1",
			EnvMode:    domain.EnvModePaper,
		},
	}}

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
		WithRepo(repo),
		WithNowFunc(func() time.Time { return now }),
	)

	svc.bootstrapPositions(context.Background())
	require.Equal(t, 1, svc.PositionCount())
	pos, ok := svc.positions["tenant-1:Paper:AAPL"]
	require.True(t, ok)
	assert.Equal(t, 5.0, pos.Quantity)
	assert.Equal(t, 155.0, pos.EntryPrice)
}

func TestService_resolveExitRules_UsesSpecStoreWhenAvailable(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	trailing := mustExitRule(t, domain.ExitRuleTrailingStop, map[string]float64{"pct": 0.05})
	eod := mustExitRule(t, domain.ExitRuleEODFlatten, map[string]float64{"minutes_before_close": 5})
	ss := &mockSpecStore{specs: map[string]*portstrategy.Spec{
		"orb_break_retest": {
			ID:      domstrategy.StrategyID("orb_break_retest"),
			Version: domstrategy.Version("1.0.0"),
			ExitRules: []domain.ExitRule{
				trailing,
				eod,
			},
		},
	}}

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(), WithSpecStore(ss))

	rules := svc.resolveExitRules(context.Background(), "orb_break_retest", domain.AssetClassEquity)
	require.Len(t, rules, 2)
	assert.Equal(t, trailing, rules[0])
	assert.Equal(t, eod, rules[1])
}

func TestService_processExitRejected_RemovesPositionOnNoPositionToExit(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AVAX/USD"),
		Side:       "BUY",
		Price:      25.0,
		Quantity:   100,
		FilledAt:   time.Now(),
		Strategy:   "crypto_avwap_v2",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})
	require.Equal(t, 1, svc.PositionCount())

	svc.processExitRejected(exitRejectedMsg{
		Symbol: domain.Symbol("AVAX/USD"),
		Reason: "position_gate: no_position_to_exit",
	})

	assert.Equal(t, 0, svc.PositionCount())
}

func TestService_processExitRejected_IgnoresUnknownSymbol(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AVAX/USD"),
		Side:       "BUY",
		Price:      25.0,
		Quantity:   100,
		FilledAt:   time.Now(),
		Strategy:   "crypto_avwap_v2",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})
	require.Equal(t, 1, svc.PositionCount())

	svc.processExitRejected(exitRejectedMsg{
		Symbol: domain.Symbol("BTC/USD"),
		Reason: "position_gate: no_position_to_exit",
	})

	assert.Equal(t, 1, svc.PositionCount(), "must not remove unrelated positions")
}

func TestService_handleExitRejected_IgnoresNonExitDirections(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AVAX/USD"),
		Side:       "BUY",
		Price:      25.0,
		Quantity:   100,
		FilledAt:   time.Now(),
		Strategy:   "crypto_avwap_v2",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})

	payload := domain.OrderIntentEventPayload{
		Symbol:    "AVAX/USD",
		Direction: "OPEN_LONG",
		Reason:    "position_gate: no_position_to_exit",
		Status:    "rejected",
	}
	ev, err := domain.NewEvent(domain.EventOrderIntentRejected, "tenant-1", domain.EnvModePaper, "reject-1", payload)
	require.NoError(t, err)

	err = svc.handleExitRejected(context.Background(), *ev)
	require.NoError(t, err)

	assert.Equal(t, 1, svc.PositionCount(), "must not enqueue for non-exit directions")
}

func TestService_handleExitRejected_IgnoresOtherRejectionReasons(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AVAX/USD"),
		Side:       "BUY",
		Price:      25.0,
		Quantity:   100,
		FilledAt:   time.Now(),
		Strategy:   "crypto_avwap_v2",
		AssetClass: domain.AssetClassCrypto,
		ExitRules:  []domain.ExitRule{},
	})

	payload := domain.OrderIntentEventPayload{
		Symbol:    "AVAX/USD",
		Direction: "CLOSE_LONG",
		Reason:    "position_gate: inflight_exit",
		Status:    "rejected",
	}
	ev, err := domain.NewEvent(domain.EventOrderIntentRejected, "tenant-1", domain.EnvModePaper, "reject-2", payload)
	require.NoError(t, err)

	err = svc.handleExitRejected(context.Background(), *ev)
	require.NoError(t, err)

	select {
	case <-svc.exitRejected:
		t.Fatal("must not enqueue for non-no_position_to_exit reasons")
	default:
	}

	assert.Equal(t, 1, svc.PositionCount(), "position must remain")
}

func TestService_ExitRejected_FullIntegration_BreaksRetryLoop(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithTickInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	fillPayload := map[string]any{
		"symbol":      "AVAX/USD",
		"side":        "BUY",
		"price":       float64(25.0),
		"quantity":    float64(100),
		"filled_at":   now,
		"strategy":    "crypto_avwap_v2",
		"asset_class": "Crypto",
	}
	fillEv, err := domain.NewEvent(domain.EventFillReceived, "tenant-1", domain.EnvModePaper, "fill-avax", fillPayload)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *fillEv))

	require.Eventually(t, func() bool { return svc.PositionCount() == 1 }, 500*time.Millisecond, 10*time.Millisecond)

	rejectPayload := domain.OrderIntentEventPayload{
		Symbol:    "AVAX/USD",
		Direction: "CLOSE_LONG",
		Reason:    "position_gate: no_position_to_exit",
		Status:    "rejected",
	}
	rejectEv, err := domain.NewEvent(domain.EventOrderIntentRejected, "tenant-1", domain.EnvModePaper, "reject-avax", rejectPayload)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *rejectEv))

	require.Eventually(t, func() bool { return svc.PositionCount() == 0 }, 500*time.Millisecond, 10*time.Millisecond,
		"position must be removed after no_position_to_exit rejection")
}

func TestService_resolveExitRules_FallsBackToDefaultsWhenSpecStoreIsNil(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	rules := svc.resolveExitRules(context.Background(), "any_strategy", domain.AssetClassEquity)
	require.Len(t, rules, 2)
	assert.Equal(t, domain.ExitRuleMaxLoss, rules[0].Type)
	assert.Equal(t, domain.ExitRuleEODFlatten, rules[1].Type)
}

// ---------------------------------------------------------------------------
// Backtest-mode tests
// ---------------------------------------------------------------------------

type mockBrokerWithTracking struct {
	mockBroker
	mu                 sync.Mutex
	getPositionsCalled bool
}

func (m *mockBrokerWithTracking) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	m.mu.Lock()
	m.getPositionsCalled = true
	m.mu.Unlock()
	return m.mockBroker.GetPositions(ctx, tenantID, envMode)
}

func TestEvalExitRules_TriggersEODFlatten(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	et, _ := time.LoadLocation("America/New_York")
	entryTime := time.Date(2026, 3, 10, 10, 0, 0, 0, et) // Tuesday 10:00 ET

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return entryTime }),
		WithDisableTickLoop(),
		WithDisableReconcile(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   entryTime,
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules: []domain.ExitRule{
			mustExitRule(t, domain.ExitRuleEODFlatten, map[string]float64{"minutes_before_close": 5}),
		},
	})
	require.Equal(t, 1, svc.PositionCount())

	barTime := time.Date(2026, 3, 10, 15, 56, 0, 0, et) // 15:56 ET — 4 min before close
	pc.UpdatePrice(domain.Symbol("AAPL"), 155, barTime)

	svc.EvalExitRules(barTime)

	require.Eventually(t, func() bool {
		return bus.publishedCount(domain.EventExitTriggered) == 1 &&
			bus.publishedCount(domain.EventOrderIntentCreated) == 1
	}, 500*time.Millisecond, 10*time.Millisecond)
}

func TestEvalExitRules_DrainsFillsBeforeEval(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	et, _ := time.LoadLocation("America/New_York")
	entryTime := time.Date(2026, 3, 10, 10, 0, 0, 0, et)

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return entryTime }),
		WithDisableTickLoop(),
		WithDisableReconcile(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	// Enqueue fill via channel (as the event bus handler would).
	svc.fills <- fillMsg{
		Symbol:     domain.Symbol("MSFT"),
		Side:       "BUY",
		Price:      400,
		Quantity:   5,
		FilledAt:   entryTime,
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules: []domain.ExitRule{
			mustExitRule(t, domain.ExitRuleEODFlatten, map[string]float64{"minutes_before_close": 5}),
		},
	}

	barTime := time.Date(2026, 3, 10, 15, 56, 0, 0, et)
	pc.UpdatePrice(domain.Symbol("MSFT"), 410, barTime)

	// EvalExitRules should drain the pending fill first, then evaluate.
	svc.EvalExitRules(barTime)

	assert.Equal(t, 1, svc.PositionCount(), "fill should be drained and processed")

	require.Eventually(t, func() bool {
		return bus.publishedCount(domain.EventExitTriggered) == 1
	}, 500*time.Millisecond, 10*time.Millisecond)
}

func TestDisableTickLoop_NoTickFires(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 10, 15, 56, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithTickInterval(5*time.Millisecond),
		WithDisableTickLoop(),
		WithDisableReconcile(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	svc.processFill(fillMsg{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		FilledAt:   now.Add(-5 * time.Minute),
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		ExitRules: []domain.ExitRule{
			mustExitRule(t, domain.ExitRuleEODFlatten, map[string]float64{"minutes_before_close": 5}),
		},
	})

	pos := svc.positions["tenant-1:Paper:AAPL"]
	require.NotNil(t, pos)
	pos.HighWaterMark = 200
	pc.UpdatePrice(domain.Symbol("AAPL"), 180, now)

	// With 5ms tick interval, multiple ticks would fire in 100ms if enabled.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, bus.totalPublished(), "tick loop disabled — no events should be published")
}

func TestWithDisableReconcile_SkipsBootstrap(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())

	broker := &mockBrokerWithTracking{
		mockBroker: mockBroker{positions: []domain.Trade{{
			Symbol:     domain.Symbol("AAPL"),
			Quantity:   10,
			Price:      150,
			AssetClass: domain.AssetClassEquity,
			TenantID:   "tenant-1",
			EnvMode:    domain.EnvModePaper,
		}}},
	}
	pg := execution.NewPositionGate(broker, zerolog.Nop())

	repo := &mockRepo{trades: []domain.Trade{{
		Symbol:     domain.Symbol("AAPL"),
		Side:       "BUY",
		Price:      150,
		Quantity:   10,
		Strategy:   "orb_break_retest",
		AssetClass: domain.AssetClassEquity,
		Time:       time.Now().Add(-10 * time.Minute),
		TenantID:   "tenant-1",
		EnvMode:    domain.EnvModePaper,
	}}}

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
		WithRepo(repo),
		WithDisableReconcile(),
		WithDisableTickLoop(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	broker.mu.Lock()
	called := broker.getPositionsCalled
	broker.mu.Unlock()

	assert.False(t, called, "GetPositions must not be called when reconcile is disabled")
	assert.Equal(t, 0, svc.PositionCount(), "no positions should be bootstrapped")
}
