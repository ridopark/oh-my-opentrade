package ibkr

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/scmhub/ibsync"
)

// TestStreamPartialFillPrecedesTerminalFill_IBKR enforces the strict
// OrderStreamPort invariant the audit (H5) named: for any single
// BrokerOrderID, every OrderEventPartialFill emitted must precede every
// terminal event (OrderEventFill / OrderEventCanceled / OrderEventRejected
// / OrderEventExpired) for the same orderID.
//
// Why this invariant matters in production (not just LSP purity): the
// strategy code at avwap_v1.go:3832-3849 and macd_v1.go:712-749 assumes
// PendingEntry is still populated when partials arrive — it transitions
// to PositionSide on the terminal event. If a terminal arrived before a
// partial, execution.handleStreamFill's LoadAndDelete would claim the
// pending order on the terminal, and the late partial would fail to
// load and be silently dropped (service.go:1669-1681). MACD's
// pending-entry timeout (line 380-388) would then block exits for 5
// minutes while the position book carries qty the strategy doesn't
// know about.
//
// Test scenario (engineered against the IBKR adapter's execReconciler
// labeling at order_stream.go:225-232):
//   1. Seed mockIB with one trade: TotalQuantity=10, Status=Filled.
//      Status=Filled is required so IsDone() returns true, which
//      prevents SubscribeOrderUpdates from spawning a watchTradeDone
//      goroutine that would block on a nil Done() channel
//      (mockIB.makeTrade doesn't initialize Done).
//   2. Subscribe.
//   3. Inject two fills via mock.SeedFill — partial (cumQty=5) then
//      terminal (cumQty=10). Order matters: the reconciler iterates
//      Fills() in slice order, and partial→terminal labeling depends
//      on cumQty < totalQty for partial vs cumQty >= totalQty for
//      terminal.
//   4. Wait one reconciler poll cycle (2s + 500ms margin).
//   5. Drain events for the test orderID; assert that within the
//      sequence, every "partial_fill" event index is less than every
//      terminal event index.
//
// Test runtime: ~2.5s — driven by the IBKR adapter's 2s
// execReconcileInterval (order_stream.go:22). Future PR may add a
// test-only signal that lets the harness wait for one reconciler
// cycle deterministically; for now the time.Sleep is acceptable.
func TestStreamPartialFillPrecedesTerminalFill_IBKR(t *testing.T) {
	const (
		orderID    int64   = 7777
		totalQty   float64 = 10.0
		partialQty float64 = 5.0
	)

	// Step 1: seed the trade. Status=Filled so IsDone() returns true
	// and SubscribeOrderUpdates skips watchTradeDone (avoids nil-Done
	// hang). Order.TotalQuantity drives the reconciler's terminal
	// labeling threshold.
	order := &ibsync.Order{}
	order.OrderID = orderID
	order.TotalQuantity = ibsync.StringToDecimal(fmt.Sprintf("%.6f", totalQty))
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
	for len(observed) < 2 {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed before observing 2 fills; got %d", len(observed))
			}
			if ev.BrokerOrderID == wantOrderID {
				observed = append(observed, ev)
			}
		case <-deadline:
			t.Fatalf("did not observe 2 fills within %s; got %d events with broker_order_id=%s", reconcilerWait+2*time.Second, len(observed), wantOrderID)
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
		t.Fatalf("no OrderEventPartialFill observed; events=%+v", observed)
	}
	if firstTerminalIdx < 0 {
		t.Fatalf("no terminal event observed; events=%+v", observed)
	}
	if lastPartialIdx >= firstTerminalIdx {
		t.Errorf("partial_fill at index %d arrived AT OR AFTER terminal at index %d; contract requires every partial_fill to precede every terminal event for the same BrokerOrderID. events=%+v", lastPartialIdx, firstTerminalIdx, observed)
	}
}
