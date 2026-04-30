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
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reconcileFillsBroker exposes a fixed set of fills via FillLister.
type reconcileFillsBroker struct {
	backfillMockBroker
	fills []ports.FillRecord
}

func (b *reconcileFillsBroker) GetAllFills(context.Context) ([]ports.FillRecord, error) {
	return b.fills, nil
}

// reconcileFillsRepo lets the test seed the dedup sets and capture RecordFill calls.
type reconcileFillsRepo struct {
	mu                 sync.Mutex
	existingByOrder    map[string]*domain.BrokerOrder
	recordedExecIDs    map[string]struct{}
	reconciledOrderIDs map[string]struct{}
	recorded           []domain.Trade
}

func (r *reconcileFillsRepo) GetOrderByBrokerOrderID(_ context.Context, id string) (*domain.BrokerOrder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.existingByOrder[id]; ok {
		return o, nil
	}
	return nil, nil
}
func (r *reconcileFillsRepo) GetRecordedExecutionIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	if r.recordedExecIDs == nil {
		return map[string]struct{}{}, nil
	}
	return r.recordedExecIDs, nil
}
func (r *reconcileFillsRepo) GetReconciledOrderIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	if r.reconciledOrderIDs == nil {
		return map[string]struct{}{}, nil
	}
	return r.reconciledOrderIDs, nil
}
func (r *reconcileFillsRepo) RecordFill(_ context.Context, _ string, _ time.Time, _, _ float64, t domain.Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recorded = append(r.recorded, t)
	return nil
}

