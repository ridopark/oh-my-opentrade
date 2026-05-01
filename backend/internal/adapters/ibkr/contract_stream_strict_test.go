package ibkr

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/scmhub/ibsync"
)

// TestStreamPartialFillPrecedesTerminalFill_IBKR enforces the strict
// OrderStreamPort invariant the audit (H5) named: for any single
// BrokerOrderID, every OrderEventPartialFill emitted must precede every
// terminal event (Fill / Canceled / Rejected / Expired) for that orderID.
//
// Why this matters in production: AVWAP and MACD strategy code assumes
// PendingEntry is still set when partials arrive and transitions to
// PositionSide on the terminal event. If a terminal arrived first,
// execution.handleStreamFill's LoadAndDelete would claim the pending
// order on the terminal and the late partial would silently fail to
// load (service.go:1669-1681). MACD's pending-entry timeout would then
// block exits for ~5 minutes while the position book carries qty the
// strategy doesn't know about.
//
// Runtime: ~2s, driven by the IBKR adapter's execReconcileInterval.
func TestStreamPartialFillPrecedesTerminalFill_IBKR(t *testing.T) {
	const (
		orderID        int64   = 7777
		totalQty       float64 = 10.0
		partialQty     float64 = 5.0
		expectedEvents         = 2
	)

	// Step 1: seed the trade. Status=Filled so IsDone() returns true
	// and SubscribeOrderUpdates skips watchTradeDone (avoids nil-Done
	// hang). Order.TotalQuantity drives the reconciler's terminal
	// labeling threshold.
	order := &ibsync.Order{}
	order.OrderID = orderID
	order.TotalQuantity = toDecimal(totalQty)
	trade := &ibsync.Trade{Order: order}
	trade.OrderStatus.Status = ibsync.Filled

	mock := &mockIB{
		connected: true,
		trades:    []*ibsync.Trade{trade},
	}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Step 2: subscribe BEFORE seeding fills. The execReconciler's seed
	// loop (order_stream.go:174-182) marks any fills present at
	// Subscribe time as seen without emitting them; tests must inject
	// fills after Subscribe to observe them.
	ch, err := a.SubscribeOrderUpdates(ctx)
	if err != nil {
		t.Fatalf("SubscribeOrderUpdates: %v", err)
	}

	// SubscribeOrderUpdates spawns the execReconciler goroutine, which
	// runs the seed loop synchronously before reaching its ticker.
	// Wait briefly so SeedFill below cannot race the seed loop and end
	// up marked as already-seen.
	time.Sleep(100 * time.Millisecond)

	// Step 3: inject the two fills.
	mock.SeedFill(orderID, "exec-partial-A", partialQty, partialQty, 100.0, 100.0)
	mock.SeedFill(orderID, "exec-final-B", totalQty-partialQty, totalQty, 100.0, 100.0)

	// Step 4: wait for one reconciler poll cycle.
	const reconcilerWait = 2*time.Second + 500*time.Millisecond
	deadline := time.After(reconcilerWait + 2*time.Second)

	// Step 5: drain events for the test orderID until we see both
	// fills, then assert ordering. We accept any number of unrelated
	// events (none expected, but tolerant — the contract is on
	// ordering, not on event-set restriction).
	wantOrderID := fmt.Sprintf("%d", orderID)
	var observed []ports.OrderUpdate
	for len(observed) < expectedEvents {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed before observing %d fills; got %d", expectedEvents, len(observed))
			}
			if ev.BrokerOrderID == wantOrderID {
				observed = append(observed, ev)
			}
		case <-deadline:
			t.Fatalf("did not observe %d fills within %s; got %d events with broker_order_id=%s", expectedEvents, reconcilerWait+2*time.Second, len(observed), wantOrderID)
		}
	}

	// Invariant: every partial_fill index is less than every terminal index.
	lastPartialIdx := -1
	firstTerminalIdx := -1
	for i, ev := range observed {
		switch ev.Event {
		case ports.OrderEventPartialFill:
			lastPartialIdx = i
		case ports.OrderEventFill, ports.OrderEventCanceled, ports.OrderEventRejected, ports.OrderEventExpired:
			if firstTerminalIdx < 0 {
				firstTerminalIdx = i
			}
		}
	}

	if lastPartialIdx < 0 {
		t.Fatalf("no OrderEventPartialFill observed; sequence=%s", eventSequence(observed))
	}
	if firstTerminalIdx < 0 {
		t.Fatalf("no terminal event observed; sequence=%s", eventSequence(observed))
	}
	if lastPartialIdx >= firstTerminalIdx {
		t.Errorf("partial_fill at index %d arrived AT OR AFTER terminal at index %d; contract requires every partial_fill to precede every terminal event for the same BrokerOrderID. sequence=%s", lastPartialIdx, firstTerminalIdx, eventSequence(observed))
	}
}

// eventSequence formats a slice of OrderUpdates as a compact list of
// just their Event types, e.g. [partial_fill, fill]. The default %+v
// formatting drowns the actual event ordering in field detail; a
// failing test wants the sequence at a glance.
func eventSequence(events []ports.OrderUpdate) string {
	parts := make([]string, len(events))
	for i, ev := range events {
		parts[i] = ev.Event
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
