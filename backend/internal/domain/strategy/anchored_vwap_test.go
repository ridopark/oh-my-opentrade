package strategy_test

import (
	"math"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnchoredVWAPCalc_AVWAP_SingleAnchor_BasicMath(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)

	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "pd_high", AnchorTime: t0, Price: 0})

	c.Update(t0.Add(1*time.Minute), 102, 98, 100, 1000)
	v, ok := c.Value("pd_high")
	require.True(t, ok)
	assert.Equal(t, 100.0, v)

	c.Update(t0.Add(2*time.Minute), 105, 101, 103, 2000)
	v, ok = c.Value("pd_high")
	require.True(t, ok)
	assert.Equal(t, 102.0, v)

	c.Update(t0.Add(3*time.Minute), 108, 102, 105, 3000)
	v, ok = c.Value("pd_high")
	require.True(t, ok)
	assert.Equal(t, 103.5, v)
}

func TestAnchoredVWAPCalc_AVWAP_MultipleAnchors(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	t2 := t0.Add(2 * time.Minute)

	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "pd_high", AnchorTime: t0, Price: 0})
	c.AddAnchor(strategy.AnchorPoint{Name: "session_open", AnchorTime: t2, Price: 0})

	bars := []struct {
		t      time.Time
		h, l   float64
		cl     float64
		v      float64
		pd     float64
		open   float64
		openOK bool
	}{
		{t0, 102, 98, 100, 1000, 100.0, 0, false},
		{t0.Add(1 * time.Minute), 105, 101, 103, 2000, 102.0, 0, false},
		{t2, 110, 100, 105, 1000, (306000.0 + 105000.0) / (3000.0 + 1000.0), 105.0, true},
		{t0.Add(3 * time.Minute), 111, 99, 105, 1000, (306000.0 + 105000.0 + 105000.0) / (3000.0 + 2000.0), 105.0, true},
	}

	for _, b := range bars {
		c.Update(b.t, b.h, b.l, b.cl, b.v)

		gotPD, ok := c.Value("pd_high")
		require.True(t, ok)
		assert.InDelta(t, b.pd, gotPD, 1e-12)

		gotOpen, ok := c.Value("session_open")
		if !b.openOK {
			assert.False(t, ok)
			assert.Equal(t, 0.0, gotOpen)
			continue
		}
		assert.True(t, ok)
		assert.InDelta(t, b.open, gotOpen, 1e-12)
	}
}

func TestAnchoredVWAPCalc_AVWAP_IgnoresBarBeforeAnchorTime(t *testing.T) {
	t1 := time.Date(2026, 1, 2, 9, 31, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 2, 9, 33, 0, 0, time.UTC)
	t5 := time.Date(2026, 1, 2, 9, 35, 0, 0, time.UTC)
	t7 := time.Date(2026, 1, 2, 9, 37, 0, 0, time.UTC)

	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "or_high", AnchorTime: t5, Price: 0})

	c.Update(t1, 102, 98, 100, 1000)
	_, ok := c.Value("or_high")
	assert.False(t, ok)

	c.Update(t3, 105, 101, 103, 2000)
	_, ok = c.Value("or_high")
	assert.False(t, ok)

	c.Update(t5, 102, 98, 100, 1000)
	v, ok := c.Value("or_high")
	require.True(t, ok)
	assert.Equal(t, 100.0, v)

	c.Update(t7, 105, 101, 103, 2000)
	v, ok = c.Value("or_high")
	require.True(t, ok)
	assert.Equal(t, 102.0, v)
}

func TestAnchoredVWAPCalc_AVWAP_ZeroVolume(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "pd_low", AnchorTime: t0, Price: 0})

	c.Update(t0.Add(1*time.Minute), 102, 98, 100, 0)
	v, ok := c.Value("pd_low")
	require.True(t, ok)
	assert.Equal(t, 0.0, v)
}

func TestAnchoredVWAPCalc_AVWAP_ValueNotFound(t *testing.T) {
	c := strategy.NewAnchoredVWAPCalc()
	v, ok := c.Value("nonexistent")
	assert.False(t, ok)
	assert.Equal(t, 0.0, v)
}

