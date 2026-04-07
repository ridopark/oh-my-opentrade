package gate

import "context"

type buyingPowerGate struct {
	checker BuyingPowerChecker
}

func newBuyingPowerGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &buyingPowerGate{checker: deps.BuyingPowerGuard}, nil
}

func (g *buyingPowerGate) Name() string { return "buying_power_guard" }

func (g *buyingPowerGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	if g.checker == nil {
		return nil
	}
	if err := g.checker.Check(ctx, gctx.Intent); err != nil {
		return &GateResult{GateName: "buying_power_guard", Reason: err.Error()}
	}
	return nil
}
