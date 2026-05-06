package builtin

import (
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// armPendingEntry records a freshly emitted entry on the strategy's pending
// fields. Live and paper wait for FillConfirmation to set PositionSide;
// backtest sets PositionSide inline so subsequent OnBars can run exit logic
// without waiting for the deferred bus cascade to deliver FillReceived.
// EntryRejection (rollbackPendingEntry) reverses the optimistic transition.
//
// Strategies wrap this in a one-line method on their state for ergonomic
// call sites. The helper covers the common bookkeeping; per-strategy extras
// (e.g. CooldownUntil) stay at the call site.
func armPendingEntry(positionSide, pendingEntry *start.Side, pendingAt *time.Time, side start.Side, now time.Time, ctx start.Context) {
	*pendingEntry = side
	*pendingAt = now
	if ctx != nil && ctx.IsBacktest() {
		*positionSide = side
		*pendingEntry = ""
		*pendingAt = time.Time{}
	}
}

// rollbackPendingEntry reverses an armPendingEntry: clears PositionSide,
// PendingEntry, PendingEntryAt and refunds TradesToday. The refund fires
// when EITHER PendingEntry (live, before broker confirmation) OR
// PositionSide (backtest, after optimistic-set) is non-empty — both
// signal a previous increment that needs balancing.
//
// Per-strategy extras (CooldownUntil reset, log lines, AI request bookkeeping)
// stay at the OnEvent EntryRejection call site since they vary per strategy.
func rollbackPendingEntry(positionSide, pendingEntry *start.Side, pendingAt *time.Time, tradesToday *int) {
	wasArmed := *positionSide != "" || *pendingEntry != ""
	if wasArmed && *tradesToday > 0 {
		*tradesToday--
	}
	*positionSide = ""
	*pendingEntry = ""
	*pendingAt = time.Time{}
}
