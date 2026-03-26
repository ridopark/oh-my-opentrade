package monitor

import "github.com/oh-my-opentrade/backend/internal/domain"

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

	// FVG-based stop-loss (0 = not set, use default stop_bps)
	FVGStop float64 // stop level from FVG far edge / manipulation wick

	// Regime labels for downstream display
	EMARegime    string // EMA-based regime: TREND / BALANCE / REVERSAL
	VIXBucket    string // VIX bucket: LOW_VOL / NORMAL / HIGH_VOL
	MarketContext string // composite: e.g. "NORMAL | NR7 | VWAP+"
}
