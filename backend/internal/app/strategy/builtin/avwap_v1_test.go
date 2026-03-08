package builtin_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func avwapParams() map[string]any {
	return map[string]any{
		"breakout_enabled":   true,
		"hold_bars":          2,
		"volume_mult":        1.5,
		"bounce_enabled":     true,
		"rsi_bounce_max":     30.0,
		"exit_hold_bars":     2,
		"cooldown_seconds":   120,
		"max_trades_per_day": 3,
		"allow_regimes":      []any{"BALANCE", "REVERSAL"},
	}
}

func feedAVWAPBar(t *testing.T, s *builtin.AVWAPStrategy, ctx *testContext, symbol string, st strat.State, bar strat.Bar, ind strat.IndicatorData) (strat.State, []strat.Signal) {
	t.Helper()
	ctx.now = bar.Time
	avwapSt := st.(*builtin.AVWAPState)
	avwapSt.SetIndicators(ind)
	st2, signals, err := s.OnBar(ctx, symbol, bar, st)
	require.NoError(t, err)
	return st2, signals
}

func TestAVWAPStrategy_Meta(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	meta := s.Meta()
	assert.Equal(t, "avwap_v1", meta.ID.String())
	assert.Equal(t, "1.0.0", meta.Version.String())
	assert.Equal(t, "AVWAP Breakout/Bounce", meta.Name)
	assert.Equal(t, "system", meta.Author)
}

func TestAVWAPStrategy_WarmupBars(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	assert.Equal(t, 30, s.WarmupBars())
}

func TestAVWAPStrategy_Init_Fresh(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)
	require.NotNil(t, st)
	_, ok := st.(*builtin.AVWAPState)
	assert.True(t, ok, "Init should return *AVWAPState")
}

func TestAVWAPStrategy_Init_MultiAnchorsAndWarmup(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	params := avwapParams()
	params["anchors"] = []any{"pd_high", "pd_low", "session_open"}

	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	avwapSt := st.(*builtin.AVWAPState)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}
	for i := 0; i < 5; i++ {
		b := strat.Bar{Time: start.Add(time.Duration(i) * time.Minute), Open: 100, High: 110, Low: 90, Close: 105, Volume: 10}
		st, err = s.ReplayOnBar(ctx, "AAPL", b, st, ind)
		require.NoError(t, err)
	}

	values := avwapSt.Calc.Values()
	require.Len(t, values, 3)
	assert.Contains(t, values, "pd_high")
	assert.Contains(t, values, "pd_low")
	assert.Contains(t, values, "session_open")

	bars := []strat.Bar{
		{Time: start.Add(10 * time.Minute), Open: 100, High: 112, Low: 92, Close: 112, Volume: 10},
		{Time: start.Add(11 * time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10},
		{Time: start.Add(12 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20},
	}

	var signals []strat.Signal
	for i, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
		if i < len(bars)-1 {
			assert.Empty(t, signals)
		}
	}
	require.Len(t, signals, 1)
	assert.Contains(t, signals[0].Tags, "anchor")
}

func TestAVWAPStrategy_ImplementsInterface(t *testing.T) {
	var _ strat.Strategy = (*builtin.AVWAPStrategy)(nil)
}

func TestAVWAPStrategy_Breakout_Long(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	// 3 bars closing above AVWAP; only the last one has volume confirmation.
	bars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 92, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20},
	}

	var signals []strat.Signal
	for i, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
		if i < len(bars)-1 {
			assert.Empty(t, signals)
		}
	}

	require.Len(t, signals, 1)
	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideBuy, sig.Side)
	assert.Equal(t, "AAPL", sig.Symbol)
	assert.Equal(t, "avwap_v1:1.0.0:AAPL", sig.StrategyInstanceID.String())
	assert.Equal(t, "breakout", sig.Tags["mode"])
	assert.Equal(t, "avwap_breakout", sig.Tags["setup"])
	assert.Equal(t, "BALANCE", sig.Tags["regime_5m"])
	assert.Contains(t, sig.Tags, "ref_price")
	assert.Contains(t, sig.Tags, "anchor")
	assert.Contains(t, sig.Tags, "avwap")
	assert.Contains(t, sig.Tags, "vol_ratio")
	assert.Contains(t, sig.Tags, "hold_bars")
}

