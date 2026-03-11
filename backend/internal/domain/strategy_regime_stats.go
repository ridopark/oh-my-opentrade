package domain

import "time"

// StrategyRegimeStats holds aggregate performance metrics for a strategy
// in a specific market regime (or overall when Regime is empty).
// Pure value object — computed by adapters, consumed by debate prompt.
type StrategyRegimeStats struct {
	Strategy   string
	Symbol     string        // empty = all symbols
	Regime     RegimeType    // empty = overall (all regimes)
	Period     time.Duration // lookback window
	TradeCount int
	WinCount   int
	LossCount  int
	WinRate    float64 // 0.0–1.0
	Expectancy float64 // avg $ per trade (can be negative)
	TotalPnL   float64
}

// StrategyPerformanceSummary groups overall + per-regime + per-symbol stats for debate prompt injection.
type StrategyPerformanceSummary struct {
	Strategy string
	Symbol   string
	Overall  StrategyRegimeStats   // aggregated across all regimes
	BySymbol *StrategyRegimeStats  // per-symbol stats (nil when unavailable)
	ByRegime []StrategyRegimeStats // one per regime (TREND, BALANCE, REVERSAL) — empty in Phase A
}

// HasNegativeExpectancy returns true if the given regime (or overall when regime
// is empty) has negative expectancy with at least minTrades data points.
func (s *StrategyPerformanceSummary) HasNegativeExpectancy(regime RegimeType, minTrades int) bool {
	for _, r := range s.ByRegime {
		if r.Regime == regime && r.TradeCount >= minTrades {
			return r.Expectancy < 0
		}
	}
	if s.Overall.TradeCount >= minTrades {
		return s.Overall.Expectancy < 0
	}
	return false
}

// HasNegativeExpectancyForSymbol checks per-symbol stats only. Returns false
// if no symbol-level data exists or insufficient trades — never falls back to
// overall strategy stats, so one bad symbol can't block all others.
func (s *StrategyPerformanceSummary) HasNegativeExpectancyForSymbol(minTrades int) bool {
	if s.BySymbol == nil || s.BySymbol.TradeCount < minTrades {
		return false
	}
	return s.BySymbol.Expectancy < 0
}