func TestAnchoredVWAPCalc_AVWAP_StatesAndRestore(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	points := []strategy.AnchorPoint{{Name: "pd_high", AnchorTime: t0, Price: 0}}

	bars := []struct {
		t    time.Time
		h, l float64
		cl   float64
		v    float64
	}{
		{t0.Add(1 * time.Minute), 102, 98, 100, 1000},
		{t0.Add(2 * time.Minute), 105, 101, 103, 2000},
		{t0.Add(3 * time.Minute), 108, 102, 105, 3000},
		{t0.Add(4 * time.Minute), 111, 99, 105, 1000},
		{t0.Add(5 * time.Minute), 114, 96, 105, 1000},
	}

	baseline := strategy.NewAnchoredVWAPCalc()
	for _, p := range points {
		baseline.AddAnchor(p)
	}
	for _, b := range bars {
		baseline.Update(b.t, b.h, b.l, b.cl, b.v)
	}
	baselineVals := baseline.Values()

	c1 := strategy.NewAnchoredVWAPCalc()
	for _, p := range points {
		c1.AddAnchor(p)
	}
	for _, b := range bars[:3] {
		c1.Update(b.t, b.h, b.l, b.cl, b.v)
	}
	savedStates := c1.States()

	c2 := strategy.NewAnchoredVWAPCalc()
	c2.Restore(points, savedStates)
	for _, b := range bars[3:] {
		c2.Update(b.t, b.h, b.l, b.cl, b.v)
	}

	assert.Equal(t, baselineVals, c2.Values())
}

func TestAnchoredVWAPState_SD_SingleBar(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "test", AnchorTime: t0})
	c.Update(t0.Add(time.Minute), 102, 98, 100, 1000) // tp=100

	states := c.States()
	st := states["test"]
	assert.Equal(t, 0.0, st.SD(), "single bar should have zero SD")
	assert.Equal(t, 0.0, st.Variance())
}

func TestAnchoredVWAPState_SD_KnownValues(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "test", AnchorTime: t0})

	// Bar 1: tp=(102+98+100)/3=100, v=1000
	// Bar 2: tp=(105+101+103)/3=103, v=2000
	// Bar 3: tp=(108+102+105)/3=105, v=3000
	c.Update(t0.Add(1*time.Minute), 102, 98, 100, 1000)
	c.Update(t0.Add(2*time.Minute), 105, 101, 103, 2000)
	c.Update(t0.Add(3*time.Minute), 108, 102, 105, 3000)

	states := c.States()
	st := states["test"]

	// Manual Welford:
	// Bar1: old=0, new=100, M2=1000*(100-0)*(100-100)=0
	// Bar2: old=100, new=102, M2=0+2000*(103-100)*(103-102)=6000
	// Bar3: old=102, new=103.5, M2=6000+3000*(105-102)*(105-103.5)=19500
	// Var = 19500/6000 = 3.25, SD = sqrt(3.25) ≈ 1.80278
	assert.InDelta(t, 3.25, st.Variance(), 1e-9)
	assert.InDelta(t, math.Sqrt(3.25), st.SD(), 1e-9)
}

func TestAnchoredVWAPCalc_SDBands_SingleAnchor(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "test", AnchorTime: t0})

	c.Update(t0.Add(1*time.Minute), 102, 98, 100, 1000)
	c.Update(t0.Add(2*time.Minute), 105, 101, 103, 2000)
	c.Update(t0.Add(3*time.Minute), 108, 102, 105, 3000)
	for i := 4; i <= 10; i++ {
		c.Update(t0.Add(time.Duration(i)*time.Minute), 108, 102, 105, 3000)
	}

	vals := c.States()
	st := vals["test"]
	sd := st.SD()
	vwap := st.Value()

	upper, lower, ok := c.SDBands("test", 2.0)
	require.True(t, ok)
	assert.InDelta(t, vwap+2.0*sd, upper, 0.01)
	assert.InDelta(t, vwap-2.0*sd, lower, 0.01)

	upper1, lower1, ok := c.SDBands("test", 1.0)
	require.True(t, ok)
	assert.InDelta(t, vwap+sd, upper1, 0.01)
	assert.InDelta(t, vwap-sd, lower1, 0.01)
}

func TestAnchoredVWAPCalc_SDBands_NotFound(t *testing.T) {
	c := strategy.NewAnchoredVWAPCalc()
	_, _, ok := c.SDBands("nonexistent", 2.0)
	assert.False(t, ok)
}

func TestAnchoredVWAPCalc_SDBands_SingleBarReturnsNotOK(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "test", AnchorTime: t0})
	c.Update(t0.Add(time.Minute), 100, 100, 100, 1000)

	_, _, ok := c.SDBands("test", 2.0)
	assert.False(t, ok, "single bar has M2=0, bands should be unavailable")
}

