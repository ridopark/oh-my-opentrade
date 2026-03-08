package strategy_test

import (
	"math"
	"testing"
	"time"

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

	sd := math.Sqrt(3.25) // ≈ 1.80278
	vwap := 103.5

	upper, lower, ok := c.SDBands("test", 2.0)
	require.True(t, ok)
	assert.InDelta(t, vwap+2.0*sd, upper, 1e-9)
	assert.InDelta(t, vwap-2.0*sd, lower, 1e-9)

	upper1, lower1, ok := c.SDBands("test", 1.0)
	require.True(t, ok)
	assert.InDelta(t, vwap+sd, upper1, 1e-9)
	assert.InDelta(t, vwap-sd, lower1, 1e-9)
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

	bands := c.AllSDBands(2.0)
	require.Len(t, bands, 2)
	assert.Contains(t, bands, "a")
	assert.Contains(t, bands, "b")

	sd := math.Sqrt(3.25)
	vwap := 103.5
	for _, pair := range bands {
		assert.InDelta(t, vwap+2.0*sd, pair[0], 1e-9)
		assert.InDelta(t, vwap-2.0*sd, pair[1], 1e-9)
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
