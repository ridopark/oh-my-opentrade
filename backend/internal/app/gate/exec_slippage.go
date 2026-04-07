package gate

import "context"

type slippageGate struct {
	checker SlippageChecker
}

func newSlippageGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &slippageGate{checker: deps.SlippageGuard}, nil
}

func (g *slippageGate) Name() string { return "slippage_guard" }

func (g *slippageGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	if g.checker == nil {
		return nil
	}
	if err := g.checker.Check(ctx, gctx.Intent); err != nil {
		return &GateResult{GateName: "slippage_guard", Reason: err.Error()}
	}
	return nil
}
