package builtin_test

import (
	"math"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── CheckHistAccel ──────────────────────────────────────────────────────────

func TestCheckHistAccel_Disabled(t *testing.T) {
	// requiredBars <= 0 means disabled — always pass
	assert.True(t, builtin.CheckHistAccel([]float64{1, 2, 3}, 0))
	assert.True(t, builtin.CheckHistAccel([]float64{1, 2, 3}, -1))
}

func TestCheckHistAccel_NotEnoughData(t *testing.T) {
	// Need requiredBars+1 elements; with fewer, pass by default
	assert.True(t, builtin.CheckHistAccel([]float64{1.0}, 2))
	assert.True(t, builtin.CheckHistAccel([]float64{1.0, 0.5}, 2))
}

func TestCheckHistAccel_Converging(t *testing.T) {
	// Deltas: |0.5-1.0|=0.5, |0.3-0.5|=0.2, |0.2-0.3|=0.1 → decreasing ✓
	hists := []float64{1.0, 0.5, 0.3, 0.2}
	assert.True(t, builtin.CheckHistAccel(hists, 2))
	assert.True(t, builtin.CheckHistAccel(hists, 3))
}

func TestCheckHistAccel_Diverging(t *testing.T) {
	// Deltas: |0.5-1.0|=0.5, |0.3-0.5|=0.2, |0.0-0.3|=0.3 → last delta increased ✗
	hists := []float64{1.0, 0.5, 0.3, 0.0}
	assert.False(t, builtin.CheckHistAccel(hists, 2))
}

func TestCheckHistAccel_Flat(t *testing.T) {
	// Equal deltas — non-increasing, so should pass
	hists := []float64{1.0, 0.5, 0.0, -0.5}
	// Deltas: 0.5, 0.5, 0.5 — all equal → non-increasing ✓
	assert.True(t, builtin.CheckHistAccel(hists, 2))
}

func TestCheckHistAccel_SingleBar(t *testing.T) {
	// requiredBars=1 needs at least 2 deltas (3 values)
	// With exactly 2 values, only 1 delta → not enough for comparison
	assert.True(t, builtin.CheckHistAccel([]float64{1.0, 0.5}, 1))
	// With 3 values: deltas [0.5, 0.3] → check last 1 delta is non-increasing vs prior
	assert.True(t, builtin.CheckHistAccel([]float64{1.0, 0.5, 0.3}, 1))
}

func TestCheckHistAccel_NegativeHistograms(t *testing.T) {
	// Negative histogram values converging toward zero
	// Deltas: |(-0.5)-(-1.0)|=0.5, |(-0.3)-(-0.5)|=0.2, |(-0.2)-(-0.3)|=0.1
	hists := []float64{-1.0, -0.5, -0.3, -0.2}
	assert.True(t, builtin.CheckHistAccel(hists, 2))
}

// ─── ComputeSignalScore ──────────────────────────────────────────────────────

func TestComputeSignalScore_AllPerfect_Long(t *testing.T) {
	// histAccelOK=true (0.4), RSI=60 ideal for long (0.3), volumeRatio=1.5+ (0.3)
	score := builtin.ComputeSignalScore(true, 60.0, true, 1.5)
	assert.InDelta(t, 1.0, score, 0.001)
}

func TestComputeSignalScore_AllPerfect_Short(t *testing.T) {
	// histAccelOK=true (0.4), RSI=40 ideal for short (0.3), volumeRatio=1.5+ (0.3)
	score := builtin.ComputeSignalScore(true, 40.0, false, 1.5)
	assert.InDelta(t, 1.0, score, 0.001)
}

func TestComputeSignalScore_AllZero(t *testing.T) {
	// histAccelOK=false, RSI=0 (disabled), volumeRatio=0 (no data)
	score := builtin.ComputeSignalScore(false, 0, true, 0)
	assert.InDelta(t, 0.0, score, 0.001)
}

func TestComputeSignalScore_HistOnly(t *testing.T) {
	// Only histogram acceleration passes
	score := builtin.ComputeSignalScore(true, 0, true, 0)
	assert.InDelta(t, 0.4, score, 0.001)
}

func TestComputeSignalScore_RSI_DeadZone_Long(t *testing.T) {
	// RSI at 30 is the bottom of useful range for long
	score30 := builtin.ComputeSignalScore(false, 30.0, true, 0)
	assert.InDelta(t, 0.0, score30, 0.001)

	// RSI at 50 is middle — gets partial credit
	score50 := builtin.ComputeSignalScore(false, 50.0, true, 0)
	// rsiScore = 1.0 - |50-60|/30 = 1.0 - 0.333 = 0.667
	assert.InDelta(t, 0.3*0.667, score50, 0.01)
}

func TestComputeSignalScore_RSI_DeadZone_Short(t *testing.T) {
	// RSI at 70 is the top of useful range for short
	score70 := builtin.ComputeSignalScore(false, 70.0, false, 0)
	assert.InDelta(t, 0.0, score70, 0.001)

	// RSI at 50 gets partial credit for short
	score50 := builtin.ComputeSignalScore(false, 50.0, false, 0)
	// rsiScore = 1.0 - |50-40|/30 = 0.667
	assert.InDelta(t, 0.3*0.667, score50, 0.01)
}

func TestComputeSignalScore_Volume_Scaling(t *testing.T) {
	// Volume at 0.8x → score 0
	scoreBelow := builtin.ComputeSignalScore(false, 0, true, 0.8)
	assert.InDelta(t, 0.0, scoreBelow, 0.001)

	// Volume at 1.15x → midpoint
	scoreMid := builtin.ComputeSignalScore(false, 0, true, 1.15)
	// volScore = (1.15-0.8)/0.7 = 0.5
	assert.InDelta(t, 0.3*0.5, scoreMid, 0.01)

	// Volume at 2.0x → capped at 1.0
	scoreHigh := builtin.ComputeSignalScore(false, 0, true, 2.0)
	assert.InDelta(t, 0.3, scoreHigh, 0.001)
}

func TestComputeSignalScore_TypicalGood(t *testing.T) {
	// Good long signal: converging histogram, RSI=55, vol 1.3x
	score := builtin.ComputeSignalScore(true, 55.0, true, 1.3)
	// hist=0.4, rsi=1-|55-60|/30=0.833→0.3*0.833=0.25, vol=(1.3-0.8)/0.7=0.714→0.3*0.714=0.214
	expected := 0.4 + 0.3*(1.0-math.Abs(55.0-60.0)/30.0) + 0.3*((1.3-0.8)/0.7)
	assert.InDelta(t, expected, score, 0.01)
	assert.Greater(t, score, 0.65, "typical good signal should exceed 0.65 threshold")
}

func TestComputeSignalScore_TypicalBad(t *testing.T) {
	// Bad long signal: no convergence, RSI=35 (near dead zone), vol 0.9x
	score := builtin.ComputeSignalScore(false, 35.0, true, 0.9)
	// hist=0, rsi=1-|35-60|/30=0.167→0.3*0.167=0.05, vol=(0.9-0.8)/0.7=0.143→0.3*0.143=0.043
	assert.Less(t, score, 0.15, "bad signal should be well below threshold")
}

// ─── Integration: OnBar with signal scoring ──────────────────────────────────

func bmParams() map[string]any {
	return map[string]any{
		"macd_zero_band":      1.0, // relaxed: allow crossover up to MACD line < 1.0
		"risk_reward_ratio":   1.5,
		"swing_lookback":      5,
		"volume_mult":         1.0,
		"allowed_hours_start": "09:30",
		"allowed_hours_end":   "15:45",
		"allowed_hours_tz":    "America/New_York",
		"cooldown_seconds":    300,
		"max_trades_per_day":  5,
		"stabilization_bars":  2,
	}
}

func feedBMBar(t *testing.T, s *builtin.BollingerMACDStrategy, ctx *testContext, symbol string, st strat.State, bar strat.Bar, ind strat.IndicatorData) (strat.State, []strat.Signal) {
	t.Helper()
	ctx.now = bar.Time
	bmSt := st.(*builtin.BMState)
	bmSt.SetIndicators(ind)
	st2, signals, err := s.OnBar(ctx, symbol, bar, st)
	require.NoError(t, err)
	return st2, signals
}

// warmupBM feeds stabilization bars and primes MACD history for crossover detection
func warmupBM(t *testing.T, s *builtin.BollingerMACDStrategy, ctx *testContext, st strat.State, n int) strat.State {
	t.Helper()
	base := time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC) // 09:30 ET
	for i := 0; i < n; i++ {
		bar := strat.Bar{
			Time:   base.Add(time.Duration(i) * 15 * time.Minute),
			Open:   100, High: 101, Low: 99, Close: 100,
			Volume: 1000,
		}
		ind := strat.IndicatorData{
			EMA9: 99, EMA200: 95, VolumeSMA: 1000,
			MACDLine: -0.5, MACDSignal: -0.3, MACDHistogram: -0.2,
			RSI: 50,
		}
		st, _ = feedBMBar(t, s, ctx, "TEST", st, bar, ind)
	}
	return st
}

