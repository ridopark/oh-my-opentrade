package domain_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestHasNegativeExpectancy(t *testing.T) {
	tests := []struct {
		name      string
		summary   domain.StrategyPerformanceSummary
		regime    domain.RegimeType
		minTrades int
		want      bool
	}{
		{
			name: "overall positive expectancy, no per-regime data",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 50,
					Expectancy: 12.5,
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      false,
		},
		{
			name: "overall negative expectancy with enough trades",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 30,
					Expectancy: -5.0,
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      true,
		},
		{
			name: "overall negative expectancy but fewer than minTrades",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 5,
					Expectancy: -20.0,
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      false,
		},
		{
			name: "per-regime match with negative expectancy",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 100,
					Expectancy: 10.0,
				},
				ByRegime: []domain.StrategyRegimeStats{
					{Regime: domain.RegimeBalance, TradeCount: 20, Expectancy: 5.0},
					{Regime: domain.RegimeTrend, TradeCount: 25, Expectancy: -8.0},
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      true,
		},
		{
			name: "per-regime match with positive expectancy",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 100,
					Expectancy: -3.0,
				},
				ByRegime: []domain.StrategyRegimeStats{
					{Regime: domain.RegimeTrend, TradeCount: 40, Expectancy: 15.0},
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      false,
		},
		{
			name: "per-regime match but below minTrades falls back to overall negative",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 50,
					Expectancy: -2.0,
				},
				ByRegime: []domain.StrategyRegimeStats{
					{Regime: domain.RegimeTrend, TradeCount: 3, Expectancy: -100.0},
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      true,
		},
		{
			name: "per-regime match but below minTrades falls back to overall positive",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 50,
					Expectancy: 7.0,
				},
				ByRegime: []domain.StrategyRegimeStats{
					{Regime: domain.RegimeTrend, TradeCount: 3, Expectancy: -100.0},
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      false,
		},
		{
			name: "no matching regime falls back to overall",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 40,
					Expectancy: -6.0,
				},
				ByRegime: []domain.StrategyRegimeStats{
					{Regime: domain.RegimeBalance, TradeCount: 20, Expectancy: 10.0},
					{Regime: domain.RegimeReversal, TradeCount: 15, Expectancy: 3.0},
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      true,
		},
		{
			name: "empty ByRegime slice uses overall",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 20,
					Expectancy: -1.5,
				},
				ByRegime: []domain.StrategyRegimeStats{},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      true,
		},
		{
			name: "zero trades everywhere",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "new_strat",
				Symbol:   "ETH/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 0,
					Expectancy: 0,
				},
				ByRegime: []domain.StrategyRegimeStats{
					{Regime: domain.RegimeTrend, TradeCount: 0, Expectancy: 0},
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      false,
		},
		{
			name: "exactly at minTrades boundary with negative expectancy",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 10,
					Expectancy: -0.01,
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      true,
		},
		{
			name: "exactly at minTrades boundary in per-regime",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 100,
					Expectancy: 50.0,
				},
				ByRegime: []domain.StrategyRegimeStats{
					{Regime: domain.RegimeBalance, TradeCount: 10, Expectancy: -3.0},
				},
			},
			regime:    domain.RegimeBalance,
			minTrades: 10,
			want:      true,
		},
		{
			name: "BySymbol present does not affect result",
			summary: domain.StrategyPerformanceSummary{
				Strategy: "momentum",
				Symbol:   "BTC/USD",
				Overall: domain.StrategyRegimeStats{
					TradeCount: 30,
					Expectancy: 10.0,
				},
				BySymbol: &domain.StrategyRegimeStats{
					Strategy:   "momentum",
					Symbol:     "BTC/USD",
					Period:     24 * time.Hour,
					TradeCount: 25,
					Expectancy: -50.0,
				},
				ByRegime: []domain.StrategyRegimeStats{
					{Regime: domain.RegimeTrend, TradeCount: 15, Expectancy: 5.0},
				},
			},
			regime:    domain.RegimeTrend,
			minTrades: 10,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.summary.HasNegativeExpectancy(tt.regime, tt.minTrades)
			assert.Equal(t, tt.want, got)
		})
	}
}
