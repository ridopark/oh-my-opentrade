// Package realism derives "expected live" estimates from raw backtest metrics
// by applying deflators that account for known backtest-vs-live divergences
// (compounding ramp, temporal concentration inflating Sharpe, fill-model
// optimism, perfect execution assumptions).
//
// The estimates are calibrated for intraday options strategies on US equities.
// They are deliberately conservative. Consumers should treat the output as a
// sanity-check against over-interpreting headline backtest figures, not as a
// forecast.
package realism

// Deflator constants. Documented rationale below. Changing any of these
// materially is a behavior change for every backtest consumer — do not tune
// on the same dataset the deflators evaluate, that defeats the purpose.
const (
	// SharpeDivisor: live intraday-options Sharpe typically lands at ~1/3 of
	// backtest Sharpe. This captures fill model optimism, temporal
	// concentration artifacts (low-variance denominator), and survivorship.
	SharpeDivisor = 2.8

	// DrawdownMultiplier: live DD typically 1.5-2.5x backtest, driven by
	// adverse regimes the backtest period doesn't include (gap-ups during
	// shorts, vol crushes during longs) and correlated-loss clusters.
	DrawdownMultiplier = 1.8

	// PFDecayFactor: how fast the PF-above-1 edge decays live. Live PF tends
	// to converge toward 1.0 faster than Sharpe does. We halve the edge.
	//   live_pf = 1 + (backtest_pf - 1) / PFDecayFactor
	PFDecayFactor = 1.5

	// PartialCompoundingMultiplier: live traders rarely hit full compounding
	// from the start — sizing is conservative out of the gate. Multiply
	// fixed-notional PnL by this to estimate a more realistic compounded PnL.
	PartialCompoundingMultiplier = 1.5
)

// FlagLevel indicates the severity of an observation about the backtest.
type FlagLevel string

const (
	FlagRed    FlagLevel = "red"
	FlagYellow FlagLevel = "yellow"
	FlagGreen  FlagLevel = "green"
)

// Flag surfaces a single observation about the backtest's likely realism.
type Flag struct {
	Level   FlagLevel `json:"level"`
	Metric  string    `json:"metric"`
	Message string    `json:"message"`
}

// Estimate bundles the deflated live expectations plus the flag list.
type Estimate struct {
	LiveSharpe            float64 `json:"live_sharpe"`
	LiveDrawdownPct       float64 `json:"live_dd_pct"`
	LiveProfitFactor      float64 `json:"live_pf"`
	FixedNotionalPnL      float64 `json:"fixed_notional_pnl"`
	CompoundedPnLEstimate float64 `json:"compounded_pnl_estimate"`
	CompoundingRamp       float64 `json:"compounding_ramp"`
	Flags                 []Flag  `json:"flags"`
	Disclaimer            string  `json:"disclaimer"`
}

// Inputs is the minimal set of backtest metrics needed to compute Estimate.
// All callers should populate every field; zero values may be treated as
// missing signals where that makes sense (e.g. SharpeRatio nil → no
// Sharpe-related output).
type Inputs struct {
	InitialEquity  float64
	FinalEquity    float64
	TotalPnL       float64
	TradeCount     int
	WinRatePct     float64
	MaxDrawdownPct float64
	SharpeRatio    *float64
	ProfitFactor   float64
	BacktestDays   int // length of the backtest window in calendar days
}

// Compute derives an Estimate from the given Inputs.
func Compute(in Inputs) *Estimate {
	est := &Estimate{
		Disclaimer: "Estimates from intraday-options-strategy priors. Not a forecast. Calibrate against live-paper data over 90 days before deploying capital.",
	}

	// Fixed-notional PnL: remove the compounding ramp.
	// backtest_pnl × (initial_equity / final_equity) approximates the PnL
	// the same trades would have produced under flat per-trade sizing.
	if in.FinalEquity > 0 {
		est.CompoundingRamp = in.FinalEquity / in.InitialEquity
		est.FixedNotionalPnL = in.TotalPnL * (in.InitialEquity / in.FinalEquity)
		est.CompoundedPnLEstimate = est.FixedNotionalPnL * PartialCompoundingMultiplier
	}

	// Deflated Sharpe.
	if in.SharpeRatio != nil && *in.SharpeRatio > 0 {
		est.LiveSharpe = *in.SharpeRatio / SharpeDivisor
	}

	// Inflated drawdown.
	est.LiveDrawdownPct = in.MaxDrawdownPct * DrawdownMultiplier

	// Deflated profit factor — edge above 1.0 decays faster than Sharpe.
	if in.ProfitFactor > 1.0 {
		est.LiveProfitFactor = 1.0 + (in.ProfitFactor-1.0)/PFDecayFactor
	} else {
		est.LiveProfitFactor = in.ProfitFactor
	}

	est.Flags = computeFlags(in)
	return est
}