func TestBollingerMACD_Confluence_NoFilter(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	// No min_confluence_score → all signals pass (default 0)

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)

	st = warmupBM(t, s, ctx, st, 3)

	// Now fire a MACD crossover: prev line < prev signal, now line > signal
	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC), // 10:30 ET
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1000,
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 55,
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	require.Len(t, sigs, 1, "should fire signal with no min_confluence_score filter")
	assert.Equal(t, strat.SideBuy, sigs[0].Side)
	assert.Contains(t, sigs[0].Tags, "confluence")
	assert.Contains(t, sigs[0].Tags, "confluence_detail")
	_ = st
}

func TestBollingerMACD_Confluence_FilteredOut(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["min_confluence_score"] = 95 // very high threshold — virtually impossible

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)

	st = warmupBM(t, s, ctx, st, 3)

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 800, // low volume
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 35, // bad RSI for long
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "signal should be filtered by high min_confluence_score")
	_ = st
}

func TestBollingerMACD_Confluence_HighThreshold_Blocks(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["min_confluence_score"] = 80 // high threshold blocks weak signals

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)

	st = warmupBM(t, s, ctx, st, 3)

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1500,
	}

	// Decent but not perfect indicators — won't hit 80
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 45,
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "signal should be blocked when confluence score below high threshold")
	_ = st
}

