package builtin_test

import (
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testContext implements strategy.Context for testing.
type testContext struct {
	now    time.Time
	logger *slog.Logger
	events []any
}

func newTestContext(now time.Time) *testContext {
	return &testContext{
		now:    now,
		logger: slog.Default(),
	}
}

func (c *testContext) Now() time.Time       { return c.now }
func (c *testContext) Logger() *slog.Logger { return c.logger }
func (c *testContext) EmitDomainEvent(evt any) error {
	c.events = append(c.events, evt)
	return nil
}

// orbParams returns default ORB config as a params map.
func orbParams() map[string]any {
	return map[string]any{
		"orb_window_minutes":       30,
		"min_rvol":                 1.5,
		"min_confidence":           0.65,
		"breakout_confirm_bps":     2,
		"touch_tolerance_bps":      2,
		"hold_confirm_bps":         0,
		"max_retest_bars":          15,
		"allow_missing_range_bars": 1,
		"max_signals_per_session":  1,
	}
}

func TestORBStrategy_Meta(t *testing.T) {
	s := builtin.NewORBStrategy()
	meta := s.Meta()
	assert.Equal(t, "orb_break_retest", meta.ID.String())
	assert.Equal(t, "1.0.0", meta.Version.String())
	assert.Equal(t, "ORB Break & Retest", meta.Name)
	assert.Equal(t, "system", meta.Author)
}

func TestORBStrategy_WarmupBars(t *testing.T) {
	s := builtin.NewORBStrategy()
	assert.Equal(t, 0, s.WarmupBars())
}

func TestORBStrategy_ReplayOnBar_ReconstructsState(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	indicators := strat.IndicatorData{Volume: 10, VolumeSMA: 10}

	// Replay 30 range bars (9:30-9:59 UTC=14:30-14:59)
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
		st, err = s.ReplayOnBar(ctx, "AAPL", bar, st, indicators)
		require.NoError(t, err)
	}

	// Replay transition bar
	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	st, err = s.ReplayOnBar(ctx, "AAPL", strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st, indicators)
	require.NoError(t, err)

	// Replay breakout bar with high volume
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	highVolInd := strat.IndicatorData{Volume: 50, VolumeSMA: 10}
	st, err = s.ReplayOnBar(ctx, "AAPL", strat.Bar{Time: breakT, Open: 100, High: 104, Low: 100, Close: 104, Volume: 50}, st, highVolInd)
	require.NoError(t, err)

	// Replay retest bar — signal suppressed, state cycles back to RANGE_SET
	retestT := breakT.Add(time.Minute)
	retestInd := strat.IndicatorData{Volume: 20, VolumeSMA: 10}
	st, err = s.ReplayOnBar(ctx, "AAPL", strat.Bar{Time: retestT, Open: 101, High: 104, Low: 101, Close: 103, Volume: 20}, st, retestInd)
	require.NoError(t, err)

	// After replay-suppressed signal, state cycles to RANGE_SET.
	// A live breakout bar triggers AWAITING_RETEST (no signal yet).
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT.Add(time.Minute), Open: 103, High: 105, Low: 102, Close: 104, Volume: 50}, st)
	require.NoError(t, err)
	assert.Empty(t, signals, "breakout bar does not emit signal")

	// Live retest bar — should now produce a signal (replay didn't consume session)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10})
	st, signals, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT.Add(2 * time.Minute), Open: 101, High: 104, Low: 101, Close: 103, Volume: 20}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1, "live signal should fire after replay-suppressed cycle")
	assert.Equal(t, strat.SignalEntry, signals[0].Type)
	assert.Equal(t, strat.SideBuy, signals[0].Side)
}

func TestORBStrategy_ReplayOnBar_PartialReplay_ThenLiveSignal(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	indicators := strat.IndicatorData{Volume: 10, VolumeSMA: 10}

	// Replay only range bars (no breakout yet)
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
		st, err = s.ReplayOnBar(ctx, "AAPL", bar, st, indicators)
		require.NoError(t, err)
	}

	// Replay transition to RANGE_SET
	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	st, err = s.ReplayOnBar(ctx, "AAPL", strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st, indicators)
	require.NoError(t, err)

	// Now live bars should be able to produce a signal (range is set, breakout hasn't happened)
	// Live breakout
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: breakT, Open: 100, High: 104, Low: 100, Close: 104, Volume: 50}, st)
	require.NoError(t, err)
	assert.Empty(t, signals, "breakout bar should not emit signal yet")

	// Live retest
	retestT := breakT.Add(time.Minute)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10})
	st, signals, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT, Open: 101, High: 104, Low: 101, Close: 103, Volume: 20}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1, "live retest after replayed range should produce signal")
	assert.Equal(t, strat.SignalEntry, signals[0].Type)
	assert.Equal(t, strat.SideBuy, signals[0].Side)
}

