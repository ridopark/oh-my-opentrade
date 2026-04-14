package positionmonitor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServiceWithBroker(broker *mockBroker) *Service {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(broker, zerolog.Nop())
	return NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
	)
}

func seedPosition(t *testing.T, svc *Service, symbol string) {
	t.Helper()
	svc.processFill(fillMsg{
		Symbol:     domain.Symbol(symbol),
		Side:       "BUY",
		Price:      100,
		Quantity:   10,
		FilledAt:   time.Now(),
		Strategy:   "test_strat",
		AssetClass: domain.AssetClassEquity,
		ExitRules:  []domain.ExitRule{},
	})
}

func TestReconcile_PositionConfirmedOnBroker_ResetsMissCount(t *testing.T) {
	broker := &mockBroker{
		positions: []domain.Trade{{Symbol: domain.Symbol("AAPL")}},
	}
	svc := newTestServiceWithBroker(broker)
	seedPosition(t, svc, "AAPL")

	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "AAPL")
	svc.ghostMissCounts[key] = 2

	svc.reconcileWithBroker(context.Background())

	assert.Equal(t, 1, svc.PositionCount())
	assert.Equal(t, 0, svc.ghostMissCounts[key])
}

func TestReconcile_GhostPositionRemovedAfterThreshold(t *testing.T) {
	broker := &mockBroker{positions: nil}
	svc := newTestServiceWithBroker(broker)
	seedPosition(t, svc, "AAPL")
	require.Equal(t, 1, svc.PositionCount())

	svc.reconcileWithBroker(context.Background())
	assert.Equal(t, 1, svc.PositionCount(), "miss 1: position retained")

	svc.reconcileWithBroker(context.Background())
	assert.Equal(t, 1, svc.PositionCount(), "miss 2: position retained")

	svc.reconcileWithBroker(context.Background())
	assert.Equal(t, 0, svc.PositionCount(), "miss 3: ghost position removed")
}

func TestReconcile_MissCountResetsIfPositionReappears(t *testing.T) {
	broker := &mockBroker{positions: nil}
	svc := newTestServiceWithBroker(broker)
	seedPosition(t, svc, "AAPL")

	svc.reconcileWithBroker(context.Background())
	svc.reconcileWithBroker(context.Background())

	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "AAPL")
	require.Equal(t, 2, svc.ghostMissCounts[key])

	broker.positions = []domain.Trade{{Symbol: domain.Symbol("AAPL")}}
	svc.reconcileWithBroker(context.Background())

	assert.Equal(t, 1, svc.PositionCount())
	assert.Equal(t, 0, svc.ghostMissCounts[key])
}

func TestReconcile_SkipsGracefullyWhenBrokerIsNil(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop())

	seedPosition(t, svc, "AAPL")

	svc.reconcileWithBroker(context.Background())

	assert.Equal(t, 1, svc.PositionCount())
}

func TestReconcile_HandlesBrokerAPIError(t *testing.T) {
	broker := &mockBroker{posErr: assert.AnError}
	svc := newTestServiceWithBroker(broker)
	seedPosition(t, svc, "AAPL")

	svc.reconcileWithBroker(context.Background())

	assert.Equal(t, 1, svc.PositionCount())
	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "AAPL")
	assert.Equal(t, 0, svc.ghostMissCounts[key])
}

type capturingRepo struct {
	mockRepo
	savedTrades []domain.Trade
}

func (r *capturingRepo) SaveTrade(_ context.Context, trade domain.Trade) error {
	r.savedTrades = append(r.savedTrades, trade)
	return nil
}

func newTestServiceWithBrokerAndRepo(broker *mockBroker, repo *capturingRepo) *Service {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(broker, zerolog.Nop())
	return NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
		WithRepo(repo),
	)
}

func TestReconcile_QuantitySyncFromBroker(t *testing.T) {
	broker := &mockBroker{
		positions: []domain.Trade{{Symbol: domain.Symbol("AAPL"), Quantity: 8}},
	}
	svc := newTestServiceWithBroker(broker)
	seedPosition(t, svc, "AAPL")

	key := fmt.Sprintf("%s:%s:%s", svc.tenantID, svc.envMode, "AAPL")
	require.Equal(t, 10.0, svc.positions[key].Quantity)

	svc.reconcileWithBroker(context.Background())

	assert.Equal(t, 1, svc.PositionCount())
	assert.Equal(t, 8.0, svc.positions[key].Quantity)
}

// countingBroker wraps mockBroker and counts GetPositions calls so we can
// assert that reconcile short-circuits without touching the broker.
type countingBroker struct {
	mockBroker
	getPositionsCalls int
}

func (b *countingBroker) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	b.getPositionsCalls++
	return b.mockBroker.GetPositions(ctx, tenantID, envMode)
}

// TestReconcile_ShutdownFlag_ShortCircuits ensures SignalShutdown() makes
// both reconcile entry points no-op. The guard exists so a reconcile tick
// that fires between orchestrator.Stop() and broker.Close() during graceful
// shutdown cannot emit a spurious reconciliation trade or stale position
// event against a half-torn-down broker.
func TestReconcile_ShutdownFlag_ShortCircuits(t *testing.T) {
	broker := &countingBroker{mockBroker: mockBroker{positions: nil}}
	repo := &capturingRepo{}
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(broker, zerolog.Nop())
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
		WithRepo(repo),
	)
	seedPosition(t, svc, "AAPL")
	require.Equal(t, 1, svc.PositionCount())

	// Before shutdown: both loops hit the broker.
	svc.reconcileWithBroker(context.Background())
	svc.reconcileGlobal(context.Background())
	assert.Equal(t, 2, broker.getPositionsCalls, "both reconcile loops should call broker normally")

	// Signal shutdown; subsequent calls must be no-ops.
	svc.SignalShutdown()
	require.True(t, svc.IsShuttingDown())

	svc.reconcileWithBroker(context.Background())
	svc.reconcileWithBroker(context.Background())
	svc.reconcileGlobal(context.Background())
	svc.reconcileGlobal(context.Background())

	assert.Equal(t, 2, broker.getPositionsCalls,
		"no broker calls must be made after SignalShutdown")
	assert.Equal(t, 1, svc.PositionCount(), "position state must not be mutated during shutdown")
	assert.Empty(t, repo.savedTrades, "no reconciliation trades must be written during shutdown")
}

func TestReconcile_GhostRemoved_NoSyntheticTradeWritten(t *testing.T) {
	// Reconciler must NEVER write synthetic trades to "balance the books".
	// Stale broker data (e.g. IBKR paper's ib.Positions() returning phantom
	// values) previously caused synthetic SELL fills that corrupted the
	// trade ledger. The reconciler now only removes ghost positions from
	// in-memory state and surfaces drift via ERROR logs for manual action.
	broker := &mockBroker{positions: nil}
	repo := &capturingRepo{}
	svc := newTestServiceWithBrokerAndRepo(broker, repo)
	seedPosition(t, svc, "AAPL")
	require.Equal(t, 1, svc.PositionCount())

	svc.reconcileWithBroker(context.Background())
	svc.reconcileWithBroker(context.Background())
	svc.reconcileWithBroker(context.Background())
	assert.Equal(t, 0, svc.PositionCount(), "ghost removed after threshold")
	assert.Empty(t, repo.savedTrades, "reconciler must not write synthetic trades")
}
