package builtin_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func brParams() map[string]any {
	return map[string]any{
		"pivot_lookback":      2,
		"vol_surge_mult":      2.0,
		"body_range_ratio":    0.7,
		"atr_breakout_mult":   1.5,
		"breakout_buffer_atr": 0.2,
		"max_wick_ratio":      0.3,
		"retest_band_atr":     0.15,
		"retest_expiry_bars":  20,
		"invalidation_atr":    0.5,
		"fib_confluence_atr":  0.1,
		"engulf_body_mult":    2.0,
		"stop_mode":           "retest_low",
		"tp1_mode":            "breakout_peak",
		"tp2_mode":            "fib_1618_ext",
		"min_rr_ratio":        2.0,
		"ai_enabled":          false,
		"cooldown_seconds":    300,
		"max_trades_per_day":  5,
	}
}

func brIndicators() strat.IndicatorData {
	return strat.IndicatorData{
		ATR:       1.0,
		VolumeSMA: 100,
		AnchorRegimes: map[string]strat.AnchorRegime{
			"5m": {Type: "TREND", Strength: 0.8},
		},
	}
}

var brBaseTime = time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)

func brBar(minuteOffset int, open, high, low, close, volume float64) strat.Bar {
	return strat.Bar{
		Time:   brBaseTime.Add(time.Duration(minuteOffset) * time.Minute),
		Open:   open,
		High:   high,
		Low:    low,
		Close:  close,
		Volume: volume,
	}
}

func feedBRBar(t *testing.T, s *builtin.BreakRetestStrategy, ctx *testContext, symbol string, st strat.State, bar strat.Bar, ind strat.IndicatorData) (strat.State, []strat.Signal) {
	t.Helper()
	ctx.now = bar.Time
	brSt := st.(*builtin.BreakRetestState)
	brSt.SetIndicators(ind)
	st2, signals, err := s.OnBar(ctx, symbol, bar, st)
	require.NoError(t, err)
	return st2, signals
}

func replayBRBar(t *testing.T, s *builtin.BreakRetestStrategy, ctx *testContext, symbol string, st strat.State, bar strat.Bar, ind strat.IndicatorData) strat.State {
	t.Helper()
	ctx.now = bar.Time
	st2, err := s.ReplayOnBar(ctx, symbol, bar, st, ind)
	require.NoError(t, err)
	return st2
}

func TestBreakRetestStrategy_Meta(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	meta := s.Meta()
	assert.Equal(t, "break_retest_v1", meta.ID.String())
	assert.Equal(t, "1.0.0", meta.Version.String())
	assert.Equal(t, "Break & Retest", meta.Name)
	assert.Equal(t, "system", meta.Author)
}

func TestBreakRetestStrategy_WarmupBars(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	assert.Equal(t, 50, s.WarmupBars())
}

func TestBreakRetestStrategy_Init_Fresh(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "BTC/USD", brParams(), nil)
	require.NoError(t, err)
	require.NotNil(t, st)
	_, ok := st.(*builtin.BreakRetestState)
	assert.True(t, ok, "Init should return *BreakRetestState")
}

func TestBreakRetestStrategy_ImplementsInterface(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	var _ strat.Strategy = s
	var _ strat.ReplayableStrategy = s
}

func TestBreakRetestStrategy_PivotDetection(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)
	ind := brIndicators()

	st, err := s.Init(ctx, "BTC/USD", brParams(), nil)
	require.NoError(t, err)

	// pivot_lookback=2 needs 2*2+1=5 bars. Pattern: ascending to peak(105) then descending.
	bars := []strat.Bar{
		brBar(0, 99, 100, 98, 100, 50),
		brBar(5, 100, 102, 99, 102, 50),
		brBar(10, 102, 105, 101, 104, 50),
		brBar(15, 104, 103, 100, 101, 50),
		brBar(20, 101, 101, 98, 99, 50),
	}

	for _, bar := range bars {
		st = replayBRBar(t, s, ctx, "BTC/USD", st, bar, ind)
	}

	brSt := st.(*builtin.BreakRetestState)
	assert.NotEmpty(t, brSt.SwingPoints, "should detect swing points after 5 bars with clear peak/trough")
}

