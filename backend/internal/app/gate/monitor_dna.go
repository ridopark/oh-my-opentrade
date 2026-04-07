package gate

import "context"

// dnaApprovalGate blocks setups when the active DNA version is not approved
// for the strategy. If no checker is configured the gate always passes,
// matching the current inline behavior.
type dnaApprovalGate struct {
	checker DNAGateChecker
}

func (g *dnaApprovalGate) Name() string { return "dna_approval" }

func (g *dnaApprovalGate) Check(ctx context.Context, gctx *MonitorGateContext) *GateResult {
	if g.checker == nil {
		return nil
	}
	approved, err := g.checker.IsDNAApproved(ctx, gctx.StrategyKey)
	if err != nil {
		// Log warning, allow through (matching current behavior).
		return nil
	}
	if !approved {
		return &GateResult{
			GateName: "dna_approval",
			Reason:   "DNA version not approved for " + gctx.StrategyKey,
		}
	}
	return nil
}

func newDNAApprovalGate(_ map[string]any, deps *GateDeps) (MonitorGate, error) {
	if deps == nil || deps.DNAGate == nil {
		return &dnaApprovalGate{}, nil
	}
	return &dnaApprovalGate{checker: deps.DNAGate}, nil
}
