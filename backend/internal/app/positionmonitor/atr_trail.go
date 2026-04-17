package positionmonitor

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// computeATRTrailMult computes the ATR%-percentile-bucketed multiplier
// for an option position's underlying. Called at fill time (once per
// position) and the result is stamped into pos.CustomState so the tick
// loop does not re-compute on every bar.
//
// The implementation is deliberately self-contained (no import of
// internal/app/monitor) so positionmonitor's dependency graph stays
// tight — the daily ATR computation here duplicates 5 lines of
// ComputeDailyATR but avoids a cross-package seam that would otherwise
// pull monitor's full indicator state into this hot path.
//
// Returns 1.0 (the "no-op" multiplier) when:
//   - cfg.Enabled is false
//   - the underlying cannot be resolved (non-OCC symbol)
//   - the repo dependency is nil (backtest / bootstrap without repo)
//   - fewer than MinHistoryDays of daily bars are available
//
// Returns cfg.InsufficientHistoryMultiplier when days ∈ [MinHistoryDays, lookbackDays).
func (s *Service) computeATRTrailMult(ctx context.Context, pos *domain.MonitoredPosition) (mult float64, pctile float64, ok bool) {
	cfg := s.atrTrailCfg
	if !cfg.Enabled {
		return 1.0, 0, false
	}
	if s.repo == nil {
		return 1.0, 0, false
	}
	if len(cfg.TercileMultipliers) != 3 {
		// Misconfigured — quant spec hard-codes three buckets.
		return 1.0, 0, false
	}

	underlying := domain.UnderlyingFromOCC(pos.Symbol)
	if underlying == "" {
		return 1.0, 0, false
	}

	lookback := cfg.ATRLookbackDays
	if pos.AssetClass == domain.AssetClassCrypto && cfg.ATRLookbackDaysCrypto > 0 {
		lookback = cfg.ATRLookbackDaysCrypto
	}
	if lookback <= 0 {
		return 1.0, 0, false
	}

	// Pull ~lookback+ATRPeriod calendar days so we have enough bars post
	// weekend/holiday gaps to fill the rolling ATR window cleanly.
	fetchWindow := time.Duration(lookback+cfg.ATRPeriod+7) * 24 * time.Hour
	end := s.nowFunc()
	start := end.Add(-fetchWindow)

	bars, err := s.repo.GetMarketBars(ctx, underlying, domain.Timeframe("1d"), start, end)
	if err != nil || len(bars) == 0 {
		s.log.Warn().
			Err(err).
			Str("underlying", string(underlying)).
			Msg("atr_trail: daily bars unavailable — falling back to no-op multiplier")
		return 1.0, 0, false
	}

	// Sort bars by time ascending to be robust against repo ordering.
	sort.Slice(bars, func(i, j int) bool { return bars[i].Time.Before(bars[j].Time) })

	days := len(bars)
	if days < cfg.MinHistoryDays {
		// Below the floor: do not attempt any scaling. Quant spec: 1.0.
		return 1.0, 0, false
	}

	// Compute rolling ATR% series. For each window end i >= ATRPeriod, ATR
	// is the mean true range over the last ATRPeriod bars, and ATR% = ATR /
	// close[i]. The series drives the percentile-of-latest calculation.
	atrPct := buildATRPctSeries(bars, cfg.ATRPeriod)
	if len(atrPct) == 0 {
		return 1.0, 0, false
	}

	if days < lookback {
		// Some history, but short of the full window. Quant spec: use
		// InsufficientHistoryMultiplier (default 1.0 = neutral) rather
		// than guess a bucket from sparse data.
		m := cfg.InsufficientHistoryMultiplier
		if m <= 0 {
			m = 1.0
		}
		return m, 0, false
	}

	// Truncate to the last `lookback` daily observations so the percentile
	// reflects only the requested rolling window.
	if len(atrPct) > lookback {
		atrPct = atrPct[len(atrPct)-lookback:]
	}
	latest := atrPct[len(atrPct)-1]

	pct := percentileRank(atrPct, latest)
	bucket := classifyTercile(pct, cfg.TercileLowPctile, cfg.TercileHighPctile)
	return cfg.TercileMultipliers[bucket], pct, true
}

// buildATRPctSeries returns a slice of ATR% values, one per day starting
// at index `period` of the input bars. ATR is simple mean of true ranges
// (quant spec cites ComputeDailyATR which uses this method).
func buildATRPctSeries(bars []domain.MarketBar, period int) []float64 {
	if period <= 0 || len(bars) <= period {
		return nil
	}
	out := make([]float64, 0, len(bars)-period)
	for i := period; i < len(bars); i++ {
		sum := 0.0
		for j := i - period + 1; j <= i; j++ {
			sum += trueRange(bars[j].High, bars[j].Low, bars[j-1].Close)
		}
		atr := sum / float64(period)
		if bars[i].Close > 0 {
			out = append(out, atr/bars[i].Close)
		} else {
			out = append(out, 0)
		}
	}
	return out
}

// trueRange is the Wilder TR = max(high-low, |high-prevClose|, |low-prevClose|).
func trueRange(high, low, prevClose float64) float64 {
	a := high - low
	b := math.Abs(high - prevClose)
	c := math.Abs(low - prevClose)
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}

// percentileRank returns the fraction of samples strictly less than x
// plus half the fraction equal to x — the "mid-rank" convention used
// by most stats libraries. Zero-length input returns 0.
func percentileRank(samples []float64, x float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	var less, equal int
	for _, s := range samples {
		switch {
		case s < x:
			less++
		case s == x:
			equal++
		}
	}
	return (float64(less) + 0.5*float64(equal)) / float64(len(samples))
}

// classifyTercile maps a percentile rank to a bucket index {0,1,2}.
// Defaults to 33/67 cutoffs; cfg-exposed so quant can flip to 25/75.
func classifyTercile(pct, lowCut, highCut float64) int {
	if lowCut <= 0 {
		lowCut = 0.33
	}
	if highCut <= 0 || highCut <= lowCut {
		highCut = 0.67
	}
	switch {
	case pct < lowCut:
		return 0
	case pct < highCut:
		return 1
	default:
		return 2
	}
}

// stampATRTrailOnPos is called from processFill after the option branch
// has populated the rest of CustomState. Failure is non-fatal — a nil
// repo or missing history leaves the multiplier unset, and the tick
// loop falls back to the EvalContext default of 1.0.
func (s *Service) stampATRTrailOnPos(pos *domain.MonitoredPosition) {
	if pos.InstrumentType != domain.InstrumentTypeOption {
		return
	}
	if pos.CustomState == nil {
		return
	}
	if !s.atrTrailCfg.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mult, pct, ok := s.computeATRTrailMult(ctx, pos)
	if mult <= 0 {
		mult = 1.0
	}
	pos.CustomState["atr_trail_mult"] = mult
	pos.CustomState["atr_pct_pctile"] = pct
	s.log.Info().
		Str("symbol", string(pos.Symbol)).
		Float64("atr_trail_mult", mult).
		Float64("atr_pct_pctile", pct).
		Bool("scaled", ok).
		Msg("atr_trail: multiplier stamped")
}
