package monitor

import "github.com/oh-my-opentrade/backend/internal/domain"

// RetestQualityInputs holds pre-computed retest metrics for confluence scoring.
type RetestQualityInputs struct {
	BarCount           int
	PullbackDepthPct   float64
	RetestAvgVolume    float64
	BreakoutVolume     float64
	ConfirmBodyRatio   float64
	ConfirmDirectional bool
}

// SetupCondition describes a detected trade entry condition
// including the triggering indicators and current market regime.
type SetupCondition struct {
	Symbol    domain.Symbol
	Timeframe domain.Timeframe
	Direction domain.Direction
	Trigger   string
	Snapshot  domain.IndicatorSnapshot
	Regime    domain.MarketRegime
	// BarClose is the close price of the bar that triggered this setup.
	// Used by the strategy engine as the reference price for limit/stop computation.
	BarClose float64

	ORBHigh    float64
	ORBLow     float64
	RVOL       float64
	Confidence float64
	VIXAdjust  string // "widen_stops" when VIX is elevated but not skip-level

	// Swing-based stop from retest bars (0 = not set, use default stop_bps)
	RetestSwingLow  float64 // lowest low during retest bars (for LONG stop)
	RetestSwingHigh float64 // highest high during retest bars (for SHORT stop)

	// FVG-based stop-loss (0 = not set, use default stop_bps)
	FVGStop float64 // stop level from FVG far edge / manipulation wick

	RetestQuality RetestQualityInputs // populated by ORB tracker for retest quality scoring

	// Regime labels for downstream display
	EMARegime    string // EMA-based regime: TREND / BALANCE / REVERSAL
	VIXBucket    string // VIX bucket: LOW_VOL / NORMAL / HIGH_VOL
	MarketContext string // composite: e.g. "NORMAL | NR7 | VWAP+"
}
