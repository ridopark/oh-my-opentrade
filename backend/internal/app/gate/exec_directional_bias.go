package gate

import "context"

// directionalBiasGate is an execution gate that refuses new entries whose
// projected net directional exposure (|Σ long − Σ short| / equity) would
// exceed the configured cap AND would push the portfolio further from
// neutral than it already is. Bias-reducing intents always pass.
//
// The heavy lifting lives in app/risk/DirectionalBias; this gate is a thin
// adapter that plugs the checker into the ExecutionGateChain following the
// same pattern as sectorExposureGate (see exec_sector_exposure.go).
type directionalBiasGate struct {
	checker DirectionalBiasChecker
}

func newDirectionalBiasGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &directionalBiasGate{checker: deps.DirectionalBiasGuard}, nil
}

func (g *directionalBiasGate) Name() string { return "directional_bias_guard" }

func (g *directionalBiasGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	return checkerGate(ctx, "directional_bias_guard", g.checker, gctx.Intent)
}
