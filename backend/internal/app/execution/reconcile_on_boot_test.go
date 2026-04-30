package execution

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reconcileOnBootBroker returns a fixed details map for GetOrderDetails.
type reconcileOnBootBroker struct {
	backfillMockBroker
	details map[string]ports.OrderDetails
}

func (b *reconcileOnBootBroker) GetOrderDetails(_ context.Context, id string) (ports.OrderDetails, error) {
	if d, ok := b.details[id]; ok {
		return d, nil
	}
	return ports.OrderDetails{}, ports.ErrOrderNotFound
}

// reconcileOnBootRepo captures SaveTrade calls and serves the orders/fillQty queries.
type reconcileOnBootRepo struct {
	mu               sync.Mutex
	nonTerminal      []domain.BrokerOrder
	recordedFillQty  float64
	saved            []domain.Trade
	updateFillCalls  []reconcileOnBootFillUpdate
}

type reconcileOnBootFillUpdate struct {
	brokerOrderID string
	at            time.Time
	price         float64
	qty           float64
}

func (r *reconcileOnBootRepo) GetNonTerminalOrders(context.Context, string, domain.EnvMode) ([]domain.BrokerOrder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.BrokerOrder, len(r.nonTerminal))
	copy(out, r.nonTerminal)
	return out, nil
}

func (r *reconcileOnBootRepo) GetRecordedFillQty(context.Context, string, domain.EnvMode, domain.Symbol, string, time.Time) (float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recordedFillQty, nil
}

func (r *reconcileOnBootRepo) SaveTrade(_ context.Context, t domain.Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved = append(r.saved, t)
	return nil
}

func (r *reconcileOnBootRepo) UpdateOrderFill(_ context.Context, id string, at time.Time, price, qty float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateFillCalls = append(r.updateFillCalls, reconcileOnBootFillUpdate{id, at, price, qty})
	for i := range r.nonTerminal {
		if r.nonTerminal[i].BrokerOrderID == id {
			t := at
			r.nonTerminal[i].FilledAt = &t
			r.nonTerminal[i].FilledPrice = price
			r.nonTerminal[i].FilledQty = qty
		}
	}
	return nil
}

func (r *reconcileOnBootRepo) UpdateOrderStatus(_ context.Context, id string, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.nonTerminal {
		if r.nonTerminal[i].BrokerOrderID == id {
			r.nonTerminal[i].Status = status
		}
	}
	return nil
}

