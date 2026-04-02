package ibkr

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/scmhub/ibsync"
)

const orderPollInterval = 200 * time.Millisecond
const execReconcileInterval = 10 * time.Second

func (a *Adapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
	ib := a.conn.IB()
	if ib == nil {
		return nil, fmt.Errorf("ibkr: not connected")
	}

	out := make(chan ports.OrderUpdate, 64)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.pollOrderUpdates(ctx, out) }()
	go func() { defer wg.Done(); a.execReconciler(ctx, out) }()
	go func() { wg.Wait(); close(out) }()

	return out, nil
}

func (a *Adapter) pollOrderUpdates(ctx context.Context, out chan<- ports.OrderUpdate) {
	type tradeState struct {
		status ibsync.Status
		filled float64
	}
	seen := make(map[int64]tradeState)

	ticker := time.NewTicker(orderPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ib := a.conn.IB()
			if ib == nil {
				continue
			}
			trades := ib.Trades()
			for _, t := range trades {
				if t.Order == nil {
					continue
				}
				id := t.Order.OrderID
				cur := tradeState{
					status: t.OrderStatus.Status,
					filled: t.OrderStatus.Filled.Float(),
				}
				prev, existed := seen[id]
				seen[id] = cur

				shouldEmit := !existed ||
					cur.status != prev.status ||
					(cur.status == ibsync.Submitted && cur.filled > prev.filled)

				if shouldEmit {
					update := tradeToOrderUpdate(t)
					select {
					case out <- update:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// execReconciler periodically calls ReqFills to catch fills that the
// Trades() polling loop missed (known IBKR paper trading issue).
func (a *Adapter) execReconciler(ctx context.Context, out chan<- ports.OrderUpdate) {
	ib := a.conn.IB()
	if ib == nil {
		return
	}

	seenExecIDs := make(map[string]struct{})

	// Seed with existing fills so we don't re-emit old ones.
	if fills, err := ib.ReqFills(); err == nil {
		for _, f := range fills {
			if f.Execution != nil {
				seenExecIDs[f.Execution.ExecID] = struct{}{}
			}
		}
		a.log.Info().Int("seeded", len(seenExecIDs)).Msg("exec reconciler: seeded existing fills")
	}

	ticker := time.NewTicker(execReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ib := a.conn.IB()
			if ib == nil {
				continue
			}
			fills, err := ib.ReqFills()
			if err != nil {
				a.log.Warn().Err(err).Msg("exec reconciler: ReqFills failed")
				continue
			}
			for _, f := range fills {
				if f.Execution == nil {
					continue
				}
				execID := f.Execution.ExecID
				if _, seen := seenExecIDs[execID]; seen {
					continue
				}
				seenExecIDs[execID] = struct{}{}

				update := fillToOrderUpdate(f)
				a.log.Info().
					Str("exec_id", execID).
					Str("broker_order_id", update.BrokerOrderID).
					Str("event", update.Event).
					Float64("qty", update.Qty).
					Float64("price", update.Price).
					Msg("exec reconciler: detected missed fill")

				select {
				case out <- update:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func fillToOrderUpdate(f ibsync.Fill) ports.OrderUpdate {
	exec := f.Execution
	return ports.OrderUpdate{
		BrokerOrderID:  strconv.FormatInt(exec.OrderID, 10),
		ExecutionID:    exec.ExecID,
		Event:          "fill",
		Qty:            exec.Shares.Float(),
		Price:          exec.Price,
		FilledQty:      exec.CumQty.Float(),
		FilledAvgPrice: exec.AvgPrice,
		FilledAt:       f.Time,
	}
}

func tradeToOrderUpdate(t *ibsync.Trade) ports.OrderUpdate {
	os := t.OrderStatus
	fills := t.Fills()

	var filledAt time.Time
	var execID string
	var fillQty, fillPrice float64
	if len(fills) > 0 {
		last := fills[len(fills)-1]
		filledAt = last.Time
		if last.Execution != nil {
			execID = last.Execution.ExecID
			fillQty = last.Execution.Shares.Float()
			fillPrice = last.Execution.Price
		}
	}

	return ports.OrderUpdate{
		BrokerOrderID:  strconv.FormatInt(os.OrderID, 10),
		ExecutionID:    execID,
		Event:          mapStatusToEvent(os.Status),
		Qty:            fillQty,
		Price:          fillPrice,
		FilledQty:      os.Filled.Float(),
		FilledAvgPrice: os.AvgFillPrice,
		FilledAt:       filledAt,
	}
}

func mapStatusToEvent(s ibsync.Status) string {
	switch s {
	case ibsync.Filled:
		return "fill"
	case ibsync.Submitted:
		return "new"
	case ibsync.PreSubmitted:
		return "accepted"
	case ibsync.PendingSubmit, ibsync.ApiPending:
		return "new"
	case ibsync.Cancelled, ibsync.ApiCancelled: //nolint:misspell // external ibsync constant
		return "canceled"
	case ibsync.Inactive:
		return "expired"
	default:
		return "new"
	}
}