func TestAVWAPStrategy_Breakout_Short(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	bars := []strat.Bar{
		{Time: start, Open: 130, High: 135, Low: 110, Close: 110, Volume: 10},
		{Time: start.Add(time.Minute), Open: 110, High: 115, Low: 95, Close: 95, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 95, High: 100, Low: 80, Close: 80, Volume: 20},
	}

	var signals []strat.Signal
	for i, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
		if i < len(bars)-1 {
			assert.Empty(t, signals)
		}
	}

	require.Len(t, signals, 1)
	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideSell, sig.Side)
	assert.Equal(t, "breakout", sig.Tags["mode"])
	assert.Equal(t, "avwap_breakout", sig.Tags["setup"])
	assert.Contains(t, sig.Tags, "ref_price")
}

func TestAVWAPStrategy_Bounce_Long(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	// Seed AVWAP to 100.
	st, signals := feedAVWAPBar(t, s, ctx, "AAPL", st, strat.Bar{Time: start, Open: 100, High: 100, Low: 100, Close: 100, Volume: 10}, strat.IndicatorData{VolumeSMA: 10})
	assert.Empty(t, signals)

	ind := strat.IndicatorData{RSI: 20, VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.4}}}
	bounce := strat.Bar{Time: start.Add(time.Minute), Open: 99, High: 101, Low: 99, Close: 100.5, Volume: 10}
	st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, bounce, ind)

	require.Len(t, signals, 1)
	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideBuy, sig.Side)
	assert.Equal(t, "bounce", sig.Tags["mode"])
	assert.Equal(t, "avwap_bounce", sig.Tags["setup"])
	assert.Contains(t, sig.Tags, "ref_price")
	assert.Contains(t, sig.Tags, "rsi")
}

func TestAVWAPStrategy_Bounce_Short(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	// Seed AVWAP to 100.
	st, signals := feedAVWAPBar(t, s, ctx, "AAPL", st, strat.Bar{Time: start, Open: 100, High: 100, Low: 100, Close: 100, Volume: 10}, strat.IndicatorData{VolumeSMA: 10})
	assert.Empty(t, signals)

	ind := strat.IndicatorData{RSI: 80, VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.4}}}
	bounce := strat.Bar{Time: start.Add(time.Minute), Open: 101, High: 101, Low: 99, Close: 99.5, Volume: 10}
	st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, bounce, ind)

	require.Len(t, signals, 1)
	sig := signals[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideSell, sig.Side)
	assert.Equal(t, "bounce", sig.Tags["mode"])
	assert.Equal(t, "avwap_bounce", sig.Tags["setup"])
	assert.Contains(t, sig.Tags, "ref_price")
	assert.Contains(t, sig.Tags, "rsi")
}

func TestAVWAPStrategy_RegimeGating_ReversalBlocksBreakout(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "REVERSAL", Strength: 0.9}}}
	bars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 92, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20},
	}

	for _, b := range bars {
		st, signals := feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
		assert.Empty(t, signals)
		_ = st
	}
}

func TestAVWAPStrategy_RegimeGating_TrendBlocksBounce(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	params := avwapParams()
	params["allow_regimes"] = []any{"TREND"}
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	// Seed AVWAP to 100.
	st, signals := feedAVWAPBar(t, s, ctx, "AAPL", st, strat.Bar{Time: start, Open: 100, High: 100, Low: 100, Close: 100, Volume: 10}, strat.IndicatorData{VolumeSMA: 10})
	assert.Empty(t, signals)

	ind := strat.IndicatorData{RSI: 20, VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "TREND", Strength: 0.8}}}
	bounce := strat.Bar{Time: start.Add(time.Minute), Open: 99, High: 101, Low: 99, Close: 100.5, Volume: 10}
	_, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, bounce, ind)
	assert.Empty(t, signals)
}

