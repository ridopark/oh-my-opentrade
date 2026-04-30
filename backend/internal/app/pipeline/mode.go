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
package pipeline

// Mode identifies which composition path this pipeline serves. Every
// per-mode wiring decision is a switch on Mode — never an inline
// `if isBacktest` branch. Defined as a string type to match the
// codebase convention for enum-shaped values (see EnvMode, Direction,
// Venue, AssetClass in internal/domain/value.go).
type Mode string

const (
	// ModeLive: production trading via cmd/omo-core. Wires every
	// subscriber, refresher, and notifier the live path needs.
	ModeLive Mode = "live"

	// ModeBacktest: in-process backtest via internal/app/backtest
	// (POST /backtest/run). Wires the deterministic subset; opts in
	// to EntryGated persistence via RunConfig.EmitGatedDiag.
	ModeBacktest Mode = "backtest"

	// ModeReplay: out-of-process replay via cmd/omo-replay. Same
	// deterministic subset as ModeBacktest, with separate flags for
	// progress-event suppression and gated-diag emission.
	ModeReplay Mode = "replay"
)

func (m Mode) String() string { return string(m) }

// IsBacktest reports whether the mode is one of the historical-replay
// modes (backtest or replay). Useful for the (rare) decisions that
// genuinely need to distinguish "real-time" from "replay" rather than
// any specific replay variant.
func (m Mode) IsBacktest() bool {
	return m == ModeBacktest || m == ModeReplay
}
