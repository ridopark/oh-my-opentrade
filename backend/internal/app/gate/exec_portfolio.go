package gate

import "context"

type portfolioGate struct {
	checker PortfolioChecker
}

func newPortfolioGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &portfolioGate{checker: deps.PortfolioGuard}, nil
}

func (g *portfolioGate) Name() string { return "portfolio_guard" }

func (g *portfolioGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	if g.checker == nil {
		return nil
	}
	if err := g.checker.Check(ctx, gctx.Intent); err != nil {
		return &GateResult{GateName: "portfolio_guard", Reason: err.Error()}
	}
	return nil
}
