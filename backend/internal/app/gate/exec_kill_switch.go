package gate

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/app/risk"
)

// KillSwitchChecker reports the current kill switch state. Implemented by
// risk.DailyLossBreaker so the gate can read the atomic state field without
// importing the risk package's full interface surface.
type KillSwitchChecker interface {
	State() risk.KillSwitchState
}

// killSwitchGate enforces the Sprint 4 3-state kill switch:
//
//	ACTIVE   — everything passes.
//	REDUCING — exits pass, new entries rejected.
//	HALTED   — everything rejected (including exits).
//
// Unlike portfolio_heat / sector_exposure / directional_bias, this gate does
// NOT short-circuit on Direction.IsExit() at the top: in HALTED mode even
// exits must be blocked (operator has declared a full shutdown).
type killSwitchGate struct {
	checker KillSwitchChecker
}

func newKillSwitchGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &killSwitchGate{checker: deps.KillSwitchGuard}, nil
}

func (g *killSwitchGate) Name() string { return "kill_switch" }

func (g *killSwitchGate) Check(_ context.Context, gctx *ExecutionGateContext) *GateResult {
	if g.checker == nil {
		return nil // nil guard = disabled
	}
	switch g.checker.State() {
	case risk.KillSwitchActive:
		return nil
	case risk.KillSwitchReducing:
		if gctx.Intent.Direction.IsExit() {
			return nil
		}
		return &GateResult{
			GateName: "kill_switch",
			Reason:   "kill switch REDUCING: new entries blocked, exits allowed",
		}
	case risk.KillSwitchHalted:
		return &GateResult{
			GateName: "kill_switch",
			Reason:   "kill switch HALTED: all orders blocked",
		}
	}
	return nil
}
