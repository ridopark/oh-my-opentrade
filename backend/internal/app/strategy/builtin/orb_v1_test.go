package builtin_test

import (
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
	assert.Equal(t, 30, s.WarmupBars())
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
	retestBar := strat.Bar{Time: retestT, Open: 104, High: 104, Low: 101, Close: 103, Volume: 20}
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
	assert.Equal(t, "ORB Break & Retest", sig.Tags["trigger"])
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
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT, Open: 95, High: 99, Low: 95, Close: 98, Volume: 20}, st)
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
	st, signals, err := s.OnBar(ctx, "AAPL", strat.Bar{Time: retestT, Open: 104, High: 104, Low: 101, Close: 103, Volume: 20}, st)
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

func TestORBStrategy_ImplementsInterface(t *testing.T) {
	var _ strat.Strategy = (*builtin.ORBStrategy)(nil)
}
