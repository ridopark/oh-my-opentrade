package gate

import (
	"context"
	"fmt"
)

// regimeGate blocks setups when the resolved market regime is not in the
// allowed set. An empty allowed set disables the gate. The gate prefers
// the anchor-timeframe regime over the default 1m regime.
type regimeGate struct {
	allowedRegimes map[string]bool
}

func (g *regimeGate) Name() string { return "regime" }

func (g *regimeGate) Check(_ context.Context, gctx *MonitorGateContext) *GateResult {
	if len(g.allowedRegimes) == 0 {
		return nil
	}

	// Resolve regime: prefer anchor timeframe, fall back to 1m.
	regime := gctx.Regime
	if gctx.ORBTimeframe != "" && gctx.ORBTimeframe != "1m" {
		symTF := string(gctx.Setup.Symbol) + ":" + string(gctx.ORBTimeframe)
		if ar, ok := gctx.AnchorRegimes[symTF]; ok && ar.Type != "" {
			regime = ar
		}
	}

	regimeStr := string(regime.Type)
	if !g.allowedRegimes[regimeStr] {
		return &GateResult{
			GateName: "regime",
			Reason:   fmt.Sprintf("regime %s not in allowed set", regimeStr),
		}
	}
	return nil
}

func newRegimeGate(params map[string]any, _ *GateDeps) (MonitorGate, error) {
	allowed := extractStringSlice(params, "allowed")
	m := make(map[string]bool, len(allowed))
	for _, r := range allowed {
		m[r] = true
	}
	return &regimeGate{allowedRegimes: m}, nil
}
