package builtin_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cryptoRevertDefaultParams returns the recalibrated defaults (matching the
// shipped TOML). Individual tests override fields as needed.
func cryptoRevertDefaultParams() map[string]any {
	return map[string]any{
		"entry_dev_z":              -2.0,
		"exit_dev_z":               -0.3,
		"hard_stop_dev_z":          -3.5,
		"tfi_lookback_min":         15,
		"tfi_min":                  0.15,
		"sigma_method":             "rolling",
		"sigma_lookback_bars":      96,
		"session_reset_utc":        0,
		"time_stop_min":            90,
		"max_concurrent":           2,
		"require_tfi":              true,
		"require_inducement":       true,
		"inducement_lookback_bars": 2,
		"use_bar_sign_tfi":         true,
		"tfi_source":               "auto",
		"require_xv_flow":          false,
		"require_skew_ok":          false,
		"weight_us_hours":          1.0,
		"weight_asia_hours":        1.0,
		"weight_weekend":           1.0,
	}
}

// bullBar: close > open — positive bar-sign TFI contribution.
func bullBar(t time.Time, open, close, volume float64) strat.Bar {
	high := close
	if open > close {
		high = open
	}
	low := open
	if close < open {
		low = close
	}
	return strat.Bar{Time: t, Open: open, High: high, Low: low, Close: close, Volume: volume}
}

// sweepBar: a bullish sweep — low wicks below a round-number level then
// closes above it. volume is 2x the prior SMA to satisfy the inducement
// detector's volume gate.
func sweepBar(t time.Time, roundLevel, closePrice, volume float64) strat.Bar {
	low := roundLevel - (roundLevel * 0.002) // 20 bps under (inside 5-150 bps breach window)
	return strat.Bar{
		Time:   t,
		Open:   roundLevel + 1,
		High:   closePrice,
		Low:    low,
		Close:  closePrice,
		Volume: volume,
	}
}

// run feeds history and returns the state + all signals emitted across
// every OnBar call (flattened). Any bars before warmup call ReplayOnBar;
// after warmup OnBar is used.
func run(t *testing.T, params map[string]any, bars []strat.Bar) (*builtin.CryptoRevertState, []strat.Signal) {
	t.Helper()
	s := builtin.NewCryptoRevertStrategy()
	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)

	warmup := s.WarmupBars()
	var all []strat.Signal
	for i, b := range bars {
		if i < warmup {
			st, err = s.ReplayOnBar(nil, "BTC/USD", b, st, strat.IndicatorData{})
			require.NoError(t, err)
			continue
		}
		var sigs []strat.Signal
		st, sigs, err = s.OnBar(nil, "BTC/USD", b, st)
		require.NoError(t, err)
		all = append(all, sigs...)
	}
	rst, ok := st.(*builtin.CryptoRevertState)
	require.True(t, ok)
	return rst, all
}

// buildHistory creates a warmup run with small price oscillation around
// priceAnchor. Each bar is bull (positive bar-sign TFI) but prices drift in
// a ±50 range so the rolling-sigma estimator has a non-degenerate baseline —
// without this, rolling sigma collapses to ~0 and any deviation looks like
// an infinite z-score, which breaks "not extended" negative tests.
func buildHistory(start time.Time, priceAnchor float64, n int) []strat.Bar {
	bars := make([]strat.Bar, 0, n)
	for i := range n {
		ts := start.Add(time.Duration(i) * 5 * time.Minute)
		// Small deterministic oscillation ±50 around anchor.
		wobble := 50.0
		if i%2 == 1 {
			wobble = -50.0
		}
		open := priceAnchor + wobble - 1
		close := priceAnchor + wobble + 1
		bars = append(bars, bullBar(ts, open, close, 100))
	}
	return bars
}

func TestCryptoRevert_WarmupOnly_NoSignals(t *testing.T) {
	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	bars := buildHistory(anchor, 100_000, 96)
	_, sigs := run(t, cryptoRevertDefaultParams(), bars)
	assert.Empty(t, sigs, "no signals should fire during warmup")
}