func TestBreakRetestStrategy_MarketStructure_Bullish(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)
	ind := brIndicators()

	params := brParams()
	params["pivot_lookback"] = 2

	st, err := s.Init(ctx, "BTC/USD", params, nil)
	require.NoError(t, err)

	// HH/HL pattern: Wave1 trough@95→peak@105, Wave2 trough@100(HL)→peak@110(HH)
	bars := []strat.Bar{
		brBar(0, 100, 101, 99, 100, 50),
		brBar(5, 100, 100, 96, 97, 50),
		brBar(10, 97, 97, 95, 95, 50),
		brBar(15, 95, 99, 95, 98, 50),
		brBar(20, 98, 101, 97, 100, 50),
		brBar(25, 100, 103, 99, 102, 50),
		brBar(30, 102, 105, 101, 104, 50),
		brBar(35, 104, 104, 100, 101, 50),
		brBar(40, 101, 101, 98, 99, 50),
		brBar(45, 99, 100, 98, 99, 50),
		brBar(50, 99, 100, 100, 100, 50),
		brBar(55, 100, 103, 99, 102, 50),
		brBar(60, 102, 104, 101, 103, 50),
		brBar(65, 103, 107, 102, 106, 50),
		brBar(70, 106, 110, 105, 109, 50),
		brBar(75, 109, 109, 104, 105, 50),
		brBar(80, 105, 106, 102, 103, 50),
	}

	for _, bar := range bars {
		st = replayBRBar(t, s, ctx, "BTC/USD", st, bar, ind)
	}

	brSt := st.(*builtin.BreakRetestState)
	assert.Equal(t, "bullish", brSt.TrendDirection, "HH/HL pattern should classify as bullish")
}

func TestBreakRetestStrategy_MarketStructure_Bearish(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)
	ind := brIndicators()

	params := brParams()
	params["pivot_lookback"] = 2

	st, err := s.Init(ctx, "BTC/USD", params, nil)
	require.NoError(t, err)

	// LH/LL pattern: Wave1 peak@110→trough@100, Wave2 peak@105(LH)→trough@95(LL)
	bars := []strat.Bar{
		brBar(0, 105, 106, 104, 105, 50),
		brBar(5, 105, 108, 104, 107, 50),
		brBar(10, 107, 110, 106, 109, 50),
		brBar(15, 109, 109, 103, 104, 50),
		brBar(20, 104, 105, 101, 102, 50),
		brBar(25, 102, 103, 100, 101, 50),
		brBar(30, 101, 101, 100, 100, 50),
		brBar(35, 100, 103, 100, 102, 50),
		brBar(40, 102, 104, 101, 103, 50),
		brBar(45, 103, 104, 102, 103, 50),
		brBar(50, 103, 105, 102, 104, 50),
		brBar(55, 104, 104, 100, 101, 50),
		brBar(60, 101, 101, 97, 98, 50),
		brBar(65, 98, 99, 96, 97, 50),
		brBar(70, 97, 97, 95, 95, 50),
		brBar(75, 95, 98, 95, 97, 50),
		brBar(80, 97, 99, 96, 98, 50),
	}

	for _, bar := range bars {
		st = replayBRBar(t, s, ctx, "BTC/USD", st, bar, ind)
	}

	brSt := st.(*builtin.BreakRetestState)
	assert.Equal(t, "bearish", brSt.TrendDirection, "LH/LL pattern should classify as bearish")
}

func TestBreakRetestStrategy_FullCycle_BullishEntry(t *testing.T) {
	// Set up state directly at WaitingRetest phase, then feed bars
	// satisfying retest zone + fib confluence + engulfing conditions.
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)
	ind := brIndicators()

	st, err := s.Init(ctx, "BTC/USD", brParams(), nil)
	require.NoError(t, err)

	brSt := st.(*builtin.BreakRetestState)
	brSt.Phase = builtin.BRPhaseWaitingRetest
	brSt.TrendDirection = "bullish"
	brSt.BreakoutLevel = 100.0
	brSt.BreakoutSide = strat.SideBuy
	brSt.BreakoutBar = strat.Bar{
		Time: brBaseTime.Add(-10 * time.Minute), Open: 99, High: 104, Low: 98.5, Close: 103, Volume: 250,
	}
	brSt.BreakoutVolume = 250
	// Fib: swingHigh=104, swingLow=96 → fib50 = 104-(104-96)*0.5 = 100.0 = breakoutLevel
	brSt.SwingLowOfMove = 96.0
	brSt.BarsSinceBreakout = 3
	// classifyTrend runs during OnBar — need HH/HL swing points to preserve bullish.
	brSt.SwingPoints = []builtin.SwingPoint{
		{Price: 90.0, Time: brBaseTime.Add(-40 * time.Minute), IsHigh: false},
		{Price: 99.0, Time: brBaseTime.Add(-30 * time.Minute), IsHigh: true},
		{Price: 93.0, Time: brBaseTime.Add(-20 * time.Minute), IsHigh: false},
		{Price: 104.0, Time: brBaseTime.Add(-10 * time.Minute), IsHigh: true},
	}
	brSt.HasPrevBar = true
	brSt.PrevBar = strat.Bar{
		Time: brBaseTime.Add(-5 * time.Minute), Open: 100.5, High: 100.6, Low: 99.9, Close: 100.0, Volume: 80,
	}

	// Entry bar: Low=99.9 in retest zone [99.85,100.15] ✓
	// Engulfing: body=1.25 >= 2*0.5, bearish→bullish, engulfs ✓
	// Fib: fib50=100 = level ✓
	// R:R: stop=99.8, TP1=104, risk=1.4, reward=2.8, ratio=2.0 ✓
	entryBar := brBar(0, 99.95, 101.5, 99.9, 101.2, 120)

	st2, signals := feedBRBar(t, s, ctx, "BTC/USD", brSt, entryBar, ind)
	require.NotEmpty(t, signals, "should emit entry signal on valid retest + engulfing")

	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideBuy, sig.Side)
	assert.Contains(t, sig.Tags, "ref_price")
	assert.Equal(t, "break_retest", sig.Tags["setup"])
	assert.Equal(t, "bullish", sig.Tags["trend"])

	brSt2 := st2.(*builtin.BreakRetestState)
	assert.Equal(t, strat.SideBuy, brSt2.PendingEntry)
	assert.Equal(t, 1, brSt2.TradesToday)
}

