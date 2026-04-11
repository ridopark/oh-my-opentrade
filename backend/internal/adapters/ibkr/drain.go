package ibkr

import (
	"context"
	"fmt"
	"time"
)

// drainPollInterval is how often DrainPending polls ib.OpenTrades() while
// waiting for working orders to reach a terminal state.
const drainPollInterval = 200 * time.Millisecond

// DrainPending blocks until every currently-working IBKR order reaches a
// terminal state (Filled, Canceled, Rejected, Inactive) or ctx is canceled.
//
// The motivation is graceful shutdown: if waitForShutdown() closes the IBKR
// connection the moment orchestrator.Stop() returns, any exit order that is
// submitted-but-not-yet-filled loses its fill callback. On the next startup,
// the position appears open in the journal even though the broker already
// closed it, which can trigger a second, duplicate exit. By draining working
// orders with a hard deadline, we give the fill pipeline time to land the
// terminal events before the socket is torn down.
//
// The implementation polls ib.OpenTrades() — the same ibsync API used by the
// order poller elsewhere in this adapter. There is no internal "workingOrders"
// map to query; ibsync is the source of truth.
//
// DrainPending returns nil if all orders terminate within the deadline, or an
// error describing how many orders remained when the deadline fired.
func (a *Adapter) DrainPending(ctx context.Context) error {
	ib := a.conn.IB()
	if ib == nil {
		// Not connected: nothing to drain, nothing to error on.
		return nil
	}

	// Fast path: no open trades at all.
	if len(ib.OpenTrades()) == 0 {
		return nil
	}

	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()

	a.log.Info().
		Int("initial_working", len(ib.OpenTrades())).
		Msg("ibkr: draining pending orders before shutdown")

	for {
		select {
		case <-ctx.Done():
			remaining := len(ib.OpenTrades())
			return fmt.Errorf("ibkr: drain timeout — %d orders still working", remaining)
		case <-ticker.C:
			if ib2 := a.conn.IB(); ib2 != nil {
				if len(ib2.OpenTrades()) == 0 {
					a.log.Info().Msg("ibkr: drain complete — all orders reached terminal state")
					return nil
				}
			} else {
				// Connection disappeared mid-drain; nothing more we can do.
				return nil
			}
		}
	}
}