func TestAVWAPStrategy_Exit_LongPosition(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	params := avwapParams()
	params["cooldown_seconds"] = 1
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}
	// Enter long via breakout.
	entryBars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 92, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20},
	}
	for _, b := range entryBars {
		st, _ = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	avwapSt := st.(*builtin.AVWAPState)
	require.Equal(t, strat.SideBuy, avwapSt.PendingEntry, "entry should set PendingEntry")

	// Simulate fill confirmation to promote PendingEntry → PositionSide.
	st, _, err = s.OnEvent(ctx, "AAPL", strat.FillConfirmation{Symbol: "AAPL", Side: strat.SideBuy, Price: 128}, st)
	require.NoError(t, err)
	avwapSt = st.(*builtin.AVWAPState)
	require.Equal(t, strat.SideBuy, avwapSt.PositionSide, "fill should confirm position")
	// Move past cooldown.
	ctx.now = ctx.now.Add(2 * time.Second)

	// Two bars below AVWAP should exit.
	exitBar1 := strat.Bar{Time: start.Add(3 * time.Minute), Open: 128, High: 130, Low: 80, Close: 80, Volume: 10}
	exitBar2 := strat.Bar{Time: start.Add(4 * time.Minute), Open: 80, High: 90, Low: 70, Close: 70, Volume: 10}
	st, signals := feedAVWAPBar(t, s, ctx, "AAPL", st, exitBar1, ind)
	assert.Empty(t, signals)
	_, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, exitBar2, ind)

	require.Len(t, signals, 1)
	sig := signals[0]
	assert.Equal(t, strat.SignalExit, sig.Type)
	assert.Equal(t, strat.SideSell, sig.Side)
	assert.Contains(t, sig.Tags, "ref_price")
}

func TestAVWAPStrategy_CooldownPreventsEntry(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	params := avwapParams()
	params["cooldown_seconds"] = 120
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}
	entryBars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 92, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20},
	}

	for _, b := range entryBars {
		st, _ = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}

	// Same minute: would still meet conditions, but cooldown should short-circuit.
	bar := strat.Bar{Time: start.Add(3 * time.Minute), Open: 128, High: 140, Low: 120, Close: 140, Volume: 30}
	_, signals := feedAVWAPBar(t, s, ctx, "AAPL", st, bar, ind)
	assert.Empty(t, signals)
}

func TestAVWAPStrategy_MaxTradesPerDay(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	params := avwapParams()
	params["max_trades_per_day"] = 2
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	// Set TradesToday to max so next entry is blocked.
	avwapSt := st.(*builtin.AVWAPState)
	avwapSt.TradesToday = 2

	// Feed bars that would normally trigger breakout.
	st, _ = feedAVWAPBar(t, s, ctx, "AAPL", st, strat.Bar{Time: start, Open: 100, High: 112, Low: 92, Close: 112, Volume: 10}, ind)
	st, _ = feedAVWAPBar(t, s, ctx, "AAPL", st, strat.Bar{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10}, ind)
	_, signals := feedAVWAPBar(t, s, ctx, "AAPL", st, strat.Bar{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20}, ind)
	assert.Empty(t, signals, "max trades reached, should block entry")
}

func TestAVWAPStrategy_OnEvent_UnrecognizedEvent(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	ctx := newTestContext(time.Now())
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	st2, signals, err := s.OnEvent(ctx, "AAPL", "some_event", st)
	require.NoError(t, err)
	assert.Empty(t, signals)
	_ = st2
}

func TestAVWAPState_MarshalUnmarshal(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{RSI: 55, Volume: 5000, VolumeSMA: 4500, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.6}}}
	st, _ = feedAVWAPBar(t, s, ctx, "AAPL", st, strat.Bar{Time: start, Open: 100, High: 100, Low: 100, Close: 100, Volume: 10}, ind)

	avwapSt := st.(*builtin.AVWAPState)
	avwapSt.TradesToday = 2
	avwapSt.PositionSide = strat.SideBuy
	avwapSt.AboveCount["session_open"] = 7
	avwapSt.BelowCount["session_open"] = 0
	avwapSt.CooldownUntil = start.Add(5 * time.Minute)

	data, err := avwapSt.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &builtin.AVWAPState{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)
	assert.Equal(t, "AAPL", restored.Symbol)
	assert.Equal(t, 2, restored.TradesToday)
	assert.Equal(t, strat.SideBuy, restored.PositionSide)
	assert.Equal(t, 55.0, restored.Indicators.RSI)
	assert.NotNil(t, restored.Calc)
	_, ok := restored.AboveCount["session_open"]
	assert.True(t, ok)
}

func TestAVWAPStrategy_OnEvent_FillConfirmation(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	avwapSt := st.(*builtin.AVWAPState)
	avwapSt.PendingEntry = strat.SideBuy
	avwapSt.PendingEntryAt = ctx.Now()

	st2, signals, err := s.OnEvent(ctx, "AAPL", strat.FillConfirmation{Symbol: "AAPL", Side: strat.SideBuy, Price: 150.0, Quantity: 10}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)

	avwapSt2 := st2.(*builtin.AVWAPState)
	assert.Equal(t, strat.SideBuy, avwapSt2.PositionSide, "fill should promote PendingEntry to PositionSide")
	assert.Equal(t, strat.Side(""), avwapSt2.PendingEntry, "PendingEntry should be cleared after fill")
	assert.True(t, avwapSt2.PendingEntryAt.IsZero(), "PendingEntryAt should be reset")
}

