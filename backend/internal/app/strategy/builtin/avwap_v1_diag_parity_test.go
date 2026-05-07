package builtin_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// avwapDiagParams returns a minimal AVWAP config with the HVN + EMA tag-only
// diagnostics enabled. Trading-side knobs are kept small so OnBar walks
// through every per-bar update path without firing entry signals.
func avwapDiagParams() map[string]any {
	return map[string]any{
		"breakout_enabled":    true,
		"hold_bars":           2,
		"volume_mult":         100.0, // suppress entries; we only care about per-bar state
		"bounce_enabled":      false,
		"pullback_enabled":    false,
		"pinch_enabled":       false,
		"min_confluence_score": 999, // never gate-pass; we only care about updateHVN/updateEMA
		"exit_hold_bars":      2,
		"cooldown_seconds":    120,
		"max_trades_per_day":  3,
		"allow_regimes":       []any{"TREND_UP", "TREND_DOWN", "BALANCE"},
		"allowed_hours_start": "09:30",
		"allowed_hours_end":   "16:00",
		"allowed_hours_tz":    "America/New_York",
		"hvn_diag_enabled":    true,
		"hvn_lookback_days":   2,
		"hvn_bin_bps":         10.0,
		"hvn_threshold_pct":   80.0,
		"hvn_rth_only":        true,
		"ema_diag_enabled":    true,
		"ema_diag_period":     9,
	}
}

func diagBar(t time.Time, close, vol float64) strat.Bar {
	return strat.Bar{
		Time:   t,
		Open:   close,
		High:   close + 0.05,
		Low:    close - 0.05,
		Close:  close,
		Volume: vol,
	}
}

// diagFixtureBars produces 3 RTH sessions (Mon-Wed 2026-03-09..11) with 12
// 5m bars each, distinct anchor prices so HVN merging is observable across
// the rolling window.
func diagFixtureBars() []strat.Bar {
	bars := []strat.Bar{}
	starts := []time.Time{
		mustET(2026, 3, 9, 10, 0),
		mustET(2026, 3, 10, 10, 0),
		mustET(2026, 3, 11, 10, 0),
	}
	prices := []float64{90.0, 100.0, 110.0}
	for i, start := range starts {
		for j := 0; j < 12; j++ {
			bars = append(bars, diagBar(start.Add(time.Duration(j*5)*time.Minute), prices[i], 1000))
		}
	}
	return bars
}

