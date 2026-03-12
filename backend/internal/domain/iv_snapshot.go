package domain

import "time"

// IVSnapshot represents a single point-in-time implied volatility reading
// for an underlying symbol. Typically captured once daily at market close.
type IVSnapshot struct {
	Time      time.Time // snapshot timestamp (usually market close)
	Symbol    Symbol    // underlying symbol (e.g., "AAPL")
	ATMIV     float64   // at-the-money implied volatility (average of call + put)
	ATMStrike float64   // strike price nearest to spot
	SpotPrice float64   // underlying spot price at snapshot time
	CallIV    float64   // ATM call implied volatility
	PutIV     float64   // ATM put implied volatility
}

// IVStats holds computed IV rank and percentile statistics for a symbol.
type IVStats struct {
	Symbol       Symbol
	CurrentIV    float64 // latest ATM IV
	IVRank       float64 // (current - 52w low) / (52w high - 52w low), range [0, 1]
	IVPercentile float64 // fraction of days in lookback where IV < current, range [0, 1]
	High52W      float64 // highest ATM IV in the last 252 trading days
	Low52W       float64 // lowest ATM IV in the last 252 trading days
	LookbackDays int     // number of data points in the lookback window
}