func TestBollingerMACD_Confluence_ShortSignal(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	// No min_confluence_score → short signals pass

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)

	// Warmup with bearish indicators for short signal
	base := time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		bar := strat.Bar{
			Time: base.Add(time.Duration(i) * 15 * time.Minute),
			Open: 100, High: 101, Low: 99, Close: 100, Volume: 1000,
		}
		ind := strat.IndicatorData{
			EMA9: 101, EMA200: 105, VolumeSMA: 1000,
			MACDLine: 0.5, MACDSignal: 0.3, MACDHistogram: 0.2,
			RSI: 45,
		}
		st, _ = feedBMBar(t, s, ctx, "TEST", st, bar, ind)
	}

	// MACD cross down: prev line > prev signal, now line < signal
	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 101, Low: 98, Close: 99, Volume: 1500,
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 105, VolumeSMA: 1000,
		MACDLine: -0.1, MACDSignal: -0.05, MACDHistogram: -0.05,
		RSI: 45,
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	require.Len(t, sigs, 1, "short signal should fire with no confluence filter")
	assert.Equal(t, strat.SideSell, sigs[0].Side)
	assert.Contains(t, sigs[0].Tags, "confluence")
	assert.Contains(t, sigs[0].Tags, "confluence_detail")
	_ = st
}

func TestBollingerMACD_DefaultsPreserveBehavior(t *testing.T) {
	// With all defaults, min_confluence_score=0 should not filter any signals
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["min_confluence_score"] = 0

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)

	st = warmupBM(t, s, ctx, st, 3)

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 500, // low volume
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 30, // low RSI
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	require.Len(t, sigs, 1, "with min_confluence_score=0 (disabled), signal should still fire")
	_ = st
}

func TestBollingerMACD_ConfluenceUsedAsStrength(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	// No filter, but confluence score / 100 should be used as signal strength

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)

	st = warmupBM(t, s, ctx, st, 3)

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1500,
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 60, // ideal for long
	}
	_, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	require.Len(t, sigs, 1)

	// Signal strength should be confluence score / 100, with 0.1 floor
	assert.GreaterOrEqual(t, sigs[0].Strength, 0.1, "strength should be at least 0.1 floor")
	assert.LessOrEqual(t, sigs[0].Strength, 1.0, "strength should be <= 1.0")
}

