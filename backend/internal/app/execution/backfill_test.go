package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// backfillMockBroker implements ports.BrokerPort AND ports.FilledOrderLister.
type backfillMockBroker struct {
	filled    []ports.FilledOrder
	filledErr error
	calls     int32
}

func (b *backfillMockBroker) GetFilledOrders(context.Context) ([]ports.FilledOrder, error) {
	atomic.AddInt32(&b.calls, 1)
	if b.filledErr != nil {
		return nil, b.filledErr
	}
	return b.filled, nil
}

func (b *backfillMockBroker) SubmitOrder(context.Context, domain.OrderIntent) (string, error) {
	return "", nil
}
func (b *backfillMockBroker) CancelOrder(context.Context, string) error      { return nil }
func (b *backfillMockBroker) CancelOpenOrders(context.Context, domain.Symbol, string) (int, error) {
	return 0, nil
}
func (b *backfillMockBroker) GetOrderStatus(context.Context, string) (string, error) { return "", nil }
func (b *backfillMockBroker) GetPositions(context.Context, string, domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}
func (b *backfillMockBroker) GetPosition(context.Context, domain.Symbol) (float64, error) {
	return 0, nil
}
func (b *backfillMockBroker) CloseAtMarket(context.Context, domain.Symbol) (string, error) {
	return "", nil
}
func (b *backfillMockBroker) GetOrderDetails(context.Context, string) (ports.OrderDetails, error) {
	return ports.OrderDetails{}, nil
}
func (b *backfillMockBroker) CancelAllOpenOrders(context.Context) (int, error) { return 0, nil }
func (b *backfillMockBroker) GetOpenOrders(context.Context) ([]ports.OpenOrder, error) {
	return nil, nil
}

// backfillMockRepo tracks writes for assertions.
type backfillMockRepo struct {
	mu              sync.Mutex
	existingByOrder map[string]*domain.BrokerOrder
	savedOrders     []domain.BrokerOrder
	savedTrades    []domain.Trade
	updatedFills    []fillUpdate
}

type fillUpdate struct {
	brokerOrderID string
	filledAt      time.Time
	price         float64
	qty           float64
}

func (r *backfillMockRepo) GetOrderByBrokerOrderID(_ context.Context, id string) (*domain.BrokerOrder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.existingByOrder[id]; ok {
		return o, nil
	}
	return nil, nil
}
func (r *backfillMockRepo) SaveOrder(_ context.Context, o domain.BrokerOrder) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.savedOrders = append(r.savedOrders, o)
	return nil
}
func (r *backfillMockRepo) SaveTrade(_ context.Context, t domain.Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.savedTrades = append(r.savedTrades, t)
	return nil
}
func (r *backfillMockRepo) UpdateOrderFill(_ context.Context, id string, at time.Time, price, qty float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedFills = append(r.updatedFills, fillUpdate{id, at, price, qty})
	return nil
}

