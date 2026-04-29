// Package parity provides a single env-var-gated toggle for parity-diag
// log emissions across the strategy/execution pipeline. The point is to
// let live and backtest dump the same per-stage state into the log so a
// post-hoc diff reveals where they diverge — without paying the log-
// volume cost in production.
//
// All sites are info-level when enabled; default off. Backtest runs that
// want the diag set the env var before launching omo-core.
//
// Usage:
//
//	if parity.Enabled() {
//	    log.Info().
//	        Str("stage", "BarReceived").
//	        Str("symbol", sym).
//	        Float64("close", bar.Close).
//	        Msg("parity-diag")
//	}
//
// Designed as a thin seam — when the parity investigation closes out,
// the call sites get deleted and this package goes with them. See
// `_workspace/parity_observability_followup.md` for the long-term
// observability plan that supersedes these log lines.
package parity

import "os"

const envVar = "PARITY_DIAG_ENABLED"

// Stage identifiers shared across all parity-diag emit sites. Constants
// instead of literal strings so a misspelling fails the build instead of
// silently dropping events from grep aggregations.
const (
	StageBarReceived       = "BarReceived"
	StageIndicatorSnapshot = "IndicatorSnapshot"
	StageEntryGated        = "EntryGated"
	StageSignalCreated     = "SignalCreated"
	StageRiskSized         = "RiskSized"
	StageOrderSubmitted    = "OrderSubmitted"
	StageFillRecorded      = "FillRecorded"
)

// enabled is set once at process startup. Tests in this package reset it
// directly. Hot-path callers (millions per backtest) pay a single
// non-atomic bool load — sync.Once would add an atomic acquire-load per
// call which we measured as material in long backtest replays.
var enabled = os.Getenv(envVar) == "true"

// Enabled reports whether PARITY_DIAG_ENABLED was "true" at process
// start. Mid-process env changes are intentionally ignored — flipping
// the toggle on a long-running live process to start emitting (or stop)
// would be a config-drift surprise.
func Enabled() bool { return enabled }
