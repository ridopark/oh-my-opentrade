package strategy

import (
	"math"
	"sort"
)

// VolumeHistogram accumulates volume into price bins anchored to a fixed
// reference price. Bin width is derived from anchor and a basis-points knob,
// floored at 0.01 dollars to avoid degenerate widths on very low-priced
// instruments. Bin index 5000 always corresponds to the anchor itself; this
// gives ample range on either side while keeping integer keys small.
//
// Volume is distributed uniformly across the price range [Low, High] of each
// accumulated bar so wide-range bars do not artificially weight a single bin.
//
// NOT thread-safe; the caller serializes access.
type VolumeHistogram struct {
	binWidth    float64
	anchor      float64
	anchorFloor float64
	bins        map[int]float64
}

// NewVolumeHistogram constructs a histogram bound to the given anchor price.
// binBps controls bin width as basis points of the anchor; the dollar width
// is max(0.01, anchor * binBps * 1e-4).
func NewVolumeHistogram(binBps, anchor float64) *VolumeHistogram {
	bw := math.Max(0.01, anchor*binBps*1e-4)
	return &VolumeHistogram{
		binWidth:    bw,
		anchor:      anchor,
		anchorFloor: anchor - 5000*bw,
		bins:        make(map[int]float64),
	}
}

// BinWidth returns the dollar width of one bin.
func (h *VolumeHistogram) BinWidth() float64 { return h.binWidth }

// BinIndex returns the integer bin index for a given price.
func (h *VolumeHistogram) BinIndex(price float64) int {
	return int(math.Floor((price - h.anchorFloor) / h.binWidth))
}

// BinCenter returns the centroid price of the given bin index.
func (h *VolumeHistogram) BinCenter(idx int) float64 {
	return h.anchorFloor + (float64(idx)+0.5)*h.binWidth
}

// Bins returns the underlying bin map. Caller must not mutate.
func (h *VolumeHistogram) Bins() map[int]float64 { return h.bins }

// Accumulate adds bar.Volume to every bin touched by [bar.Low, bar.High],
// proportional to the overlap of each bin with the bar range. A bar with
// High == Low (halted) deposits the full volume into the single bin
// containing that price.
func (h *VolumeHistogram) Accumulate(bar Bar) {
	if bar.Volume <= 0 {
		return
	}
	if bar.High <= bar.Low {
		idx := h.BinIndex(bar.Low)
		h.bins[idx] += bar.Volume
		return
	}
	loIdx := h.BinIndex(bar.Low)
	hiIdx := h.BinIndex(bar.High)
	span := bar.High - bar.Low
	for idx := loIdx; idx <= hiIdx; idx++ {
		binLow := h.anchorFloor + float64(idx)*h.binWidth
		binHigh := binLow + h.binWidth
		ovLow := math.Max(binLow, bar.Low)
		ovHigh := math.Min(binHigh, bar.High)
		overlap := ovHigh - ovLow
		if overlap <= 0 {
			continue
		}
		h.bins[idx] += bar.Volume * (overlap / span)
	}
}

// POC returns the centroid price of the bin holding the most volume. Returns
// 0 if the histogram is empty.
func (h *VolumeHistogram) POC() float64 {
	bestIdx, bestVol, found := 0, 0.0, false
	for idx, v := range h.bins {
		if !found || v > bestVol {
			bestIdx, bestVol, found = idx, v, true
		}
	}
	if !found {
		return 0
	}
	return h.BinCenter(bestIdx)
}

// HVNBins returns the sorted bin indices whose volume is at least
// thresholdPct percent of the POC bin's volume.
func (h *VolumeHistogram) HVNBins(thresholdPct float64) []int {
	pocVol := 0.0
	for _, v := range h.bins {
		if v > pocVol {
			pocVol = v
		}
	}
	if pocVol <= 0 {
		return nil
	}
	cutoff := pocVol * thresholdPct / 100.0
	out := make([]int, 0)
	for idx, v := range h.bins {
		if v >= cutoff {
			out = append(out, idx)
		}
	}
	sort.Ints(out)
	return out
}

// HasHVNInRange returns true if any HVN bin index falls inside the price
// range [priceLow, priceHigh] (inclusive on both ends). thresholdPct sets
// the HVN cutoff relative to POC volume.
func (h *VolumeHistogram) HasHVNInRange(priceLow, priceHigh, thresholdPct float64) bool {
	if priceHigh < priceLow {
		priceLow, priceHigh = priceHigh, priceLow
	}
	hvns := h.HVNBins(thresholdPct)
	if len(hvns) == 0 {
		return false
	}
	loIdx := h.BinIndex(priceLow)
	hiIdx := h.BinIndex(priceHigh)
	for _, idx := range hvns {
		if idx >= loIdx && idx <= hiIdx {
			return true
		}
	}
	return false
}
