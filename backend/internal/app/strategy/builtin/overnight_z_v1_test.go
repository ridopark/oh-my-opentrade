package builtin_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ozParams() map[string]any {
	return map[string]any{
		"late_z_long_threshold":     -1.5,
		"late_z_short_threshold":    1.5,
		"long_only":                 true,
		"entry_time":                "09:35",
		"exit_time":                 "15:45",
		"allowed_hours_tz":          "America/New_York",
		"hard_stop_bps":             200.0,
		"risk_per_trade_pct":        2.0,
		"max_positions":             6,
		"rolling_wr_kill_threshold": 0.38,
		"rolling_wr_kill_window":    20,
		"rolling_wr_cooldown_days":  5,
	}
}

func feedOZBar(t *testing.T, s *builtin.OvernightZStrategy, ctx *testContext, symbol string, st strat.State, bar strat.Bar, ind strat.IndicatorData) (strat.State, []strat.Signal) {
	t.Helper()
	ctx.now = bar.Time
	ozSt := st.(*builtin.OZState)
	ozSt.SetIndicators(ind)
	st2, signals, err := s.OnBar(ctx, symbol, bar, st)
	require.NoError(t, err)
	return st2, signals
}

func TestOZ_LongEntry_FavorableZ(t *testing.T) {
	s := builtin.NewOvernightZStrategy()
	ctx := newTestContext(time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC)) // 09:35 ET
	st, err := s.Init(ctx, "AAPL", ozParams(), nil)
	require.NoError(t, err)

	bar := strat.Bar{
		Time: time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC), // 09:35 ET
		Open: 180, High: 181, Low: 179, Close: 180.50, Volume: 5000,
	}
	ind := strat.IndicatorData{LateSessionDPZ: -2.0}

	st, sigs := feedOZBar(t, s, ctx, "AAPL", st, bar, ind)
	require.Len(t, sigs, 1, "should emit long entry when late Z < -1.5")
	assert.Equal(t, strat.SignalEntry, sigs[0].Type)
	assert.Equal(t, strat.SideBuy, sigs[0].Side)
	assert.Contains(t, sigs[0].Tags["setup"], "overnight_z_entry")
	_ = st
}

func TestOZ_NoEntry_NeutralZ(t *testing.T) {
	s := builtin.NewOvernightZStrategy()
	ctx := newTestContext(time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", ozParams(), nil)
	require.NoError(t, err)

	bar := strat.Bar{
		Time: time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC),
		Open: 180, High: 181, Low: 179, Close: 180.50, Volume: 5000,
	}
	ind := strat.IndicatorData{LateSessionDPZ: -0.5} // neutral, not below -1.5

	st, sigs := feedOZBar(t, s, ctx, "AAPL", st, bar, ind)
	assert.Empty(t, sigs, "should not enter when Z is neutral")
	_ = st
}

func TestOZ_NoEntry_WrongTime(t *testing.T) {
	s := builtin.NewOvernightZStrategy()
	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC)) // 10:30 ET
	st, err := s.Init(ctx, "AAPL", ozParams(), nil)
	require.NoError(t, err)

	bar := strat.Bar{
		Time: time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC), // 10:30 ET, not 09:35
		Open: 180, High: 181, Low: 179, Close: 180.50, Volume: 5000,
	}
	ind := strat.IndicatorData{LateSessionDPZ: -2.0}

	st, sigs := feedOZBar(t, s, ctx, "AAPL", st, bar, ind)
	assert.Empty(t, sigs, "should not enter at wrong time")
	_ = st
}

func TestOZ_LongOnlyMode_NoShort(t *testing.T) {
	s := builtin.NewOvernightZStrategy()
	ctx := newTestContext(time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", ozParams(), nil)
	require.NoError(t, err)

	bar := strat.Bar{
		Time: time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC),
		Open: 180, High: 181, Low: 179, Close: 180.50, Volume: 5000,
	}
	ind := strat.IndicatorData{LateSessionDPZ: 2.0} // high Z would be short if allowed

	st, sigs := feedOZBar(t, s, ctx, "AAPL", st, bar, ind)
	assert.Empty(t, sigs, "should not emit short entry in long-only mode")
	_ = st
}