// TestCryptoRevert_CleanLongEntry — all gates pass: dev_z < -2, tfi > 0.15,
// bullish sweep in last N bars. A single entry signal is emitted (further
// re-entry suppressed by PendingEntry).
func TestCryptoRevert_CleanLongEntry(t *testing.T) {
	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	bars := buildHistory(anchor, 101_000, 96)

	// Sweep bar: big-volume wick under $100k round then close above — this
	// bar itself sets dev_z well below -2 (because the 500-volume print at
	// 100_500 drops the cumulative VWAP materially), so the entry fires on
	// the same bar the sweep registers.
	tSweep := anchor.Add(96 * 5 * time.Minute)
	bars = append(bars, sweepBar(tSweep, 100_000, 100_500, 500))

	_, sigs := run(t, cryptoRevertDefaultParams(), bars)

	require.Len(t, sigs, 1, "expected exactly one entry signal")
	sig := sigs[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideBuy, sig.Side)
	assert.Equal(t, "mean_revert_long", sig.Tags["reason"])
}

// TestCryptoRevert_NoEntry_WhenDevZNotExtended — price near VWAP, no signal
// even though inducement fires. Uses a low-volume sweep (just above the
// inducement volume gate) so the VWAP is not dragged far from the anchor.
func TestCryptoRevert_NoEntry_WhenDevZNotExtended(t *testing.T) {
	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	bars := buildHistory(anchor, 101_000, 96)

	// Sweep with close VERY near the anchor (100_999 ≈ VWAP) and modest
	// volume — inducement fires on the wick, but VWAP barely moves and
	// dev_z stays inside -2.
	tSweep := anchor.Add(96 * 5 * time.Minute)
	bars = append(bars, sweepBar(tSweep, 100_000, 100_999, 130))

	// Trigger bar at 101_000 — AT VWAP, dev_z ~= 0.
	tTrig := tSweep.Add(5 * time.Minute)
	bars = append(bars, bullBar(tTrig, 100_999, 101_001, 100))

	_, sigs := run(t, cryptoRevertDefaultParams(), bars)
	assert.Empty(t, sigs, "no entry when dev_z is not below entry threshold")
}

// TestCryptoRevert_NoEntry_WhenTFIBelowFloor — dev_z extended and sweep
// present, but last ~15m of bars are all BEAR so tfi < 0 < 0.15.
func TestCryptoRevert_NoEntry_WhenTFIBelowFloor(t *testing.T) {
	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	bars := make([]strat.Bar, 0, 96)
	for i := range 96 {
		ts := anchor.Add(time.Duration(i) * 5 * time.Minute)
		// Alternate bull/bear but make the last 5 bars STRONGLY bear so the
		// 15-min TFI window is negative regardless of what the sweep adds.
		if i >= 91 {
			bars = append(bars, bullBar(ts, 101_001, 100_999, 1000))
		} else if i%2 == 0 {
			bars = append(bars, bullBar(ts, 100_999, 101_001, 100))
		} else {
			bars = append(bars, bullBar(ts, 101_001, 100_999, 100))
		}
	}

	// A sweep with modest volume so its bull bar-sign can't flip TFI.
	tSweep := anchor.Add(96 * 5 * time.Minute)
	bars = append(bars, sweepBar(tSweep, 100_000, 100_500, 130))

	_, sigs := run(t, cryptoRevertDefaultParams(), bars)
	assert.Empty(t, sigs, "no entry when tfi magnitude is below floor")
}

// TestCryptoRevert_NoEntry_WhenNoInducement — dev_z extended, tfi OK, but no
// sweep occurred — price drifts down without a wick.
func TestCryptoRevert_NoEntry_WhenNoInducement(t *testing.T) {
	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	bars := buildHistory(anchor, 101_000, 96)

	// Two normal bars + trigger — no round-level wick.
	tBar := anchor.Add(96 * 5 * time.Minute)
	bars = append(bars, bullBar(tBar, 100_500, 100_510, 100))
	tTrig := tBar.Add(5 * time.Minute)
	bars = append(bars, bullBar(tTrig, 93_000, 93_050, 100))

	_, sigs := run(t, cryptoRevertDefaultParams(), bars)
	assert.Empty(t, sigs, "no entry without a bullish inducement sweep")
}

