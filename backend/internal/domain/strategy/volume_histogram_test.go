package strategy

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func vhBar(t time.Time, open, high, low, close, volume float64) Bar {
	return Bar{Time: t, Open: open, High: high, Low: low, Close: close, Volume: volume}
}

func TestVolumeHistogram_BinAlignment(t *testing.T) {
	t.Run("anchor and bin width", func(t *testing.T) {
		anchor := 100.0
		binBps := 10.0
		h := NewVolumeHistogram(binBps, anchor)
		expectedWidth := math.Max(0.01, anchor*binBps*1e-4)
		assert.InDelta(t, expectedWidth, h.BinWidth(), 1e-12)
	})

	t.Run("bin width floor at 0.01", func(t *testing.T) {
		anchor := 1.0
		binBps := 1.0
		h := NewVolumeHistogram(binBps, anchor)
		assert.InDelta(t, 0.01, h.BinWidth(), 1e-12)
	})

	t.Run("BinIndex anchor maps to floor index 5000", func(t *testing.T) {
		anchor := 100.0
		binBps := 10.0
		h := NewVolumeHistogram(binBps, anchor)
		idx := h.BinIndex(anchor)
		assert.Equal(t, 5000, idx)
	})

	t.Run("BinCenter round-trip via BinIndex", func(t *testing.T) {
		anchor := 200.0
		binBps := 10.0
		h := NewVolumeHistogram(binBps, anchor)
		want := 5012
		c := h.BinCenter(want)
		got := h.BinIndex(c)
		assert.Equal(t, want, got)
	})
}

func TestVolumeHistogram_UniformAcrossRange(t *testing.T) {
	anchor := 100.0
	binBps := 10.0
	h := NewVolumeHistogram(binBps, anchor)
	bw := h.BinWidth()

	bar := vhBar(time.Now(), 100, 100+5*bw, 100, 100+5*bw, 1000)
	h.Accumulate(bar)

	totalVol := 0.0
	for _, v := range h.Bins() {
		totalVol += v
	}
	assert.InDelta(t, 1000.0, totalVol, 1e-6, "total volume preserved")

	bins := h.Bins()
	require.NotEmpty(t, bins)
	first := 0.0
	for _, v := range bins {
		if first == 0 {
			first = v
			continue
		}
		assert.InDelta(t, first, v, 1e-6, "bins should hold equal volume for uniform-across-range")
	}
}

func TestVolumeHistogram_SingleBinBar(t *testing.T) {
	anchor := 100.0
	binBps := 10.0
	h := NewVolumeHistogram(binBps, anchor)
	bw := h.BinWidth()
	mid := 100.0 + bw*0.4
	bar := vhBar(time.Now(), mid, mid+bw*0.05, mid-bw*0.05, mid, 500)
	h.Accumulate(bar)

	bins := h.Bins()
	require.Len(t, bins, 1, "single-bin bar should populate exactly one bin")
	for _, v := range bins {
		assert.InDelta(t, 500.0, v, 1e-6)
	}
}

func TestVolumeHistogram_HaltedBar(t *testing.T) {
	anchor := 100.0
	binBps := 10.0
	h := NewVolumeHistogram(binBps, anchor)
	bar := vhBar(time.Now(), 100.05, 100.05, 100.05, 100.05, 750)
	assert.NotPanics(t, func() { h.Accumulate(bar) })

	bins := h.Bins()
	require.Len(t, bins, 1, "halted bar should populate exactly one bin")
	for _, v := range bins {
		assert.InDelta(t, 750.0, v, 1e-6)
	}
}

func TestVolumeHistogram_POC(t *testing.T) {
	t.Run("returns price at max-volume bin", func(t *testing.T) {
		anchor := 100.0
		binBps := 10.0
		h := NewVolumeHistogram(binBps, anchor)
		bw := h.BinWidth()

		mid := 100.0 + bw*0.5
		h.Accumulate(vhBar(time.Now(), mid, mid+bw*0.05, mid-bw*0.05, mid, 1000))

		other := 100.0 + bw*4.5
		h.Accumulate(vhBar(time.Now(), other, other+bw*0.05, other-bw*0.05, other, 100))

		poc := h.POC()
		idx := h.BinIndex(mid)
		assert.InDelta(t, h.BinCenter(idx), poc, 1e-6)
	})

	t.Run("empty histogram returns 0", func(t *testing.T) {
		h := NewVolumeHistogram(10, 100)
		assert.Equal(t, 0.0, h.POC())
	})
}

func TestVolumeHistogram_HVNBins(t *testing.T) {
	anchor := 100.0
	binBps := 10.0
	h := NewVolumeHistogram(binBps, anchor)
	bw := h.BinWidth()

	priceA := 100.0 + bw*0.5
	h.Accumulate(vhBar(time.Now(), priceA, priceA+bw*0.05, priceA-bw*0.05, priceA, 1000))

	priceB := 100.0 + bw*4.5
	h.Accumulate(vhBar(time.Now(), priceB, priceB+bw*0.05, priceB-bw*0.05, priceB, 850))

	priceC := 100.0 + bw*8.5
	h.Accumulate(vhBar(time.Now(), priceC, priceC+bw*0.05, priceC-bw*0.05, priceC, 100))

	hvns := h.HVNBins(80.0)
	assert.Len(t, hvns, 2, "two bins are >= 80% of POC")

	for i := 1; i < len(hvns); i++ {
		assert.Less(t, hvns[i-1], hvns[i], "HVN indices should be sorted")
	}
}

func TestVolumeHistogram_HasHVNInRange(t *testing.T) {
	anchor := 100.0
	binBps := 10.0
	h := NewVolumeHistogram(binBps, anchor)
	bw := h.BinWidth()

	// strong volume at price = 100 + 5*bw
	hot := 100.0 + bw*5.5
	h.Accumulate(vhBar(time.Now(), hot, hot+bw*0.05, hot-bw*0.05, hot, 1000))
	cold := 100.0 + bw*15.5
	h.Accumulate(vhBar(time.Now(), cold, cold+bw*0.05, cold-bw*0.05, cold, 50))

	// range that contains the hot bin
	low := 100.0 + bw*5.0
	high := 100.0 + bw*6.0
	assert.True(t, h.HasHVNInRange(low, high, 80.0))

	// range that excludes any HVN
	farLow := 100.0 + bw*20.0
	farHigh := 100.0 + bw*22.0
	assert.False(t, h.HasHVNInRange(farLow, farHigh, 80.0))
}

func TestVolumeHistogram_EmptyHistogram(t *testing.T) {
	h := NewVolumeHistogram(10, 100)
	assert.Equal(t, 0.0, h.POC())
	assert.Empty(t, h.HVNBins(80.0))
	assert.False(t, h.HasHVNInRange(99, 101, 80.0))
}