func TestBreakRetestStrategy_InvalidatesOnExpiry(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)
	ind := brIndicators()

	st, err := s.Init(ctx, "BTC/USD", brParams(), nil)
	require.NoError(t, err)

	brSt := st.(*builtin.BreakRetestState)
	brSt.Phase = builtin.BRPhaseWaitingRetest
	brSt.TrendDirection = "bullish"
	brSt.BreakoutLevel = 100.0
	brSt.BreakoutSide = strat.SideBuy
	brSt.BarsSinceBreakout = 20

	bar := brBar(0, 101, 102, 100.5, 101.5, 50)
	st2, signals := feedBRBar(t, s, ctx, "BTC/USD", brSt, bar, ind)

	assert.Empty(t, signals, "should not emit signals on expiry")
	brSt2 := st2.(*builtin.BreakRetestState)
	assert.Equal(t, builtin.BRPhaseIdle, brSt2.Phase, "phase should reset to Idle after expiry")
}

func TestBreakRetestStrategy_RejectsWeakBreakout(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)
	ind := brIndicators()

	st, err := s.Init(ctx, "BTC/USD", brParams(), nil)
	require.NoError(t, err)

	brSt := st.(*builtin.BreakRetestState)
	brSt.Phase = builtin.BRPhaseLevelDetected
	brSt.TrendDirection = "bullish"
	brSt.BreakoutLevel = 100.0
	brSt.BreakoutSide = strat.SideBuy
	brSt.HasPrevBar = true
	brSt.PrevBar = brBar(-5, 99, 100, 98, 99.5, 50)

	weakBar := brBar(0, 99.5, 102, 99, 101.5, 50) // volume=50 < 2.0*100(VolumeSMA)

	st2, signals := feedBRBar(t, s, ctx, "BTC/USD", brSt, weakBar, ind)
	assert.Empty(t, signals, "should not emit signal for weak breakout")

	brSt2 := st2.(*builtin.BreakRetestState)
	assert.NotEqual(t, builtin.BRPhaseBreakoutConfirmed, brSt2.Phase,
		"phase should NOT advance to BreakoutConfirmed with low volume")
}

func TestBreakRetestStrategy_OnEvent_FillConfirmation(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)

	st, err := s.Init(ctx, "BTC/USD", brParams(), nil)
	require.NoError(t, err)

	brSt := st.(*builtin.BreakRetestState)
	brSt.PendingEntry = strat.SideBuy
	brSt.PendingEntryAt = brBaseTime

	evt := strat.FillConfirmation{
		Symbol:   "BTC/USD",
		Side:     strat.SideBuy,
		Quantity: 0.1,
		Price:    100.0,
	}

	st2, signals, err := s.OnEvent(ctx, "BTC/USD", evt, brSt)
	require.NoError(t, err)
	assert.Empty(t, signals)

	brSt2 := st2.(*builtin.BreakRetestState)
	assert.Equal(t, strat.SideBuy, brSt2.PositionSide, "position should be set after fill")
	assert.Empty(t, string(brSt2.PendingEntry), "pending entry should be cleared after fill")
}

func TestBreakRetestStrategy_OnEvent_EntryRejection(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)

	st, err := s.Init(ctx, "BTC/USD", brParams(), nil)
	require.NoError(t, err)

	brSt := st.(*builtin.BreakRetestState)
	brSt.PendingEntry = strat.SideBuy
	brSt.PendingEntryAt = brBaseTime

	evt := strat.EntryRejection{
		Symbol: "BTC/USD",
		Side:   strat.SideBuy,
		Reason: "risk_limit",
	}

	st2, signals, err := s.OnEvent(ctx, "BTC/USD", evt, brSt)
	require.NoError(t, err)
	assert.Empty(t, signals)

	brSt2 := st2.(*builtin.BreakRetestState)
	assert.Empty(t, string(brSt2.PendingEntry), "pending entry should be cleared after rejection")
}

