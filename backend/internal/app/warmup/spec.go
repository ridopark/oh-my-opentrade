// Package warmup centralizes the indicator-warmup spec used by both
// the live boot path and the backtest replay pipeline. Sharing one spec
// is what keeps live and backtest indicator state identical at the same
// instant.
package warmup

import "github.com/oh-my-opentrade/backend/internal/domain"

// 4x an indicator's period reaches ~98% of steady-state through recursive
// averaging — the heuristic for EMA-family convergence.
const convergenceFactor = 4

// Mirror the constants in backend/internal/app/monitor/indicators.go.
const (
	emaLongestPeriod = 200
	ema1hPeriod      = 50
	// 15m anchors mid-horizon regime classification (ADX, EMA50 slope, BB
	// bandwidth) — none of which need EMA200 convergence. EMA50 × 4 = 200
	// bars matches the 1h spec's reasoning and gives ADX/slope windows
	// plenty of headroom.
	ema15mPeriod = 50
)

type Spec struct {
	Required  map[domain.Timeframe]int
	RTHFilter bool
}

func defaultRequired() map[domain.Timeframe]int {
	return map[domain.Timeframe]int{
		"1m":  emaLongestPeriod * convergenceFactor,
		"5m":  emaLongestPeriod * convergenceFactor,
		"15m": ema15mPeriod * convergenceFactor,
		"1h":  ema1hPeriod * convergenceFactor,
		"1d":  emaLongestPeriod * convergenceFactor,
	}
}

func EquitySpec() Spec {
	return Spec{Required: defaultRequired(), RTHFilter: true}
}

func CryptoSpec() Spec {
	return Spec{Required: defaultRequired(), RTHFilter: false}
}
