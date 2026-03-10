package domain_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeExpectancy_WinningSystem(t *testing.T) {
	wr := 0.6
	// 6 wins averaging $200 each = $1200 gross profit
	// 4 losses averaging $100 each = $400 gross loss
	exp := domain.ComputeExpectancy(&wr, 1200, 400, 6, 4)
	require.NotNil(t, exp)
	// (0.6 * 200) - (0.4 * 100) = 120 - 40 = 80
	assert.InDelta(t, 80.0, *exp, 0.01)
}

func TestComputeExpectancy_LosingSystem(t *testing.T) {
	wr := 0.3
	// 3 wins @ $100 avg = $300, 7 losses @ $200 avg = $1400
	exp := domain.ComputeExpectancy(&wr, 300, 1400, 3, 7)
	require.NotNil(t, exp)
	// (0.3 * 100) - (0.7 * 200) = 30 - 140 = -110
	assert.InDelta(t, -110.0, *exp, 0.01)
}

func TestComputeExpectancy_NilWinRate(t *testing.T) {
	exp := domain.ComputeExpectancy(nil, 100, 50, 0, 0)
	assert.Nil(t, exp)
}

func TestComputeExpectancy_NoTrades(t *testing.T) {
	wr := 0.0
	exp := domain.ComputeExpectancy(&wr, 0, 0, 0, 0)
	assert.Nil(t, exp)
}

func TestComputeCAGR_OneYear(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pts := []domain.EquityPoint{
		{Time: start, Equity: 100000},
		{Time: end, Equity: 110000},
	}
	cagr := domain.ComputeCAGR(pts)
	require.NotNil(t, cagr)
	// 10% return over 365 days → CAGR ≈ 10%
	assert.InDelta(t, 0.10, *cagr, 0.01)
}

func TestComputeCAGR_HalfYear(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	pts := []domain.EquityPoint{
		{Time: start, Equity: 100000},
		{Time: end, Equity: 105000},
	}
	cagr := domain.ComputeCAGR(pts)
	require.NotNil(t, cagr)
	// 5% in ~181 days → annualized ≈ (1.05)^(365/181) - 1 ≈ 10.2%
	assert.InDelta(t, 0.102, *cagr, 0.02)
}

func TestComputeCAGR_InsufficientData(t *testing.T) {
	assert.Nil(t, domain.ComputeCAGR(nil))
	assert.Nil(t, domain.ComputeCAGR([]domain.EquityPoint{{Equity: 100000}}))
}

func TestComputeCAGR_ZeroStartEquity(t *testing.T) {
	pts := []domain.EquityPoint{
		{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Equity: 0},
		{Time: time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC), Equity: 100},
	}
	assert.Nil(t, domain.ComputeCAGR(pts))
}

func TestComputeDrawdownCurve_RisingThenFalling(t *testing.T) {
	pts := []domain.EquityPoint{
		{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Equity: 100},
		{Time: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), Equity: 110},
		{Time: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), Equity: 100},
		{Time: time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC), Equity: 120},
	}
	dd := domain.ComputeDrawdownCurve(pts)
	require.Len(t, dd, 4)
	assert.InDelta(t, 0.0, dd[0].DrawdownPct, 0.001)
	assert.InDelta(t, 0.0, dd[1].DrawdownPct, 0.001)
	// Peak is 110, current is 100 → dd = 10/110 ≈ 0.0909
	assert.InDelta(t, 0.0909, dd[2].DrawdownPct, 0.001)
	assert.InDelta(t, 0.0, dd[3].DrawdownPct, 0.001)
}

func TestComputeDrawdownCurve_Empty(t *testing.T) {
	assert.Nil(t, domain.ComputeDrawdownCurve(nil))
}

func TestComputeSortino_PositiveReturns(t *testing.T) {
	// Mix of positive and negative returns
	returns := []float64{0.01, -0.005, 0.02, -0.01, 0.015, 0.005, -0.003, 0.01}
	sortino := domain.ComputeSortino(returns)
	require.NotNil(t, sortino)
	assert.Greater(t, *sortino, 0.0)
}

func TestComputeSortino_AllPositive(t *testing.T) {
	// All positive returns → zero downside deviation → nil
	returns := []float64{0.01, 0.02, 0.015, 0.005}
	assert.Nil(t, domain.ComputeSortino(returns))
}

func TestComputeSortino_InsufficientData(t *testing.T) {
	assert.Nil(t, domain.ComputeSortino(nil))
	assert.Nil(t, domain.ComputeSortino([]float64{0.01}))
}

func TestComputeSummary_IntegrationWithAllKPIs(t *testing.T) {
	daily := []domain.DailyPnL{
		{RealizedPnL: 500, TradeCount: 5},
		{RealizedPnL: -200, TradeCount: 3},
		{RealizedPnL: 300, TradeCount: 4},
		{RealizedPnL: -100, TradeCount: 2},
		{RealizedPnL: 400, TradeCount: 6},
	}
	sharpe := 1.5
	sortino := 2.0
	maxDD := 0.05

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	pts := []domain.EquityPoint{
		{Time: start, Equity: 100000},
		{Time: start.AddDate(0, 0, 30), Equity: 100900},
	}

	s := domain.ComputeSummary(daily, maxDD, &sharpe, &sortino, pts)

	assert.InDelta(t, 900.0, s.RealizedPnL, 0.01)
	assert.Equal(t, 20, s.NumTrades)
	assert.Equal(t, 3, s.WinningDays)
	assert.Equal(t, 2, s.LosingDays)
	assert.InDelta(t, 0.6, *s.WinRate, 0.01)
	assert.InDelta(t, 1.5, *s.Sharpe, 0.01)
	assert.InDelta(t, 2.0, *s.Sortino, 0.01)
	assert.InDelta(t, 0.05, s.MaxDrawdownPct, 0.01)
	require.NotNil(t, s.ProfitFactor)
	// GrossProfit = 1200, GrossLoss = 300 → PF = 4.0
	assert.InDelta(t, 4.0, *s.ProfitFactor, 0.01)
	require.NotNil(t, s.Expectancy)
	require.NotNil(t, s.CAGR)
}
