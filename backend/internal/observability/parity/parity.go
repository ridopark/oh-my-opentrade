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

import (
	"os"
	"sync"
)

const envVar = "PARITY_DIAG_ENABLED"

var (
	once    sync.Once
	enabled bool
)

// Enabled reports whether PARITY_DIAG_ENABLED was set to "true" at the
// time of the first call. The env var is read once and cached so call
// sites on the hot path pay only a bool deref.
func Enabled() bool {
	once.Do(func() {
		enabled = os.Getenv(envVar) == "true"
	})
	return enabled
}
