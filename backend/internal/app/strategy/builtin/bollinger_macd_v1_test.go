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

func TestBollingerMACD_SignalScore_NoFilter(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	// No min_signal_score → all signals pass (default 0)

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
	require.Len(t, sigs, 1, "should fire signal with no min_signal_score filter")
	assert.Equal(t, strat.SideBuy, sigs[0].Side)
	assert.Contains(t, sigs[0].Tags, "signal_score")
	_ = st
}

func TestBollingerMACD_SignalScore_FilteredOut(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["min_signal_score"] = 0.90 // very high threshold

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
	assert.Empty(t, sigs, "signal should be filtered by high min_signal_score")
	_ = st
}

func TestBollingerMACD_RSI_LongFilter(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["rsi_long_min"] = 50.0 // require RSI >= 50 for longs

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)

	st = warmupBM(t, s, ctx, st, 3)

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1500,
	}

	// RSI = 45 → below minimum 50 → filtered
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 45,
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "long signal should be filtered when RSI < rsi_long_min")
	_ = st
}

func TestBollingerMACD_RSI_ShortFilter(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["rsi_short_max"] = 50.0 // require RSI <= 50 for shorts

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
			RSI: 55,
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
		RSI: 55, // above max 50 → filtered
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "short signal should be filtered when RSI > rsi_short_max")
	_ = st
}

func TestBollingerMACD_DefaultsPreserveBehavior(t *testing.T) {
	// With all defaults, new params should not change behavior
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	// Explicitly set defaults
	params["min_signal_score"] = 0.0
	params["hist_accel_bars"] = 0
	params["rsi_long_min"] = 0.0
	params["rsi_short_max"] = 100.0

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
	require.Len(t, sigs, 1, "with all defaults (disabled), signal should still fire")
	_ = st
}

func TestBollingerMACD_ScoreUsedAsStrength(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	// No filter, but score should be used as signal strength

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

	// Signal strength should NOT be hardcoded 0.8 anymore
	assert.NotEqual(t, 0.8, sigs[0].Strength, "strength should be computed, not hardcoded 0.8")
	assert.Greater(t, sigs[0].Strength, 0.0, "strength should be positive")
	assert.LessOrEqual(t, sigs[0].Strength, 1.0, "strength should be <= 1.0")
}

// ─── Entry confirmation filters ──────────────────────────────────────────────

func TestBollingerMACD_DirectionalClose_Blocks(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["require_directional_close"] = true

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)
	st = warmupBM(t, s, ctx, st, 3)

	// Bearish candle on a long crossover → should be blocked
	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 102, High: 103, Low: 99, Close: 100, Volume: 1500, // Close < Open
	}
	crossInd := strat.IndicatorData{
		EMA9: 99, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 55,
	}
	_, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "bearish candle should block long entry when require_directional_close=true")
}

func TestBollingerMACD_DirectionalClose_Passes(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["require_directional_close"] = true

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)
	st = warmupBM(t, s, ctx, st, 3)

	// Bullish candle on a long crossover → should pass
	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 99, High: 102, Low: 98, Close: 101, Volume: 1500, // Close > Open
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 55,
	}
	_, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	require.Len(t, sigs, 1, "bullish candle should pass directional close filter")
}

func TestBollingerMACD_ADX_Blocks(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["min_adx"] = 25.0

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
		RSI: 55, ADX: 15, // below min_adx=25
	}
	_, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "low ADX should block entry when min_adx=25")
}

func TestBollingerMACD_VWAP_Blocks(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["require_vwap_alignment"] = true

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
		RSI: 55, VWAP: 102, // price below VWAP → blocks long
	}
	_, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "price below VWAP should block long when require_vwap_alignment=true")
}

func TestBollingerMACD_BodyRatio_Blocks(t *testing.T) {
	s := builtin.NewBollingerMACDStrategy()
	params := bmParams()
	params["min_body_ratio"] = 0.5

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)
	st = warmupBM(t, s, ctx, st, 3)

	// Doji candle: body is tiny relative to range
	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100.0, High: 103, Low: 98, Close: 100.1, Volume: 1500, // body=0.1, range=5, ratio=0.02
	}
	crossInd := strat.IndicatorData{
		EMA9: 99, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI: 55,
	}
	_, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "doji candle should be blocked by min_body_ratio=0.5")
}