// TestCryptoRevert_ExitOnReversion — after a long entry fills, a subsequent
// bar with dev_z inside exit_dev_z should emit an exit signal.
func TestCryptoRevert_ExitOnReversion(t *testing.T) {
	params := cryptoRevertDefaultParams()
	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	bars := buildHistory(anchor, 101_000, 96)

	tSweep := anchor.Add(96 * 5 * time.Minute)
	sweepB := sweepBar(tSweep, 100_000, 100_500, 500)
	bars = append(bars, sweepB)

	s := builtin.NewCryptoRevertStrategy()
	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)
	warmup := s.WarmupBars()
	var sigs []strat.Signal
	for i, b := range bars {
		if i < warmup {
			st, err = s.ReplayOnBar(nil, "BTC/USD", b, st, strat.IndicatorData{})
		} else {
			st, sigs, err = s.OnBar(nil, "BTC/USD", b, st)
		}
		require.NoError(t, err)
	}
	require.Len(t, sigs, 1, "entry should fire on the sweep bar")
	require.Equal(t, strat.SignalEntry, sigs[0].Type)

	// Simulate fill confirmation.
	st, _, err = s.OnEvent(nil, "BTC/USD", strat.FillConfirmation{
		Side:  strat.SideBuy,
		Price: 100_500,
	}, st)
	require.NoError(t, err)

	// Next bars push price up into the exit band. VWAP now sits ~100_976;
	// we rally back through it so dev_z reverts toward 0.
	last := sweepB.Time
	for i := 1; i <= 6; i++ {
		b := bullBar(last.Add(time.Duration(i)*5*time.Minute), 100_970, 100_990, 100)
		st, sigs, err = s.OnBar(nil, "BTC/USD", b, st)
		require.NoError(t, err)
		if len(sigs) > 0 {
			break
		}
	}

	require.NotEmpty(t, sigs, "expected an exit signal after reversion")
	assert.Equal(t, strat.SignalExit, sigs[0].Type)
	assert.Equal(t, "reversion_complete", sigs[0].Tags["reason"])
}

// TestCryptoRevert_ExitOnHardStop — after entry, price drops further so
// dev_z <= hard_stop_dev_z, triggering hard stop.
func TestCryptoRevert_ExitOnHardStop(t *testing.T) {
	params := cryptoRevertDefaultParams()
	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	bars := buildHistory(anchor, 101_000, 96)

	tSweep := anchor.Add(96 * 5 * time.Minute)
	sweepB := sweepBar(tSweep, 100_000, 100_500, 500)
	bars = append(bars, sweepB)

	s := builtin.NewCryptoRevertStrategy()
	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)
	warmup := s.WarmupBars()
	var sigs []strat.Signal
	for i, b := range bars {
		if i < warmup {
			st, err = s.ReplayOnBar(nil, "BTC/USD", b, st, strat.IndicatorData{})
		} else {
			st, sigs, err = s.OnBar(nil, "BTC/USD", b, st)
		}
		require.NoError(t, err)
	}
	require.Len(t, sigs, 1)

	st, _, err = s.OnEvent(nil, "BTC/USD", strat.FillConfirmation{
		Side: strat.SideBuy, Price: 100_500,
	}, st)
	require.NoError(t, err)

	// Next bar: massive dump — price far below VWAP, dev_z << -3.5.
	tCrash := sweepB.Time.Add(5 * time.Minute)
	crashBar := strat.Bar{Time: tCrash, Open: 85_000, High: 85_010, Low: 80_000, Close: 80_100, Volume: 100}
	st, sigs, err = s.OnBar(nil, "BTC/USD", crashBar, st)
	require.NoError(t, err)

	require.NotEmpty(t, sigs)
	exitSig := sigs[0]
	assert.Equal(t, strat.SignalExit, exitSig.Type)
	assert.Equal(t, "hard_stop", exitSig.Tags["reason"])
}

