package realism

import (
	"testing"
)

// Today's pair backtest (2026-04-18 session) as a real-world input. These
// numbers must map to deflators the quant analyst signed off on.
func TestCompute_TodaysPairBacktest(t *testing.T) {
	sh := 6.84
	in := Inputs{
		InitialEquity:  100000,
		FinalEquity:    1462886, // initial + pnl
		TotalPnL:       1362886,
		TradeCount:     4809,
		WinRatePct:     57.1,
		MaxDrawdownPct: 5.06,
		SharpeRatio:    &sh,
		ProfitFactor:   2.64,
		BacktestDays:   365,
	}

	est := Compute(in)

	// Deflated Sharpe ~2.44
	if got, want := est.LiveSharpe, 6.84/SharpeDivisor; !approxEq(got, want, 0.01) {
		t.Errorf("LiveSharpe: got %.3f, want %.3f", got, want)
	}

	// Inflated DD ~9.11%
	if got, want := est.LiveDrawdownPct, 5.06*DrawdownMultiplier; !approxEq(got, want, 0.01) {
		t.Errorf("LiveDrawdownPct: got %.3f, want %.3f", got, want)
	}

	// PF decayed: 1 + (2.64-1)/1.5 ≈ 2.093
	if got, want := est.LiveProfitFactor, 1.0+(2.64-1.0)/PFDecayFactor; !approxEq(got, want, 0.001) {
		t.Errorf("LiveProfitFactor: got %.3f, want %.3f", got, want)
	}

	// Compounding ramp = 14.63 (initial 100k → final 1.46M)
	if got := est.CompoundingRamp; !approxEq(got, 14.63, 0.01) {
		t.Errorf("CompoundingRamp: got %.3f, want ~14.63", got)
	}

	// Fixed-notional PnL = total_pnl × (initial/final) ≈ 93,162
	if got, want := est.FixedNotionalPnL, 1362886.0*(100000.0/1462886.0); !approxEq(got, want, 1.0) {
		t.Errorf("FixedNotionalPnL: got %.0f, want %.0f", got, want)
	}

	// Compounded estimate = fixed × 1.5 ≈ 139,743
	if got, want := est.CompoundedPnLEstimate, est.FixedNotionalPnL*PartialCompoundingMultiplier; !approxEq(got, want, 1.0) {
		t.Errorf("CompoundedPnLEstimate: got %.0f, want %.0f", got, want)
	}

	// Flags: Sharpe > 5 (RED), PF > 3 false, WR+PF combo false (WR 57.1 < 60),
	// compounding ramp > 3 (YELLOW).
	var red, yellow int
	for _, f := range est.Flags {
		switch f.Level {
		case FlagRed:
			red++
		case FlagYellow:
			yellow++
		}
	}
	if red == 0 {
		t.Error("expected at least one RED flag (Sharpe > 5)")
	}
	if yellow == 0 {
		t.Error("expected at least one YELLOW flag (compounding ramp > 3)")
	}
}

func TestCompute_HealthyHonestStrategy(t *testing.T) {
	sh := 2.0
	in := Inputs{
		InitialEquity:  100000,
		FinalEquity:    125000,
		TotalPnL:       25000,
		TradeCount:     800,
		WinRatePct:     48.0,
		MaxDrawdownPct: 6.0,
		SharpeRatio:    &sh,
		ProfitFactor:   1.7,
		BacktestDays:   365,
	}
	est := Compute(in)

	// Expect green flag (healthy profile) and nothing red.
	greenFound := false
	for _, f := range est.Flags {
		if f.Level == FlagRed {
			t.Errorf("unexpected RED flag for healthy inputs: %s", f.Message)
		}
		if f.Level == FlagGreen {
			greenFound = true
		}
	}
	if !greenFound {
		t.Error("expected GREEN flag for healthy honest metrics")
	}
}

func TestCompute_CurveFitSuspect(t *testing.T) {
	sh := 7.5
	in := Inputs{
		InitialEquity:  100000,
		FinalEquity:    700000,
		TotalPnL:       600000,
		TradeCount:     1500,
		WinRatePct:     65.0,
		MaxDrawdownPct: 2.5,
		SharpeRatio:    &sh,
		ProfitFactor:   3.3,
		BacktestDays:   365,
	}
	est := Compute(in)

	// Should trip four RED flags: Sharpe>5, PF>3, WR>60+PF>2, DD<3.
	redSet := map[string]bool{}
	for _, f := range est.Flags {
		if f.Level == FlagRed {
			redSet[f.Metric] = true
		}
	}
	for _, m := range []string{"sharpe_ratio", "profit_factor", "win_rate_pct", "max_drawdown_pct"} {
		if !redSet[m] {
			t.Errorf("expected RED flag on %s", m)
		}
	}
}

func TestCompute_InsufficientSample(t *testing.T) {
	sh := 1.8
	in := Inputs{
		InitialEquity:  100000,
		FinalEquity:    110000,
		TotalPnL:       10000,
		TradeCount:     120,
		WinRatePct:     55.0,
		MaxDrawdownPct: 4.0,
		SharpeRatio:    &sh,
		ProfitFactor:   1.5,
		BacktestDays:   90, // also < 180
	}
	est := Compute(in)

	yellowMetrics := map[string]bool{}
	for _, f := range est.Flags {
		if f.Level == FlagYellow {
			yellowMetrics[f.Metric] = true
		}
	}
	if !yellowMetrics["trade_count"] {
		t.Error("expected YELLOW flag on trade_count")
	}
	if !yellowMetrics["sample_period"] {
		t.Error("expected YELLOW flag on sample_period")
	}
}

func TestCompute_PFBelowOneDoesNotDeflate(t *testing.T) {
	sh := 0.5
	in := Inputs{
		InitialEquity: 100000, FinalEquity: 95000, TotalPnL: -5000,
		TradeCount: 300, WinRatePct: 45, MaxDrawdownPct: 8,
		SharpeRatio: &sh, ProfitFactor: 0.8, BacktestDays: 180,
	}
	est := Compute(in)
	if got, want := est.LiveProfitFactor, 0.8; !approxEq(got, want, 0.001) {
		t.Errorf("LiveProfitFactor for losing strategy should not inflate: got %.3f, want %.3f", got, want)
	}
}

func TestCompute_NoSharpeLeavesLiveSharpeZero(t *testing.T) {
	in := Inputs{
		InitialEquity: 100000, FinalEquity: 110000, TotalPnL: 10000,
		TradeCount: 500, WinRatePct: 50, MaxDrawdownPct: 5,
		SharpeRatio: nil, ProfitFactor: 1.5, BacktestDays: 365,
	}
	est := Compute(in)
	if est.LiveSharpe != 0 {
		t.Errorf("LiveSharpe should be zero when no SharpeRatio: got %.3f", est.LiveSharpe)
	}
}

func approxEq(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}