func TestBreakRetestStrategy_MarshalUnmarshal(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)

	st, err := s.Init(ctx, "BTC/USD", brParams(), nil)
	require.NoError(t, err)

	brSt := st.(*builtin.BreakRetestState)
	brSt.Phase = builtin.BRPhaseWaitingRetest
	brSt.TrendDirection = "bullish"
	brSt.BreakoutLevel = 100.5
	brSt.BreakoutSide = strat.SideBuy
	brSt.BreakoutVolume = 250.0
	brSt.SwingLowOfMove = 95.0
	brSt.BarsSinceBreakout = 7
	brSt.StopPrice = 99.0
	brSt.TP1Price = 103.0
	brSt.TP2Price = 106.0
	brSt.PositionSide = strat.SideBuy
	brSt.TradesToday = 2
	brSt.CooldownUntil = brBaseTime.Add(5 * time.Minute)
	brSt.LastAIVerdict = "bull"
	brSt.LastAIConfidence = 0.85
	brSt.SwingPoints = []builtin.SwingPoint{
		{Price: 95.0, Time: brBaseTime.Add(-30 * time.Minute), IsHigh: false},
		{Price: 105.0, Time: brBaseTime.Add(-15 * time.Minute), IsHigh: true},
	}
	brSt.RecentBars = []strat.Bar{
		brBar(-10, 99, 100, 98, 99.5, 50),
		brBar(-5, 99.5, 101, 99, 100.5, 60),
	}
	brSt.HasPrevBar = true
	brSt.PrevBar = brBar(-5, 99.5, 101, 99, 100.5, 60)

	data, err := brSt.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &builtin.BreakRetestState{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, brSt.Phase, restored.Phase)
	assert.Equal(t, brSt.TrendDirection, restored.TrendDirection)
	assert.Equal(t, brSt.BreakoutLevel, restored.BreakoutLevel)
	assert.Equal(t, brSt.BreakoutSide, restored.BreakoutSide)
	assert.Equal(t, brSt.BreakoutVolume, restored.BreakoutVolume)
	assert.Equal(t, brSt.SwingLowOfMove, restored.SwingLowOfMove)
	assert.Equal(t, brSt.BarsSinceBreakout, restored.BarsSinceBreakout)
	assert.Equal(t, brSt.StopPrice, restored.StopPrice)
	assert.Equal(t, brSt.TP1Price, restored.TP1Price)
	assert.Equal(t, brSt.TP2Price, restored.TP2Price)
	assert.Equal(t, brSt.PositionSide, restored.PositionSide)
	assert.Equal(t, brSt.TradesToday, restored.TradesToday)
	assert.Equal(t, brSt.LastAIVerdict, restored.LastAIVerdict)
	assert.InDelta(t, brSt.LastAIConfidence, restored.LastAIConfidence, 0.001)
	assert.Len(t, restored.SwingPoints, 2)
	assert.Len(t, restored.RecentBars, 2)
	assert.True(t, restored.HasPrevBar)
}

func TestBreakRetestStrategy_CooldownPreventsEntry(t *testing.T) {
	s := builtin.NewBreakRetestStrategy()
	ctx := newTestContext(brBaseTime)
	ind := brIndicators()

	st, err := s.Init(ctx, "BTC/USD", brParams(), nil)
	require.NoError(t, err)

	brSt := st.(*builtin.BreakRetestState)
	brSt.Phase = builtin.BRPhaseWaitingRetest
	brSt.TrendDirection = "bullish"
	brSt.BreakoutLevel = 100.0
	brSt.BreakoutSide = strat.SideBuy
	brSt.BreakoutVolume = 250
	brSt.SwingLowOfMove = 98.0
	brSt.BarsSinceBreakout = 3
	brSt.HasPrevBar = true
	brSt.PrevBar = strat.Bar{
		Time: brBaseTime.Add(-5 * time.Minute), Open: 100.5, High: 100.6, Low: 99.9, Close: 100.0, Volume: 80,
	}
	brSt.BreakoutBar = strat.Bar{
		Time: brBaseTime.Add(-10 * time.Minute), Open: 99, High: 102, Low: 98.5, Close: 101.5, Volume: 250,
	}

	brSt.CooldownUntil = brBaseTime.Add(10 * time.Minute)

	entryBar := brBar(0, 99.95, 101.5, 99.9, 101.2, 120)
	_, signals := feedBRBar(t, s, ctx, "BTC/USD", brSt, entryBar, ind)

	assert.Empty(t, signals, "should not emit signals during cooldown")
}