// Unused stubs.
func (r *reconcileFillsRepo) SaveMarketBar(context.Context, domain.MarketBar) error           { return nil }
func (r *reconcileFillsRepo) SaveMarketBars(context.Context, []domain.MarketBar) (int, error) { return 0, nil }
func (r *reconcileFillsRepo) GetMarketBars(context.Context, domain.Symbol, domain.Timeframe, time.Time, time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (r *reconcileFillsRepo) GetMarketBarsMulti(context.Context, []domain.Symbol, domain.Timeframe, time.Time, time.Time) (map[string][]domain.MarketBar, error) {
	return nil, nil
}
func (r *reconcileFillsRepo) SaveTrade(context.Context, domain.Trade) error { return nil }
func (r *reconcileFillsRepo) GetTrades(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (r *reconcileFillsRepo) SaveStrategyDNA(context.Context, domain.StrategyDNA) error { return nil }
func (r *reconcileFillsRepo) GetLatestStrategyDNA(context.Context, string, domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (r *reconcileFillsRepo) SaveOrder(context.Context, domain.BrokerOrder) error { return nil }
func (r *reconcileFillsRepo) UpdateOrderFill(context.Context, string, time.Time, float64, float64) error {
	return nil
}
func (r *reconcileFillsRepo) ListTrades(context.Context, ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (r *reconcileFillsRepo) ListOrders(context.Context, ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (r *reconcileFillsRepo) SaveThoughtLog(context.Context, domain.ThoughtLog) error { return nil }
func (r *reconcileFillsRepo) GetThoughtLogsByIntentID(context.Context, string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (r *reconcileFillsRepo) UpdateTradeThesis(context.Context, string, domain.EnvMode, domain.Symbol, json.RawMessage) error {
	return nil
}
func (r *reconcileFillsRepo) GetMaxBarHighSince(context.Context, domain.Symbol, domain.Timeframe, time.Time) (float64, error) {
	return 0, nil
}
func (r *reconcileFillsRepo) GetLatestThesisForSymbol(context.Context, string, domain.EnvMode, domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (r *reconcileFillsRepo) GetNonTerminalOrders(context.Context, string, domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (r *reconcileFillsRepo) GetRecordedFillQty(context.Context, string, domain.EnvMode, domain.Symbol, string, time.Time) (float64, error) {
	return 0, nil
}
func (r *reconcileFillsRepo) UpdateOrderStatus(context.Context, string, string) error { return nil }
func (r *reconcileFillsRepo) GetNetPositions(context.Context, string, domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (r *reconcileFillsRepo) GetAvgEntryPrice(context.Context, string, domain.EnvMode, domain.Symbol) (float64, error) {
	return 0, nil
}
func (r *reconcileFillsRepo) HasCanceledExitOrder(context.Context, string, domain.EnvMode, domain.Symbol) (bool, error) {
	return false, nil
}

func (r *reconcileFillsRepo) HasTradeForBrokerOrderID(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (r *reconcileFillsRepo) UpdateBarIndicators(context.Context, domain.Symbol, domain.Timeframe, time.Time, float64, float64, float64, float64, map[string]float64) error {
	return nil
}

func newReconcileFillsService(broker ports.BrokerPort, repo ports.RepositoryPort) *Service {
	bus := memory.NewBus()
	return &Service{
		eventBus: bus,
		broker:   broker,
		repo:     repo,
		tenantID: "tenant-1",
		envMode:  domain.EnvModeLive,
		log:      zerolog.Nop(),
		nowFn:    func() time.Time { return time.Date(2026, 4, 27, 20, 0, 0, 0, time.UTC) },
	}
}

// Order has a live Path B row (no exec_id) -> reconcile must NOT reinsert legs.
func TestReconcileFillsOnBoot_SkipsOrdersAlreadyRecorded(t *testing.T) {
	filledAt := time.Date(2026, 4, 27, 16, 35, 0, 0, time.UTC)
	broker := &reconcileFillsBroker{
		fills: []ports.FillRecord{
			{BrokerOrderID: "3512", ExecutionID: "0000e242.69eefed5.01.01", Symbol: "NVDA260501C00207500", Side: "BUY", Qty: 1, Price: 7.70, CumQty: 1, AvgPrice: 7.70, FilledAt: filledAt},
			{BrokerOrderID: "3512", ExecutionID: "0000e242.69eefed6.01.01", Symbol: "NVDA260501C00207500", Side: "BUY", Qty: 1, Price: 7.70, CumQty: 2, AvgPrice: 7.70, FilledAt: filledAt},
			{BrokerOrderID: "3512", ExecutionID: "0000e242.69eefed7.01.01", Symbol: "NVDA260501C00207500", Side: "BUY", Qty: 2, Price: 7.70, CumQty: 4, AvgPrice: 7.70, FilledAt: filledAt},
		},
	}
	repo := &reconcileFillsRepo{
		reconciledOrderIDs: map[string]struct{}{"3512": {}},
		existingByOrder: map[string]*domain.BrokerOrder{
			"3512": {BrokerOrderID: "3512", Strategy: "avwap_v4", InstrumentType: domain.InstrumentTypeOption},
		},
	}
	svc := newReconcileFillsService(broker, repo)

	svc.reconcileFillsOnBoot(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.recorded, "must not insert any legs when order already has a live row")
}

// Order has no rows at all -> reconcile inserts every leg with option metadata.
func TestReconcileFillsOnBoot_InsertsMissingOrderWithOptionMetadata(t *testing.T) {
	filledAt := time.Date(2026, 4, 27, 16, 35, 0, 0, time.UTC)
	expiry := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	broker := &reconcileFillsBroker{
		fills: []ports.FillRecord{
			{BrokerOrderID: "9999", ExecutionID: "exec-a", Symbol: "NVDA260501C00207500", Side: "BUY", Qty: 4, Price: 7.70, CumQty: 4, AvgPrice: 7.70, FilledAt: filledAt},
		},
	}
	repo := &reconcileFillsRepo{
		existingByOrder: map[string]*domain.BrokerOrder{
			"9999": {
				BrokerOrderID:  "9999",
				Strategy:       "avwap_v4",
				InstrumentType: domain.InstrumentTypeOption,
				OptionSymbol:   "NVDA260501C00207500",
				Underlying:     "NVDA",
				Strike:         207.5,
				Expiry:         expiry,
				OptionRight:    "C",
			},
		},
	}
	svc := newReconcileFillsService(broker, repo)

	svc.reconcileFillsOnBoot(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.recorded, 1)
	got := repo.recorded[0]
	assert.Equal(t, "exec-a", got.ExecutionID)
	assert.Equal(t, "9999", got.BrokerOrderID)
	assert.Equal(t, domain.InstrumentTypeOption, got.InstrumentType)
	assert.Equal(t, "NVDA260501C00207500", got.OptionSymbol)
	assert.Equal(t, "NVDA", got.Underlying)
	assert.InDelta(t, 207.5, got.Strike, 1e-9)
	assert.Equal(t, expiry, got.Expiry)
	assert.Equal(t, "C", got.OptionRight)
}

// Exec ID already recorded -> dedup by exec_id (the original gate, still works).
func TestReconcileFillsOnBoot_SkipsAlreadyRecordedExecutions(t *testing.T) {
	filledAt := time.Date(2026, 4, 27, 16, 35, 0, 0, time.UTC)
	broker := &reconcileFillsBroker{
		fills: []ports.FillRecord{
			{BrokerOrderID: "9999", ExecutionID: "exec-a", Symbol: "NVDA", Side: "BUY", Qty: 1, Price: 7.70, CumQty: 1, AvgPrice: 7.70, FilledAt: filledAt},
		},
	}
	repo := &reconcileFillsRepo{
		recordedExecIDs: map[string]struct{}{"exec-a": {}},
		existingByOrder: map[string]*domain.BrokerOrder{"9999": {BrokerOrderID: "9999", Strategy: "x"}},
	}
	svc := newReconcileFillsService(broker, repo)

	svc.reconcileFillsOnBoot(context.Background())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.recorded)
}
