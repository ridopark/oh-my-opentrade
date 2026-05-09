package builtin_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tttDefaultParams returns the prereg-locked defaults used by the strategy
// and the shipped TOML, mirrored here so tests don't depend on the file.
func tttDefaultParams() map[string]any {
	return map[string]any{
		"expiry_dte":             0,
		"min_dte":                2,
		"atr_breakout_mult":      1.5,
		"breakout_buffer_atr":    0.2,
		"body_range_ratio":       0.5,
		"vol_surge_mult":         1.5,
		"max_wick_ratio":         0.4,
		"retest_band_atr":        0.15,
		"retest_expiry_bars":     20,
		"invalidation_atr":       0.5,
		"retest_quality_gate":    true,
		"entry_cutoff_et":        "13:30",
		"trigger_drift_pct":      0.5,
		"freshness_max_age_secs": 60,
	}
}

func tttIndicators() strat.IndicatorData {
	return strat.IndicatorData{ATR: 1.0, VolumeSMA: 100}
}

// etLoc anchors test timestamps to America/New_York so the entry-cutoff
// gate evaluates the same wall-clock window the prereg locks.
var etLoc = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// tttBaseTime: a Tuesday at 10:00 ET (well before the 13:30 cutoff and not
// a Friday so nearestFriday rolls forward instead of pinning today).
var tttBaseTime = time.Date(2026, 5, 12, 10, 0, 0, 0, etLoc)

func tttBar(minuteOffset int, open, high, low, closeP, volume float64) strat.Bar {
	return strat.Bar{
		Time:   tttBaseTime.Add(time.Duration(minuteOffset) * time.Minute),
		Open:   open,
		High:   high,
		Low:    low,
		Close:  closeP,
		Volume: volume,
	}
}

func tttSignal(symbol string, trigger, strike float64, right string, postedAt time.Time) strat.TradingTheTrendSignal {
	return strat.TradingTheTrendSignal{
		SignalID:  "tradingthetrend:msg-1:0",
		MessageID: "msg-1",
		Author:    "TradingTheTrend",
		PostedAt:  postedAt,
		Ticker:    symbol,
		Strike:    strike,
		Right:     right,
		Trigger:   trigger,
		RawLine:   "RKLB 90c > 88.00",
	}
}

func feedTTTBar(t *testing.T, s *builtin.TradingTheTrendStrategy, ctx *testContext, symbol string, st strat.State, bar strat.Bar) (strat.State, []strat.Signal) {
	t.Helper()
	ctx.now = bar.Time
	tst := st.(*builtin.TradingTheTrendState)
	tst.SetIndicators(tttIndicators())
	st2, sigs, err := s.OnBar(ctx, symbol, bar, st)
	require.NoError(t, err)
	return st2, sigs
}

func TestTradingTheTrend_Meta(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	meta := s.Meta()
	assert.Equal(t, "tradingthetrend_v1", meta.ID.String())
	assert.Equal(t, "1.0.0", meta.Version.String())
}

func TestTradingTheTrend_ImplementsInterfaces(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	var _ strat.Strategy = s
	var _ strat.ReplayableStrategy = s
}

func TestTradingTheTrend_Init(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	tst, ok := st.(*builtin.TradingTheTrendState)
	require.True(t, ok)
	assert.Equal(t, builtin.TTTPhaseIdle, tst.Phase)
}

// armSignal: feed a TradingTheTrendSignal and assert state moves to Watching.
func armSignal(t *testing.T, s *builtin.TradingTheTrendStrategy, ctx *testContext, symbol string, st strat.State, postedAt time.Time, trigger, strike float64, right string) strat.State {
	t.Helper()
	ctx.now = postedAt
	sig := tttSignal(symbol, trigger, strike, right, postedAt)
	st2, sigs, err := s.OnEvent(ctx, symbol, sig, st)
	require.NoError(t, err)
	require.Empty(t, sigs)
	tst := st2.(*builtin.TradingTheTrendState)
	require.Equal(t, builtin.TTTPhaseWatching, tst.Phase)
	require.Equal(t, trigger, tst.Trigger)
	return st2
}

