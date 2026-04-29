package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dedupRepo struct {
	mu      sync.Mutex
	fills   []domain.Trade
	execIDs map[string]struct{}
}

func newDedupRepo() *dedupRepo {
	return &dedupRepo{execIDs: make(map[string]struct{})}
}

func (r *dedupRepo) RecordFill(_ context.Context, _ string, _ time.Time, _ float64, _ float64, trade domain.Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if trade.ExecutionID != "" {
		if _, dup := r.execIDs[trade.ExecutionID]; dup {
			return nil
		}
		r.execIDs[trade.ExecutionID] = struct{}{}
	}
	r.fills = append(r.fills, trade)
	return nil
}

func (r *dedupRepo) UpdateOrderFill(context.Context, string, time.Time, float64, float64) error {
	return nil
}
func (r *dedupRepo) SaveTrade(context.Context, domain.Trade) error             { return nil }
func (r *dedupRepo) SaveOrder(context.Context, domain.BrokerOrder) error       { return nil }
func (r *dedupRepo) SaveMarketBar(context.Context, domain.MarketBar) error     { return nil }
func (r *dedupRepo) SaveMarketBars(context.Context, []domain.MarketBar) (int, error) {
	return 0, nil
}
func (r *dedupRepo) GetMarketBars(context.Context, domain.Symbol, domain.Timeframe, time.Time, time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (r *dedupRepo) GetMarketBarsMulti(context.Context, []domain.Symbol, domain.Timeframe, time.Time, time.Time) (map[string][]domain.MarketBar, error) {
	return nil, nil
}
func (r *dedupRepo) GetTrades(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (r *dedupRepo) SaveStrategyDNA(context.Context, domain.StrategyDNA) error { return nil }
func (r *dedupRepo) GetLatestStrategyDNA(context.Context, string, domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (r *dedupRepo) ListTrades(context.Context, ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (r *dedupRepo) ListOrders(context.Context, ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (r *dedupRepo) SaveThoughtLog(context.Context, domain.ThoughtLog) error { return nil }
func (r *dedupRepo) GetThoughtLogsByIntentID(context.Context, string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (r *dedupRepo) UpdateTradeThesis(context.Context, string, domain.EnvMode, domain.Symbol, json.RawMessage) error {
	return nil
}
func (r *dedupRepo) GetMaxBarHighSince(context.Context, domain.Symbol, domain.Timeframe, time.Time) (float64, error) {
	return 0, nil
}
func (r *dedupRepo) GetLatestThesisForSymbol(context.Context, string, domain.EnvMode, domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (r *dedupRepo) GetNonTerminalOrders(context.Context, string, domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (r *dedupRepo) GetOrderByBrokerOrderID(context.Context, string) (*domain.BrokerOrder, error) {
	return nil, nil
}
func (r *dedupRepo) GetRecordedFillQty(context.Context, string, domain.EnvMode, domain.Symbol, string, time.Time) (float64, error) {
	return 0, nil
}
func (r *dedupRepo) UpdateOrderStatus(context.Context, string, string) error { return nil }
func (r *dedupRepo) GetRecordedExecutionIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (r *dedupRepo) GetReconciledOrderIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (r *dedupRepo) GetNetPositions(context.Context, string, domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (r *dedupRepo) GetAvgEntryPrice(context.Context, string, domain.EnvMode, domain.Symbol) (float64, error) {
	return 0, nil
}
func (r *dedupRepo) HasCanceledExitOrder(context.Context, string, domain.EnvMode, domain.Symbol) (bool, error) {
	return false, nil
}
func (r *dedupRepo) UpdateBarIndicators(context.Context, domain.Symbol, domain.Timeframe, time.Time, float64, float64, float64, float64, map[string]float64) error {
	return nil
}

type dedupPlainBroker struct{}

func (b *dedupPlainBroker) SubmitOrder(context.Context, domain.OrderIntent) (string, error) {
	return "", nil
}
func (b *dedupPlainBroker) CancelOrder(context.Context, string) error                      { return nil }
func (b *dedupPlainBroker) CancelOpenOrders(context.Context, domain.Symbol, string) (int, error) {
	return 0, nil
}
func (b *dedupPlainBroker) GetOrderStatus(context.Context, string) (string, error) {
	return "FILLED", nil
}
func (b *dedupPlainBroker) GetPositions(context.Context, string, domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}
func (b *dedupPlainBroker) GetPosition(context.Context, domain.Symbol) (float64, error) {
	return 0, nil
}
func (b *dedupPlainBroker) CloseAtMarket(context.Context, domain.Symbol) (string, error) {
	return "", nil
}
func (b *dedupPlainBroker) GetOrderDetails(context.Context, string) (ports.OrderDetails, error) {
	return ports.OrderDetails{}, nil
}
func (b *dedupPlainBroker) CancelAllOpenOrders(context.Context) (int, error) { return 0, nil }
func (b *dedupPlainBroker) GetOpenOrders(context.Context) ([]ports.OpenOrder, error) {
	return nil, nil
}

type dedupListerBroker struct {
	dedupPlainBroker
	fills []ports.FillRecord
	err   error
}

func (b *dedupListerBroker) GetAllFills(context.Context) ([]ports.FillRecord, error) {
	return b.fills, b.err
}

func newDedupSvc(broker ports.BrokerPort, repo *dedupRepo) *Service {
	bus := memory.NewBus()
	return &Service{
		eventBus: bus,
		broker:   broker,
		repo:     repo,
		tenantID: "tenant-1",
		envMode:  domain.EnvModePaper,
		log:      zerolog.Nop(),
		nowFn:    func() time.Time { return time.Date(2026, 4, 28, 18, 30, 12, 0, time.UTC) },
	}
}

func newDedupPO(qty, limit float64) *pendingOrder {
	return &pendingOrder{
		intent: domain.OrderIntent{
			ID:         uuid.New(),
			Symbol:     domain.Symbol("NFLX260515P00090000"),
			Direction:  domain.DirectionLong,
			Quantity:   qty,
			LimitPrice: limit,
			Strategy:   "test",
			Rationale:  "phase1-dedup",
		},
		tenantID:    "tenant-1",
		envMode:     domain.EnvModePaper,
		submitStart: time.Date(2026, 4, 28, 18, 29, 50, 0, time.UTC),
	}
}

func TestPollPath_PopulatesExecutionID(t *testing.T) {
	repo := newDedupRepo()
	filledAt := time.Date(2026, 4, 28, 18, 30, 12, 0, time.UTC)
	broker := &dedupListerBroker{
		fills: []ports.FillRecord{{
			BrokerOrderID: "BO-42",
			ExecutionID:   "EX-1",
			Symbol:        "NFLX260515P00090000",
			Side:          "BUY",
			Qty:           10,
			Price:         1.50,
			CumQty:        10,
			AvgPrice:      1.50,
			FilledAt:      filledAt,
		}},
	}
	svc := newDedupSvc(broker, repo)
	po := newDedupPO(10, 1.50)

	svc.recordFillsFromExecHistory(po, "BO-42", zerolog.Nop())

	require.Len(t, repo.fills, 1, "want exactly one trade row")
	assert.Equal(t, "EX-1", repo.fills[0].ExecutionID)
	assert.InDelta(t, 10.0, repo.fills[0].Quantity, 1e-9)
}

func TestPollPath_MultiLegFill(t *testing.T) {
	repo := newDedupRepo()
	filledAt := time.Date(2026, 4, 28, 18, 30, 12, 0, time.UTC)
	broker := &dedupListerBroker{
		fills: []ports.FillRecord{
			{
				BrokerOrderID: "BO-42",
				ExecutionID:   "EX-1",
				Symbol:        "NFLX260515P00090000",
				Side:          "BUY",
				Qty:           5,
				Price:         1.50,
				CumQty:        5,
				AvgPrice:      1.50,
				FilledAt:      filledAt,
			},
			{
				BrokerOrderID: "BO-42",
				ExecutionID:   "EX-2",
				Symbol:        "NFLX260515P00090000",
				Side:          "BUY",
				Qty:           5,
				Price:         1.50,
				CumQty:        10,
				AvgPrice:      1.50,
				FilledAt:      filledAt.Add(time.Second),
			},
		},
	}
	svc := newDedupSvc(broker, repo)
	po := newDedupPO(10, 1.50)

	svc.recordFillsFromExecHistory(po, "BO-42", zerolog.Nop())

	require.Len(t, repo.fills, 2, "want exactly two trade rows for two execs")

	seen := map[string]bool{}
	var totalQty float64
	for _, tr := range repo.fills {
		seen[tr.ExecutionID] = true
		totalQty += tr.Quantity
	}
	assert.True(t, seen["EX-1"] && seen["EX-2"], "both execution IDs must appear, got %v", seen)
	assert.InDelta(t, 10.0, totalQty, 1e-9, "leg quantities must sum to intent quantity")
}

func TestPollPath_FillListerUnsupported(t *testing.T) {
	repo := newDedupRepo()
	svc := newDedupSvc(&dedupPlainBroker{}, repo)
	po := newDedupPO(10, 1.50)

	svc.recordFillsFromExecHistory(po, "BO-42", zerolog.Nop())

	require.Len(t, repo.fills, 1, "want exactly one trade row from the legacy fallback")
	assert.Equal(t, "", repo.fills[0].ExecutionID,
		"fallback path preserves today's empty-execution_id behaviour")
}

func TestPollPath_GetAllFillsError(t *testing.T) {
	repo := newDedupRepo()
	broker := &dedupListerBroker{err: errors.New("ibkr: not connected")}
	svc := newDedupSvc(broker, repo)
	po := newDedupPO(10, 1.50)

	svc.recordFillsFromExecHistory(po, "BO-42", zerolog.Nop())

	require.Len(t, repo.fills, 1, "want exactly one trade row from the error fallback")
}
