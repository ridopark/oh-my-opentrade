package execution

import (
	"context"
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

// newFastPollSvc wires a Service with the given bus, broker and repo so the
// test can subscribe to events before driving the persist path.
func newFastPollSvc(bus *memory.Bus, broker ports.BrokerPort, repo ports.RepositoryPort, now time.Time) *Service {
	return &Service{
		eventBus: bus,
		broker:   broker,
		repo:     repo,
		tenantID: "tenant-1",
		envMode:  domain.EnvModePaper,
		log:      zerolog.Nop(),
		nowFn:    func() time.Time { return now },
	}
}

func newFastPollPO(symbol string, qty, limit float64, submitStart time.Time) *pendingOrder {
	return &pendingOrder{
		intent: domain.OrderIntent{
			ID:         uuid.New(),
			Symbol:     domain.Symbol(symbol),
			Direction:  domain.DirectionLong,
			Quantity:   qty,
			LimitPrice: limit,
			Strategy:   "copytrade_v1",
			Rationale:  "copytrade BTO",
		},
		tenantID:    "tenant-1",
		envMode:     domain.EnvModePaper,
		submitStart: submitStart,
	}
}

// TestFastPoll_RecordFillsFromExecHistory_EmitsFillReceived asserts that the
// fast-poll fill path publishes EventFillReceived after persisting per-exec
// trade rows. Without this, copytrade ghost positions never confirm, the
// position monitor never registers entries, and downstream consumers
// (signal_tracker, perf ledger, SSE fan-out) silently miss the fill.
//
// Reproduces the 2026-05-04 SPY 5/07 724C copytrade incident: BTO filled at
// IBKR, no FillReceived emitted, ghost expired at TTL=120s, STC dropped 90
// minutes later as "no prior BTO".
func TestFastPoll_RecordFillsFromExecHistory_EmitsFillReceived(t *testing.T) {
	bus := memory.NewBus()
	repo := &reconcileFillsRepo{}
	filledAt := time.Date(2026, 5, 4, 14, 39, 12, 0, time.UTC)
	broker := &dedupListerBroker{
		fills: []ports.FillRecord{{
			BrokerOrderID: "5114",
			ExecutionID:   "00020057.69f86210.01.01",
			Symbol:        "SPY260507C00724000",
			Side:          "BUY",
			Qty:           12,
			Price:         2.475,
			CumQty:        12,
			AvgPrice:      2.475,
			FilledAt:      filledAt,
		}},
	}
	svc := newFastPollSvc(bus, broker, repo, filledAt)
	payloads := captureFillEvents(t, bus)

	po := newFastPollPO("SPY260507C00724000", 12, 2.64, filledAt.Add(-3*time.Second))
	svc.recordFillsFromExecHistory(context.Background(), po, "5114", zerolog.Nop())

	require.Len(t, *payloads, 1, "fast-poll path must emit exactly one FillReceived per order")

	payload := (*payloads)[0]
	// Keys read by strategy.runner.handleFill — see runner.go:handleFill.
	// Must remain in sync with what execution.runFillFinalization emits.
	assert.Equal(t, "SPY260507C00724000", payload["symbol"])
	assert.Equal(t, "BUY", payload["side"])
	assert.Equal(t, "copytrade_v1", payload["strategy"])
	assert.InDelta(t, 12.0, payload["quantity"].(float64), 1e-9)
	assert.InDelta(t, 2.475, payload["price"].(float64), 1e-9)
	assert.Equal(t, filledAt, payload["filled_at"], "filled_at must propagate cumulative leg timestamp")
	assert.Equal(t, "5114", payload["broker_order_id"])
}

// TestFastPoll_MultiLeg_EmitsSingleFillReceived ensures the per-leg insert
// loop in recordFillsFromExecHistory emits exactly ONE cumulative
// FillReceived (variant C: aggregated emit), not N per-leg events. Per-leg
// emission would cause copytrade to false-page ORPHAN FILL on leg 2 (the
// 1st leg flips Pending=false, so the 2nd no longer matches a Pending
// position) and would double-count in perf LedgerWriter / backtest
// Collector.
func TestFastPoll_MultiLeg_EmitsSingleFillReceived(t *testing.T) {
	bus := memory.NewBus()
	repo := &reconcileFillsRepo{}
	filledAt := time.Date(2026, 5, 4, 14, 39, 12, 0, time.UTC)
	broker := &dedupListerBroker{
		fills: []ports.FillRecord{
			{
				BrokerOrderID: "5114",
				ExecutionID:   "EX-A",
				Symbol:        "SPY260507C00724000",
				Side:          "BUY",
				Qty:           5,
				Price:         2.50,
				CumQty:        5,
				AvgPrice:      2.50,
				FilledAt:      filledAt,
			},
			{
				BrokerOrderID: "5114",
				ExecutionID:   "EX-B",
				Symbol:        "SPY260507C00724000",
				Side:          "BUY",
				Qty:           7,
				Price:         2.46,
				CumQty:        12,
				AvgPrice:      (5*2.50 + 7*2.46) / 12,
				FilledAt:      filledAt.Add(time.Second),
			},
		},
	}
	svc := newFastPollSvc(bus, broker, repo, filledAt)
	payloads := captureFillEvents(t, bus)

	po := newFastPollPO("SPY260507C00724000", 12, 2.64, filledAt.Add(-3*time.Second))
	svc.recordFillsFromExecHistory(context.Background(), po, "5114", zerolog.Nop())

	require.Len(t, *payloads, 1, "multi-leg fast-poll must collapse to one cumulative FillReceived")

	payload := (*payloads)[0]
	assert.InDelta(t, 12.0, payload["quantity"].(float64), 1e-9, "qty must be cumulative")
	expectedVWAP := (5*2.50 + 7*2.46) / 12
	assert.InDelta(t, expectedVWAP, payload["price"].(float64), 1e-6, "price must be cumulative VWAP")
}

// TestFastPoll_FillListerUnsupported_StillEmits covers the fallback branch:
// when the broker doesn't implement FillLister (paper sim, tests),
// recordFillsFromExecHistory writes a single agg-row trade. It must still
// publish FillReceived so subscribers don't miss the entry.
func TestFastPoll_FillListerUnsupported_StillEmits(t *testing.T) {
	bus := memory.NewBus()
	repo := &reconcileFillsRepo{}
	filledAt := time.Date(2026, 5, 4, 14, 39, 12, 0, time.UTC)
	svc := newFastPollSvc(bus, &noopBroker{}, repo, filledAt)
	payloads := captureFillEvents(t, bus)

	po := newFastPollPO("SPY260507C00724000", 12, 2.64, filledAt.Add(-3*time.Second))
	svc.recordFillsFromExecHistory(context.Background(), po, "5114", zerolog.Nop())

	require.Len(t, *payloads, 1, "fallback branch must emit FillReceived")
	payload := (*payloads)[0]
	assert.InDelta(t, 12.0, payload["quantity"].(float64), 1e-9)
	assert.InDelta(t, 2.64, payload["price"].(float64), 1e-9, "fallback uses intent.LimitPrice")
}
