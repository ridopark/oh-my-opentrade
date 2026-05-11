package execution

import (
	"context"
	"encoding/json"
	"strings"
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

// dualWriteRepo simulates the production dedup contract:
//   - per-exec writes delete any agg row for the same broker_order_id then insert
//   - aggregate writes insert the residual (cumQty - SUM(per-exec qty)) when > epsilon
//   - sync.Mutex stands in for pg_advisory_xact_lock(hashtext(bo_id))
//   - execution_id UNIQUE catches duplicates (real exec id and synthesized agg id)
type dualWriteRepo struct {
	mu       sync.Mutex
	rows     []domain.Trade
	execIDs  map[string]int // execID → index in rows; lets the test detect dedup
}

func newDualWriteRepo() *dualWriteRepo {
	return &dualWriteRepo{execIDs: map[string]int{}}
}

func (r *dualWriteRepo) seedPhantom(t domain.Trade) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, t)
	if t.ExecutionID != "" {
		r.execIDs[t.ExecutionID] = len(r.rows) - 1
	}
}

func (r *dualWriteRepo) sumPerExecForBrokerOrder(bo string) float64 {
	var total float64
	for _, row := range r.rows {
		if row.BrokerOrderID != bo {
			continue
		}
		if row.ExecutionID == "" || strings.HasPrefix(row.ExecutionID, "agg:") {
			continue
		}
		total += row.Quantity
	}
	return total
}

func (r *dualWriteRepo) deleteAggForBrokerOrder(bo string) {
	out := r.rows[:0]
	for _, row := range r.rows {
		if row.BrokerOrderID == bo && strings.HasPrefix(row.ExecutionID, "agg:") {
			delete(r.execIDs, row.ExecutionID)
			continue
		}
		out = append(out, row)
	}
	r.rows = out
	r.execIDs = map[string]int{}
	for i, row := range r.rows {
		if row.ExecutionID != "" {
			r.execIDs[row.ExecutionID] = i
		}
	}
}

func (r *dualWriteRepo) RecordFillPerExec(_ context.Context, brokerOrderID string, _ time.Time, _, _ float64, t domain.Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.execIDs[t.ExecutionID]; dup && t.ExecutionID != "" {
		return nil
	}

	r.deleteAggForBrokerOrder(brokerOrderID)
	r.rows = append(r.rows, t)
	if t.ExecutionID != "" {
		r.execIDs[t.ExecutionID] = len(r.rows) - 1
	}
	return nil
}

func (r *dualWriteRepo) RecordFillAggregate(_ context.Context, brokerOrderID string, _ time.Time, _, filledQty float64, t domain.Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.execIDs[t.ExecutionID]; dup && t.ExecutionID != "" {
		return nil
	}
	residual := filledQty - r.sumPerExecForBrokerOrder(brokerOrderID)
	if residual <= 1e-9 {
		return nil
	}
	t.Quantity = residual
	r.rows = append(r.rows, t)
	if t.ExecutionID != "" {
		r.execIDs[t.ExecutionID] = len(r.rows) - 1
	}
	return nil
}

func (r *dualWriteRepo) snapshot() []domain.Trade {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Trade, len(r.rows))
	copy(out, r.rows)
	return out
}

// All other RepositoryPort methods are no-ops; insertFillLeg only touches
// the two RecordFill* methods and the position lookup.