// computeFlags scans the inputs for known backtest-vs-live divergence
// signatures and surfaces them as actionable flags. Thresholds are aligned
// with the overfitting detection checklist in
// .claude/skills/strategy-tuning/SKILL.md.
func computeFlags(in Inputs) []Flag {
	flags := []Flag{}

	// RED flags: strong signals of backtest over-optimism.
	if in.SharpeRatio != nil && *in.SharpeRatio > 5.0 {
		flags = append(flags, Flag{
			Level:  FlagRed,
			Metric: "sharpe_ratio",
			Message: fmtf("Sharpe %.2f > 5 — likely temporal concentration or fill-model optimism; realistic intraday-options Sharpe is 2-3.", *in.SharpeRatio),
		})
	}
	if in.ProfitFactor > 3.0 {
		flags = append(flags, Flag{
			Level:  FlagRed,
			Metric: "profit_factor",
			Message: fmtf("PF %.2f > 3.0 — strong overfit signature for intraday strategies; healthy PF range is 1.3-2.2.", in.ProfitFactor),
		})
	}
	if in.WinRatePct > 60.0 && in.ProfitFactor > 2.0 {
		flags = append(flags, Flag{
			Level:  FlagRed,
			Metric: "win_rate_pct",
			Message: fmtf("WR %.1f%% combined with PF %.2f — curve-fit suspect; this combo is rare in honest intraday strategies.", in.WinRatePct, in.ProfitFactor),
		})
	}
	if in.MaxDrawdownPct > 0 && in.MaxDrawdownPct < 3.0 {
		flags = append(flags, Flag{
			Level:  FlagRed,
			Metric: "max_drawdown_pct",
			Message: fmtf("DD %.2f%% < 3%% — suspiciously tight; live DD rarely stays below 5%% for real intraday strategies.", in.MaxDrawdownPct),
		})
	}

	// YELLOW flags: moderate concerns worth noting.
	if in.TradeCount > 0 && in.TradeCount < 200 {
		flags = append(flags, Flag{
			Level:  FlagYellow,
			Metric: "trade_count",
			Message: fmtf("Only %d trades — below the 200-trade statistical significance floor.", in.TradeCount),
		})
	}
	if in.FinalEquity > 0 && in.InitialEquity > 0 {
		ramp := in.FinalEquity / in.InitialEquity
		if ramp > 3.0 {
			flags = append(flags, Flag{
				Level:  FlagYellow,
				Metric: "compounding",
				Message: fmtf("Equity ramped %.1fx — compounding amplifies headline PnL. Fixed-notional estimate is a better live anchor.", ramp),
			})
		}
	}
	if in.BacktestDays > 0 && in.BacktestDays < 180 {
		flags = append(flags, Flag{
			Level:  FlagYellow,
			Metric: "sample_period",
			Message: fmtf("Backtest window %d days — below 6 months; likely misses an adverse regime.", in.BacktestDays),
		})
	}

	// GREEN flag: healthy baseline profile.
	if len(flags) == 0 && in.TradeCount >= 500 &&
		in.SharpeRatio != nil && *in.SharpeRatio >= 1.5 && *in.SharpeRatio <= 3.0 &&
		in.ProfitFactor >= 1.4 && in.ProfitFactor <= 2.2 {
		flags = append(flags, Flag{
			Level:   FlagGreen,
			Metric:  "overall",
			Message: "Backtest metrics fall within typical honest ranges; no major realism red flags detected.",
		})
	}

	return flags
}

// fmtf is a thin wrapper to keep the flag authoring readable without pulling
// fmt into the public API surface.
func fmtf(format string, a ...any) string { return sprintf(format, a...) }
