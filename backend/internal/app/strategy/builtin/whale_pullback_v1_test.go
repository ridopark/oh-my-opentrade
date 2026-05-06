package builtin_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wpParams() map[string]any {
	return map[string]any{
		"ema_period":           9,
		"pullback_touch_atr":   0.15,
		"min_trend_bars":       3,
		"vwap_break_atr":       0.5,
		"vp_lookback_days":     5,
		"vp_bin_bps":           10,
		"vp_hvn_threshold_pct": 80.0,
		"vp_clear_atr":         0.6,
		"vp_required":          true,
		"vp_rth_only":          true,
		"atr_stop_mult":        1.75,
		"exit_body_closes":     2,
		"cooldown_seconds":     1800,
		"max_trades_per_day":   3,
		"allowed_hours_start":  "09:35",
		"allowed_hours_end":    "15:30",
		"allowed_hours_tz":     "America/New_York",
	}
}

func wpIndicators(vwap, atr float64) strat.IndicatorData {
	return strat.IndicatorData{VWAP: vwap, ATR: atr}
}

// 2026-03-10 is a Tuesday, RTH 09:30-16:00 ET.
var wpBaseET = mustET(2026, 3, 10, 9, 35)

func mustET(y, mo, d, h, mi int) time.Time {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, loc)
}

func wpBar(t time.Time, open, high, low, close, volume float64) strat.Bar {
	return strat.Bar{Time: t, Open: open, High: high, Low: low, Close: close, Volume: volume}
}

func feedWPBar(t *testing.T, s *builtin.WhalePullbackStrategy, ctx *testContext, symbol string, st strat.State, bar strat.Bar, ind strat.IndicatorData) (strat.State, []strat.Signal) {
	t.Helper()
	ctx.now = bar.Time
	wp := st.(*builtin.WhalePullbackState)
	wp.SetIndicators(ind)
	st2, signals, err := s.OnBar(ctx, symbol, bar, st)
	require.NoError(t, err)
	return st2, signals
}

func replayWPBar(t *testing.T, s *builtin.WhalePullbackStrategy, ctx *testContext, symbol string, st strat.State, bar strat.Bar, ind strat.IndicatorData) strat.State {
	t.Helper()
	ctx.now = bar.Time
	st2, err := s.ReplayOnBar(ctx, symbol, bar, st, ind)
	require.NoError(t, err)
	return st2
}

func TestWhalePullback_Meta(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	meta := s.Meta()
	assert.Equal(t, "whale_pullback_v1", meta.ID.String())
	assert.Equal(t, "1.0.0", meta.Version.String())
	assert.NotEmpty(t, meta.Name)
	assert.NotEmpty(t, meta.Description)
	assert.Equal(t, "system", meta.Author)
}

func TestWhalePullback_WarmupBars(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	assert.Equal(t, 60, s.WarmupBars())
}

func TestWhalePullback_Init_Fresh(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)
	require.NotNil(t, st)
	_, ok := st.(*builtin.WhalePullbackState)
	assert.True(t, ok)
}

func TestWhalePullback_ImplementsInterface(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	var _ strat.Strategy = s
	var _ strat.ReplayableStrategy = s
}

// buildLongTrend feeds N rising bars above VWAP with a final large-candle
// break to satisfy the trend filter.
func buildLongTrend(t *testing.T, s *builtin.WhalePullbackStrategy, ctx *testContext, st strat.State, vwap, atr float64, startTime time.Time) strat.State {
	t.Helper()
	ind := wpIndicators(vwap, atr)
	params := wpParams()
	params["vp_required"] = false
	bars := []strat.Bar{
		wpBar(startTime.Add(0*time.Minute), 100.5, 100.7, 100.4, 100.6, 100),
		wpBar(startTime.Add(5*time.Minute), 100.6, 100.85, 100.55, 100.8, 100),
		wpBar(startTime.Add(10*time.Minute), 100.8, 102.0, 100.75, 101.9, 100),
	}
	for _, b := range bars {
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}
	return st
}