func (r *dualWriteRepo) SaveMarketBar(context.Context, domain.MarketBar) error           { return nil }
func (r *dualWriteRepo) SaveMarketBars(context.Context, []domain.MarketBar) (int, error) { return 0, nil }
func (r *dualWriteRepo) GetMarketBars(context.Context, domain.Symbol, domain.Timeframe, time.Time, time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (r *dualWriteRepo) GetMarketBarsMulti(context.Context, []domain.Symbol, domain.Timeframe, time.Time, time.Time) (map[string][]domain.MarketBar, error) {
	return nil, nil
}
func (r *dualWriteRepo) SaveTrade(context.Context, domain.Trade) error { return nil }
func (r *dualWriteRepo) GetTrades(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (r *dualWriteRepo) SaveStrategyDNA(context.Context, domain.StrategyDNA) error { return nil }
func (r *dualWriteRepo) GetLatestStrategyDNA(context.Context, string, domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (r *dualWriteRepo) SaveOrder(context.Context, domain.BrokerOrder) error { return nil }
func (r *dualWriteRepo) UpdateOrderFill(context.Context, string, time.Time, float64, float64) error {
	return nil
}
func (r *dualWriteRepo) ListTrades(context.Context, ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (r *dualWriteRepo) ListOrders(context.Context, ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (r *dualWriteRepo) SaveThoughtLog(context.Context, domain.ThoughtLog) error { return nil }
func (r *dualWriteRepo) GetThoughtLogsByIntentID(context.Context, string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (r *dualWriteRepo) UpdateTradeThesis(context.Context, string, domain.EnvMode, domain.Symbol, json.RawMessage) error {
	return nil
}
func (r *dualWriteRepo) GetMaxBarHighSince(context.Context, domain.Symbol, domain.Timeframe, time.Time) (float64, error) {
	return 0, nil
}
func (r *dualWriteRepo) GetLatestThesisForSymbol(context.Context, string, domain.EnvMode, domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (r *dualWriteRepo) GetNonTerminalOrders(context.Context, string, domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (r *dualWriteRepo) GetOrderByBrokerOrderID(context.Context, string) (*domain.BrokerOrder, error) {
	return nil, nil
}
func (r *dualWriteRepo) GetRecordedFillQty(context.Context, string, domain.EnvMode, domain.Symbol, string, time.Time) (float64, error) {
	return 0, nil
}
func (r *dualWriteRepo) UpdateOrderStatus(context.Context, string, string) error { return nil }
func (r *dualWriteRepo) GetRecordedExecutionIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (r *dualWriteRepo) GetReconciledOrderIDs(context.Context, string, domain.EnvMode, time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (r *dualWriteRepo) GetNetPositions(context.Context, string, domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (r *dualWriteRepo) GetAvgEntryPrice(context.Context, string, domain.EnvMode, domain.Symbol) (float64, error) {
	return 0, nil
}
func (r *dualWriteRepo) HasCanceledExitOrder(context.Context, string, domain.EnvMode, domain.Symbol) (bool, error) {
	return false, nil
}
func (r *dualWriteRepo) HasTradeForBrokerOrderID(context.Context, string) (bool, error) {
	return false, nil
}
func (r *dualWriteRepo) UpdateBarIndicators(context.Context, domain.Symbol, domain.Timeframe, time.Time, float64, float64, float64, float64, map[string]float64) error {
	return nil
}

func newDualWriteService(repo *dualWriteRepo) *Service {
	bus := memory.NewBus()
	return &Service{
		eventBus: bus,
		repo:     repo,
		tenantID: "tenant-1",
		envMode:  domain.EnvModePaper,
		log:      zerolog.Nop(),
		nowFn:    func() time.Time { return time.Date(2026, 5, 2, 15, 32, 40, 0, time.UTC) },
	}
}

func makeDualWritePO(brokerOrderID string, qty float64) *pendingOrder {
	return &pendingOrder{
		intent: domain.OrderIntent{
			ID:        uuid.New(),
			Symbol:    domain.Symbol("MRVL260508C00162500"),
			Direction: domain.DirectionLong,
			Quantity:  qty,
			Strategy:  "avwap_v4",
			Rationale: "dual-write test",
		},
		tenantID:    "tenant-1",
		envMode:     domain.EnvModePaper,
		submitStart: time.Date(2026, 5, 2, 15, 32, 30, 0, time.UTC),
	}
}

func TestInsertFillLeg_PerExecThenAggregate_AggregateNoOp(t *testing.T) {
	repo := newDualWriteRepo()
	svc := newDualWriteService(repo)
	po := makeDualWritePO("9001", 4)
	now := time.Date(2026, 5, 2, 15, 32, 32, 0, time.UTC)

	svc.insertFillLeg(po, "9001", "exec-real-1", now, 1.50, 4, 4, 1.50, zerolog.Nop())
	svc.insertFillLeg(po, "9001", "", now.Add(2*time.Second), 1.50, 4, 4, 1.50, zerolog.Nop())

	rows := repo.snapshot()
	require.Len(t, rows, 1)
	assert.Equal(t, "exec-real-1", rows[0].ExecutionID)
	assert.False(t, strings.HasPrefix(rows[0].ExecutionID, "agg:"))
}

func TestInsertFillLeg_AggregateThenPerExec_PerExecReplaces(t *testing.T) {
	repo := newDualWriteRepo()
	svc := newDualWriteService(repo)
	po := makeDualWritePO("9002", 4)
	now := time.Date(2026, 5, 2, 15, 32, 32, 0, time.UTC)

	svc.insertFillLeg(po, "9002", "", now, 1.50, 4, 4, 1.50, zerolog.Nop())
	svc.insertFillLeg(po, "9002", "exec-real-2", now.Add(time.Second), 1.50, 4, 4, 1.50, zerolog.Nop())

	rows := repo.snapshot()
	require.Len(t, rows, 1)
	assert.Equal(t, "exec-real-2", rows[0].ExecutionID)
}

func TestInsertFillLeg_AggregateOnly_OneRow(t *testing.T) {
	repo := newDualWriteRepo()
	svc := newDualWriteService(repo)
	po := makeDualWritePO("9003", 4)
	now := time.Date(2026, 5, 2, 15, 32, 32, 0, time.UTC)

	svc.insertFillLeg(po, "9003", "", now, 1.50, 4, 4, 1.50, zerolog.Nop())

	rows := repo.snapshot()
	require.Len(t, rows, 1)
	assert.Equal(t, "agg:9003", rows[0].ExecutionID)
}

func TestInsertFillLeg_AggregateTwice_StillOneRow(t *testing.T) {
	repo := newDualWriteRepo()
	svc := newDualWriteService(repo)
	po := makeDualWritePO("9004", 4)
	now := time.Date(2026, 5, 2, 15, 32, 32, 0, time.UTC)

	svc.insertFillLeg(po, "9004", "", now, 1.50, 4, 4, 1.50, zerolog.Nop())
	svc.insertFillLeg(po, "9004", "", now.Add(time.Second), 1.50, 4, 4, 1.50, zerolog.Nop())

	rows := repo.snapshot()
	require.Len(t, rows, 1)
	assert.Equal(t, "agg:9004", rows[0].ExecutionID)
}

func TestInsertFillLeg_MultiLegPerExec_ThenAggregate_AggregateNoOp(t *testing.T) {
	repo := newDualWriteRepo()
	svc := newDualWriteService(repo)
	po := makeDualWritePO("9005", 3)
	t0 := time.Date(2026, 5, 2, 15, 32, 32, 0, time.UTC)

	svc.insertFillLeg(po, "9005", "exec-leg-1", t0, 1.00, 1, 1, 1.00, zerolog.Nop())
	svc.insertFillLeg(po, "9005", "exec-leg-2", t0.Add(time.Second), 1.00, 1, 2, 1.00, zerolog.Nop())
	svc.insertFillLeg(po, "9005", "exec-leg-3", t0.Add(2*time.Second), 1.00, 1, 3, 1.00, zerolog.Nop())
	svc.insertFillLeg(po, "9005", "", t0.Add(3*time.Second), 1.00, 3, 3, 1.00, zerolog.Nop())

	rows := repo.snapshot()
	require.Len(t, rows, 3)
	for i, want := range []string{"exec-leg-1", "exec-leg-2", "exec-leg-3"} {
		assert.Equal(t, want, rows[i].ExecutionID, "row %d exec id", i)
		assert.False(t, strings.HasPrefix(rows[i].ExecutionID, "agg:"), "row %d should not be agg", i)
	}
}

func TestInsertFillLeg_PerExecMultiLeg_NoDup(t *testing.T) {
	repo := newDualWriteRepo()
	svc := newDualWriteService(repo)
	po := makeDualWritePO("9006", 1)
	now := time.Date(2026, 5, 2, 15, 32, 32, 0, time.UTC)

	svc.insertFillLeg(po, "9006", "exec-replay", now, 1.00, 1, 1, 1.00, zerolog.Nop())
	svc.insertFillLeg(po, "9006", "exec-replay", now.Add(time.Second), 1.00, 1, 1, 1.00, zerolog.Nop())

	rows := repo.snapshot()
	require.Len(t, rows, 1)
	assert.Equal(t, "exec-replay", rows[0].ExecutionID)
}

func TestInsertFillLeg_AggregateAndPerExecConcurrent_PerExecWins(t *testing.T) {
	repo := newDualWriteRepo()
	svc := newDualWriteService(repo)
	po := makeDualWritePO("9007", 4)
	now := time.Date(2026, 5, 2, 15, 32, 32, 0, time.UTC)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		svc.insertFillLeg(po, "9007", "", now, 1.50, 4, 4, 1.50, zerolog.Nop())
	}()
	go func() {
		defer wg.Done()
		svc.insertFillLeg(po, "9007", "exec-concurrent", now, 1.50, 4, 4, 1.50, zerolog.Nop())
	}()
	wg.Wait()

	rows := repo.snapshot()
	require.Len(t, rows, 1)
	assert.Equal(t, "exec-concurrent", rows[0].ExecutionID, "per-exec must win regardless of arrival order")
}

// TestInsertFillLeg_PerExecPartial_AggregateWritesResidual reproduces the
// SNOW260515P00152500 -3 incident from 2026-05-08: broker reported a 2-contract
// fill but only one per-exec leg arrived on the stream. The aggregate finalization
// must write a residual row (qty=1) so the ledger stays whole, instead of leaving
// a permanent shortfall that turns into a "negative DB net" reconcile alert.
func TestInsertFillLeg_PerExecPartial_AggregateWritesResidual(t *testing.T) {
	repo := newDualWriteRepo()
	svc := newDualWriteService(repo)
	po := makeDualWritePO("9009", 2)
	now := time.Date(2026, 5, 8, 13, 47, 31, 0, time.UTC)

	// Leg 1 of a 2-contract fill arrives via per-exec.
	svc.insertFillLeg(po, "9009", "exec-leg-1", now, 10.43, 1, 1, 10.43, zerolog.Nop())
	// Leg 2 never arrives. Aggregate finalization fires with cumQty=2.
	svc.insertFillLeg(po, "9009", "", now.Add(time.Second), 10.43, 2, 2, 10.43, zerolog.Nop())

	rows := repo.snapshot()
	require.Len(t, rows, 2)
	assert.Equal(t, "exec-leg-1", rows[0].ExecutionID)
	assert.Equal(t, 1.0, rows[0].Quantity)
	assert.Equal(t, "agg:9009", rows[1].ExecutionID)
	assert.Equal(t, 1.0, rows[1].Quantity, "agg row must carry the missing residual qty")
}

func TestInsertFillLeg_ReconciliationPhantomRowUntouched(t *testing.T) {
	repo := newDualWriteRepo()
	svc := newDualWriteService(repo)

	// Pre-existing phantom row from boot reconciliation: literal exec id,
	// no broker_order_id matching the new order. New per-exec write must
	// not delete or alter it.
	phantom := domain.Trade{
		Time:          time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC),
		TenantID:      "tenant-1",
		EnvMode:       domain.EnvModePaper,
		TradeID:       uuid.New(),
		Symbol:        domain.Symbol("MRVL260508C00162500"),
		Side:          "SELL",
		Quantity:      4,
		Price:         1.20,
		Status:        "FILLED",
		ExecutionID:   "reconciliation_phantom",
		BrokerOrderID: "8000",
	}
	repo.seedPhantom(phantom)

	po := makeDualWritePO("9008", 4)
	now := time.Date(2026, 5, 2, 15, 32, 32, 0, time.UTC)
	svc.insertFillLeg(po, "9008", "exec-new", now, 1.50, 4, 4, 1.50, zerolog.Nop())

	rows := repo.snapshot()
	require.Len(t, rows, 2)
	// Phantom unchanged.
	assert.Equal(t, "reconciliation_phantom", rows[0].ExecutionID)
	assert.Equal(t, "8000", rows[0].BrokerOrderID)
	// New per-exec row written cleanly.
	assert.Equal(t, "exec-new", rows[1].ExecutionID)
	assert.Equal(t, "9008", rows[1].BrokerOrderID)
}