// Unused stubs.
func (r *reconcileOnBootRepo) SaveMarketBar(context.Context, domain.MarketBar) error           { return nil }
func (r *reconcileOnBootRepo) SaveMarketBars(context.Context, []domain.MarketBar) (int, error) { return 0, nil }
func (r *reconcileOnBootRepo) GetMarketBars(context.Context, domain.Symbol, domain.Timeframe, time.Time, time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (r *reconcileOnBootRepo) GetMarketBarsMulti(context.Context, []domain.Symbol, domain.Timeframe, time.Time, time.Time) (map[string][]domain.MarketBar, error) {
	return nil, nil
}
func (r *reconcileOnBootRepo) GetTrades(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (r *reconcileOnBootRepo) SaveStrategyDNA(context.Context, domain.StrategyDNA) error { return nil }
func (r *reconcileOnBootRepo) GetLatestStrategyDNA(context.Context, string, domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (r *reconcileOnBootRepo) SaveOrder(context.Context, domain.BrokerOrder) error { return nil }
func (r *reconcileOnBootRepo) RecordFill(context.Context, string, time.Time, float64, float64, domain.Trade) error {
	return nil
}
func (r *reconcileOnBootRepo) GetOrderByBrokerOrderID(context.Context, string) (*domain.BrokerOrder, error) {
	return nil, nil
}
func (r *reconcileOnBootRepo) GetRecordedExecutionIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (r *reconcileOnBootRepo) GetReconciledOrderIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (r *reconcileOnBootRepo) ListTrades(context.Context, ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (r *reconcileOnBootRepo) ListOrders(context.Context, ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (r *reconcileOnBootRepo) SaveThoughtLog(context.Context, domain.ThoughtLog) error { return nil }
func (r *reconcileOnBootRepo) GetThoughtLogsByIntentID(context.Context, string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (r *reconcileOnBootRepo) UpdateTradeThesis(context.Context, string, domain.EnvMode, domain.Symbol, json.RawMessage) error {
	return nil
}
func (r *reconcileOnBootRepo) GetMaxBarHighSince(context.Context, domain.Symbol, domain.Timeframe, time.Time) (float64, error) {
	return 0, nil
}
func (r *reconcileOnBootRepo) GetLatestThesisForSymbol(context.Context, string, domain.EnvMode, domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (r *reconcileOnBootRepo) GetNetPositions(context.Context, string, domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (r *reconcileOnBootRepo) GetAvgEntryPrice(context.Context, string, domain.EnvMode, domain.Symbol) (float64, error) {
	return 0, nil
}
func (r *reconcileOnBootRepo) HasCanceledExitOrder(context.Context, string, domain.EnvMode, domain.Symbol) (bool, error) {
	return false, nil
}

func (r *reconcileOnBootRepo) HasTradeForBrokerOrderID(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (r *reconcileOnBootRepo) UpdateBarIndicators(context.Context, domain.Symbol, domain.Timeframe, time.Time, float64, float64, float64, float64, map[string]float64) error {
	return nil
}

func newReconcileOnBootService(broker ports.BrokerPort, repo ports.RepositoryPort) *Service {
	bus := memory.NewBus()
	now := time.Date(2026, 4, 27, 23, 30, 0, 0, time.UTC)
	return &Service{
		eventBus: bus,
		broker:   broker,
		repo:     repo,
		tenantID: "tenant-1",
		envMode:  domain.EnvModeLive,
		log:      zerolog.Nop(),
		nowFn:    func() time.Time { return now },
	}
}

// Broker reports filled but FilledAt=zero. The synthetic trade row must use
// a stable timestamp (not nowFn) so the (trade_id, time) PK can dedup if the
// reconcile loop ever fires for the same order twice with the same FilledQty.
func TestReconcileOnBoot_ZeroFilledAt_UsesStableTimestamp(t *testing.T) {
	orderTime := time.Date(2026, 4, 27, 16, 0, 0, 0, time.UTC)
	broker := &reconcileOnBootBroker{
		details: map[string]ports.OrderDetails{
			"3000": {
				BrokerOrderID:  "3000",
				Status:         "filled",
				FilledQty:      5,
				FilledAvgPrice: 100.0,
				FilledAt:       time.Time{}, // zero — broker didn't populate
			},
		},
	}
	repo := &reconcileOnBootRepo{
		nonTerminal: []domain.BrokerOrder{
			{
				Time:          orderTime,
				BrokerOrderID: "3000",
				Symbol:        "AAPL",
				Side:          "BUY",
				Quantity:      5,
				Status:        "submitted",
				Strategy:      "x",
				IntentID:      uuid.New(),
			},
		},
	}
	svc := newReconcileOnBootService(broker, repo)

	svc.reconcileOnBoot(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.saved, 1)
	got := repo.saved[0]
	assert.Equal(t, orderTime, got.Time, "synthetic trade time must fall back to order.Time, not nowFn")
}

// Once the orders row has a filled_at from a prior reconcile pass, subsequent
// passes must reuse it so (trade_id, time) is identical and the PK dedups.
func TestReconcileOnBoot_OrderFilledAtSet_ReusesIt(t *testing.T) {
	orderTime := time.Date(2026, 4, 27, 16, 0, 0, 0, time.UTC)
	priorFillAt := time.Date(2026, 4, 27, 16, 5, 0, 0, time.UTC)
	broker := &reconcileOnBootBroker{
		details: map[string]ports.OrderDetails{
			"3001": {
				BrokerOrderID:  "3001",
				Status:         "partially_filled",
				FilledQty:      10,
				FilledAvgPrice: 50.0,
				FilledAt:       time.Time{},
			},
		},
	}
	t1 := priorFillAt
	repo := &reconcileOnBootRepo{
		nonTerminal: []domain.BrokerOrder{
			{
				Time:          orderTime,
				BrokerOrderID: "3001",
				Symbol:        "MSFT",
				Side:          "BUY",
				Quantity:      10,
				Status:        "partially_filled",
				FilledAt:      &t1,
				FilledQty:     5,
				Strategy:      "x",
				IntentID:      uuid.New(),
			},
		},
		recordedFillQty: 5, // prior pass already recorded qty=5
	}
	svc := newReconcileOnBootService(broker, repo)

	svc.reconcileOnBoot(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.saved, 1)
	assert.Equal(t, priorFillAt, repo.saved[0].Time, "must reuse order.FilledAt when present, not nowFn")
}