func TestTradingTheTrend_PhaseA_AdvanceToWaitingRetest(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	st = armSignal(t, s, ctx, "RKLB", st, tttBaseTime, 88.0, 90.0, "C")

	// Phase A bar: range=2.0 (>= 1.5*ATR=1.5), body=1.7 -> body/range=0.85,
	// volume=200 (>= 1.5*100), wick=0.3 -> wick/range=0.15, bullish,
	// close=89.7 > 88 + 0.2 = 88.2.
	bar := tttBar(1, 88.0, 90.0, 88.0, 89.7, 200)
	st2, sigs := feedTTTBar(t, s, ctx, "RKLB", st, bar)
	require.Empty(t, sigs)
	tst := st2.(*builtin.TradingTheTrendState)
	assert.Equal(t, builtin.TTTPhaseWaitingRetest, tst.Phase)
}

func TestTradingTheTrend_PhaseA_RejectsEachFilter(t *testing.T) {
	type tc struct {
		name string
		bar  strat.Bar
	}
	cases := []tc{
		// body/range = 0.6 / 2.0 = 0.30 < 0.5
		{"body_range_too_low", tttBar(1, 88.0, 90.0, 88.0, 88.6, 200)},
		// range = 1.0 < 1.5*ATR
		{"range_too_small", tttBar(1, 88.5, 89.5, 88.5, 89.4, 200)},
		// volume = 100 < 1.5*100
		{"volume_too_low", tttBar(1, 88.0, 90.0, 88.0, 89.7, 100)},
		// wick = high(91) - close(89) plus open(88)-low(88) = 2 of range 3 -> 0.667 > 0.4
		{"wick_too_high", tttBar(1, 88.0, 91.0, 88.0, 89.0, 200)},
		// bearish: close < open
		{"bearish_close_for_call", tttBar(1, 90.0, 90.0, 88.0, 88.5, 200)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := builtin.NewTradingTheTrendStrategy()
			ctx := newTestContext(tttBaseTime)
			st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
			require.NoError(t, err)
			st = armSignal(t, s, ctx, "RKLB", st, tttBaseTime, 88.0, 90.0, "C")
			st2, sigs := feedTTTBar(t, s, ctx, "RKLB", st, c.bar)
			assert.Empty(t, sigs)
			tst := st2.(*builtin.TradingTheTrendState)
			assert.Equal(t, builtin.TTTPhaseWatching, tst.Phase, "should remain Watching")
		})
	}
}

// drive Phase A -> WaitingRetest with a clean breakout bar.
func driveToWaitingRetest(t *testing.T, s *builtin.TradingTheTrendStrategy, ctx *testContext, symbol string, st strat.State, baseTime time.Time, trigger float64) strat.State {
	t.Helper()
	st = armSignal(t, s, ctx, symbol, st, baseTime, trigger, trigger+2, "C")
	bar := tttBar(1, trigger, trigger+2, trigger, trigger+1.7, 200)
	st2, _ := feedTTTBar(t, s, ctx, symbol, st, bar)
	tst := st2.(*builtin.TradingTheTrendState)
	require.Equal(t, builtin.TTTPhaseWaitingRetest, tst.Phase)
	return st2
}

func TestTradingTheTrend_PhaseB_RetestZoneEntersConfirming(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	st = driveToWaitingRetest(t, s, ctx, "RKLB", st, tttBaseTime, 88.0)

	// Retest band = 0.15 * ATR(1) = 0.15. bar.Low must enter [87.85, 88.15].
	retestBar := tttBar(2, 89.0, 89.5, 88.05, 88.5, 100)
	st2, _ := feedTTTBar(t, s, ctx, "RKLB", st, retestBar)
	tst := st2.(*builtin.TradingTheTrendState)
	assert.Equal(t, builtin.TTTPhaseConfirming, tst.Phase)
}