func TestAnchoredVWAPCalc_AllSDBands(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "a", AnchorTime: t0})
	c.AddAnchor(strategy.AnchorPoint{Name: "b", AnchorTime: t0})

	c.Update(t0.Add(1*time.Minute), 102, 98, 100, 1000)
	c.Update(t0.Add(2*time.Minute), 105, 101, 103, 2000)
	c.Update(t0.Add(3*time.Minute), 108, 102, 105, 3000)
	for i := 4; i <= 10; i++ {
		c.Update(t0.Add(time.Duration(i)*time.Minute), 108, 102, 105, 3000)
	}

	bands := c.AllSDBands(2.0)
	require.Len(t, bands, 2)
	assert.Contains(t, bands, "a")
	assert.Contains(t, bands, "b")

	vals := c.States()
	st := vals["a"]
	vwap := st.Value()
	sd := st.SD()
	require.True(t, sd > 0)
	for _, pair := range bands {
		assert.InDelta(t, vwap+2.0*sd, pair[0], 0.01)
		assert.InDelta(t, vwap-2.0*sd, pair[1], 0.01)
	}
}

func TestAnchoredVWAPState_SD_RestorePreservesM2(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "test", AnchorTime: t0})

	c.Update(t0.Add(1*time.Minute), 102, 98, 100, 1000)
	c.Update(t0.Add(2*time.Minute), 105, 101, 103, 2000)
	c.Update(t0.Add(3*time.Minute), 108, 102, 105, 3000)

	savedStates := c.States()
	origSD := savedStates["test"].SD()
	require.Greater(t, origSD, 0.0)

	c2 := strategy.NewAnchoredVWAPCalc()
	c2.Restore([]strategy.AnchorPoint{{Name: "test", AnchorTime: t0}}, savedStates)

	restoredStates := c2.States()
	assert.InDelta(t, origSD, restoredStates["test"].SD(), 1e-15)
	assert.InDelta(t, savedStates["test"].M2, restoredStates["test"].M2, 1e-15)
}

func TestAnchoredVWAPState_SD_BackwardCompat_ZeroM2(t *testing.T) {
	st := strategy.AnchoredVWAPState{CumPV: 100000, CumV: 1000, M2: 0}
	assert.Equal(t, 100.0, st.Value())
	assert.Equal(t, 0.0, st.SD(), "zero M2 should return SD=0, not NaN")
	assert.Equal(t, 0.0, st.Variance())
}

func TestAnchoredVWAPCalc_Snapshot(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)

	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{Name: "pd_high", AnchorTime: t0, Price: 0})
	c.AddAnchor(strategy.AnchorPoint{Name: "session_open", AnchorTime: t0.Add(2 * time.Minute), Price: 0})

	// Empty snapshot before any bars: anchors registered but inactive.
	snap := c.Snapshot(5)
	require.Len(t, snap, 2)
	assert.False(t, snap["pd_high"].Active)
	assert.Equal(t, 0, snap["pd_high"].BarCount)

	// Feed enough bars to exceed slope-lookback (5) on pd_high.
	for i := 1; i <= 8; i++ {
		c.Update(t0.Add(time.Duration(i)*time.Minute), 102+float64(i), 98+float64(i), 100+float64(i), 1000)
	}

	snap = c.Snapshot(5)
	pd := snap["pd_high"]
	assert.True(t, pd.Active)
	assert.Equal(t, 8, pd.BarCount)
	assert.GreaterOrEqual(t, pd.VWAPCount, 5, "ring buffer should have >= lookback samples")
	assert.Greater(t, pd.SlopeBPS, 0.0, "monotonically rising VWAP must produce positive slope")
	assert.Greater(t, pd.VWAP, 0.0)

	// session_open registered later: should be active and have fewer bars.
	so := snap["session_open"]
	assert.True(t, so.Active)
	assert.Less(t, so.BarCount, pd.BarCount)

	assert.Equal(t, t0.Add(8*time.Minute), c.LastBarTime())
}

// rthOpenET returns the UTC time corresponding to 09:30 ET on the given
// non-holiday weekday. Used by RTHOnly tests so bar timestamps fall
// inside / outside the package-local isRTH window.
func rthOpenET(t *testing.T, year int, month time.Month, day int) time.Time {
	t.Helper()
	return time.Date(year, month, day, 9, 30, 0, 0, domain.NYLocation()).UTC()
}

