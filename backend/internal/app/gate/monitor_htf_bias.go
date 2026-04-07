package gate

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// htfBiasGate blocks longs when the daily bias is BEARISH and shorts when
// the daily bias is BULLISH. NEUTRAL or missing bias always passes.
type htfBiasGate struct{}

func (g *htfBiasGate) Name() string { return "htf_bias" }

func (g *htfBiasGate) Check(_ context.Context, gctx *MonitorGateContext) *GateResult {
	htf, ok := gctx.Snapshot.HTF[domain.Timeframe("1d")]
	if !ok || htf.Bias == "" || htf.Bias == "NEUTRAL" {
		return nil
	}

	blocked := false
	if gctx.Setup.Direction == domain.DirectionLong && htf.Bias == "BEARISH" {
		blocked = true
	} else if gctx.Setup.Direction == domain.DirectionShort && htf.Bias == "BULLISH" {
		blocked = true
	}

	if blocked {
		return &GateResult{
			GateName: "htf_bias",
			Reason:   fmt.Sprintf("direction %s conflicts with daily bias %s", gctx.Setup.Direction, htf.Bias),
		}
	}
	return nil
}

func newHTFBiasGate(_ map[string]any, _ *GateDeps) (MonitorGate, error) {
	return &htfBiasGate{}, nil
}
