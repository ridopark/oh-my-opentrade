package gate

import "context"

type spreadGate struct {
	checker SpreadChecker
}

func newSpreadGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &spreadGate{checker: deps.SpreadGuard}, nil
}

func (g *spreadGate) Name() string { return "spread_guard" }

func (g *spreadGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	if g.checker == nil {
		return nil
	}
	if err := g.checker.Check(ctx, gctx.Intent); err != nil {
		return &GateResult{GateName: "spread_guard", Reason: err.Error()}
	}
	return nil
}
