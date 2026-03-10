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
