package ibkr

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/scmhub/ibsync"
)

const orderPollInterval = 200 * time.Millisecond

func (a *Adapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
	ib := a.conn.IB()
	if ib == nil {
		return nil, fmt.Errorf("ibkr: not connected")
	}

	out := make(chan ports.OrderUpdate, 64)
	go a.pollOrderUpdates(ctx, out)
	return out, nil
}

func (a *Adapter) pollOrderUpdates(ctx context.Context, out chan<- ports.OrderUpdate) {
	defer close(out)

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