func TestTradingTheTrend_PhaseB_DeepRetraceInvalidates(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	st = driveToWaitingRetest(t, s, ctx, "RKLB", st, tttBaseTime, 88.0)

	// invalidation_atr = 0.5; close < trigger - 0.5*ATR = 87.5 -> Idle.
	bar := tttBar(2, 88.0, 88.5, 87.0, 87.2, 100)
	st2, _ := feedTTTBar(t, s, ctx, "RKLB", st, bar)
	tst := st2.(*builtin.TradingTheTrendState)
	assert.Equal(t, builtin.TTTPhaseIdle, tst.Phase)
}

func TestTradingTheTrend_PhaseB_ExpiresAfter20Bars(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	st = driveToWaitingRetest(t, s, ctx, "RKLB", st, tttBaseTime, 88.0)

	// Drift sideways well above trigger but never within retest band.
	for i := 2; i <= 23; i++ {
		bar := tttBar(i, 89.0, 89.2, 88.8, 89.0, 100)
		var sigs []strat.Signal
		st, sigs = feedTTTBar(t, s, ctx, "RKLB", st, bar)
		require.Empty(t, sigs)
	}
	tst := st.(*builtin.TradingTheTrendState)
	assert.Equal(t, builtin.TTTPhaseIdle, tst.Phase)
}

// drive Phase A -> WaitingRetest -> Confirming, then verify a clean
// hold-confirm bar emits a Signal with the right OCC contract.
//
// Trigger is set high (200) so the 0.5% drift gate (~$1.00 absolute) leaves
// meaningful headroom above the 0.2 ATR breakout buffer ($0.20). At trigger=88
// the gate ($0.44) overlaps the buffer too tightly to construct a realistic
// confirm bar. The behavioural assertions below are unchanged by this rescaling.
func TestTradingTheTrend_PhaseC_HoldConfirmEmitsEntry(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	st = driveToWaitingRetest(t, s, ctx, "RKLB", st, tttBaseTime, 200.0)

	// Bar 2: enter retest band [199.85, 200.15] -> Confirming.
	retestBar := tttBar(2, 201.0, 201.5, 200.05, 200.5, 100)
	st, _ = feedTTTBar(t, s, ctx, "RKLB", st, retestBar)

	// Bar 3 (next bar after Confirming): hold-confirm.
	// close=200.7 > 200 + 0.2 buffer, < 200 * 1.005 = 201.0 (drift gate);
	// body=0.7, range=0.9 -> 0.78; bullish; > prev.Close(200.5).
	confirmBar := tttBar(3, 200.0, 200.8, 199.9, 200.7, 100)
	st2, sigs := feedTTTBar(t, s, ctx, "RKLB", st, confirmBar)
	require.Len(t, sigs, 1)
	sig := sigs[0]
	assert.Equal(t, strat.SignalEntry, sig.Type)
	assert.Equal(t, strat.SideBuy, sig.Side)
	assert.Equal(t, "RKLB", sig.Symbol)

	occ, ok := sig.Tags["contract_symbol"]
	require.True(t, ok)
	// nearestFriday with min_dte=2 from Tue 5/12/2026 -> Fri 5/15/2026 (3 DTE).
	expectedExpiry := time.Date(2026, 5, 15, 0, 0, 0, 0, etLoc)
	expectedOCC := domain.FormatOCCSymbol("RKLB", expectedExpiry, domain.OptionRightCall, 202.0)
	assert.Equal(t, expectedOCC, occ)

	tst := st2.(*builtin.TradingTheTrendState)
	assert.Equal(t, builtin.TTTPhaseIdle, tst.Phase)
	assert.True(t, tst.EnteredToday)
}