func TestWhalePullback_LongEntry_HappyPath(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_required"] = false
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	vwap, atr := 100.0, 1.0

	st = buildLongTrend(t, s, ctx, st, vwap, atr, wpBaseET)

	emaPriceBars := []strat.Bar{
		wpBar(wpBaseET.Add(15*time.Minute), 101.9, 102.0, 101.85, 101.95, 100),
		wpBar(wpBaseET.Add(20*time.Minute), 101.95, 102.05, 101.9, 102.0, 100),
		wpBar(wpBaseET.Add(25*time.Minute), 102.0, 102.1, 101.95, 102.05, 100),
		wpBar(wpBaseET.Add(30*time.Minute), 102.05, 102.15, 102.0, 102.1, 100),
		wpBar(wpBaseET.Add(35*time.Minute), 102.1, 102.2, 102.05, 102.15, 100),
		wpBar(wpBaseET.Add(40*time.Minute), 102.15, 102.25, 102.1, 102.2, 100),
	}
	ind := wpIndicators(vwap, atr)
	for _, b := range emaPriceBars {
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}

	wp := st.(*builtin.WhalePullbackState)
	require.True(t, wp.EMAReady, "EMA should be ready after enough bars")
	emaVal := wp.EMAValue
	require.Greater(t, emaVal, 0.0)

	wickLow := emaVal - 0.05*atr
	pullback := wpBar(wpBaseET.Add(45*time.Minute), emaVal+0.10, emaVal+0.20, wickLow, emaVal+0.10, 100)
	st2, signals := feedWPBar(t, s, ctx, "AAPL", st, pullback, ind)
	require.NotEmpty(t, signals, "should emit entry on EMA touch with confirming body")

	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideBuy, sig.Side)
	assert.Equal(t, "whale_pullback", sig.Tags["setup"])
	assert.Equal(t, "bullish", sig.Tags["trend"])

	wp2 := st2.(*builtin.WhalePullbackState)
	assert.Equal(t, strat.SideBuy, wp2.PendingEntry)
	assert.Equal(t, 1, wp2.TradesToday)
}

func TestWhalePullback_ShortEntry_HappyPath(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_required"] = false
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	vwap, atr := 100.0, 1.0
	ind := wpIndicators(vwap, atr)

	bars := []strat.Bar{
		wpBar(wpBaseET.Add(0*time.Minute), 99.5, 99.6, 99.3, 99.4, 100),
		wpBar(wpBaseET.Add(5*time.Minute), 99.4, 99.45, 99.15, 99.2, 100),
		wpBar(wpBaseET.Add(10*time.Minute), 99.2, 99.25, 98.0, 98.1, 100),
	}
	for _, b := range bars {
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}

	stallBars := []strat.Bar{
		wpBar(wpBaseET.Add(15*time.Minute), 98.1, 98.15, 98.0, 98.05, 100),
		wpBar(wpBaseET.Add(20*time.Minute), 98.05, 98.1, 97.95, 98.0, 100),
		wpBar(wpBaseET.Add(25*time.Minute), 98.0, 98.05, 97.9, 97.95, 100),
		wpBar(wpBaseET.Add(30*time.Minute), 97.95, 98.0, 97.85, 97.9, 100),
		wpBar(wpBaseET.Add(35*time.Minute), 97.9, 97.95, 97.8, 97.85, 100),
		wpBar(wpBaseET.Add(40*time.Minute), 97.85, 97.9, 97.75, 97.8, 100),
	}
	for _, b := range stallBars {
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}

	wp := st.(*builtin.WhalePullbackState)
	require.True(t, wp.EMAReady)
	emaVal := wp.EMAValue
	require.Greater(t, emaVal, 0.0)

	wickHigh := emaVal + 0.05*atr
	pullback := wpBar(wpBaseET.Add(45*time.Minute), emaVal-0.10, wickHigh, emaVal-0.20, emaVal-0.10, 100)
	st2, signals := feedWPBar(t, s, ctx, "AAPL", st, pullback, ind)
	require.NotEmpty(t, signals, "short entry expected")

	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideSell, sig.Side)
	assert.Equal(t, "bearish", sig.Tags["trend"])

	wp2 := st2.(*builtin.WhalePullbackState)
	assert.Equal(t, strat.SideSell, wp2.PendingEntry)
}