// ─── Rolling WR Gate ─────────────────────────────────────────────────────────

func TestBollingerMACD_RollingWR_BlocksWhenLow(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["rolling_wr_min"] = 0.30  // require 30% WR
	params["rolling_wr_window"] = 5  // over last 5 trades

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)
	st = warmupBM(t, s, ctx, st, 3)

	// Inject 5 losses into trade outcomes (0% WR)
	bmSt := st.(*builtin.BMState)
	bmSt.TradeOutcomes = []int8{-1, -1, -1, -1, -1}

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1500,
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 55,
	}
	_, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "should block entry when rolling WR below threshold")
}

func TestBollingerMACD_RollingWR_PassesWhenAbove(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["rolling_wr_min"] = 0.30
	params["rolling_wr_window"] = 5

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)
	st = warmupBM(t, s, ctx, st, 3)

	// Inject 3 wins, 2 losses (60% WR > 30% threshold)
	bmSt := st.(*builtin.BMState)
	bmSt.TradeOutcomes = []int8{1, -1, 1, 1, -1}

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1500,
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 55,
	}
	_, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	require.Len(t, sigs, 1, "should allow entry when rolling WR above threshold")
}

func TestBollingerMACD_RollingWR_DisabledByDefault(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	// rolling_wr_min = 0 (default, disabled)

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)
	st = warmupBM(t, s, ctx, st, 3)

	// Even with all losses, should still pass when disabled
	bmSt := st.(*builtin.BMState)
	bmSt.TradeOutcomes = []int8{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1500,
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 55,
	}
	_, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	require.Len(t, sigs, 1, "rolling WR gate should be disabled when rolling_wr_min=0")
}

// ─── ComputeConfluenceScore unit tests ──────────────────────────────────────

func TestComputeConfluenceScore_AllFactors(t *testing.T) {
	bar := strat.Bar{
		Open: 99, High: 102, Low: 98, Close: 101, Volume: 1800,
	}
	ind := strat.IndicatorData{
		EMA9: 100, EMA21: 99, EMA50: 98,
		ADX: 30, RSI: 55,
		MACDLine: 0.05, ATR: 1.0,
		VolumeSMA: 1000,
		BBPercentB: 0.6,
		VWAP:       100,
		HTF: map[string]strat.HTFIndicator{
			"1d": {Bias: "BULLISH"},
		},
	}
	// Converging histogram for hist_accel
	prevHists := []float64{1.0, 0.5, 0.3, 0.2, 0.15}

	result := builtin.ComputeConfluenceScore(bar, ind, prevHists, true, false)
	assert.Greater(t, result.Score, 50, "strong confluence should score well above 50")
	assert.Contains(t, result.Factors, "ema_stack")
	assert.Contains(t, result.Factors, "adx_strong")
	assert.Contains(t, result.Factors, "vwap_aligned")
	assert.Contains(t, result.Factors, "htf_agree")
}

func TestComputeConfluenceScore_EmptyIndicators(t *testing.T) {
	bar := strat.Bar{Open: 100, High: 100, Low: 100, Close: 100, Volume: 0}
	ind := strat.IndicatorData{}
	result := builtin.ComputeConfluenceScore(bar, ind, nil, true, false)
	assert.Equal(t, 0, result.Score)
	assert.Empty(t, result.Factors)
}

func TestComputeConfluenceScore_ShortSide(t *testing.T) {
	bar := strat.Bar{
		Open: 101, High: 102, Low: 98, Close: 99, Volume: 1500,
	}
	ind := strat.IndicatorData{
		EMA9: 100, EMA21: 101, EMA50: 102,
		ADX: 25, RSI: 45,
		MACDLine: -0.05, ATR: 1.0,
		VolumeSMA: 1000,
		BBPercentB: 0.35,
		VWAP:       100,
	}
	result := builtin.ComputeConfluenceScore(bar, ind, nil, false, false)
	assert.Greater(t, result.Score, 0)
	assert.Contains(t, result.Factors, "ema_stack")
	assert.Contains(t, result.Factors, "vwap_aligned")
	assert.Contains(t, result.Factors, "bb_trend")
}