func TestOZ_MOCExit(t *testing.T) {
	s := builtin.NewOvernightZStrategy()
	params := ozParams()
	ctx := newTestContext(time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	// Enter long.
	entryBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC),
		Open: 180, High: 181, Low: 179, Close: 180.50, Volume: 5000,
	}
	ind := strat.IndicatorData{LateSessionDPZ: -2.0}
	st, sigs := feedOZBar(t, s, ctx, "AAPL", st, entryBar, ind)
	require.Len(t, sigs, 1)

	// Simulate fill.
	st, _, err = s.OnEvent(ctx, "AAPL", strat.FillConfirmation{
		Symbol: "AAPL", Side: "buy", Price: 180.50,
	}, st)
	require.NoError(t, err)

	// MOC exit at 15:45 ET.
	mocBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 19, 45, 0, 0, time.UTC), // 15:45 ET
		Open: 182, High: 183, Low: 181, Close: 182, Volume: 3000,
	}
	st, sigs = feedOZBar(t, s, ctx, "AAPL", st, mocBar, ind)
	require.Len(t, sigs, 1, "should emit MOC exit at 15:45 ET")
	assert.Equal(t, strat.SignalExit, sigs[0].Type)
	assert.Contains(t, sigs[0].Tags["setup"], "oz_moc_exit")
	_ = st
}

func TestOZ_HardStop(t *testing.T) {
	s := builtin.NewOvernightZStrategy()
	ctx := newTestContext(time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", ozParams(), nil)
	require.NoError(t, err)

	// Enter long at 180.50.
	entryBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC),
		Open: 180, High: 181, Low: 179, Close: 180.50, Volume: 5000,
	}
	ind := strat.IndicatorData{LateSessionDPZ: -2.0}
	st, _ = feedOZBar(t, s, ctx, "AAPL", st, entryBar, ind)

	// Simulate fill.
	st, _, err = s.OnEvent(ctx, "AAPL", strat.FillConfirmation{
		Symbol: "AAPL", Side: "buy", Price: 180.50,
	}, st)
	require.NoError(t, err)

	// Price drops 200+ bps from 180.50 → ~176.89.
	stopBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC), // 10:00 ET
		Open: 177, High: 178, Low: 176, Close: 176.80, Volume: 8000,
	}
	st, sigs := feedOZBar(t, s, ctx, "AAPL", st, stopBar, ind)
	require.Len(t, sigs, 1, "should fire hard stop when 200+ bps against")
	assert.Equal(t, strat.SignalExit, sigs[0].Type)
	assert.Contains(t, sigs[0].Tags["setup"], "oz_hard_stop")
	_ = st
}

func TestOZ_KillSwitch(t *testing.T) {
	s := builtin.NewOvernightZStrategy()
	params := ozParams()
	params["rolling_wr_kill_window"] = 5
	params["rolling_wr_kill_threshold"] = 0.40
	params["rolling_wr_cooldown_days"] = 2
	ctx := newTestContext(time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", params, nil)
	require.NoError(t, err)

	ozSt := st.(*builtin.OZState)
	// Simulate 5 trades with 1 win, 4 losses → WR = 0.20 < 0.40.
	ozSt.TradeOutcomes = []int8{1, -1, -1, -1, -1}

	bar := strat.Bar{
		Time: time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC),
		Open: 180, High: 181, Low: 179, Close: 180.50, Volume: 5000,
	}
	ind := strat.IndicatorData{LateSessionDPZ: -2.0}

	st, sigs := feedOZBar(t, s, ctx, "AAPL", st, bar, ind)
	assert.Empty(t, sigs, "kill switch should suppress entries when WR < threshold")

	// After cooldown days expire, should allow entries again.
	ozSt = st.(*builtin.OZState)
	ozSt.KillSwitchDaysLeft = 0
	ozSt.TradeOutcomes = []int8{1, 1, 1, -1, -1} // WR = 0.60, above threshold

	bar2 := strat.Bar{
		Time: time.Date(2025, 6, 5, 13, 35, 0, 0, time.UTC), // new day
		Open: 180, High: 181, Low: 179, Close: 180.50, Volume: 5000,
	}
	st, sigs = feedOZBar(t, s, ctx, "AAPL", st, bar2, ind)
	require.Len(t, sigs, 1, "should allow entries after kill switch expires")
	_ = st
}