// TestCryptoRevert_ExitOnTimeStop — after entry, passage of
// time_stop_min minutes triggers exit even without reversion.
func TestCryptoRevert_ExitOnTimeStop(t *testing.T) {
	params := cryptoRevertDefaultParams()
	params["time_stop_min"] = 15 // tight stop for the test

	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	bars := buildHistory(anchor, 101_000, 96)
	tSweep := anchor.Add(96 * 5 * time.Minute)
	sweepB := sweepBar(tSweep, 100_000, 100_500, 500)
	bars = append(bars, sweepB)

	s := builtin.NewCryptoRevertStrategy()
	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)
	warmup := s.WarmupBars()
	var sigs []strat.Signal
	for i, b := range bars {
		if i < warmup {
			st, err = s.ReplayOnBar(nil, "BTC/USD", b, st, strat.IndicatorData{})
		} else {
			st, sigs, err = s.OnBar(nil, "BTC/USD", b, st)
		}
		require.NoError(t, err)
	}
	require.Len(t, sigs, 1)

	st, _, err = s.OnEvent(nil, "BTC/USD", strat.FillConfirmation{
		Side: strat.SideBuy, Price: 100_500,
	}, st)
	require.NoError(t, err)

	// Advance bars holding price between the exit band and hard stop.
	// After the sweep, VWAP ≈ 100_976 and sigma ≈ 48. Sitting at 100_800
	// keeps dev_z around -3.6 (beyond exit_dev_z=-0.3 but NOT past
	// hard_stop_dev_z=-3.5 once subsequent bars widen sigma via rolling).
	// We target the time_stop_min=15 firing after 15 minutes.
	last := sweepB.Time
	for i := 1; i <= 4; i++ {
		b := bullBar(last.Add(time.Duration(i)*5*time.Minute), 100_930, 100_935, 100)
		st, sigs, err = s.OnBar(nil, "BTC/USD", b, st)
		require.NoError(t, err)
		if len(sigs) > 0 {
			break
		}
	}

	require.NotEmpty(t, sigs)
	assert.Equal(t, strat.SignalExit, sigs[0].Type)
	assert.Equal(t, "time_stop", sigs[0].Tags["reason"])
}

// TestCryptoRevert_LoadSpecFromTOML — smoke-tests the shipped config loads
// cleanly via the spec loader and exposes the recalibrated defaults.
func TestCryptoRevert_LoadSpecFromTOML(t *testing.T) {
	path, err := filepath.Abs("../../../../../configs/strategies/crypto_revert_v1.toml")
	require.NoError(t, err)

	spec, err := strategy.LoadSpecFile(path)
	require.NoError(t, err)

	assert.Equal(t, 2, spec.SchemaVersion)
	assert.Equal(t, "crypto_revert_v1", spec.ID.String())
	assert.True(t, spec.Lifecycle.PaperOnly, "must be paper_only")

	hook, ok := spec.Hooks["signals"]
	require.True(t, ok, "signals hook present")
	assert.Equal(t, "crypto_revert", hook.Name)

	// Spot-check the quant-recalibrated values landed.
	assert.InDelta(t, 0.15, spec.Params["tfi_min"].(float64), 1e-9)
	assert.EqualValues(t, 2, spec.Params["inducement_lookback_bars"].(int64))
	assert.Equal(t, "ewma", spec.Params["sigma_method"].(string))
	assert.EqualValues(t, 96, spec.Params["sigma_lookback_bars"].(int64))
	assert.InDelta(t, -2.0, spec.Params["entry_dev_z"].(float64), 1e-9)
	assert.InDelta(t, -3.5, spec.Params["hard_stop_dev_z"].(float64), 1e-9)
	assert.EqualValues(t, 2, spec.Params["max_concurrent"].(int64))
}

