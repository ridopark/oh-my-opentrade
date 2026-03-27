package domain

import "time"

// HistoricalOptionChainRow represents a single option contract snapshot from
// historical data (e.g., DoltHub). Each row corresponds to one strike/expiry/right
// combination for a given underlying on a given date.
type HistoricalOptionChainRow struct {
	Date       time.Time
	Symbol     Symbol      // underlying symbol (e.g., "HIMS")
	Expiration time.Time
	Strike     float64
	Right      OptionRight // CALL or PUT
	Bid        float64
	Ask        float64
	IV         float64 // implied volatility as decimal (e.g., 0.35 = 35%)
	Delta      float64
	Gamma      float64
	Theta      float64
	Vega       float64
	Rho        float64
}

// Mid returns the bid-ask midpoint price.
func (r HistoricalOptionChainRow) Mid() float64 {
	if r.Bid > 0 && r.Ask > 0 {
		return (r.Bid + r.Ask) / 2
	}
	if r.Ask > 0 {
		return r.Ask
	}
	return r.Bid
}
