package gate

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// marketTideGate blocks long entries when the reference index (SPY or QQQ)
// is trading below its intraday VWAP, and short entries when above.
// A configurable neutral band (in basis points) allows both directions
// when the index is near VWAP.
type marketTideGate struct {
	tracker        *IndexTideTracker
	neutralBandBps int
}

func (g *marketTideGate) Name() string { return "market_tide" }

func (g *marketTideGate) Check(_ context.Context, gctx *MonitorGateContext) *GateResult {
	if g.tracker == nil {
		return nil
	}

	vwap, lastClose, ready := g.tracker.GetTide(gctx.Setup.Symbol)
	if !ready {
		return nil // warmup incomplete or no reference index
	}

	if vwap <= 0 {
		return nil
	}
	devBps := (lastClose - vwap) / vwap * 10000

	// Within neutral band — allow both directions.
	if devBps >= float64(-g.neutralBandBps) && devBps <= float64(g.neutralBandBps) {
		return nil
	}

	refIndex := ReferenceIndex(gctx.Setup.Symbol)

	// Block longs when ref index below VWAP.
	if gctx.Setup.Direction == domain.DirectionLong && devBps < float64(-g.neutralBandBps) {
		return &GateResult{
			GateName: "market_tide",
			Reason:   fmt.Sprintf("%s below VWAP (%.0f bps), blocking long", refIndex, devBps),
		}
	}

	// Block shorts when ref index above VWAP.
	if gctx.Setup.Direction == domain.DirectionShort && devBps > float64(g.neutralBandBps) {
		return &GateResult{
			GateName: "market_tide",
			Reason:   fmt.Sprintf("%s above VWAP (+%.0f bps), blocking short", refIndex, devBps),
		}
	}

	return nil
}

func newMarketTideGate(params map[string]any, deps *GateDeps) (MonitorGate, error) {
	neutralBandBps := extractInt(params, "neutral_band_bps", 10)
	var tracker *IndexTideTracker
	if deps != nil {
		tracker = deps.TideTracker
	}
	return &marketTideGate{
		tracker:        tracker,
		neutralBandBps: neutralBandBps,
	}, nil
}