func TestOZ_FillConfirmation_WinLoss(t *testing.T) {
	s := builtin.NewOvernightZStrategy()
	ctx := newTestContext(time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", ozParams(), nil)
	require.NoError(t, err)

	// Enter.
	entryBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC),
		Open: 180, High: 181, Low: 179, Close: 180, Volume: 5000,
	}
	ind := strat.IndicatorData{LateSessionDPZ: -2.0}
	st, _ = feedOZBar(t, s, ctx, "AAPL", st, entryBar, ind)

	// Fill entry at 180.
	st, _, err = s.OnEvent(ctx, "AAPL", strat.FillConfirmation{
		Symbol: "AAPL", Side: "buy", Price: 180.0,
	}, st)
	require.NoError(t, err)
	ozSt := st.(*builtin.OZState)
	assert.Equal(t, strat.SideBuy, ozSt.PositionSide)
	assert.Equal(t, 180.0, ozSt.EntryFillPrice)

	// Exit at 182 (win).
	st, _, err = s.OnEvent(ctx, "AAPL", strat.FillConfirmation{
		Symbol: "AAPL", Side: "sell", Price: 182.0,
	}, st)
	require.NoError(t, err)
	ozSt = st.(*builtin.OZState)
	assert.Equal(t, strat.Side(""), ozSt.PositionSide)
	require.Len(t, ozSt.TradeOutcomes, 1)
	assert.Equal(t, int8(1), ozSt.TradeOutcomes[0]) // win
}

func TestOZ_ReplayOnBar(t *testing.T) {
	s := builtin.NewOvernightZStrategy()
	ctx := newTestContext(time.Date(2025, 6, 2, 13, 35, 0, 0, time.UTC))
	st, err := s.Init(ctx, "AAPL", ozParams(), nil)
	require.NoError(t, err)

	bar := strat.Bar{
		Time: time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC),
		Open: 180, High: 181, Low: 179, Close: 180, Volume: 5000,
	}
	ind := strat.IndicatorData{LateSessionDPZ: -1.8}

	st, err = s.ReplayOnBar(ctx, "AAPL", bar, st, ind)
	require.NoError(t, err)
	ozSt := st.(*builtin.OZState)
	assert.Equal(t, -1.8, ozSt.LastLateZ)
}

// ─── MACD Z Conditioning Tests ──────────────────────────────────────────────

func TestMACD_ZConditioning_BlockOnAdverseZ(t *testing.T) {
	s := builtin.NewMACDStrategy()
	params := bmParams()
	params["dp_z_conditioning_enabled"] = true
	params["dp_z_macd_suppress_threshold"] = -1.0
	params["dp_z_macd_suppress_mode"] = "block"

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)
	st = warmupBM(t, s, ctx, st, 3)

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1000,
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI:            55,
		LateSessionDPZ: -1.5, // MACD-adverse (low Z = reversal setup, bad for momentum)
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	assert.Empty(t, sigs, "MACD should suppress entries when late Z is adverse (below suppress threshold)")
	_ = st
}

func TestMACD_ZConditioning_AllowOnFavorableZ(t *testing.T) {
	s := builtin.NewMACDStrategy()
	params := bmParams()
	params["dp_z_conditioning_enabled"] = true
	params["dp_z_macd_suppress_threshold"] = -1.0
	params["dp_z_macd_favorable_threshold"] = 1.0
	params["dp_z_macd_suppress_mode"] = "block"

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)
	st = warmupBM(t, s, ctx, st, 3)

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1000,
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI:            55,
		LateSessionDPZ: 1.5, // MACD-favorable (high Z = strong trend = momentum continuation)
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	require.Len(t, sigs, 1, "MACD should allow entries when late Z is favorable")
	assert.Equal(t, strat.SideBuy, sigs[0].Side)
	_ = st
}

func TestMACD_ZConditioning_Disabled_NoEffect(t *testing.T) {
	s := builtin.NewMACDStrategy()
	params := bmParams()
	// dp_z_conditioning_enabled defaults to false

	ctx := newTestContext(time.Date(2025, 6, 2, 14, 30, 0, 0, time.UTC))
	st, err := s.Init(ctx, "TEST", params, nil)
	require.NoError(t, err)
	st = warmupBM(t, s, ctx, st, 3)

	crossBar := strat.Bar{
		Time: time.Date(2025, 6, 2, 15, 30, 0, 0, time.UTC),
		Open: 100, High: 102, Low: 99, Close: 101, Volume: 1000,
	}
	crossInd := strat.IndicatorData{
		EMA9: 100, EMA200: 95, VolumeSMA: 1000,
		MACDLine: 0.1, MACDSignal: 0.05, MACDHistogram: 0.05,
		RSI:            55,
		LateSessionDPZ: -2.0, // adverse Z, but conditioning is disabled
	}
	st, sigs := feedBMBar(t, s, ctx, "TEST", st, crossBar, crossInd)
	require.Len(t, sigs, 1, "should fire signal when Z conditioning is disabled even with adverse Z")
	_ = st
}