func TestAVWAPStrategy_OnEvent_EntryRejection(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	ctx := newTestContext(time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	avwapSt := st.(*builtin.AVWAPState)
	avwapSt.PendingEntry = strat.SideSell
	avwapSt.PendingEntryAt = ctx.Now()

	st2, signals, err := s.OnEvent(ctx, "AAPL", strat.EntryRejection{Symbol: "AAPL", Side: strat.SideSell, Reason: "risk limit"}, st)
	require.NoError(t, err)
	assert.Empty(t, signals)

	avwapSt2 := st2.(*builtin.AVWAPState)
	assert.Equal(t, strat.Side(""), avwapSt2.PositionSide, "PositionSide should remain empty after rejection")
	assert.Equal(t, strat.Side(""), avwapSt2.PendingEntry, "PendingEntry should be cleared after rejection")
	assert.True(t, avwapSt2.PendingEntryAt.IsZero(), "PendingEntryAt should be reset")
}

func TestAVWAPStrategy_PendingEntryBlocksExit(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	params := avwapParams()
	params["cooldown_seconds"] = 1
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	// Enter long via breakout.
	entryBars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 92, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20},
	}
	for _, b := range entryBars {
		st, _ = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	avwapSt := st.(*builtin.AVWAPState)
	require.Equal(t, strat.SideBuy, avwapSt.PendingEntry, "should have PendingEntry set")
	require.Equal(t, strat.Side(""), avwapSt.PositionSide, "PositionSide should be empty until fill")

	// Move past cooldown.
	ctx.now = ctx.now.Add(2 * time.Second)

	// Two bars below AVWAP — would normally exit, but PendingEntry should block it.
	exitBar1 := strat.Bar{Time: start.Add(3 * time.Minute), Open: 128, High: 130, Low: 80, Close: 80, Volume: 10}
	exitBar2 := strat.Bar{Time: start.Add(4 * time.Minute), Open: 80, High: 90, Low: 70, Close: 70, Volume: 10}
	st, signals := feedAVWAPBar(t, s, ctx, "AAPL", st, exitBar1, ind)
	assert.Empty(t, signals)
	_, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, exitBar2, ind)
	assert.Empty(t, signals, "should not emit exit while PendingEntry is set")
}

func TestAVWAPStrategy_PendingEntryBlocksNewEntry(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	avwapSt := st.(*builtin.AVWAPState)
	avwapSt.PendingEntry = strat.SideBuy
	avwapSt.PendingEntryAt = ctx.Now()

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	// Feed bars that would normally trigger breakout.
	bars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 92, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20},
	}
	var signals []strat.Signal
	for _, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
		assert.Empty(t, signals, "should not emit entry while PendingEntry is set")
	}
}

func TestAVWAPStrategy_PendingEntryTimeout(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	avwapSt := st.(*builtin.AVWAPState)
	avwapSt.PendingEntry = strat.SideBuy
	avwapSt.PendingEntryAt = start.Add(-6 * time.Minute) // 6 min ago, past the 5 min timeout

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}
	bar := strat.Bar{Time: start, Open: 100, High: 100, Low: 100, Close: 100, Volume: 10}
	st2, _ := feedAVWAPBar(t, s, ctx, "AAPL", st, bar, ind)

	avwapSt2 := st2.(*builtin.AVWAPState)
	assert.Equal(t, strat.Side(""), avwapSt2.PendingEntry, "PendingEntry should be cleared after timeout")
	assert.True(t, avwapSt2.PendingEntryAt.IsZero(), "PendingEntryAt should be reset after timeout")
}

func TestAVWAPState_MarshalUnmarshal_PendingEntry(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{RSI: 55, Volume: 5000, VolumeSMA: 4500}
	st, _ = feedAVWAPBar(t, s, ctx, "AAPL", st, strat.Bar{Time: start, Open: 100, High: 100, Low: 100, Close: 100, Volume: 10}, ind)

	avwapSt := st.(*builtin.AVWAPState)
	avwapSt.PendingEntry = strat.SideSell
	avwapSt.PendingEntryAt = start

	data, err := avwapSt.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	restored := &builtin.AVWAPState{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)
	assert.Equal(t, strat.SideSell, restored.PendingEntry)
	assert.True(t, start.Equal(restored.PendingEntryAt))
	assert.Equal(t, strat.Side(""), restored.PositionSide)
}