func TestTradingTheTrend_PhaseC_WeakHoldConfirmRejected(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	st = driveToWaitingRetest(t, s, ctx, "RKLB", st, tttBaseTime, 200.0)

	retestBar := tttBar(2, 201.0, 201.5, 200.05, 200.5, 100)
	st, _ = feedTTTBar(t, s, ctx, "RKLB", st, retestBar)

	// Weak: close < trigger + buffer (200 + 0.2).
	weak := tttBar(3, 200.0, 200.15, 199.5, 200.1, 100)
	st2, sigs := feedTTTBar(t, s, ctx, "RKLB", st, weak)
	assert.Empty(t, sigs)
	tst := st2.(*builtin.TradingTheTrendState)
	// Failing the hold-confirm bar drops back to Idle (locks the prereg
	// "one entry per signal per day" rule).
	assert.Equal(t, builtin.TTTPhaseIdle, tst.Phase)
}

func TestTradingTheTrend_HardCutoff_NoEntryAfter1330ET(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)

	// PostedAt at 13:35 ET — handleSignal must NOT arm.
	postedAt := time.Date(2026, 5, 12, 13, 35, 0, 0, etLoc)
	ctx.now = postedAt
	st2, _, err := s.OnEvent(ctx, "RKLB", tttSignal("RKLB", 88.0, 90.0, "C", postedAt), st)
	require.NoError(t, err)
	tst := st2.(*builtin.TradingTheTrendState)
	assert.Equal(t, builtin.TTTPhaseIdle, tst.Phase, "hard cutoff must drop signal")
}

func TestTradingTheTrend_FridayMinDTE_RollsToNextFriday(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	// Friday at noon ET — nearestFridayWithMinDTE(min_dte=2) must advance
	// past the same-day Friday and pick the following Friday. Use trigger=200
	// so the 0.5% drift gate has room above the breakout buffer.
	fridayNoon := time.Date(2026, 5, 15, 12, 0, 0, 0, etLoc)
	ctx := newTestContext(fridayNoon)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	st = armSignal(t, s, ctx, "RKLB", st, fridayNoon, 200.0, 202.0, "C")

	bar1 := strat.Bar{Time: fridayNoon.Add(time.Minute), Open: 200, High: 202, Low: 200, Close: 201.7, Volume: 200}
	st, _ = feedTTTBar(t, s, ctx, "RKLB", st, bar1)
	bar2 := strat.Bar{Time: fridayNoon.Add(2 * time.Minute), Open: 201, High: 201.5, Low: 200.05, Close: 200.5, Volume: 100}
	st, _ = feedTTTBar(t, s, ctx, "RKLB", st, bar2)
	bar3 := strat.Bar{Time: fridayNoon.Add(3 * time.Minute), Open: 200, High: 200.8, Low: 199.9, Close: 200.7, Volume: 100}
	st, sigs := feedTTTBar(t, s, ctx, "RKLB", st, bar3)
	require.Len(t, sigs, 1, "must still emit (12:00 < 13:30 cutoff)")
	occ := sigs[0].Tags["contract_symbol"]
	expectedExpiry := time.Date(2026, 5, 22, 0, 0, 0, 0, etLoc)
	expectedOCC := domain.FormatOCCSymbol("RKLB", expectedExpiry, domain.OptionRightCall, 202.0)
	assert.Equal(t, expectedOCC, occ, "min_dte=2 must skip 0DTE Friday-of-signal")
	_ = st
}

func TestTradingTheTrend_TriggerDriftGate_RejectsConfirmTooFarAbove(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	// Trigger=100 so 1% drift = 101.0; confirm bar will close at 102 -> reject.
	st = armSignal(t, s, ctx, "RKLB", st, tttBaseTime, 100.0, 100.0, "C")

	// Phase A breakout bar.
	bar1 := tttBar(1, 100.0, 102.0, 100.0, 101.7, 200)
	st, _ = feedTTTBar(t, s, ctx, "RKLB", st, bar1)
	// Phase B retest.
	bar2 := tttBar(2, 101.0, 101.5, 100.05, 100.5, 100)
	st, _ = feedTTTBar(t, s, ctx, "RKLB", st, bar2)
	// Phase C bar that would otherwise pass but close=102 (>100*1.005=100.5).
	bar3 := tttBar(3, 100.0, 102.0, 99.5, 102.0, 100)
	st2, sigs := feedTTTBar(t, s, ctx, "RKLB", st, bar3)
	assert.Empty(t, sigs)
	tst := st2.(*builtin.TradingTheTrendState)
	assert.Equal(t, builtin.TTTPhaseIdle, tst.Phase)
}

