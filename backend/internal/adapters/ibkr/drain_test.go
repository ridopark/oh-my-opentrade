package ibkr

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/scmhub/ibsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDrainPending_FastPath_NoOpenTrades confirms that drain returns
// immediately when nothing is working — no ticker spin, no error.
func TestDrainPending_FastPath_NoOpenTrades(t *testing.T) {
	ib := &mockIB{openTrades: nil}
	a := NewAdapterWithClient(ib, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	err := a.DrainPending(ctx)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, 50*time.Millisecond, "empty drain must return immediately, not poll")
}

// TestDrainPending_DeadlineElapsed_ReturnsError asserts the 30s-style bound
// actually fires: if a trade never transitions out of the open set, the
// drain must return an error naming the remaining count instead of hanging.
func TestDrainPending_DeadlineElapsed_ReturnsError(t *testing.T) {
	stuck := &ibsync.Trade{Order: &ibsync.Order{OrderID: 42}}
	ib := &mockIB{openTrades: []*ibsync.Trade{stuck}}
	a := NewAdapterWithClient(ib, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	err := a.DrainPending(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "drain timeout")
	assert.Contains(t, err.Error(), "1 orders still working")
}

// TestDrainPending_UnblocksWhenOrdersTerminate proves that once the working
// set empties (the ibsync lib flipped every trade to a terminal state), the
// drain goroutine unblocks on the next poll tick and returns nil.
func TestDrainPending_UnblocksWhenOrdersTerminate(t *testing.T) {
	trade := &ibsync.Trade{Order: &ibsync.Order{OrderID: 99}}
	ib := &mockIB{openTrades: []*ibsync.Trade{trade}}
	a := NewAdapterWithClient(ib, zerolog.Nop())

	// After 500ms, simulate the trade reaching a terminal state by emptying
	// OpenTrades. The drain poller ticks every 200ms so it should observe
	// the transition within ~700ms.
	go func() {
		time.Sleep(500 * time.Millisecond)
		ib.mu.Lock()
		ib.openTrades = nil
		ib.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := a.DrainPending(ctx)
	require.NoError(t, err, "drain must succeed once OpenTrades drains")
}

// TestDrainPending_NilConnection_NoOp ensures a disconnected adapter does
// not panic or error — during shutdown the connection may already be torn
// down by the time we try to drain.
func TestDrainPending_NilConnection_NoOp(t *testing.T) {
	a := &Adapter{conn: &connection{}, log: zerolog.Nop()}
	err := a.DrainPending(context.Background())
	require.NoError(t, err)
}
