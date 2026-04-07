package gate

import "context"

type tradingWindowGate struct {
	checker TradingWindowChecker
}

func newTradingWindowGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &tradingWindowGate{checker: deps.TradingWindowGuard}, nil
}

func (g *tradingWindowGate) Name() string { return "trading_window" }

func (g *tradingWindowGate) Check(_ context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	if g.checker == nil {
		return nil
	}
	if err := g.checker.Check(gctx.Intent); err != nil {
		return &GateResult{GateName: "trading_window", Reason: err.Error()}
	}
	return nil
}