func TestAVWAP_HVNFingerprintParity_LiveVsWarmup(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	bars := diagFixtureBars()
	ind := strat.IndicatorData{ATR: 1.0, VWAP: 100.0}

	// Path A: every bar via ReplayOnBar (warmup feed only).
	ctxA := newTestContext(bars[0].Time)
	stA, err := s.Init(ctxA, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	for _, b := range bars {
		ctxA.now = b.Time
		stA, err = s.ReplayOnBar(ctxA, "AAPL", b, stA, ind)
		require.NoError(t, err)
	}

	// Path B: first 18 via ReplayOnBar then remaining via OnBar (mixed, mirrors
	// warm-on-restart at midday). All bars are within the strategy's
	// allowed_hours window so OnBar reaches updateHVN on every bar.
	ctxB := newTestContext(bars[0].Time)
	stB, err := s.Init(ctxB, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	const cut = 18
	for i, b := range bars {
		ctxB.now = b.Time
		if i < cut {
			stB, err = s.ReplayOnBar(ctxB, "AAPL", b, stB, ind)
			require.NoError(t, err)
		} else {
			avSt := stB.(*builtin.AVWAPState)
			avSt.SetIndicators(ind)
			st2, _, err2 := s.OnBar(ctxB, "AAPL", b, stB)
			require.NoError(t, err2)
			stB = st2
		}
	}

	a := stA.(*builtin.AVWAPState)
	b := stB.(*builtin.AVWAPState)
	require.NotEmpty(t, a.HVNFingerprint(),
		"warmup feed must populate HVN set after multi-session bar feed")
	assert.Equal(t, a.HVNFingerprint(), b.HVNFingerprint(),
		"HVN merged set must be byte-equal across replay-only and replay-then-live paths")
}

func TestAVWAP_EMAValueParity_LiveVsWarmup(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	bars := diagFixtureBars()
	ind := strat.IndicatorData{ATR: 1.0, VWAP: 100.0}

	ctxA := newTestContext(bars[0].Time)
	stA, err := s.Init(ctxA, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	for _, b := range bars {
		ctxA.now = b.Time
		stA, err = s.ReplayOnBar(ctxA, "AAPL", b, stA, ind)
		require.NoError(t, err)
	}

	ctxB := newTestContext(bars[0].Time)
	stB, err := s.Init(ctxB, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	const cut = 18
	for i, b := range bars {
		ctxB.now = b.Time
		if i < cut {
			stB, err = s.ReplayOnBar(ctxB, "AAPL", b, stB, ind)
			require.NoError(t, err)
		} else {
			avSt := stB.(*builtin.AVWAPState)
			avSt.SetIndicators(ind)
			st2, _, err2 := s.OnBar(ctxB, "AAPL", b, stB)
			require.NoError(t, err2)
			stB = st2
		}
	}

	a := stA.(*builtin.AVWAPState)
	b := stB.(*builtin.AVWAPState)
	require.True(t, a.EMAReady, "EMA must be warm after fixture (period=9, bars=36)")
	require.True(t, b.EMAReady, "EMA must be warm after fixture (period=9, bars=36)")
	assert.InDelta(t, a.EMAValue, b.EMAValue, 1e-12,
		"EMA value must be ulp-equal across replay-only and replay-then-live paths")
}

func TestAVWAP_HVNState_InitFromPrior_ResetsAndRewarms(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	bars := diagFixtureBars()
	ind := strat.IndicatorData{ATR: 1.0, VWAP: 100.0}

	ctx := newTestContext(bars[0].Time)
	st, err := s.Init(ctx, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	for _, b := range bars {
		ctx.now = b.Time
		st, err = s.ReplayOnBar(ctx, "AAPL", b, st, ind)
		require.NoError(t, err)
	}
	prior := st.(*builtin.AVWAPState)
	priorFingerprint := prior.HVNFingerprint()
	require.NotEmpty(t, priorFingerprint, "fixture must populate HVN set before restore")

	ctx2 := newTestContext(bars[0].Time)
	restored, err := s.Init(ctx2, "AAPL", avwapDiagParams(), prior)
	require.NoError(t, err)
	rs := restored.(*builtin.AVWAPState)
	assert.Empty(t, rs.HVNFingerprint(),
		"prior-restore must wipe HVN scalars; warmup re-derives via ReplayOnBar")

	for _, b := range bars {
		ctx2.now = b.Time
		restored, err = s.ReplayOnBar(ctx2, "AAPL", b, restored, ind)
		require.NoError(t, err)
	}
	rs = restored.(*builtin.AVWAPState)
	assert.Equal(t, priorFingerprint, rs.HVNFingerprint(),
		"warmup feed after prior-restore must reproduce the original HVN snapshot byte-equal")
}

// crossFixtureBars produces a single RTH session with within-session price
// variation that drives the AVWAP across +1 and -1 sign in turn so
// AVWAPCrossBarsSince / AVWAPCrossBreachMaxATR have observable state.
// Layout: 10 bars at $100, 10 bars at $110 (above-cross), 10 bars at $95
// (below-cross). 5m cadence starting 09:30 ET.
func crossFixtureBars() []strat.Bar {
	bars := []strat.Bar{}
	startT := mustET(2026, 3, 9, 9, 30)
	prices := []float64{100, 100, 100, 100, 100, 100, 100, 100, 100, 100,
		110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
		95, 95, 95, 95, 95, 95, 95, 95, 95, 95}
	for i, p := range prices {
		bars = append(bars, diagBar(startT.Add(time.Duration(i*5)*time.Minute), p, 1000))
	}
	return bars
}

func TestAVWAP_AVWAPCrossBarsSince_LiveVsWarmup(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	bars := crossFixtureBars()
	ind := strat.IndicatorData{ATR: 1.0, VWAP: 100.0}

	ctxA := newTestContext(bars[0].Time)
	stA, err := s.Init(ctxA, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	for _, b := range bars {
		ctxA.now = b.Time
		stA, err = s.ReplayOnBar(ctxA, "AAPL", b, stA, ind)
		require.NoError(t, err)
	}

	ctxB := newTestContext(bars[0].Time)
	stB, err := s.Init(ctxB, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	const cut = 15
	for i, b := range bars {
		ctxB.now = b.Time
		if i < cut {
			stB, err = s.ReplayOnBar(ctxB, "AAPL", b, stB, ind)
			require.NoError(t, err)
		} else {
			avSt := stB.(*builtin.AVWAPState)
			avSt.SetIndicators(ind)
			st2, _, err2 := s.OnBar(ctxB, "AAPL", b, stB)
			require.NoError(t, err2)
			stB = st2
		}
	}

	a := stA.(*builtin.AVWAPState)
	b := stB.(*builtin.AVWAPState)
	require.NotEmpty(t, a.AVWAPCrossBarsSince,
		"crossFixtureBars must produce at least one AVWAP cross")
	assert.Equal(t, a.AVWAPCrossBarsSince, b.AVWAPCrossBarsSince,
		"per-anchor bars-since-cross must agree across replay-only and replay-then-live paths")
}

func TestAVWAP_AVWAPCrossBreachMax_LiveVsWarmup(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	bars := crossFixtureBars()
	ind := strat.IndicatorData{ATR: 1.0, VWAP: 100.0}

	ctxA := newTestContext(bars[0].Time)
	stA, err := s.Init(ctxA, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	for _, b := range bars {
		ctxA.now = b.Time
		stA, err = s.ReplayOnBar(ctxA, "AAPL", b, stA, ind)
		require.NoError(t, err)
	}

	ctxB := newTestContext(bars[0].Time)
	stB, err := s.Init(ctxB, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	const cut = 15
	for i, b := range bars {
		ctxB.now = b.Time
		if i < cut {
			stB, err = s.ReplayOnBar(ctxB, "AAPL", b, stB, ind)
			require.NoError(t, err)
		} else {
			avSt := stB.(*builtin.AVWAPState)
			avSt.SetIndicators(ind)
			st2, _, err2 := s.OnBar(ctxB, "AAPL", b, stB)
			require.NoError(t, err2)
			stB = st2
		}
	}

	a := stA.(*builtin.AVWAPState)
	b := stB.(*builtin.AVWAPState)
	require.NotEmpty(t, a.AVWAPCrossBreachMaxATR,
		"crossFixtureBars must populate breach-max for at least one anchor")
	for k, va := range a.AVWAPCrossBreachMaxATR {
		assert.InDelta(t, va, b.AVWAPCrossBreachMaxATR[k], 1e-12,
			"per-anchor max-breach must be ulp-equal across replay-only and replay-then-live paths for anchor %s", k)
	}
}

func TestAVWAP_EMAState_InitFromPrior_ResetsAndRewarms(t *testing.T) {
	s := builtin.NewAVWAPStrategy()
	bars := diagFixtureBars()
	ind := strat.IndicatorData{ATR: 1.0, VWAP: 100.0}

	ctx := newTestContext(bars[0].Time)
	st, err := s.Init(ctx, "AAPL", avwapDiagParams(), nil)
	require.NoError(t, err)
	for _, b := range bars {
		ctx.now = b.Time
		st, err = s.ReplayOnBar(ctx, "AAPL", b, st, ind)
		require.NoError(t, err)
	}
	prior := st.(*builtin.AVWAPState)
	priorEMA := prior.EMAValue
	require.True(t, prior.EMAReady, "fixture must warm EMA before restore")

	ctx2 := newTestContext(bars[0].Time)
	restored, err := s.Init(ctx2, "AAPL", avwapDiagParams(), prior)
	require.NoError(t, err)
	rs := restored.(*builtin.AVWAPState)
	assert.False(t, rs.EMAReady, "prior-restore must reset EMA readiness; warmup re-derives")
	assert.Equal(t, 0.0, rs.EMAValue, "prior-restore must zero EMAValue; warmup re-derives")

	for _, b := range bars {
		ctx2.now = b.Time
		restored, err = s.ReplayOnBar(ctx2, "AAPL", b, restored, ind)
		require.NoError(t, err)
	}
	rs = restored.(*builtin.AVWAPState)
	require.True(t, rs.EMAReady, "warmup must re-warm EMA after prior-restore")
	assert.InDelta(t, priorEMA, rs.EMAValue, 1e-12,
		"warmup re-feed after prior-restore must reproduce the original EMA value byte-equal")
}