// TestAnchoredVWAPCalc_UpdateSingleAnchor_RTHOnly_SkipsNonRTH pins the
// invariant that UpdateSingleAnchor must skip non-RTH bars when the
// per-anchor RTHOnly flag is set, mirroring Update's gate at line 167.
// Pre-fix: replay path (which calls UpdateSingleAnchor) accumulated all
// bars regardless of session, inflating pd_high/pd_low barCount and
// distorting VWAP versus live.
func TestAnchoredVWAPCalc_UpdateSingleAnchor_RTHOnly_SkipsNonRTH(t *testing.T) {
	rthOpen := rthOpenET(t, 2026, 1, 5) // Monday, EST
	preMarket := rthOpen.Add(-2 * time.Hour)
	rthBar := rthOpen.Add(1 * time.Minute)
	afterHours := rthOpen.Add(7 * time.Hour) // 16:30 ET

	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{
		Name:       "pd_high",
		AnchorTime: preMarket,
		RTHOnly:    true,
	})

	// Pre-market bar after anchor time: must NOT count toward state.
	c.UpdateSingleAnchor("pd_high", rthOpen.Add(-30*time.Minute), 102, 98, 100, 1000)
	// RTH bar: must count.
	c.UpdateSingleAnchor("pd_high", rthBar, 105, 101, 103, 2000)
	// After-hours bar: must NOT count.
	c.UpdateSingleAnchor("pd_high", afterHours, 110, 105, 108, 3000)

	snap := c.Snapshot(5)["pd_high"]
	assert.Equal(t, 1, snap.BarCount, "only the single RTH bar must increment barCount")
	assert.True(t, snap.Active, "anchor must be active once barTime >= AnchorTime")
	// VWAP equals the typical price of the lone RTH bar = (105+101+103)/3 = 103.
	assert.InDelta(t, 103.0, snap.VWAP, 1e-9, "VWAP must reflect only the RTH bar")
}

// TestAnchoredVWAPCalc_UpdateSingleAnchor_NonRTHOnly_AcceptsAll pins that
// when RTHOnly is false, UpdateSingleAnchor still accumulates every bar
// (preserves existing crypto / 24H-mode behavior).
func TestAnchoredVWAPCalc_UpdateSingleAnchor_NonRTHOnly_AcceptsAll(t *testing.T) {
	rthOpen := rthOpenET(t, 2026, 1, 5)
	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{
		Name:       "pd_high_24h",
		AnchorTime: rthOpen.Add(-2 * time.Hour),
		RTHOnly:    false,
	})

	c.UpdateSingleAnchor("pd_high_24h", rthOpen.Add(-30*time.Minute), 100, 100, 100, 1000)
	c.UpdateSingleAnchor("pd_high_24h", rthOpen.Add(1*time.Minute), 100, 100, 100, 1000)
	c.UpdateSingleAnchor("pd_high_24h", rthOpen.Add(7*time.Hour), 100, 100, 100, 1000)

	snap := c.Snapshot(5)["pd_high_24h"]
	assert.Equal(t, 3, snap.BarCount, "all three bars must count when RTHOnly is false")
}

// TestAnchoredVWAPCalc_UpdateSingleAnchor_MatchesUpdate_RTHOnly pins the
// parity invariant between Update and UpdateSingleAnchor: feeding the
// same monotonic mixed-RTH bar sequence into two equivalent calcs (one
// driven via Update, the other via UpdateSingleAnchor) must produce
// byte-identical state. Without this invariant, replay diverges from
// runtime even when both paths nominally honor RTHOnly.
func TestAnchoredVWAPCalc_UpdateSingleAnchor_MatchesUpdate_RTHOnly(t *testing.T) {
	rthOpen := rthOpenET(t, 2026, 1, 5)
	anchor := strategy.AnchorPoint{
		Name:       "pd_high",
		AnchorTime: rthOpen.Add(-2 * time.Hour),
		RTHOnly:    true,
	}

	bars := []struct {
		t          time.Time
		h, l, c, v float64
	}{
		{rthOpen.Add(-30 * time.Minute), 100, 100, 100, 1000}, // pre-market
		{rthOpen.Add(0 * time.Minute), 102, 98, 100, 5000},    // RTH open
		{rthOpen.Add(1 * time.Minute), 105, 101, 103, 2000},   // RTH
		{rthOpen.Add(2 * time.Minute), 108, 102, 105, 3000},   // RTH
		{rthOpen.Add(3 * time.Minute), 107, 103, 105, 0},      // RTH but zero volume
		{rthOpen.Add(4 * time.Minute), 109, 104, 107, 1500},   // RTH
		{rthOpen.Add(7 * time.Hour), 110, 105, 108, 4000},     // after-hours
	}

	updateCalc := strategy.NewAnchoredVWAPCalc()
	updateCalc.AddAnchor(anchor)
	singleCalc := strategy.NewAnchoredVWAPCalc()
	singleCalc.AddAnchor(anchor)

	for _, b := range bars {
		updateCalc.Update(b.t, b.h, b.l, b.c, b.v)
		singleCalc.UpdateSingleAnchor("pd_high", b.t, b.h, b.l, b.c, b.v)
	}

	updateSnap := updateCalc.Snapshot(5)["pd_high"]
	singleSnap := singleCalc.Snapshot(5)["pd_high"]

	assert.Equal(t, updateSnap.BarCount, singleSnap.BarCount, "barCount must match")
	assert.Equal(t, updateSnap.VWAPCount, singleSnap.VWAPCount, "vwapCount must match")
	assert.InDelta(t, updateSnap.VWAP, singleSnap.VWAP, 1e-9, "VWAP must match")
	assert.Equal(t, updateSnap.Active, singleSnap.Active, "active flag must match")

	updateState := updateCalc.States()["pd_high"]
	singleState := singleCalc.States()["pd_high"]
	assert.InDelta(t, updateState.CumPV, singleState.CumPV, 1e-9, "CumPV must match")
	assert.InDelta(t, updateState.CumV, singleState.CumV, 1e-9, "CumV must match")
	assert.InDelta(t, updateState.M2, singleState.M2, 1e-9, "M2 must match")

	// Sanity: pre-market and after-hours bars (and the zero-volume one)
	// were skipped, leaving 4 RTH-and-positive-volume bars in barCount.
	assert.Equal(t, 4, singleSnap.BarCount,
		"only RTH positive-volume bars should count: open + 3 RTH bars")
}

