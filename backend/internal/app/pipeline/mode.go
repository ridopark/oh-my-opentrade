// Package pipeline owns the second-tier composition ("post-build wiring")
// that connects already-constructed components — event bus, monitor,
// execution, position monitor, strategy runner — into a working trading
// session graph. Three entry points (cmd/omo-core, cmd/omo-replay,
// internal/app/backtest) all need this graph; previously each hand-wired
// it independently, producing recurring divergences when a subscriber
// or wiring call was added in one root and forgotten in the others.
//
// The bootstrap package owns the FIRST tier (component construction —
// BuildIngestion, BuildMonitor, BuildExecutionService, BuildPositionMonitor,
// BuildStrategyPipeline). Pipeline owns the SECOND tier — Set* wiring,
// Subscribe* registration, and per-mode capability decisions.
//
// Mode controls per-mode behavior (which subscribers to wire, which
// notifiers to install, whether session-refresh polling is active, etc.)
// without scattering `if isBacktest` branches through business logic.
//
// This is the SOLID OCP fix for the divergences cataloged in
// _workspace/parity_live_vs_backtest_divergence_audit.md (H2, H4, M9,
// plus four new wiring gaps in #39-42).
package pipeline

// Mode identifies which composition path this pipeline serves. Every
// per-mode wiring decision is a switch on Mode — never an inline
// `if isBacktest` branch.
type Mode int

const (
	// ModeLive: production trading via cmd/omo-core. Wires every
	// subscriber, refresher, and notifier the live path needs.
	ModeLive Mode = iota

	// ModeBacktest: in-process backtest via internal/app/backtest
	// (POST /backtest/run). Wires the deterministic subset; opts in
	// to EntryGated persistence via RunConfig.EmitGatedDiag.
	ModeBacktest

	// ModeReplay: out-of-process replay via cmd/omo-replay. Same
	// deterministic subset as ModeBacktest, with separate flags for
	// progress-event suppression and gated-diag emission.
	ModeReplay
)

// String returns a stable label for logs and error messages.
func (m Mode) String() string {
	switch m {
	case ModeLive:
		return "live"
	case ModeBacktest:
		return "backtest"
	case ModeReplay:
		return "replay"
	default:
		return "unknown"
	}
}

// IsBacktest reports whether the mode is one of the historical-replay
// modes (backtest or replay). Useful for the (rare) decisions that
// genuinely need to distinguish "real-time" from "replay" rather than
// any specific replay variant.
func (m Mode) IsBacktest() bool {
	return m == ModeBacktest || m == ModeReplay
}
