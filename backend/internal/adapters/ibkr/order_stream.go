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

// execReconcileRefreshInterval is how often we call ReqFills (blocking API call)
// to refresh ibsync's internal fill cache. This catches fills that the real-time
// callbacks missed entirely.
const execReconcileRefreshInterval = 60 * time.Second

// execReconciler detects fills missed by the Trades() polling loop.
// It checks ibsync's local fill cache (Fills(), non-blocking) every 10s,
// and periodically refreshes that cache via ReqFills() (blocking API call).
func (a *Adapter) execReconciler(ctx context.Context, out chan<- ports.OrderUpdate) {
	seenExecIDs := make(map[string]struct{})

	// Seed from local cache immediately (non-blocking).
	if ib := a.conn.IB(); ib != nil {
		for _, f := range ib.Fills() {
			if f.Execution != nil {
				seenExecIDs[f.Execution.ExecID] = struct{}{}
			}
		}
		a.log.Info().Int("seeded", len(seenExecIDs)).Msg("exec reconciler: seeded from local fill cache")
	}

	// Periodically refresh ibsync's fill cache in the background.
	go a.execCacheRefresher(ctx)

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
			// Fills() is non-blocking — reads from ibsync's local state.
			fills := ib.Fills()
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

// execCacheRefresher periodically calls ReqFills() to refresh ibsync's
// internal fill cache. This is a blocking API call that may take up to 30s.
func (a *Adapter) execCacheRefresher(ctx context.Context) {
	// Initial delay to let connection stabilize.
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}

	for {
		if ib := a.conn.IB(); ib != nil {
			_, err := ib.ReqFills()
			if err != nil {
				a.log.Warn().Err(err).Msg("exec reconciler: ReqFills cache refresh failed")
			} else {
				a.log.Debug().Msg("exec reconciler: fill cache refreshed via ReqFills")
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(execReconcileRefreshInterval):
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
