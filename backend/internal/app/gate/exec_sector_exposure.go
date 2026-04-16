package gate

import "context"

// sectorExposureGate is an execution gate that refuses new entries whose
// projected sector- or industry-notional share of account equity would
// exceed configured caps.
//
// The heavy lifting lives in app/risk/SectorExposure; this gate is a thin
// adapter that plugs the checker into the ExecutionGateChain following the
// same pattern as portfolioHeatGate (see exec_portfolio_heat.go).
type sectorExposureGate struct {
	checker SectorExposureChecker
}

func newSectorExposureGate(_ map[string]any, deps *ExecutionGateDeps) (ExecutionGate, error) {
	return &sectorExposureGate{checker: deps.SectorExposureGuard}, nil
}

func (g *sectorExposureGate) Name() string { return "sector_exposure_guard" }

func (g *sectorExposureGate) Check(ctx context.Context, gctx *ExecutionGateContext) *GateResult {
	if gctx.Intent.Direction.IsExit() {
		return nil // exits always bypass — they reduce concentration
	}
	if g.checker == nil {
		return nil // nil guard = disabled
	}
	if err := g.checker.Check(ctx, gctx.Intent); err != nil {
		return &GateResult{GateName: "sector_exposure_guard", Reason: err.Error()}
	}
	return nil
}
