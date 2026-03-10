package domain

import "math"

// PerformanceSummary holds all computed KPIs for the performance dashboard.
type PerformanceSummary struct {
	TotalPnL       float64  `json:"total_pnl"`
	RealizedPnL    float64  `json:"realized_pnl"`
	UnrealizedPnL  float64  `json:"unrealized_pnl"`
	NumTrades      int      `json:"num_trades"`
	WinningDays    int      `json:"winning_days"`
	LosingDays     int      `json:"losing_days"`
	WinRate        *float64 `json:"win_rate"`
	Sharpe         *float64 `json:"sharpe"`
	Sortino        *float64 `json:"sortino"`
	MaxDrawdownPct float64  `json:"max_drawdown_pct"`
	GrossProfit    float64  `json:"gross_profit"`
	GrossLoss      float64  `json:"gross_loss"`
	ProfitFactor   *float64 `json:"profit_factor"`
	Expectancy     *float64 `json:"expectancy"`
	CAGR           *float64 `json:"cagr"`
}

// DrawdownPoint represents a single point on the drawdown (underwater) curve.
type DrawdownPoint struct {
	Time        string  `json:"time"`
	DrawdownPct float64 `json:"drawdown_pct"`
}

// ComputeSummary aggregates daily P&L rows into a PerformanceSummary.
func ComputeSummary(daily []DailyPnL, maxDD float64, sharpe, sortino *float64, equityPts []EquityPoint) PerformanceSummary {
	var s PerformanceSummary
	s.MaxDrawdownPct = maxDD
	s.Sharpe = sharpe
	s.Sortino = sortino

	for _, d := range daily {
		s.RealizedPnL += d.RealizedPnL
		s.UnrealizedPnL += d.UnrealizedPnL
		s.NumTrades += d.TradeCount

		if d.RealizedPnL > 0 {
			s.WinningDays++
			s.GrossProfit += d.RealizedPnL
		} else if d.RealizedPnL < 0 {
			s.LosingDays++
			s.GrossLoss += math.Abs(d.RealizedPnL)
		}
	}
	s.TotalPnL = s.RealizedPnL + s.UnrealizedPnL

	totalDays := s.WinningDays + s.LosingDays
	if totalDays > 0 {
		wr := float64(s.WinningDays) / float64(totalDays)
		s.WinRate = &wr
	}
	if s.GrossLoss > 0 {
		pf := s.GrossProfit / s.GrossLoss
		s.ProfitFactor = &pf
	}

	s.Expectancy = ComputeExpectancy(s.WinRate, s.GrossProfit, s.GrossLoss, s.WinningDays, s.LosingDays)
	s.CAGR = ComputeCAGR(equityPts)

	return s
}

// ComputeExpectancy calculates average expected return per trade.
// Expectancy = (WinRate × AvgWin) - (LossRate × AvgLoss)
func ComputeExpectancy(winRate *float64, grossProfit, grossLoss float64, winCount, lossCount int) *float64 {
	if winRate == nil || (winCount+lossCount) == 0 {
		return nil
	}
	var avgWin, avgLoss float64
	if winCount > 0 {
		avgWin = grossProfit / float64(winCount)
	}
	if lossCount > 0 {
		avgLoss = grossLoss / float64(lossCount)
	}
	wr := *winRate
	exp := (wr * avgWin) - ((1 - wr) * avgLoss)
	return &exp
}

// ComputeCAGR calculates the Compound Annual Growth Rate from equity points.
// CAGR = (EndEquity / StartEquity)^(365/days) - 1
// Returns nil if fewer than 2 points or time span < 1 day.
func ComputeCAGR(pts []EquityPoint) *float64 {
	if len(pts) < 2 {
		return nil
	}
	start := pts[0]
	end := pts[len(pts)-1]
	if start.Equity <= 0 {
		return nil
	}

	days := end.Time.Sub(start.Time).Hours() / 24
	if days < 1 {
		return nil
	}

	ratio := end.Equity / start.Equity
	cagr := math.Pow(ratio, 365.0/days) - 1
	return &cagr
}

// ComputeDrawdownCurve generates the drawdown (underwater) curve from equity points.
// Each point represents the percentage drawdown from the running peak.
func ComputeDrawdownCurve(pts []EquityPoint) []DrawdownPoint {
	if len(pts) == 0 {
		return nil
	}
	result := make([]DrawdownPoint, 0, len(pts))
	peak := pts[0].Equity
	for _, pt := range pts {
		if pt.Equity > peak {
			peak = pt.Equity
		}
		var dd float64
		if peak > 0 {
			dd = (peak - pt.Equity) / peak
		}
		result = append(result, DrawdownPoint{
			Time:        pt.Time.UTC().Format("2006-01-02T15:04:05Z"),
			DrawdownPct: dd,
		})
	}
	return result
}

// ComputeSortino calculates the annualized Sortino ratio from daily equity returns.
// Sortino = sqrt(252) * mean(returns) / downside_deviation
// Returns nil if insufficient data or zero downside deviation.
func ComputeSortino(dailyReturns []float64) *float64 {
	if len(dailyReturns) < 2 {
		return nil
	}

	var sum float64
	for _, r := range dailyReturns {
		sum += r
	}
	mean := sum / float64(len(dailyReturns))

	var downsideSum float64
	var downsideCount int
	for _, r := range dailyReturns {
		if r < 0 {
			downsideSum += r * r
			downsideCount++
		}
	}
	if downsideCount == 0 {
		return nil
	}

	downsideDev := math.Sqrt(downsideSum / float64(len(dailyReturns)))
	if downsideDev == 0 {
		return nil
	}

	sortino := math.Sqrt(252) * mean / downsideDev
	return &sortino
}
