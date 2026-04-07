package gate

import "context"

type exposureGate struct {
	checker ExposureChecker
}

func newExposureGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &exposureGate{checker: deps.ExposureGuard}, nil
}

func (g *exposureGate) Name() string { return "exposure_guard" }

func (g *exposureGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil
	}
	if g.checker == nil {
		return nil
	}
	if err := g.checker.Check(ctx, gctx.Intent); err != nil {
		return &GateResult{GateName: "exposure_guard", Reason: err.Error()}
	}
	return nil
}
