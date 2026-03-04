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
}