func TestWhalePullback_VolumeProfileVeto(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_required"] = true
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	vwap, atr := 100.0, 1.0
	ind := wpIndicators(vwap, atr)

	prevDay := mustET(2026, 3, 9, 10, 0)
	for i := 0; i < 30; i++ {
		bar := wpBar(prevDay.Add(time.Duration(i*5)*time.Minute), 102.5, 102.55, 102.45, 102.5, 5000)
		st = replayWPBar(t, s, ctx, "AAPL", st, bar, ind)
	}

	openBar := wpBar(wpBaseET, 100.5, 100.7, 100.4, 100.6, 100)
	st2, _ := feedWPBar(t, s, ctx, "AAPL", st, openBar, ind)
	st = st2

	st = buildLongTrend(t, s, ctx, st, vwap, atr, wpBaseET.Add(5*time.Minute))

	emaPriceBars := []strat.Bar{
		wpBar(wpBaseET.Add(20*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(25*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(30*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(35*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(40*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(45*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
	}
	for _, b := range emaPriceBars {
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}

	wp := st.(*builtin.WhalePullbackState)
	require.True(t, wp.EMAReady)
	emaVal := wp.EMAValue

	wickLow := emaVal - 0.05*atr
	pullback := wpBar(wpBaseET.Add(50*time.Minute), emaVal+0.10, emaVal+0.20, wickLow, emaVal+0.10, 100)
	_, signals := feedWPBar(t, s, ctx, "AAPL", st, pullback, ind)
	assert.Empty(t, signals, "HVN ahead within vp_clear_atr should veto entry")
}

func TestWhalePullback_SidewaysOscillation_NoEntry(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_required"] = false
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	vwap, atr := 100.0, 1.0
	ind := wpIndicators(vwap, atr)

	for i := 0; i < 20; i++ {
		var b strat.Bar
		ts := wpBaseET.Add(time.Duration(i*5) * time.Minute)
		if i%2 == 0 {
			b = wpBar(ts, 100.05, 100.10, 99.95, 100.05, 100)
		} else {
			b = wpBar(ts, 100.05, 100.05, 99.90, 99.95, 100)
		}
		st2, signals := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
		assert.Empty(t, signals, "no entry under sideways oscillation")
	}
}

func TestWhalePullback_VWAPBreakBelowATR_NoEntry(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_required"] = false
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	vwap, atr := 100.0, 1.0
	ind := wpIndicators(vwap, atr)

	for i := 0; i < 12; i++ {
		ts := wpBaseET.Add(time.Duration(i*5) * time.Minute)
		b := wpBar(ts, 100.10, 100.15, 100.05, 100.12, 100)
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}

	wp := st.(*builtin.WhalePullbackState)
	require.True(t, wp.EMAReady)
	emaVal := wp.EMAValue

	wickLow := emaVal - 0.05*atr
	pullback := wpBar(wpBaseET.Add(65*time.Minute), emaVal+0.10, emaVal+0.20, wickLow, emaVal+0.10, 100)
	_, signals := feedWPBar(t, s, ctx, "AAPL", st, pullback, ind)
	assert.Empty(t, signals, "no large-candle break, no entry")
}

func TestWhalePullback_PullbackTooFar_NoEntry(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_required"] = false
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	vwap, atr := 100.0, 1.0
	ind := wpIndicators(vwap, atr)

	st = buildLongTrend(t, s, ctx, st, vwap, atr, wpBaseET)

	stable := []strat.Bar{
		wpBar(wpBaseET.Add(15*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(20*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(25*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(30*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(35*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(40*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
	}
	for _, b := range stable {
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}

	wp := st.(*builtin.WhalePullbackState)
	require.True(t, wp.EMAReady)
	emaVal := wp.EMAValue

	tooFar := wpBar(wpBaseET.Add(45*time.Minute), emaVal+atr, emaVal+1.5*atr, emaVal+0.5*atr, emaVal+atr, 100)
	_, signals := feedWPBar(t, s, ctx, "AAPL", st, tooFar, ind)
	assert.Empty(t, signals, "wick too far from EMA, no entry")
}

func TestWhalePullback_BodyClosesOppositeEMA_OneBar_NoExit(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	st, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)

	wp := st.(*builtin.WhalePullbackState)
	wp.PositionSide = strat.SideBuy
	wp.EntryPrice = 100.0
	wp.EMAValue = 100.0
	wp.EMAReady = true
	wp.OppositeBodyCount = 0

	bar := wpBar(wpBaseET, 100.0, 100.05, 99.5, 99.6, 100)
	ind := wpIndicators(100.0, 1.0)
	_, signals := feedWPBar(t, s, ctx, "AAPL", wp, bar, ind)
	assert.Empty(t, signals, "single opposite-body close should not trigger exit")
}

func TestWhalePullback_BodyClosesOppositeEMA_TwoBars_Exit(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	st, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)

	wp := st.(*builtin.WhalePullbackState)
	wp.PositionSide = strat.SideBuy
	wp.EntryPrice = 100.0
	wp.EMAValue = 100.0
	wp.EMAReady = true
	wp.OppositeBodyCount = 1

	bar := wpBar(wpBaseET, 100.0, 100.05, 99.5, 99.6, 100)
	ind := wpIndicators(100.0, 1.0)
	_, signals := feedWPBar(t, s, ctx, "AAPL", wp, bar, ind)
	require.NotEmpty(t, signals, "second consecutive opposite-body close should exit")
	assert.Equal(t, strat.SignalExit, signals[0].Type)
	assert.Equal(t, strat.SideSell, signals[0].Side)
}

func TestWhalePullback_ATRStopExit(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	st, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)

	wp := st.(*builtin.WhalePullbackState)
	wp.PositionSide = strat.SideBuy
	wp.EntryPrice = 100.0
	wp.EMAValue = 100.0
	wp.EMAReady = true

	atr := 1.0
	bar := wpBar(wpBaseET, 99, 99, 97.5, 97.5, 100)
	ind := wpIndicators(100.0, atr)
	_, signals := feedWPBar(t, s, ctx, "AAPL", wp, bar, ind)
	require.NotEmpty(t, signals, "ATR stop should fire when close crosses entry - atr_stop_mult*ATR")
	assert.Equal(t, strat.SignalExit, signals[0].Type)
}

func TestWhalePullback_TunableEMAPeriod(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["ema_period"] = 21
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	ind := wpIndicators(100.0, 1.0)
	for i := 0; i < 9; i++ {
		ts := wpBaseET.Add(time.Duration(i*5) * time.Minute)
		b := wpBar(ts, 100.0, 100.1, 99.9, 100.0, 100)
		st = replayWPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	wp := st.(*builtin.WhalePullbackState)
	assert.False(t, wp.EMAReady, "with ema_period=21 the EMA should NOT be ready after 9 bars")

	for i := 9; i < 21; i++ {
		ts := wpBaseET.Add(time.Duration(i*5) * time.Minute)
		b := wpBar(ts, 100.0, 100.1, 99.9, 100.0, 100)
		st = replayWPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	wp = st.(*builtin.WhalePullbackState)
	assert.True(t, wp.EMAReady, "EMA(21) ready after 21 bars")
}

func TestWhalePullback_OnEvent_FillConfirmation(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	st, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)

	wp := st.(*builtin.WhalePullbackState)
	wp.PendingEntry = strat.SideBuy
	wp.PendingEntryAt = wpBaseET

	evt := strat.FillConfirmation{Symbol: "AAPL", Side: strat.SideBuy, Quantity: 10, Price: 100}
	st2, signals, err := s.OnEvent(ctx, "AAPL", evt, wp)
	require.NoError(t, err)
	assert.Empty(t, signals)

	wp2 := st2.(*builtin.WhalePullbackState)
	assert.Equal(t, strat.SideBuy, wp2.PositionSide)
	assert.Empty(t, string(wp2.PendingEntry))
}

func TestWhalePullback_OnEvent_EntryRejection(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	st, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)

	wp := st.(*builtin.WhalePullbackState)
	wp.PendingEntry = strat.SideBuy
	wp.PendingEntryAt = wpBaseET

	evt := strat.EntryRejection{Symbol: "AAPL", Side: strat.SideBuy, Reason: "risk_limit"}
	st2, signals, err := s.OnEvent(ctx, "AAPL", evt, wp)
	require.NoError(t, err)
	assert.Empty(t, signals)

	wp2 := st2.(*builtin.WhalePullbackState)
	assert.Empty(t, string(wp2.PendingEntry))
}

func TestWhalePullback_MarshalUnmarshal(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	st, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)

	wp := st.(*builtin.WhalePullbackState)
	wp.Phase = builtin.WPPhasePullbackArmed
	wp.TrendDirection = "bullish"
	wp.TrendBars = 4
	wp.OppositeBodyCount = 1
	wp.PositionSide = strat.SideBuy
	wp.EntryPrice = 100.5
	wp.PrevBar = wpBar(wpBaseET, 100, 101, 99, 100.5, 50)
	wp.HasPrevBar = true
	wp.SessionDate = "2026-03-10"
	wp.TradesToday = 2
	wp.CooldownUntil = wpBaseET.Add(10 * time.Minute)

	data, err := wp.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &builtin.WhalePullbackState{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, wp.Phase, restored.Phase)
	assert.Equal(t, wp.TrendDirection, restored.TrendDirection)
	assert.Equal(t, wp.TrendBars, restored.TrendBars)
	assert.Equal(t, wp.OppositeBodyCount, restored.OppositeBodyCount)
	assert.Equal(t, wp.PositionSide, restored.PositionSide)
	assert.InDelta(t, wp.EntryPrice, restored.EntryPrice, 1e-9)
	assert.True(t, restored.HasPrevBar)
	assert.Equal(t, wp.SessionDate, restored.SessionDate)
	assert.Equal(t, wp.TradesToday, restored.TradesToday)
}

func TestWhalePullback_CooldownPreventsEntry(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_required"] = false
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	wp := st.(*builtin.WhalePullbackState)
	wp.CooldownUntil = wpBaseET.Add(60 * time.Minute)

	vwap, atr := 100.0, 1.0
	ind := wpIndicators(vwap, atr)
	st = buildLongTrend(t, s, ctx, wp, vwap, atr, wpBaseET)

	emaPriceBars := []strat.Bar{
		wpBar(wpBaseET.Add(15*time.Minute), 101.9, 102.0, 101.85, 101.95, 100),
		wpBar(wpBaseET.Add(20*time.Minute), 101.95, 102.05, 101.9, 102.0, 100),
		wpBar(wpBaseET.Add(25*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(30*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(35*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(40*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
	}
	for _, b := range emaPriceBars {
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}

	wp2 := st.(*builtin.WhalePullbackState)
	require.True(t, wp2.EMAReady)
	emaVal := wp2.EMAValue

	pullback := wpBar(wpBaseET.Add(45*time.Minute), emaVal+0.1, emaVal+0.2, emaVal-0.05, emaVal+0.1, 100)
	_, signals := feedWPBar(t, s, ctx, "AAPL", st, pullback, ind)
	assert.Empty(t, signals, "cooldown should prevent entry signals")
}

func TestWhalePullback_TradesTodayResetsOnSessionRoll(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	st, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)

	wp := st.(*builtin.WhalePullbackState)
	wp.TradesToday = 99
	wp.SessionDate = "2026-03-05"

	day2 := mustET(2026, 3, 6, 9, 35)
	bar := wpBar(day2, 100.0, 100.1, 99.9, 100.0, 1000)
	st2 := replayWPBar(t, s, ctx, "AAPL", st, bar, wpIndicators(100.0, 1.0))

	wp2 := st2.(*builtin.WhalePullbackState)
	assert.Equal(t, 0, wp2.TradesToday, "TradesToday must reset on RTH session boundary")
	assert.Equal(t, "2026-03-06", wp2.SessionDate)
}

func TestWhalePullback_WindowRoll_BinReKeying(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_lookback_days"] = 2
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	ind := wpIndicators(100.0, 1.0)

	day1 := mustET(2026, 3, 6, 10, 0)
	for i := 0; i < 10; i++ {
		bar := wpBar(day1.Add(time.Duration(i*5)*time.Minute), 90.0, 90.1, 89.9, 90.0, 1000)
		st = replayWPBar(t, s, ctx, "AAPL", st, bar, ind)
	}

	day2 := mustET(2026, 3, 9, 10, 0)
	for i := 0; i < 10; i++ {
		bar := wpBar(day2.Add(time.Duration(i*5)*time.Minute), 100.0, 100.1, 99.9, 100.0, 1000)
		st = replayWPBar(t, s, ctx, "AAPL", st, bar, ind)
	}

	wp := st.(*builtin.WhalePullbackState)
	require.NotEmpty(t, wp.HVNFingerprint(), "merged HVN set should be populated after multi-session warmup")
	require.True(t, wp.HVNContainsPrice(89.9, 90.1, 80.0), "day1 HVN around 90 should be present")

	day3 := mustET(2026, 3, 10, 10, 0)
	for i := 0; i < 10; i++ {
		bar := wpBar(day3.Add(time.Duration(i*5)*time.Minute), 105.0, 105.1, 104.9, 105.0, 1000)
		st = replayWPBar(t, s, ctx, "AAPL", st, bar, ind)
	}

	wp = st.(*builtin.WhalePullbackState)
	assert.False(t, wp.HVNContainsPrice(89.9, 90.1, 80.0), "after window roll, day1 HVN should be evicted from merged set")
	assert.True(t, wp.HVNContainsPrice(99.9, 100.1, 80.0), "day2 HVN should remain in merged set after re-keying")
}

func TestWhalePullback_EMATiming_TPredecessor(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_required"] = false
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	ind := wpIndicators(100.0, 1.0)
	st = buildLongTrend(t, s, ctx, st, 100.0, 1.0, wpBaseET)

	stable := []strat.Bar{
		wpBar(wpBaseET.Add(15*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(20*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(25*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(30*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(35*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
		wpBar(wpBaseET.Add(40*time.Minute), 102.0, 102.05, 101.95, 102.0, 100),
	}
	for _, b := range stable {
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}

	wp := st.(*builtin.WhalePullbackState)
	require.True(t, wp.EMAReady)
	emaPrev := wp.EMAValue

	closePrice := emaPrev - 0.5
	pullback := wpBar(wpBaseET.Add(45*time.Minute), closePrice, closePrice+0.05, closePrice-0.05, closePrice, 100)
	_, signals := feedWPBar(t, s, ctx, "AAPL", st, pullback, ind)
	assert.Empty(t, signals, "body closed below EMA(t-1); pullback rule must judge against EMA(t-1)")
}

func TestWhalePullback_VPRequiredFalse_DoesNotBlockOnEmptyProfile(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	params := wpParams()
	params["vp_required"] = false
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	ind := wpIndicators(100.0, 1.0)
	st = buildLongTrend(t, s, ctx, st, 100.0, 1.0, wpBaseET)

	emaPriceBars := []strat.Bar{
		wpBar(wpBaseET.Add(15*time.Minute), 101.9, 102.0, 101.85, 101.95, 100),
		wpBar(wpBaseET.Add(20*time.Minute), 101.95, 102.05, 101.9, 102.0, 100),
		wpBar(wpBaseET.Add(25*time.Minute), 102.0, 102.1, 101.95, 102.05, 100),
		wpBar(wpBaseET.Add(30*time.Minute), 102.05, 102.15, 102.0, 102.1, 100),
		wpBar(wpBaseET.Add(35*time.Minute), 102.1, 102.2, 102.05, 102.15, 100),
		wpBar(wpBaseET.Add(40*time.Minute), 102.15, 102.25, 102.1, 102.2, 100),
	}
	for _, b := range emaPriceBars {
		st2, _ := feedWPBar(t, s, ctx, "AAPL", st, b, ind)
		st = st2
	}

	wp := st.(*builtin.WhalePullbackState)
	require.True(t, wp.EMAReady)
	emaVal := wp.EMAValue

	wickLow := emaVal - 0.05
	pullback := wpBar(wpBaseET.Add(45*time.Minute), emaVal+0.1, emaVal+0.2, wickLow, emaVal+0.1, 100)
	_, signals := feedWPBar(t, s, ctx, "AAPL", st, pullback, ind)
	assert.NotEmpty(t, signals, "vp_required=false should allow entry on empty profile")
}

func TestWhalePullback_HaltedBar_NoDivideByZero(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)
	st, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)

	ind := wpIndicators(100.0, 1.0)
	bar := wpBar(wpBaseET, 100.0, 100.0, 100.0, 100.0, 1000)
	assert.NotPanics(t, func() {
		_, _ = feedWPBar(t, s, ctx, "AAPL", st, bar, ind)
	})
}

func TestWhalePullback_AllowedHoursBoundary(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ind := wpIndicators(100.0, 1.0)

	cases := []struct {
		name        string
		ts          time.Time
		shouldEnter bool
	}{
		{"09:34:59 ET blocked", mustET(2026, 3, 10, 9, 34).Add(59 * time.Second), false},
		{"09:35:00 ET allowed", mustET(2026, 3, 10, 9, 35), true},
		{"15:29:59 ET allowed", mustET(2026, 3, 10, 15, 29).Add(59 * time.Second), true},
		{"15:30:00 ET blocked", mustET(2026, 3, 10, 15, 30), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := newTestContext(c.ts)
			params := wpParams()
			params["vp_required"] = false
			st, err := s.Init(ctx, "AAPL", params, nil)
			require.NoError(t, err)

			wp := st.(*builtin.WhalePullbackState)
			wp.Phase = builtin.WPPhasePullbackArmed
			wp.TrendDirection = "bullish"
			wp.TrendBars = 5
			wp.QualifiedBreak = true
			wp.EMAReady = true
			wp.EMAValue = 100.0
			wp.HasPrevBar = true
			wp.PrevBar = wpBar(c.ts.Add(-5*time.Minute), 100, 100.1, 99.9, 100, 100)

			bar := wpBar(c.ts, 100.05, 100.2, 99.95, 100.1, 100)
			_, signals := feedWPBar(t, s, ctx, "AAPL", wp, bar, ind)
			if c.shouldEnter {
				assert.NotEmpty(t, signals, "should enter at "+c.name)
			} else {
				assert.Empty(t, signals, "should not enter at "+c.name)
			}
		})
	}
}

func TestWhalePullback_ReplayThenLive_ParityWithLiveOnly(t *testing.T) {
	s := builtin.NewWhalePullbackStrategy()
	ctx := newTestContext(wpBaseET)

	day1 := mustET(2026, 3, 9, 10, 0)
	bars := []strat.Bar{}
	for i := 0; i < 12; i++ {
		bars = append(bars, wpBar(day1.Add(time.Duration(i*5)*time.Minute), 100.0, 100.05, 99.95, 100.0, 1000))
	}
	for i := 0; i < 12; i++ {
		bars = append(bars, wpBar(wpBaseET.Add(time.Duration(i*5)*time.Minute), 100.0, 100.1, 99.9, 100.0, 100))
	}
	ind := wpIndicators(100.0, 1.0)

	mixedSt, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)
	for i, b := range bars {
		if i < 12 {
			mixedSt = replayWPBar(t, s, ctx, "AAPL", mixedSt, b, ind)
		} else {
			st2, _ := feedWPBar(t, s, ctx, "AAPL", mixedSt, b, ind)
			mixedSt = st2
		}
	}

	liveSt, err := s.Init(ctx, "AAPL", wpParams(), nil)
	require.NoError(t, err)
	for _, b := range bars {
		liveSt = replayWPBar(t, s, ctx, "AAPL", liveSt, b, ind)
	}

	mixed := mixedSt.(*builtin.WhalePullbackState)
	live := liveSt.(*builtin.WhalePullbackState)

	assert.InDelta(t, live.EMAValue, mixed.EMAValue, 1e-9, "EMA must be byte-equal across replay→live and replay-only")
	assert.Equal(t, live.HVNFingerprint(), mixed.HVNFingerprint(), "HVN merged set must match")
}