// TestCryptoRevert_TradeTickPathIngested — verifies TradeTick events
// delivered through OnEvent are fed into TFI via UpdateTrade and are counted.
// This is the core "event routing works" test for the trade-tick wiring.
func TestCryptoRevert_TradeTickPathIngested(t *testing.T) {
	params := cryptoRevertDefaultParams()
	params["tfi_source"] = "trade_tick" // force the tick path unconditionally

	s := builtin.NewCryptoRevertStrategy()
	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)

	now := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	// Feed a handful of buy-side taker trades.
	for i := range 5 {
		st, _, err = s.OnEvent(nil, "BTC/USD", strat.TradeTick{
			Symbol:    "BTC/USD",
			Time:      now.Add(time.Duration(i) * time.Second),
			Price:     101_000,
			Size:      1.0,
			TakerSide: "buy",
		}, st)
		require.NoError(t, err)
	}
	rst, ok := st.(*builtin.CryptoRevertState)
	require.True(t, ok)
	assert.Equal(t, 5, rst.TradeTickCount(), "TradeTick events should flow into UpdateTrade")

	// TFI should reflect pure-buy pressure: 100%.
	_, _, _, _, _, _, tfiVal, _ := rst.DebugSnapshot()
	assert.InDelta(t, 1.0, tfiVal, 1e-9, "all-buy ticks should produce TFI=+1.0")
}

// TestCryptoRevert_BarSignFallback_WhenNoTrades — when no ticks arrive,
// "auto" mode must keep falling back to the bar-sign path so backtests
// stay deterministic. We push a bull bar and confirm TFI moves positive
// from UpdateBar alone.
func TestCryptoRevert_BarSignFallback_WhenNoTrades(t *testing.T) {
	params := cryptoRevertDefaultParams() // tfi_source="auto", UseBarSignTFI=true

	s := builtin.NewCryptoRevertStrategy()
	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)

	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	// A single bull bar through OnBar (ingest path). VWAP won't be ready
	// without warmup, but that's fine — we only care about TFI here.
	bar := bullBar(anchor, 100_000, 100_100, 50)
	st, err = s.ReplayOnBar(nil, "BTC/USD", bar, st, strat.IndicatorData{})
	require.NoError(t, err)

	rst, ok := st.(*builtin.CryptoRevertState)
	require.True(t, ok)
	assert.Equal(t, 0, rst.TradeTickCount(), "no trade ticks were fed")

	_, _, _, _, _, _, tfiVal, _ := rst.DebugSnapshot()
	assert.Greater(t, tfiVal, 0.0, "bar-sign fallback should push TFI positive on a bull bar")
}

// TestCryptoRevert_AutoMode_PrefersTicksOverBars — in auto mode once we've
// seen a recent tick, subsequent bars must NOT double-count via the bar-sign
// fallback. This verifies the mutual-exclusion logic in ingest().
func TestCryptoRevert_AutoMode_PrefersTicksOverBars(t *testing.T) {
	params := cryptoRevertDefaultParams() // auto
	s := builtin.NewCryptoRevertStrategy()
	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)

	now := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	// Feed a SELL-side tick — TFI should be -1.0.
	st, _, err = s.OnEvent(nil, "BTC/USD", strat.TradeTick{
		Symbol: "BTC/USD", Time: now, Size: 10.0, TakerSide: "sell",
	}, st)
	require.NoError(t, err)

	// Now run a BULL bar through ReplayOnBar at t+1m. In bar-sign mode this
	// would push TFI positive; in auto mode with a fresh tick it must NOT.
	bar := bullBar(now.Add(1*time.Minute), 100_000, 100_500, 1000)
	st, err = s.ReplayOnBar(nil, "BTC/USD", bar, st, strat.IndicatorData{})
	require.NoError(t, err)

	rst, ok := st.(*builtin.CryptoRevertState)
	require.True(t, ok)
	_, _, _, _, _, _, tfiVal, _ := rst.DebugSnapshot()
	assert.Less(t, tfiVal, 0.0, "fresh tick should keep TFI negative despite bull bar")
	assert.Equal(t, 1, rst.TradeTickCount())
}

