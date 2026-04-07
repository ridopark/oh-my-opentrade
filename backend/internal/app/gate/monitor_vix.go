package gate

import (
	"context"
	"fmt"
)

// vixGate blocks setups when the VIX exceeds a configurable threshold.
// A skipAbove of 0 disables the gate.
type vixGate struct {
	skipAbove float64
}

func (g *vixGate) Name() string { return "vix" }

func (g *vixGate) Check(_ context.Context, gctx *MonitorGateContext) *GateResult {
	if g.skipAbove <= 0 {
		return nil // disabled
	}
	if gctx.VIXLevel <= 0 {
		return nil // unknown VIX
	}
	if gctx.VIXLevel > g.skipAbove {
		return &GateResult{
			GateName: "vix",
			Reason:   fmt.Sprintf("VIX %.1f exceeds threshold %.1f", gctx.VIXLevel, g.skipAbove),
		}
	}
	return nil
}

func newVIXGate(params map[string]any, _ *GateDeps) (MonitorGate, error) {
	skipAbove := extractFloat64(params, "skip_above", 0)
	return &vixGate{skipAbove: skipAbove}, nil
}