func TestORBStrategy_ImplementsReplayableStrategy(t *testing.T) {
	var _ strat.ReplayableStrategy = (*builtin.ORBStrategy)(nil)
}

func TestORBStrategy_Init_Fresh(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)
	require.NotNil(t, st)
	_, ok := st.(*builtin.ORBState)
	assert.True(t, ok, "Init should return *ORBState")
}

func TestORBStrategy_Init_WithPriorState(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())

	// Create initial state
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	// Init with prior state should reuse tracker
	st2, err := s.Init(ctx, "AAPL", orbParams(), st)
	require.NoError(t, err)
	require.NotNil(t, st2)
}

func TestORBStrategy_OnBar_NoSignalDuringRange(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	// Feed bars during ORB range window (9:30-10:00 ET = 14:30-15:00 UTC)
	var signals []strat.Signal
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}

		orbSt := st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})

		st, signals, err = s.OnBar(ctx, "AAPL", bar, st)
		require.NoError(t, err)
		assert.Empty(t, signals, "no signals during range formation")
	}
}

func TestORBStrategy_FullFlow_LongEntrySignal(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	// Phase 1: Form the opening range (30 bars at 14:30-14:59 UTC)
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
		orbSt := st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
		st, _, err = s.OnBar(ctx, "AAPL", bar, st)
		require.NoError(t, err)
	}

	// Phase 2: First bar after range window → transitions to RANGE_SET
	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	bar := strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
	st, signals, err := s.OnBar(ctx, "AAPL", bar, st)
	require.NoError(t, err)
	assert.Empty(t, signals)

	// Phase 3: Breakout bar (close > orbHigh with high RVOL)
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	breakBar := strat.Bar{Time: breakT, Open: 100, High: 104, Low: 100, Close: 104, Volume: 50}
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10})
	st, signals, err = s.OnBar(ctx, "AAPL", breakBar, st)
	require.NoError(t, err)
	assert.Empty(t, signals, "breakout bar should not emit signal yet")

	// Phase 4: Retest bar (touches ORB high, holds above) → should emit entry signal
	retestT := breakT.Add(time.Minute)
	retestBar := strat.Bar{Time: retestT, Open: 101, High: 104, Low: 101, Close: 103, Volume: 20}
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10})
	st, signals, err = s.OnBar(ctx, "AAPL", retestBar, st)
	require.NoError(t, err)

	require.Len(t, signals, 1, "retest confirmation should emit exactly one signal")
	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideBuy, sig.Side)
	assert.Equal(t, "AAPL", sig.Symbol)
	assert.GreaterOrEqual(t, sig.Strength, 0.50)
	assert.LessOrEqual(t, sig.Strength, 1.0)
	assert.Equal(t, "orb_break_retest", sig.Tags["trigger"])
	assert.Contains(t, sig.Tags, "orb_high")
	assert.Contains(t, sig.Tags, "orb_low")
	assert.Contains(t, sig.Tags, "rvol")
}

func TestORBStrategy_FullFlow_ShortEntrySignal(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	// Form range
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
		orbSt := st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
		st, _, err = s.OnBar(ctx, "AAPL", bar, st)
		require.NoError(t, err)
	}

	// Transition to RANGE_SET
	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
	require.NoError(t, err)

	// Short breakout (close < orbLow with high RVOL)
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: breakT, Open: 100, High: 100, Low: 95, Close: 95, Volume: 50}, st)
	require.NoError(t, err)

	// Retest (touches orbLow from below, holds under)
	retestT := breakT.Add(time.Minute)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10})
	// Short retest: touches ORL (high=99), closes bearish below ORL (open=99, close=97)
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT, Open: 99, High: 99, Low: 95, Close: 97, Volume: 20}, st)
	require.NoError(t, err)

	require.Len(t, signals, 1)
	assert.Equal(t, strat.SignalEntry, signals[0].Type)
	assert.Equal(t, strat.SideSell, signals[0].Side)
}

func TestORBStrategy_OnEvent_Noop(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	st2, signals, err := s.OnEvent(ctx, "AAPL", "some_event", st)
	require.NoError(t, err)
	assert.Equal(t, st, st2)
	assert.Nil(t, signals)
}