// TestCryptoRevert_BarSignMode_IgnoresTicks — when operator pins the source
// to "bar_sign", trade-tick events arriving via OnEvent must NOT update TFI.
// Guards against accidental leakage once upstream wiring is live.
func TestCryptoRevert_BarSignMode_IgnoresTicks(t *testing.T) {
	params := cryptoRevertDefaultParams()
	params["tfi_source"] = "bar_sign"

	s := builtin.NewCryptoRevertStrategy()
	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)

	now := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	st, _, err = s.OnEvent(nil, "BTC/USD", strat.TradeTick{
		Symbol: "BTC/USD", Time: now, Size: 1.0, TakerSide: "buy",
	}, st)
	require.NoError(t, err)

	rst, ok := st.(*builtin.CryptoRevertState)
	require.True(t, ok)
	assert.Equal(t, 0, rst.TradeTickCount(), "bar_sign mode must ignore ticks")

	_, _, _, _, _, _, tfiVal, _ := rst.DebugSnapshot()
	assert.Equal(t, 0.0, tfiVal, "TFI should stay zero when ticks are ignored")
}

// TestCryptoRevert_MixedStream_TFIReflectsTicks — integration-flavored test:
// interleaves bars and ticks. In auto mode, TFI must track the tick side
// (dominant buy pressure) not the bar sign (alternating up/down).
func TestCryptoRevert_MixedStream_TFIReflectsTicks(t *testing.T) {
	params := cryptoRevertDefaultParams() // auto
	s := builtin.NewCryptoRevertStrategy()
	st, err := s.Init(nil, "BTC/USD", params, nil)
	require.NoError(t, err)

	anchor := time.Date(2026, 4, 15, 13, 0, 0, 0, time.UTC)
	// Prime the strategy with an initial tick so auto-mode flips to
	// trade_tick immediately. Without this, the first bar would fall
	// through the bar-sign path and pollute the TFI window with a
	// large bar-volume contribution that dwarfs the subsequent ticks.
	st, _, err = s.OnEvent(nil, "BTC/USD", strat.TradeTick{
		Symbol: "BTC/USD", Time: anchor.Add(-time.Second), Size: 1.0, TakerSide: "buy",
	}, st)
	require.NoError(t, err)

	// Alternate bars: bull, bear, bull, bear ... bar-sign TFI would be ~0.
	// Meanwhile ticks are 9 BUY / 1 SELL — tick TFI should be strongly +ve.
	for i := range 10 {
		ts := anchor.Add(time.Duration(i) * time.Minute)
		var bar strat.Bar
		if i%2 == 0 {
			bar = bullBar(ts, 100_000, 100_100, 100)
		} else {
			bar = bullBar(ts, 100_100, 100_000, 100) // bear (close < open)
		}
		st, err = s.ReplayOnBar(nil, "BTC/USD", bar, st, strat.IndicatorData{})
		require.NoError(t, err)

		side := "buy"
		if i == 3 { // one sell tick in the subsequent 10 we feed
			side = "sell"
		}
		st, _, err = s.OnEvent(nil, "BTC/USD", strat.TradeTick{
			Symbol: "BTC/USD", Time: ts.Add(30 * time.Second), Size: 1.0, TakerSide: side,
		}, st)
		require.NoError(t, err)
	}
	rst, ok := st.(*builtin.CryptoRevertState)
	require.True(t, ok)
	assert.Equal(t, 11, rst.TradeTickCount())

	_, _, _, _, _, _, tfiVal, _ := rst.DebugSnapshot()
	// 10 buy / 1 sell over the 15-min window → (10-1)/11 ≈ 0.818.
	// Critically: well above 0.6 — alternating-bar bar-sign would land
	// near 0. This is the whole point of the wiring.
	assert.Greater(t, tfiVal, 0.6, "TFI should reflect tick flow, not alternating bar signs")
	assert.InDelta(t, 9.0/11.0, tfiVal, 1e-9)
}