func TestTradingTheTrend_StaleSignal_Dropped(t *testing.T) {
	s := builtin.NewTradingTheTrendStrategy()
	ctx := newTestContext(tttBaseTime)
	st, err := s.Init(ctx, "RKLB", tttDefaultParams(), nil)
	require.NoError(t, err)
	// PostedAt 5 minutes before now, freshness_max_age_secs=60.
	postedAt := tttBaseTime.Add(-5 * time.Minute)
	ctx.now = tttBaseTime
	st2, _, err := s.OnEvent(ctx, "RKLB", tttSignal("RKLB", 88.0, 90.0, "C", postedAt), st)
	require.NoError(t, err)
	tst := st2.(*builtin.TradingTheTrendState)
	assert.Equal(t, builtin.TTTPhaseIdle, tst.Phase)
}

// ---------------------------------------------------------------------------
// Parser parity tests
// ---------------------------------------------------------------------------

// The expected outputs below are the canonical golden outputs produced by
// services/discord-tradingthetrend/parser.py for the same inputs. Any
// divergence breaks parity; the regex on both sides must match the prereg
// grammar in section 3.
func TestTradingTheTrend_ParserParity(t *testing.T) {
	type expected struct {
		Ticker, Right string
		Strike        float64
		Trigger       float64
	}
	type tc struct {
		name string
		text string
		want []expected
	}
	cases := []tc{
		{"sample_rklb", "RKLB    90c     >    88.00", []expected{{"RKLB", "C", 90.0, 88.0}}},
		{"sample_msft", "MSFT 425c > 423.00", []expected{{"MSFT", "C", 425.0, 423.0}}},
		{"sample_nvda_decimal", "NVDA 217.5c > 215.00", []expected{{"NVDA", "C", 217.5, 215.0}}},
		{"sample_tsla_put", "TSLA 425p < 421.00", []expected{{"TSLA", "P", 425.0, 421.0}}},
		{"lowercase_ticker", "aapl 150c > 148.00", []expected{{"AAPL", "C", 150.0, 148.0}}},
		{"no_space_around_gt", "AAPL 150c>148.00", []expected{{"AAPL", "C", 150.0, 148.0}}},
		{"multiline_full", "RKLB 90c > 88.00\nMSFT 425c > 423.00\nTSLA 425p < 421.00",
			[]expected{
				{"RKLB", "C", 90.0, 88.0},
				{"MSFT", "C", 425.0, 423.0},
				{"TSLA", "P", 425.0, 421.0},
			}},
		{"mixed_with_commentary", "RKLB 90c > 88.00\nWatching this one\nMSFT 425c > 423.00",
			[]expected{
				{"RKLB", "C", 90.0, 88.0},
				{"MSFT", "C", 425.0, 423.0},
			}},
		// Noise cases must produce empty.
		{"noise_commentary", "Watching RKLB closely today", nil},
		{"noise_missing_right", "RKLB 90 > 88.00", nil},
		{"noise_backwards_lt", "RKLB 90c < 88.00", nil},
		{"noise_extra_tokens", "RKLB 90c > 88.00 partial", nil},
		{"noise_negative_strike", "RKLB -90c > 88.00", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := builtin.ParseTradingTheTrendMessage(c.text)
			require.Len(t, got, len(c.want))
			for i, w := range c.want {
				assert.Equal(t, w.Ticker, got[i].Ticker)
				assert.Equal(t, w.Right, got[i].Right)
				assert.Equal(t, w.Strike, got[i].Strike)
				assert.Equal(t, w.Trigger, got[i].Trigger)
			}
		})
	}
}
