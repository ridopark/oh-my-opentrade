// Package warmup centralizes the indicator-warmup spec used by both
// the live boot path (cmd/omo-core/warmup.go) and the backtest replay
// pipeline (internal/app/backtest/runner.go). Sharing one spec is what
// keeps live and backtest indicator state identical at the same instant.
package warmup

import "github.com/oh-my-opentrade/backend/internal/domain"

// ConvergenceFactor multiplies an indicator's nominal period to give the
// number of bars needed to reach ~98% of steady-state value through
// recursive averaging. Standard heuristic: 4x for EMA family.
const ConvergenceFactor = 4

// Indicator nominal periods. Mirror the constants in
// backend/internal/app/monitor/indicators.go; if those change, update
// here and the test in loader_test.go will pin the relationship.
const (
	emaLongestPeriod = 200 // EMA200 dominates the warmup requirement
	ema1hPeriod      = 50  // 1h timeframe uses EMA50 as its longest
)

// Spec describes a warmup window. One Spec produces consistent indicator
// state at boot in both live and backtest contexts.
type Spec struct {
	// Required holds the bar count the loader must produce per timeframe
	// after RTH filtering and truncation. Already includes ConvergenceFactor.
	Required map[domain.Timeframe]int

	// RTHFilter, when true, drops pre-market and post-market bars on
	// intraday timeframes (1m, 5m). Always false for crypto. Always true
	// for US equities to keep indicator state aligned with the regime in
	// which positions actually clear.
	RTHFilter bool
}

// EquitySpec returns the canonical warmup spec for US equity strategies.
func EquitySpec() Spec {
	return Spec{
		Required: map[domain.Timeframe]int{
			"1m": emaLongestPeriod * ConvergenceFactor, // 800
			"5m": emaLongestPeriod * ConvergenceFactor, // 800
			"1h": ema1hPeriod * ConvergenceFactor,      // 200
			"1d": emaLongestPeriod * ConvergenceFactor, // 800
		},
		RTHFilter: true,
	}
}

// CryptoSpec returns the warmup spec for 24/7 crypto symbols. No RTH filter
// since crypto markets do not have a regular trading session.
func CryptoSpec() Spec {
	return Spec{
		Required: map[domain.Timeframe]int{
			"1m": emaLongestPeriod * ConvergenceFactor,
			"5m": emaLongestPeriod * ConvergenceFactor,
			"1h": ema1hPeriod * ConvergenceFactor,
			"1d": emaLongestPeriod * ConvergenceFactor,
		},
		RTHFilter: false,
	}
}