func TestORBState_MarshalUnmarshal(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{
		RSI: 55, StochK: 70, StochD: 65,
		EMA9: 150, EMA21: 148, VWAP: 149,
		Volume: 5000, VolumeSMA: 4500,
	})

	data, err := orbSt.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	// Unmarshal into a new state
	restored := &builtin.ORBState{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)
	assert.Equal(t, "AAPL", restored.Symbol)
	assert.Equal(t, 55.0, restored.Indicators.RSI)
	assert.Equal(t, 5000.0, restored.Indicators.Volume)
}

func TestORBStrategy_NoSignalAfterDone(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	// Run full flow to get a signal
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		orbSt := st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
		st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
		require.NoError(t, err)
	}

	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
	require.NoError(t, err)

	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: breakT, Open: 100, High: 104, Low: 100, Close: 104, Volume: 50}, st)
	require.NoError(t, err)

	retestT := breakT.Add(time.Minute)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT, Open: 101, High: 104, Low: 101, Close: 103, Volume: 20}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1) // Got the signal

	// Additional bars should produce no more signals
	for i := 2; i < 10; i++ {
		bt := retestT.Add(time.Duration(i) * time.Minute)
		orbSt = st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
		st, signals, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: bt, Open: 103, High: 105, Low: 102, Close: 104, Volume: 10}, st)
		require.NoError(t, err)
		assert.Empty(t, signals, "no signals after DONE_FOR_SESSION")
	}
}

func TestORBStrategy_CyclesBackToRangeSet_MultipleSignals(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	params := orbParams()
	params["max_signals_per_session"] = 3
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	// Form range: 30 bars
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		orbSt := st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
		st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
		require.NoError(t, err)
	}

	// Transition to RANGE_SET
	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
	require.NoError(t, err)

	baseTime := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)

	for sig := 0; sig < 2; sig++ {
		offset := time.Duration(sig*10) * time.Minute

		// Breakout
		orbSt = st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10})
		st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: baseTime.Add(offset), Open: 100, High: 104, Low: 100, Close: 104, Volume: 50}, st)
		require.NoError(t, err)
		assert.Empty(t, signals)

		// Retest
		orbSt = st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10})
		st, signals, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: baseTime.Add(offset + time.Minute), Open: 101, High: 104, Low: 101, Close: 103, Volume: 20}, st)
		require.NoError(t, err)
		require.Len(t, signals, 1, "signal #%d should fire", sig+1)
		assert.Equal(t, strat.SignalEntry, signals[0].Type)
	}
}

func TestORBStrategy_RetestTimeout_CyclesBack(t *testing.T) {
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	params := orbParams()
	params["max_retest_bars"] = 3
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	// Form range
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		orbSt := st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
		st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
		require.NoError(t, err)
	}

	// Transition
	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
	require.NoError(t, err)

	// Breakout
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: breakT, Open: 100, High: 104, Low: 100, Close: 104, Volume: 50}, st)
	require.NoError(t, err)

	// 4 bars without retest — exceeds max_retest_bars=3, should cycle to RANGE_SET
	for i := 1; i <= 4; i++ {
		orbSt = st.(*builtin.ORBState)
		orbSt.SetIndicators(strat.IndicatorData{Volume: 10, VolumeSMA: 10})
		st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: breakT.Add(time.Duration(i) * time.Minute), Open: 104, High: 105, Low: 103, Close: 104, Volume: 10}, st)
		require.NoError(t, err)
	}

	// After timeout cycle, a new breakout+retest should produce a signal
	newBreakT := breakT.Add(5 * time.Minute)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: newBreakT, Open: 104, High: 108, Low: 104, Close: 108, Volume: 50}, st)
	require.NoError(t, err)

	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: newBreakT.Add(time.Minute), Open: 101, High: 108, Low: 101, Close: 103, Volume: 20}, st)
	require.NoError(t, err)
	require.Len(t, signals, 1, "signal should fire after retest timeout cycle")
}

func TestORBStrategy_ImplementsInterface(t *testing.T) {
	var _ strat.Strategy = (*builtin.ORBStrategy)(nil)
}

