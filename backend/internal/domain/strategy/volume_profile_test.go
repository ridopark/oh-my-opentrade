package strategy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeBar(t0 time.Time, offsetMinutes int, open, high, low, close_, volume float64) Bar {
	return Bar{
		Time:   t0.Add(time.Duration(offsetMinutes) * time.Minute),
		Open:   open,
		High:   high,
		Low:    low,
		Close:  close_,
		Volume: volume,
	}
}

func TestVolumeProfiler_RotationAndBreakoutUp(t *testing.T) {
	vp := NewVolumeProfiler(0.25, 5, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// Normal-volume bars to establish baseline (avg vol ~1000)
	for i := 0; i < 10; i++ {
		price := 100.0 + float64(i)*0.5
		vp.Push(makeBar(t0, i*5, price, price+0.3, price-0.3, price+0.1, 1000))
	}

	// 5 bars of tight range, HIGH volume (rotation — 3× baseline)
	for i := 0; i < 5; i++ {
		vp.Push(makeBar(t0, (10+i)*5, 100.0, 100.3, 99.8, 100.1, 3000))
	}

	// Breakout bar — price jumps above value area with volume spike
	result := vp.Push(makeBar(t0, 80, 100.5, 102.0, 100.5, 101.5, 8000))

	require.NotNil(t, result)
	assert.Equal(t, AnchorVolumeRotation, result.Type)
	assert.Equal(t, "5m", result.Timeframe)
	require.NotNil(t, result.VolumeContext)
	assert.Equal(t, 8000.0, result.VolumeContext.BreakoutVolume)
	assert.True(t, result.VolumeContext.RotationBars >= 5)
}

func TestVolumeProfiler_RotationAndBreakoutDown(t *testing.T) {
	vp := NewVolumeProfiler(0.25, 5, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	for i := 0; i < 10; i++ {
		price := 100.0 + float64(i)*0.5
		vp.Push(makeBar(t0, i*5, price, price+0.3, price-0.3, price+0.1, 1000))
	}

	for i := 0; i < 5; i++ {
		vp.Push(makeBar(t0, (10+i)*5, 100.0, 100.3, 99.8, 100.1, 3000))
	}

	result := vp.Push(makeBar(t0, 80, 99.5, 99.8, 97.5, 98.0, 8000))

	require.NotNil(t, result)
	assert.Equal(t, AnchorVolumeRotation, result.Type)
	assert.True(t, result.Price > 97.0 && result.Price < 101.0, "price should be VA midpoint")
}

func TestVolumeProfiler_NoRotationWideRange(t *testing.T) {
	vp := NewVolumeProfiler(0.25, 5, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// Wide price range — not a rotation
	for i := 0; i < 10; i++ {
		price := 100.0 + float64(i)*2.0
		vp.Push(makeBar(t0, i*5, price, price+1.0, price-1.0, price+0.5, 1000))
	}

	result := vp.Push(makeBar(t0, 55, 122.0, 125.0, 122.0, 124.0, 5000))
	assert.Nil(t, result)
}

func TestVolumeProfiler_NoBreakout(t *testing.T) {
	vp := NewVolumeProfiler(0.25, 5, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// Tight rotation
	for i := 0; i < 5; i++ {
		vp.Push(makeBar(t0, i*5, 100.0, 100.3, 99.8, 100.1, 5000))
	}

	// Next bar stays in range with normal volume — no breakout
	result := vp.Push(makeBar(t0, 30, 100.0, 100.2, 99.9, 100.1, 1000))
	assert.Nil(t, result)
}

func TestVolumeProfiler_SlidingWindow(t *testing.T) {
	vp := NewVolumeProfiler(0.25, 5, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// Push more bars than window — old bars should drop off
	for i := 0; i < 20; i++ {
		price := 100.0 + float64(i%3)*0.1
		vp.Push(makeBar(t0, i*5, price, price+0.2, price-0.2, price+0.1, 1000))
	}

	// Histogram should only reflect last windowBars bars
	var histVol float64
	for _, v := range vp.histogram {
		histVol += v
	}
	assert.InDelta(t, 5*1000.0, histVol, 1.0, "histogram should only contain window's volume")
}

func TestVolumeProfiler_DifferentBucketPct(t *testing.T) {
	// Tighter bucket = more sensitive to rotation detection
	vpTight := NewVolumeProfiler(0.10, 5, "5m")
	vpWide := NewVolumeProfiler(1.00, 5, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	bars := []Bar{
		makeBar(t0, 0, 100.0, 100.2, 99.9, 100.1, 5000),
		makeBar(t0, 5, 100.1, 100.3, 99.8, 100.0, 5000),
		makeBar(t0, 10, 100.0, 100.2, 99.9, 100.1, 5000),
		makeBar(t0, 15, 99.9, 100.1, 99.8, 100.0, 5000),
		makeBar(t0, 20, 100.0, 100.2, 99.9, 100.1, 5000),
	}
	for _, b := range bars {
		vpTight.Push(b)
		vpWide.Push(b)
	}

	// With wide buckets, rotation is more easily detected (everything falls in fewer buckets)
	breakout := makeBar(t0, 25, 100.5, 103.0, 100.5, 102.5, 20000)
	rTight := vpTight.Push(breakout)
	rWide := vpWide.Push(breakout)

	// Both should detect or not — but the bucket sizing affects it
	// The point is they behave differently
	_ = rTight
	_ = rWide
	// Just verify no panic and both return without error
}

func TestVolumeProfiler_Reset(t *testing.T) {
	vp := NewVolumeProfiler(0.25, 5, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		vp.Push(makeBar(t0, i*5, 100.0, 100.3, 99.8, 100.1, 5000))
	}

	assert.True(t, vp.barCount > 0)
	vp.Reset()
	assert.Equal(t, int64(0), vp.barCount)
	assert.Equal(t, 0, vp.count)
	assert.Empty(t, vp.histogram)
}

func TestVolumeProfiler_WarmupPeriod(t *testing.T) {
	vp := NewVolumeProfiler(0.25, 10, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// Push fewer bars than window — should never trigger
	for i := 0; i < 8; i++ {
		result := vp.Push(makeBar(t0, i*5, 100.0, 100.3, 99.8, 100.1, 5000))
		assert.Nil(t, result, "should not trigger during warmup, bar %d", i)
	}
}

// TestVolumeProfiler_PushDedupsReplay covers the SRP fix that closes the
// M3-shape latent bug. Replaying a bar must not double-count totalVolume,
// double-increment barCount, shift the rolling window twice, double-add
// to histogram buckets, or advance rotation-detection state. Mirrors the
// dedup precedent in IndicatorCalculator (PR #35), AnchoredVWAP (PR #25),
// and BarAggregator.
func TestVolumeProfiler_PushDedupsReplay(t *testing.T) {
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// Single-pass: feed 8 bars (less than the 10-bar window so we
	// observe pre-rotation state, where every accumulator is still
	// growing).
	single := NewVolumeProfiler(0.25, 10, "5m")
	for i := 0; i < 8; i++ {
		single.Push(makeBar(t0, i*5, 100.0, 100.3, 99.8, 100.1, 1000))
	}

	// Bridge + replay: feed first 4 bars (the bridge), then feed all 8
	// bars (the runtime replay). Without dedup, bars 0-3 contribute
	// twice to totalVolume / barCount / histogram.
	bridged := NewVolumeProfiler(0.25, 10, "5m")
	for i := 0; i < 4; i++ {
		bridged.Push(makeBar(t0, i*5, 100.0, 100.3, 99.8, 100.1, 1000))
	}
	for i := 0; i < 8; i++ {
		bridged.Push(makeBar(t0, i*5, 100.0, 100.3, 99.8, 100.1, 1000))
	}

	// Snapshots must match — if dedup is missing, the bridged path's
	// totalVolume = 12000 (4 doubled + 4 single = 4000 + 4000 + 4000)
	// vs single's 8000.
	assert.Equal(t, single.totalVolume, bridged.totalVolume, "totalVolume must match single-pass")
	assert.Equal(t, single.barCount, bridged.barCount, "barCount must match single-pass")
	assert.Equal(t, single.count, bridged.count, "rolling-window count must match")
	assert.Equal(t, single.head, bridged.head, "rolling-window head must match")
	require.Equal(t, len(single.histogram), len(bridged.histogram), "histogram bucket count must match")
	for k, v := range single.histogram {
		assert.InDelta(t, v, bridged.histogram[k], 1e-9, "histogram bucket %d must match", k)
	}
}

// TestVolumeProfiler_ResetClearsDedup asserts Reset clears the dedup
// watermark so a freshly-reset profiler accepts its first bar regardless
// of how its time compares to the prior session's last bar.
func TestVolumeProfiler_ResetClearsDedup(t *testing.T) {
	vp := NewVolumeProfiler(0.25, 5, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// Push a bar at t0 + 30m so lastBarTime advances.
	vp.Push(makeBar(t0, 30, 100.0, 100.3, 99.8, 100.1, 1000))
	require.Equal(t, int64(1), vp.barCount)

	// Reset and push a bar EARLIER than the prior lastBarTime.
	vp.Reset()
	vp.Push(makeBar(t0, 0, 100.0, 100.3, 99.8, 100.1, 1000))

	// barCount must reflect the post-reset push — proves dedup was cleared.
	require.Equal(t, int64(1), vp.barCount, "post-Reset push must be accepted regardless of time vs prior lastBarTime")
}