// RepositoryPort stubs (unused by backfill).
func (r *backfillMockRepo) SaveMarketBar(context.Context, domain.MarketBar) error         { return nil }
func (r *backfillMockRepo) SaveMarketBars(context.Context, []domain.MarketBar) (int, error) { return 0, nil }
func (r *backfillMockRepo) GetMarketBars(context.Context, domain.Symbol, domain.Timeframe, time.Time, time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (r *backfillMockRepo) GetMarketBarsMulti(context.Context, []domain.Symbol, domain.Timeframe, time.Time, time.Time) (map[string][]domain.MarketBar, error) {
	return nil, nil
}
func (r *backfillMockRepo) GetTrades(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (r *backfillMockRepo) SaveStrategyDNA(context.Context, domain.StrategyDNA) error { return nil }
func (r *backfillMockRepo) GetLatestStrategyDNA(context.Context, string, domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (r *backfillMockRepo) RecordFill(context.Context, string, time.Time, float64, float64, domain.Trade) error {
	return nil
}
func (r *backfillMockRepo) ListTrades(context.Context, ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (r *backfillMockRepo) ListOrders(context.Context, ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (r *backfillMockRepo) SaveThoughtLog(context.Context, domain.ThoughtLog) error { return nil }
func (r *backfillMockRepo) GetThoughtLogsByIntentID(context.Context, string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (r *backfillMockRepo) UpdateTradeThesis(context.Context, string, domain.EnvMode, domain.Symbol, json.RawMessage) error {
	return nil
}
func (r *backfillMockRepo) GetMaxBarHighSince(context.Context, domain.Symbol, domain.Timeframe, time.Time) (float64, error) {
	return 0, nil
}
func (r *backfillMockRepo) GetLatestThesisForSymbol(context.Context, string, domain.EnvMode, domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (r *backfillMockRepo) GetNonTerminalOrders(context.Context, string, domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (r *backfillMockRepo) GetRecordedFillQty(context.Context, string, domain.EnvMode, domain.Symbol, string, time.Time) (float64, error) {
	return 0, nil
}
func (r *backfillMockRepo) UpdateOrderStatus(context.Context, string, string) error { return nil }
func (r *backfillMockRepo) GetRecordedExecutionIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func (r *backfillMockRepo) GetReconciledOrderIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (r *backfillMockRepo) GetNetPositions(context.Context, string, domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (r *backfillMockRepo) GetAvgEntryPrice(context.Context, string, domain.EnvMode, domain.Symbol) (float64, error) {
	return 0, nil
}
func (r *backfillMockRepo) HasCanceledExitOrder(context.Context, string, domain.EnvMode, domain.Symbol) (bool, error) {
	return false, nil
}

func (r *backfillMockRepo) HasTradeForBrokerOrderID(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (r *backfillMockRepo) UpdateBarIndicators(context.Context, domain.Symbol, domain.Timeframe, time.Time, float64, float64, float64, float64, map[string]float64) error {
	return nil
}

// newBackfillService wires a Service ready to run backfillFromBrokerHistory.
func newBackfillService(broker ports.BrokerPort, repo ports.RepositoryPort) (*Service, *memory.Bus) {
	bus := memory.NewBus()
	return &Service{
		eventBus: bus,
		broker:   broker,
		repo:     repo,
		tenantID: "tenant-1",
		envMode:  domain.EnvModePaper,
		log:      zerolog.Nop(),
		nowFn:    func() time.Time { return time.Date(2026, 4, 24, 16, 0, 0, 0, time.UTC) },
	}, bus
}

func TestBackfillFromBrokerHistory_NoHistory(t *testing.T) {
	broker := &backfillMockBroker{filled: nil}
	repo := &backfillMockRepo{}
	svc, _ := newBackfillService(broker, repo)

	svc.backfillFromBrokerHistory(context.Background())

	assert.Empty(t, repo.savedOrders)
	assert.Empty(t, repo.savedTrades)
	assert.Empty(t, repo.updatedFills)
}

func TestBackfillFromBrokerHistory_OrderAlreadyInDB(t *testing.T) {
	broker := &backfillMockBroker{filled: []ports.FilledOrder{
		{
			BrokerOrderID:  "3222",
			Symbol:         "SPY260424C00712000",
			Side:           "BUY",
			Quantity:       34,
			FilledQty:      34,
			FilledAvgPrice: 1.00,
			FilledAt:       time.Date(2026, 4, 24, 14, 30, 0, 0, time.UTC),
			Status:         "filled",
		},
	}}
	repo := &backfillMockRepo{
		existingByOrder: map[string]*domain.BrokerOrder{
			"3222": {BrokerOrderID: "3222", Quantity: 34, FilledQty: 34},
		},
	}
	svc, _ := newBackfillService(broker, repo)

	svc.backfillFromBrokerHistory(context.Background())

	assert.Empty(t, repo.savedOrders, "existing row must not be clobbered")
	assert.Empty(t, repo.savedTrades)
	assert.Empty(t, repo.updatedFills)
}

func TestBackfillFromBrokerHistory_MissingOrderWritten(t *testing.T) {
	filledAt := time.Date(2026, 4, 24, 14, 30, 0, 0, time.UTC)
	broker := &backfillMockBroker{filled: []ports.FilledOrder{
		{
			BrokerOrderID:  "3222",
			Symbol:         "SPY260424C00712000",
			Side:           "BUY",
			Quantity:       34,
			FilledQty:      34,
			FilledAvgPrice: 1.00,
			FilledAt:       filledAt,
			Status:         "filled",
		},
	}}
	repo := &backfillMockRepo{}
	svc, bus := newBackfillService(broker, repo)

	var fillEvents []map[string]any
	var mu sync.Mutex
	require.NoError(t, bus.Subscribe(context.Background(), domain.EventFillReceived, func(_ context.Context, e domain.Event) error {
		if p, ok := e.Payload.(map[string]any); ok {
			mu.Lock()
			fillEvents = append(fillEvents, p)
			mu.Unlock()
		}
		return nil
	}))

	svc.backfillFromBrokerHistory(context.Background())

	require.Len(t, repo.savedOrders, 1)
	assert.Equal(t, "3222", repo.savedOrders[0].BrokerOrderID)
	assert.Equal(t, "backfill", repo.savedOrders[0].Strategy)
	assert.Equal(t, domain.InstrumentTypeOption, repo.savedOrders[0].InstrumentType)
	assert.Equal(t, "SPY", repo.savedOrders[0].Underlying)
	assert.InDelta(t, 712.0, repo.savedOrders[0].Strike, 1e-6)
	assert.Equal(t, "CALL", repo.savedOrders[0].OptionRight)

	require.Len(t, repo.savedTrades, 1)
	assert.Equal(t, "backfill:3222", repo.savedTrades[0].ExecutionID)
	assert.InDelta(t, 34.0, repo.savedTrades[0].Quantity, 1e-9)
	assert.InDelta(t, 1.00, repo.savedTrades[0].Price, 1e-9)
	assert.Equal(t, "BUY", repo.savedTrades[0].Side)

	require.Len(t, repo.updatedFills, 1)
	assert.Equal(t, "3222", repo.updatedFills[0].brokerOrderID)
	assert.InDelta(t, 34.0, repo.updatedFills[0].qty, 1e-9)
	assert.InDelta(t, 1.00, repo.updatedFills[0].price, 1e-9)

	// Give the async bus handler a moment, then inspect.
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, fillEvents, 1)
	assert.Equal(t, true, fillEvents[0]["synthetic"])
	assert.Equal(t, "broker_history_backfill", fillEvents[0]["source"])
}

func TestBackfillFromBrokerHistory_IdempotentAcrossRuns(t *testing.T) {
	filledAt := time.Date(2026, 4, 24, 14, 30, 0, 0, time.UTC)
	broker := &backfillMockBroker{filled: []ports.FilledOrder{
		{
			BrokerOrderID:  "3222",
			Symbol:         "SPY260424C00712000",
			Side:           "BUY",
			Quantity:       34,
			FilledQty:      34,
			FilledAvgPrice: 1.00,
			FilledAt:       filledAt,
			Status:         "filled",
		},
	}}
	repo := &backfillMockRepo{}
	svc, _ := newBackfillService(broker, repo)

	svc.backfillFromBrokerHistory(context.Background())
	require.Len(t, repo.savedOrders, 1)

	// Simulate the second-run state: the order now exists in DB.
	repo.existingByOrder = map[string]*domain.BrokerOrder{"3222": &repo.savedOrders[0]}
	svc.backfillFromBrokerHistory(context.Background())

	assert.Len(t, repo.savedOrders, 1, "second run must not re-insert")
	assert.Len(t, repo.savedTrades, 1, "second run must not duplicate trade")
	assert.Len(t, repo.updatedFills, 1)
}

func TestBackfillFromBrokerHistory_SkipsZeroPrice(t *testing.T) {
	broker := &backfillMockBroker{filled: []ports.FilledOrder{
		{
			BrokerOrderID:  "9999",
			Symbol:         "AAPL",
			Side:           "BUY",
			Quantity:       10,
			FilledQty:      10,
			FilledAvgPrice: 0,
			FilledAt:       time.Now(),
			Status:         "filled",
		},
	}}
	repo := &backfillMockRepo{}
	svc, _ := newBackfillService(broker, repo)

	svc.backfillFromBrokerHistory(context.Background())

	assert.Empty(t, repo.savedOrders)
	assert.Empty(t, repo.savedTrades)
}

func TestBackfillFromBrokerHistory_SkipsZeroFilledAt(t *testing.T) {
	broker := &backfillMockBroker{filled: []ports.FilledOrder{
		{
			BrokerOrderID:  "9998",
			Symbol:         "AAPL",
			Side:           "BUY",
			Quantity:       10,
			FilledQty:      10,
			FilledAvgPrice: 150.0,
			FilledAt:       time.Time{},
			Status:         "filled",
		},
	}}
	repo := &backfillMockRepo{}
	svc, _ := newBackfillService(broker, repo)

	svc.backfillFromBrokerHistory(context.Background())

	assert.Empty(t, repo.savedOrders)
}

func TestBackfillFromBrokerHistory_BrokerError(t *testing.T) {
	broker := &backfillMockBroker{filledErr: errors.New("broker offline")}
	repo := &backfillMockRepo{}
	svc, _ := newBackfillService(broker, repo)

	svc.backfillFromBrokerHistory(context.Background())

	assert.Empty(t, repo.savedOrders)
}

// plainBroker implements BrokerPort but NOT FilledOrderLister.
type plainBroker struct{}

func (plainBroker) SubmitOrder(context.Context, domain.OrderIntent) (string, error) { return "", nil }
func (plainBroker) CancelOrder(context.Context, string) error                       { return nil }
func (plainBroker) CancelOpenOrders(context.Context, domain.Symbol, string) (int, error) {
	return 0, nil
}
func (plainBroker) GetOrderStatus(context.Context, string) (string, error) { return "", nil }
func (plainBroker) GetPositions(context.Context, string, domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}
func (plainBroker) GetPosition(context.Context, domain.Symbol) (float64, error) { return 0, nil }
func (plainBroker) CloseAtMarket(context.Context, domain.Symbol) (string, error) {
	return "", nil
}
func (plainBroker) GetOrderDetails(context.Context, string) (ports.OrderDetails, error) {
	return ports.OrderDetails{}, nil
}
func (plainBroker) CancelAllOpenOrders(context.Context) (int, error) { return 0, nil }
func (plainBroker) GetOpenOrders(context.Context) ([]ports.OpenOrder, error) {
	return nil, nil
}

func TestBackfillFromBrokerHistory_NoOpWhenBrokerLacksCapability(t *testing.T) {
	repo := &backfillMockRepo{}
	svc, _ := newBackfillService(plainBroker{}, repo)

	svc.backfillFromBrokerHistory(context.Background())

	assert.Empty(t, repo.savedOrders)
}
