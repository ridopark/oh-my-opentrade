package gate

import (
	"context"
	"fmt"
)

// minATRPctGate blocks setups on symbols where the daily ATR as a
// percentage of price is below a minimum threshold. A minPct of 0
// disables the gate.
type minATRPctGate struct {
	minPct float64
}

func (g *minATRPctGate) Name() string { return "min_atr_pct" }

func (g *minATRPctGate) Check(_ context.Context, gctx *MonitorGateContext) *GateResult {
	if g.minPct <= 0 {
		return nil
	}
	dailyATR := gctx.Snapshot.HTFDailyATR()
	if dailyATR <= 0 || gctx.Bar.Close <= 0 {
		return nil
	}
	atrPct := dailyATR / gctx.Bar.Close * 100
	if atrPct < g.minPct {
		return &GateResult{
			GateName: "min_atr_pct",
			Reason:   fmt.Sprintf("ATR%% %.2f below minimum %.2f", atrPct, g.minPct),
		}
	}
	return nil
}

func newMinATRPctGate(params map[string]any, _ *GateDeps) (MonitorGate, error) {
	minPct := extractFloat64(params, "min_pct", 0)
	return &minATRPctGate{minPct: minPct}, nil
}
