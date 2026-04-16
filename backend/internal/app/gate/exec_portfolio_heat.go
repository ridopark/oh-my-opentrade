package gate

import "context"

// portfolioHeatGate is an execution gate that refuses new entries when the
// aggregate portfolio heat (sum of per-position risk across all open
// positions, plus the proposed intent) would exceed the configured max
// as a fraction of account equity.
//
// The heavy lifting lives in app/risk/PortfolioHeat; this gate is a thin
// adapter that plugs that checker into the ExecutionGateChain following
// the same pattern as portfolioGate (see exec_portfolio.go).
type portfolioHeatGate struct {
	checker PortfolioHeatChecker
}

func newPortfolioHeatGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &portfolioHeatGate{checker: deps.PortfolioHeatGuard}, nil
}

func (g *portfolioHeatGate) Name() string { return "portfolio_heat_guard" }

func (g *portfolioHeatGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil // exits always bypass — they reduce heat
	}
	if g.checker == nil {
		return nil // nil guard = disabled
	}
	if err := g.checker.Check(ctx, gctx.Intent); err != nil {
		return &GateResult{GateName: "portfolio_heat_guard", Reason: err.Error()}
	}
	return nil
}
