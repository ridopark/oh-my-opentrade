package gate

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// RegTChecker validates the Reg-T 50% initial margin requirement against
// the account's effective buying power. Implemented by risk.RegTCheck.
type RegTChecker interface {
	Check(ctx context.Context, intent domain.OrderIntent) error
}

type regTGate struct {
	checker RegTChecker
}

func newRegTGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &regTGate{checker: deps.RegTGuard}, nil
}

func (g *regTGate) Name() string { return "reg_t_guard" }

func (g *regTGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if g.checker == nil {
		return nil
	}
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	if err := g.checker.Check(ctx, gctx.Intent); err != nil {
		return &GateResult{GateName: "reg_t_guard", Reason: err.Error()}
	}
	return nil
}