// --- Higher Lows / Lower Highs tests ---

func higherLowsParams() map[string]any {
	p := avwapParams()
	p["require_higher_lows"] = true
	p["higher_lows_bars"] = 3
	return p
}

func TestAVWAPStrategy_HigherLows_BlocksBreakoutWithoutPattern(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", higherLowsParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	bars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 95, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 90, Close: 120, Volume: 10}, // low 90 < 95: NOT higher
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20},
	}

	var signals []strat.Signal
	for _, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	assert.Empty(t, signals, "non-increasing lows should block long breakout")
}

func TestAVWAPStrategy_HigherLows_AllowsBreakoutWithPattern(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", higherLowsParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	bars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 92, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10},     // low 100 > 92
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20}, // low 108 > 100
	}

	var signals []strat.Signal
	for _, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	require.Len(t, signals, 1, "strictly increasing lows should allow long breakout")
	assert.Equal(t, strat.SideBuy, signals[0].Side)
}

func TestAVWAPStrategy_HigherLows_DisabledByDefault(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", avwapParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	bars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 95, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 90, Close: 120, Volume: 10}, // low 90 < 95: NOT higher
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 20},
	}

	var signals []strat.Signal
	for _, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	require.Len(t, signals, 1, "with RequireHigherLows=false (default), breakout should fire regardless")
	assert.Equal(t, strat.SideBuy, signals[0].Side)
}

func TestAVWAPStrategy_LowerHighs_BlocksShortBreakout(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", higherLowsParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	bars := []strat.Bar{
		{Time: start, Open: 130, High: 135, Low: 110, Close: 110, Volume: 10},
		{Time: start.Add(time.Minute), Open: 110, High: 140, Low: 95, Close: 95, Volume: 10}, // high 140 > 135: NOT lower
		{Time: start.Add(2 * time.Minute), Open: 95, High: 100, Low: 80, Close: 80, Volume: 20},
	}

	var signals []strat.Signal
	for _, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	assert.Empty(t, signals, "non-decreasing highs should block short breakout")
}

// --- Midday Trap Shield tests ---

func middayTrapParams() map[string]any {
	p := avwapParams()
	p["midday_trap_shield"] = true
	p["midday_volume_mult"] = 2.0
	p["asset_class"] = "EQUITY"
	return p
}

func TestAVWAPStrategy_MiddayTrapShield_BlocksLowVolumeShort(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	et, _ := time.LoadLocation("America/New_York")
	start := time.Date(2025, 3, 4, 12, 0, 0, 0, et)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", middayTrapParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	bars := []strat.Bar{
		{Time: start, Open: 130, High: 135, Low: 110, Close: 110, Volume: 10},
		{Time: start.Add(time.Minute), Open: 110, High: 115, Low: 95, Close: 95, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 95, High: 98, Low: 80, Close: 80, Volume: 18}, // 18 < 2.0*10 = 20
	}

	var signals []strat.Signal
	for _, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	assert.Empty(t, signals, "midday low-volume short should be blocked")
}

func TestAVWAPStrategy_MiddayTrapShield_AllowsHighVolumeShort(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	et, _ := time.LoadLocation("America/New_York")
	start := time.Date(2025, 3, 4, 12, 0, 0, 0, et)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", middayTrapParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	bars := []strat.Bar{
		{Time: start, Open: 130, High: 135, Low: 110, Close: 110, Volume: 10},
		{Time: start.Add(time.Minute), Open: 110, High: 115, Low: 95, Close: 95, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 95, High: 98, Low: 80, Close: 80, Volume: 25}, // 25 > 2.0*10 = 20
	}

	var signals []strat.Signal
	for _, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	require.Len(t, signals, 1, "midday high-volume short should be allowed")
	assert.Equal(t, strat.SideSell, signals[0].Side)
}