// runORBToRetest drives the ORB strategy through its full cycle
// (30 range bars → transition → breakout → retest) with the given
// AnchorRegimes set on each bar. Returns the final state and signals
// from the retest bar.
func runORBToRetest(t *testing.T, anchorRegimes map[string]strat.AnchorRegime) (strat.State, []strat.Signal) {
	t.Helper()
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", orbParams(), nil)
	require.NoError(t, err)

	indicators := strat.IndicatorData{Volume: 10, VolumeSMA: 10, AnchorRegimes: anchorRegimes}

	// Phase 1: Form the opening range (30 bars)
	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
		orbSt := st.(*builtin.ORBState)
		orbSt.SetIndicators(indicators)
		st, _, err = s.OnBar(ctx, "AAPL", bar, st)
		require.NoError(t, err)
	}

	// Phase 2: Transition to RANGE_SET
	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(indicators)
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
	require.NoError(t, err)

	// Phase 3: Breakout bar
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10, AnchorRegimes: anchorRegimes})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: breakT, Open: 100, High: 104, Low: 100, Close: 104, Volume: 50}, st)
	require.NoError(t, err)

	// Phase 4: Retest bar — this is where the signal (or gating) happens
	retestT := breakT.Add(time.Minute)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10, AnchorRegimes: anchorRegimes})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT, Open: 101, High: 104, Low: 101, Close: 103, Volume: 20}, st)
	require.NoError(t, err)
	return st, signals
}

func TestORBStrategy_AnchorRegimeGating_TrendAllows(t *testing.T) {
	regimes := map[string]strat.AnchorRegime{
		"5m": {Type: "TREND", Strength: 0.8},
	}
	_, signals := runORBToRetest(t, regimes)
	require.Len(t, signals, 1, "TREND anchor regime should allow signal")
	assert.Equal(t, strat.SignalEntry, signals[0].Type)
	assert.Equal(t, strat.SideBuy, signals[0].Side)
	assert.Equal(t, "TREND", signals[0].Tags["regime_anchor"])
}

func TestORBStrategy_AnchorRegimeGating_BalanceAllows(t *testing.T) {
	regimes := map[string]strat.AnchorRegime{
		"5m": {Type: "BALANCE", Strength: 0.5},
	}
	_, signals := runORBToRetest(t, regimes)
	require.Len(t, signals, 1, "BALANCE anchor regime should allow signal")
	assert.Equal(t, "BALANCE", signals[0].Tags["regime_anchor"])
}

func TestORBStrategy_AnchorRegimeGating_ReversalBlocks(t *testing.T) {
	regimes := map[string]strat.AnchorRegime{
		"5m": {Type: "REVERSAL", Strength: 0.9},
	}
	_, signals := runORBToRetest(t, regimes)
	assert.Empty(t, signals, "REVERSAL anchor regime should suppress signal")
}

func TestORBStrategy_AnchorRegimeGating_NilAllows(t *testing.T) {
	_, signals := runORBToRetest(t, nil)
	require.Len(t, signals, 1, "nil AnchorRegimes should allow signal (backward compat)")
	assert.Equal(t, "none", signals[0].Tags["regime_anchor"])
}

func runORBToRetestWithHTF(t *testing.T, htf map[string]strat.HTFIndicator, htfBiasEnabled bool) (strat.State, []strat.Signal) {
	t.Helper()
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	params := orbParams()
	params["htf_bias_enabled"] = htfBiasEnabled
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	indicators := strat.IndicatorData{Volume: 10, VolumeSMA: 10, HTF: htf}

	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
		orbSt := st.(*builtin.ORBState)
		orbSt.SetIndicators(indicators)
		st, _, err = s.OnBar(ctx, "AAPL", bar, st)
		require.NoError(t, err)
	}

	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(indicators)
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
	require.NoError(t, err)

	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10, HTF: htf})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: breakT, Open: 100, High: 104, Low: 100, Close: 104, Volume: 50}, st)
	require.NoError(t, err)

	retestT := breakT.Add(time.Minute)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10, HTF: htf})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT, Open: 101, High: 104, Low: 101, Close: 103, Volume: 20}, st)
	require.NoError(t, err)
	return st, signals
}

func TestORBStrategy_HTFBias_BullishAllowsLong(t *testing.T) {
	htf := map[string]strat.HTFIndicator{
		"1d": {EMA200: 200, Bias: "BULLISH"},
	}
	_, signals := runORBToRetestWithHTF(t, htf, true)
	require.Len(t, signals, 1, "BULLISH bias should allow LONG signal")
	assert.Equal(t, "BULLISH", signals[0].Tags["htf_bias"])
}

func TestORBStrategy_HTFBias_BearishBlocksLong(t *testing.T) {
	htf := map[string]strat.HTFIndicator{
		"1d": {EMA200: 200, Bias: "BEARISH"},
	}
	_, signals := runORBToRetestWithHTF(t, htf, true)
	assert.Empty(t, signals, "BEARISH bias should block LONG signal")
}