// TestAnchoredVWAPCalc_UpdateSingleAnchor_DedupesReplayedBars pins the
// per-anchor lastReplayedBarTime guard. Replay paths can route the same
// prior-day window through UpdateSingleAnchor more than once when a
// caller doesn't honor the freshAnchors return from ResetAnchors. The
// dedup ensures a re-feed of the same (or earlier) bar time is a no-op,
// preventing CumPV / CumV / barCount inflation on a preserved-state
// anchor.
func TestAnchoredVWAPCalc_UpdateSingleAnchor_DedupesReplayedBars(t *testing.T) {
	rthOpen := rthOpenET(t, 2026, 1, 5)
	c := strategy.NewAnchoredVWAPCalc()
	c.AddAnchor(strategy.AnchorPoint{
		Name:       "pd_high",
		AnchorTime: rthOpen,
		RTHOnly:    true,
	})

	bar1 := rthOpen.Add(1 * time.Minute)
	bar2 := rthOpen.Add(2 * time.Minute)

	c.UpdateSingleAnchor("pd_high", bar1, 102, 98, 100, 1000)
	c.UpdateSingleAnchor("pd_high", bar2, 105, 101, 103, 2000)

	beforeSnap := c.Snapshot(5)["pd_high"]
	beforeState := c.States()["pd_high"]
	require.Equal(t, 2, beforeSnap.BarCount, "two bars accepted on first pass")

	// Re-feed the same two bars: must be no-op.
	c.UpdateSingleAnchor("pd_high", bar1, 102, 98, 100, 1000)
	c.UpdateSingleAnchor("pd_high", bar2, 105, 101, 103, 2000)

	afterSnap := c.Snapshot(5)["pd_high"]
	afterState := c.States()["pd_high"]
	assert.Equal(t, beforeSnap.BarCount, afterSnap.BarCount,
		"barCount must not grow on re-feed of same bars")
	assert.InDelta(t, beforeState.CumPV, afterState.CumPV, 1e-9,
		"CumPV must not grow on re-feed")
	assert.InDelta(t, beforeState.CumV, afterState.CumV, 1e-9,
		"CumV must not grow on re-feed")
	assert.InDelta(t, beforeSnap.VWAP, afterSnap.VWAP, 1e-9,
		"VWAP must be identical")

	// Out-of-order earlier bar must also be skipped.
	earlier := rthOpen.Add(-1 * time.Minute)
	c.UpdateSingleAnchor("pd_high", earlier, 200, 100, 150, 9999)
	skipSnap := c.Snapshot(5)["pd_high"]
	assert.Equal(t, beforeSnap.BarCount, skipSnap.BarCount,
		"out-of-order earlier bar must not increment barCount")

	// A new, strictly-later bar must still accept.
	bar3 := rthOpen.Add(3 * time.Minute)
	c.UpdateSingleAnchor("pd_high", bar3, 108, 102, 105, 3000)
	finalSnap := c.Snapshot(5)["pd_high"]
	assert.Equal(t, beforeSnap.BarCount+1, finalSnap.BarCount,
		"a strictly-later bar must increment barCount")
}