func TestAVWAPStrategy_MiddayTrapShield_IgnoresCrypto(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	et, _ := time.LoadLocation("America/New_York")
	start := time.Date(2025, 3, 4, 12, 0, 0, 0, et)
	ctx := newTestContext(start)
	params := avwapParams()
	params["midday_trap_shield"] = true
	params["midday_volume_mult"] = 2.0
	params["asset_class"] = "CRYPTO"
	st, err := s.Init(ctx, "BTCUSD", params, nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	bars := []strat.Bar{
		{Time: start, Open: 130, High: 135, Low: 110, Close: 110, Volume: 10},
		{Time: start.Add(time.Minute), Open: 110, High: 115, Low: 95, Close: 95, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 95, High: 98, Low: 80, Close: 80, Volume: 18}, // low volume but crypto
	}

	var signals []strat.Signal
	for _, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "BTCUSD", st, b, ind)
	}
	require.Len(t, signals, 1, "midday trap shield should not apply to crypto")
	assert.Equal(t, strat.SideSell, signals[0].Side)
}

func TestAVWAPStrategy_MiddayTrapShield_IgnoresLongEntries(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	et, _ := time.LoadLocation("America/New_York")
	start := time.Date(2025, 3, 4, 12, 0, 0, 0, et)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", middayTrapParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

	bars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 92, Close: 112, Volume: 10},
		{Time: start.Add(time.Minute), Open: 112, High: 120, Low: 100, Close: 120, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 120, High: 128, Low: 108, Close: 128, Volume: 18}, // low volume but LONG
	}

	var signals []strat.Signal
	for _, b := range bars {
		st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}
	require.Len(t, signals, 1, "midday trap shield should not affect long entries")
	assert.Equal(t, strat.SideBuy, signals[0].Side)
}

func TestAVWAPStrategy_MiddayTrapShield_IgnoresOutsideWindow(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	et, _ := time.LoadLocation("America/New_York")

	for _, hour := range []int{10, 14} {
		t.Run(time.Date(2025, 3, 4, hour, 0, 0, 0, et).Format("15:04"), func(t *testing.T) {
			start := time.Date(2025, 3, 4, hour, 0, 0, 0, et)
			ctx := newTestContext(start)
			st, err := s.Init(ctx, "AAPL", middayTrapParams(), nil)
			require.NoError(t, err)

			ind := strat.IndicatorData{VolumeSMA: 10, AnchorRegimes: map[string]strat.AnchorRegime{"5m": {Type: "BALANCE", Strength: 0.5}}}

			bars := []strat.Bar{
				{Time: start, Open: 130, High: 135, Low: 110, Close: 110, Volume: 10},
				{Time: start.Add(time.Minute), Open: 110, High: 115, Low: 95, Close: 95, Volume: 10},
				{Time: start.Add(2 * time.Minute), Open: 95, High: 98, Low: 80, Close: 80, Volume: 18}, // low volume but outside window
			}

			var signals []strat.Signal
			for _, b := range bars {
				st, signals = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
			}
			require.Len(t, signals, 1, "outside midday window, short should fire")
			assert.Equal(t, strat.SideSell, signals[0].Side)
		})
	}
}

// --- Serialization: RecentLows/RecentHighs ---

func TestAVWAPState_MarshalUnmarshal_RecentLows(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	start := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	ctx := newTestContext(start)
	st, err := s.Init(ctx, "AAPL", higherLowsParams(), nil)
	require.NoError(t, err)

	ind := strat.IndicatorData{VolumeSMA: 10}

	bars := []strat.Bar{
		{Time: start, Open: 100, High: 112, Low: 92, Close: 105, Volume: 10},
		{Time: start.Add(time.Minute), Open: 105, High: 115, Low: 98, Close: 110, Volume: 10},
		{Time: start.Add(2 * time.Minute), Open: 110, High: 120, Low: 104, Close: 115, Volume: 10},
	}

	for _, b := range bars {
		st, _ = feedAVWAPBar(t, s, ctx, "AAPL", st, b, ind)
	}

	avwapSt := st.(*builtin.AVWAPState)
	require.Equal(t, []float64{92, 98, 104}, avwapSt.RecentLows)
	require.Equal(t, []float64{112, 115, 120}, avwapSt.RecentHighs)

	data, err := avwapSt.Marshal()
	require.NoError(t, err)

	restored := &builtin.AVWAPState{}
	err = restored.Unmarshal(data)
	require.NoError(t, err)
	assert.Equal(t, []float64{92, 98, 104}, restored.RecentLows)
	assert.Equal(t, []float64{112, 115, 120}, restored.RecentHighs)
}