func TestORBStrategy_HTFBias_NeutralAllowsLong(t *testing.T) {
	htf := map[string]strat.HTFIndicator{
		"1d": {EMA200: 200, Bias: "NEUTRAL"},
	}
	_, signals := runORBToRetestWithHTF(t, htf, true)
	require.Len(t, signals, 1, "NEUTRAL bias should allow LONG signal")
}

func TestORBStrategy_HTFBias_DisabledAllowsAll(t *testing.T) {
	htf := map[string]strat.HTFIndicator{
		"1d": {EMA200: 200, Bias: "BEARISH"},
	}
	_, signals := runORBToRetestWithHTF(t, htf, false)
	require.Len(t, signals, 1, "disabled HTF bias should allow all signals")
	assert.Equal(t, "none", signals[0].Tags["htf_bias"])
}

func TestORBStrategy_HTFBias_MissingHTFBlocks(t *testing.T) {
	_, signals := runORBToRetestWithHTF(t, nil, true)
	assert.Empty(t, signals, "missing HTF data should BLOCK signal (fail-closed safety gate)")
}

func TestORBStrategy_HTFBias_EmptyBiasBlocks(t *testing.T) {
	htf := map[string]strat.HTFIndicator{
		"1d": {EMA200: 200, Bias: ""},
	}
	_, signals := runORBToRetestWithHTF(t, htf, true)
	assert.Empty(t, signals, "empty bias string should BLOCK signal (fail-closed)")
}

func TestORBStrategy_HTFBias_DisabledAllowsWithoutHTF(t *testing.T) {
	_, signals := runORBToRetestWithHTF(t, nil, false)
	require.Len(t, signals, 1, "disabled HTF bias should allow signal even without HTF data")
}

func runORBWithATR(t *testing.T, atrMultiplier float64, atrValue float64) (strat.State, []strat.Signal) {
	t.Helper()
	s := builtin.NewORBStrategy()
	ctx := newTestContext(time.Now())
	params := orbParams()
	params["atr_multiplier"] = atrMultiplier
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	indicators := strat.IndicatorData{Volume: 10, VolumeSMA: 10, ATR: atrValue}

	for i := 0; i < 30; i++ {
		bt := time.Date(2025, 3, 4, 14, 30+i, 0, 0, time.UTC)
		bar := strat.Bar{Time: bt, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}
		orbSt := st.(*builtin.ORBState)
		orbSt.SetIndicators(indicators)
		st, _, err = s.OnBar(ctx, "AAPL", bar, st)
		require.NoError(t, err)
	}

	postRange := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	orbSt := st.(*builtin.ORBState)
	orbSt.SetIndicators(indicators)
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: postRange, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}, st)
	require.NoError(t, err)

	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 50, VolumeSMA: 10, ATR: atrValue})
	st, _, err = s.OnBar(ctx, "AAPL", strat.Bar{Time: breakT, Open: 100, High: 104, Low: 100, Close: 104, Volume: 50}, st)
	require.NoError(t, err)

	retestT := breakT.Add(time.Minute)
	orbSt = st.(*builtin.ORBState)
	orbSt.SetIndicators(strat.IndicatorData{Volume: 20, VolumeSMA: 10, ATR: atrValue})
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT, Open: 101, High: 104, Low: 101, Close: 103, Volume: 20}, st)
	require.NoError(t, err)
	return st, signals
}

func TestORBStrategy_ATRStop_EmitsStopPriceTag(t *testing.T) {
	_, signals := runORBWithATR(t, 2.0, 1.5)
	require.Len(t, signals, 1)
	sp, ok := signals[0].Tags["stop_price"]
	require.True(t, ok, "signal should have stop_price tag when ATR multiplier > 0")
	assert.Contains(t, signals[0].Tags, "atr_stop_distance")

	spFloat := 0.0
	fmt.Sscanf(sp, "%f", &spFloat)
	assert.InDelta(t, 103.0-3.0, spFloat, 0.01, "stop_price = close(103) - ATR(1.5)*mult(2.0)")
}

func TestORBStrategy_ATRStop_ZeroMultiplierNoTag(t *testing.T) {
	_, signals := runORBWithATR(t, 0.0, 1.5)
	require.Len(t, signals, 1)
	_, ok := signals[0].Tags["stop_price"]
	assert.False(t, ok, "no stop_price tag when ATR multiplier is 0")
}

func TestORBStrategy_ATRStop_ZeroATRNoTag(t *testing.T) {
	_, signals := runORBWithATR(t, 2.0, 0.0)
	require.Len(t, signals, 1)
	_, ok := signals[0].Tags["stop_price"]
	assert.False(t, ok, "no stop_price tag when ATR is 0")
}
