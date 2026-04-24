package ibkr

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/scmhub/ibsync"
)

const orderPollInterval = 2 * time.Second

// execReconcileInterval is fast because Fills() is a non-blocking local cache
// read; the real cost is the periodic ReqFills refresh which runs on its
// own slower timer. The reconciler is now the single source of fill truth
// (per-ExecID), so a 2s cadence keeps end-to-end fill latency bounded.
const execReconcileInterval = 2 * time.Second

func (a *Adapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
	ib := a.conn.IB()
	if ib == nil {
		return nil, fmt.Errorf("ibkr: not connected")
	}

	out := make(chan ports.OrderUpdate, 64)
	a.setOrderOut(ctx, out)

	// Watch existing active trades from previous session.
	for _, t := range ib.Trades() {
		if t.Order != nil && !t.IsDone() {
			go a.watchTradeDone(ctx, t, out)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.pollOrderUpdates(ctx, out) }()
	go func() { defer wg.Done(); a.execReconciler(ctx, out) }()
	go func() { wg.Wait(); a.setOrderOut(context.Background(), nil); close(out) }()

	return out, nil
}

// watchTradeDone waits for a single trade's Done() channel to close, then
// emits one terminal OrderUpdate for non-fill terminal states only. Fills
// are owned by execReconciler (per-ExecID), so an order-level dedup here
// would mask multi-exec deliveries. emittedDone keys on (orderID, "term")
// to prevent the poller from re-emitting the same cancel/expire/reject.
func (a *Adapter) watchTradeDone(ctx context.Context, trade *ibsync.Trade, out chan<- ports.OrderUpdate) {
	if trade == nil || trade.Order == nil {
		return
	}
	// Watchers are not tracked in the SubscribeOrderUpdates WaitGroup, so
	// close(out) during teardown can race this goroutine's send. The send
	// select guards with ctx.Done(), but Go's select is non-deterministic
	// when both cases are ready — recover narrowly from the send-on-closed
	// panic and re-raise anything else.
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if e, ok := r.(runtime.Error); ok && strings.Contains(e.Error(), "closed channel") {
			return
		}
		panic(r)
	}()
	select {
	case <-ctx.Done():
		return
	case <-trade.Done():
		orderID := trade.Order.OrderID
		update := tradeToOrderUpdate(trade)
		// Fills come from execReconciler (per-ExecID, race-free vs OrderStatus
		// snapshots). Suppress them here to keep this goroutine to the
		// non-fill terminal lane only.
		if update.Event == ports.OrderEventFill {
			return
		}
		if _, loaded := a.emittedDone.LoadOrStore(orderID, struct{}{}); loaded {
			return
		}
		a.log.Info().
			Str("order_id", update.BrokerOrderID).
			Str("event", update.Event).
			Float64("filled_qty", update.FilledQty).
			Float64("price", update.Price).
			Msg("trade watcher: terminal state via Done()")
		select {
		case out <- update:
		case <-ctx.Done():
		}
	}
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
					// Fills flow through execReconciler (per-ExecID). Skip them
					// here so a single terminal OrderStatus snapshot doesn't
					// replace N distinct exec events.
					if update.Event == ports.OrderEventFill {
						continue
					}
					// Dedup non-fill terminal events against the Done() watcher.
					if update.Event == ports.OrderEventCanceled || update.Event == ports.OrderEventExpired {
						if _, already := a.emittedDone.LoadOrStore(id, struct{}{}); already {
							continue
						}
					}
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
			// Index trades by OrderID so each fill resolves in O(1) instead
			// of scanning the whole trades slice per leg.
			tradesByOrder := make(map[int64]*ibsync.Trade)
			for _, t := range ib.Trades() {
				if t != nil && t.Order != nil {
					tradesByOrder[t.Order.OrderID] = t
				}
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

				// Label terminal vs partial by correlating with the Trade's
				// OrderStatus. The last leg of a multi-fill order must arrive
				// as Event="fill" so execution.handleStreamFill knows when to
				// claim the pending entry and run lifecycle cleanup.
				update.Event = ports.OrderEventPartialFill
				totalQty := 0.0
				if t, ok := tradesByOrder[f.Execution.OrderID]; ok {
					totalQty = t.Order.TotalQuantity.Float()
					if t.OrderStatus.Status == ibsync.Filled && totalQty > 0 && update.FilledQty+1e-9 >= totalQty {
						update.Event = ports.OrderEventFill
					}
				}

				a.log.Info().
					Str("exec_id", execID).
					Str("broker_order_id", update.BrokerOrderID).
					Str("event", update.Event).
					Float64("qty", update.Qty).
					Float64("cum_qty", update.FilledQty).
					Float64("total_qty", totalQty).
					Float64("price", update.Price).
					Float64("avg_price", update.FilledAvgPrice).
					Msg("exec reconciler: emitting fill leg")

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
		Event:          ports.OrderEventFill,
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
		return ports.OrderEventFill
	case ibsync.Submitted:
		return ports.OrderEventNew
	case ibsync.PreSubmitted:
		return ports.OrderEventAccepted
	case ibsync.PendingSubmit, ibsync.ApiPending:
		return ports.OrderEventNew
	case ibsync.Cancelled, ibsync.ApiCancelled: //nolint:misspell // external ibsync constant
		return ports.OrderEventCanceled
	case ibsync.Inactive:
		return ports.OrderEventExpired
	default:
		return "new"
	}
}
