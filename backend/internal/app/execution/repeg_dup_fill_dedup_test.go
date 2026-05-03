package execution

import (
	"context"
	"errors"
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

type dedupListerBroker struct {
	noopBroker
	fills []ports.FillRecord
	err   error
}

func (b *dedupListerBroker) GetAllFills(context.Context) ([]ports.FillRecord, error) {
	return b.fills, b.err
}

func newDedupSvc(broker ports.BrokerPort, repo ports.RepositoryPort) *Service {
	return &Service{
		eventBus: memory.NewBus(),
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
	repo := &reconcileFillsRepo{}
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

	svc.recordFillsFromExecHistory(context.Background(), po, "BO-42", zerolog.Nop())

	require.Len(t, repo.recorded, 1, "want exactly one trade row")
	assert.Equal(t, "EX-1", repo.recorded[0].ExecutionID)
	assert.InDelta(t, 10.0, repo.recorded[0].Quantity, 1e-9)
}

func TestPollPath_MultiLegFill(t *testing.T) {
	repo := &reconcileFillsRepo{}
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

	svc.recordFillsFromExecHistory(context.Background(), po, "BO-42", zerolog.Nop())

	require.Len(t, repo.recorded, 2, "want exactly two trade rows for two execs")

	seen := map[string]bool{}
	var totalQty float64
	for _, tr := range repo.recorded {
		seen[tr.ExecutionID] = true
		totalQty += tr.Quantity
	}
	assert.True(t, seen["EX-1"] && seen["EX-2"], "both execution IDs must appear, got %v", seen)
	assert.InDelta(t, 10.0, totalQty, 1e-9, "leg quantities must sum to intent quantity")
}

func TestPollPath_FillListerUnsupported(t *testing.T) {
	repo := &reconcileFillsRepo{}
	svc := newDedupSvc(&noopBroker{}, repo)
	po := newDedupPO(10, 1.50)

	svc.recordFillsFromExecHistory(context.Background(), po, "BO-42", zerolog.Nop())

	require.Len(t, repo.recorded, 1, "want exactly one trade row from the legacy fallback")
	assert.Equal(t, "agg:BO-42", repo.recorded[0].ExecutionID,
		"fallback path now writes a synthesized aggregate exec id for dedup")
}

func TestPollPath_GetAllFillsError(t *testing.T) {
	repo := &reconcileFillsRepo{}
	broker := &dedupListerBroker{err: errors.New("ibkr: not connected")}
	svc := newDedupSvc(broker, repo)
	po := newDedupPO(10, 1.50)

	svc.recordFillsFromExecHistory(context.Background(), po, "BO-42", zerolog.Nop())

	require.Len(t, repo.recorded, 1, "want exactly one trade row from the error fallback")
}
