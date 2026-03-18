package sweep

import (
	"math"
	"sort"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/backtest"
)

type ParamRange struct {
	Key  string  `json:"key"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Step float64 `json:"step"`
}

type SweepConfig struct {
	StrategyID     string
	Ranges         []ParamRange
	TargetMetric   string
	BacktestConfig backtest.RunConfig
	MaxConcurrency int
}

type SweepRunResult struct {
	Index    int             `json:"index"`
	Params   map[string]any  `json:"params"`
	Metrics  backtest.Result `json:"metrics"`
	Duration time.Duration   `json:"duration_ms"`
}

type SweepResult struct {
	Config        SweepConfig      `json:"-"`
	Runs          []SweepRunResult `json:"runs"`
	BestIndex     int              `json:"best_index"`
	TotalDuration time.Duration    `json:"total_duration_ms"`
}

func GenerateGrid(ranges []ParamRange) []map[string]any {
	if len(ranges) == 0 {
		return []map[string]any{{}}
	}

	valueSets := make([][]float64, len(ranges))
	for i, r := range ranges {
		if r.Step <= 0 {
			valueSets[i] = []float64{r.Min}
			continue
		}
		var vals []float64
		for v := r.Min; v <= r.Max+r.Step*0.001; v += r.Step {
			vals = append(vals, math.Round(v*1e6)/1e6)
		}
		valueSets[i] = vals
	}

	total := TotalRuns(ranges)
	grid := make([]map[string]any, 0, total)

	indices := make([]int, len(ranges))
	for {
		combo := make(map[string]any, len(ranges))
		for i, r := range ranges {
			combo[r.Key] = valueSets[i][indices[i]]
		}
		grid = append(grid, combo)

		carry := true
		for i := len(ranges) - 1; i >= 0 && carry; i-- {
			indices[i]++
			if indices[i] < len(valueSets[i]) {
				carry = false
			} else {
				indices[i] = 0
			}
		}
		if carry {
			break
		}
	}

	return grid
}

func TotalRuns(ranges []ParamRange) int {
	if len(ranges) == 0 {
		return 1
	}
	total := 1
	for _, r := range ranges {
		if r.Step <= 0 {
			continue
		}
		n := int(math.Floor((r.Max-r.Min)/r.Step)) + 1
		if n < 1 {
			n = 1
		}
		total *= n
	}
	return total
}

func RankRuns(runs []SweepRunResult, metric string, ascending bool) []SweepRunResult {
	ranked := make([]SweepRunResult, len(runs))
	copy(ranked, runs)

	sort.SliceStable(ranked, func(i, j int) bool {
		vi := metricValue(ranked[i].Metrics, metric)
		vj := metricValue(ranked[j].Metrics, metric)
		if ascending {
			return vi < vj
		}
		return vi > vj
	})

	return ranked
}

func metricValue(r backtest.Result, metric string) float64 {
	switch metric {
	case "sharpe_ratio":
		return r.SharpeRatio
	case "profit_factor":
		return r.ProfitFactor
	case "total_pnl":
		return r.TotalPnL
	case "win_rate_pct":
		return r.WinRate
	case "max_drawdown_pct":
		return r.MaxDrawdown
	case "trade_count":
		return float64(r.TradeCount)
	default:
		return r.SharpeRatio
	}
}
