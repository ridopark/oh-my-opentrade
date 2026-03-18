package strategy

import (
	"math"
	"sort"
	"time"
)

// VolumeProfiler detects volume rotation zones — tight price ranges with
// concentrated volume — and emits a CandidateAnchor when a breakout from
// such a zone is confirmed. The anchor is placed at the last bar of the
// rotation (the point of control).
type VolumeProfiler struct {
	bucketPct  float64
	windowBars int
	timeframe  string
	bucketSize float64

	bars        []Bar
	head        int
	count       int
	histogram   map[int]float64
	totalVolume float64
	barCount    int64

	rotationDetected bool
	rotationVALow    float64
	rotationVAHigh   float64
	rotationAvgVol   float64
	rotationBars     int
	rotationLastBar  Bar
}

// NewVolumeProfiler creates a VolumeProfiler.
// bucketPct is the price bucket width as a percentage (e.g., 0.25 = 0.25%).
// windowBars is the sliding window size for rotation analysis.
func NewVolumeProfiler(bucketPct float64, windowBars int, timeframe string) *VolumeProfiler {
	if bucketPct <= 0 {
		bucketPct = 0.25
	}
	if windowBars < 5 {
		windowBars = 5
	}
	return &VolumeProfiler{
		bucketPct:  bucketPct,
		windowBars: windowBars,
		timeframe:  timeframe,
		bars:       make([]Bar, windowBars),
		histogram:  make(map[int]float64),
	}
}

func (p *VolumeProfiler) bucketIndex(price float64) int {
	if p.bucketSize <= 0 {
		return 0
	}
	return int(math.Floor(price / p.bucketSize))
}

func (p *VolumeProfiler) typicalPrice(bar Bar) float64 {
	return (bar.High + bar.Low + bar.Close) / 3.0
}

func (p *VolumeProfiler) addBar(bar Bar) {
	tp := p.typicalPrice(bar)
	idx := p.bucketIndex(tp)
	p.histogram[idx] += bar.Volume
}

func (p *VolumeProfiler) removeBar(bar Bar) {
	tp := p.typicalPrice(bar)
	idx := p.bucketIndex(tp)
	p.histogram[idx] -= bar.Volume
	if p.histogram[idx] <= 0 {
		delete(p.histogram, idx)
	}
}

type bucketEntry struct {
	index  int
	volume float64
}

func (p *VolumeProfiler) valueArea() (low, high float64, width float64) {
	if len(p.histogram) == 0 {
		return 0, 0, 0
	}

	entries := make([]bucketEntry, 0, len(p.histogram))
	var total float64
	for idx, vol := range p.histogram {
		entries = append(entries, bucketEntry{idx, vol})
		total += vol
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].volume > entries[j].volume
	})

	target := total * 0.70
	var accum float64
	minIdx, maxIdx := entries[0].index, entries[0].index
	for _, e := range entries {
		if accum >= target {
			break
		}
		accum += e.volume
		if e.index < minIdx {
			minIdx = e.index
		}
		if e.index > maxIdx {
			maxIdx = e.index
		}
	}

	low = float64(minIdx) * p.bucketSize
	high = float64(maxIdx+1) * p.bucketSize
	width = high - low
	return low, high, width
}

func (p *VolumeProfiler) windowVolume() float64 {
	var total float64
	for _, v := range p.histogram {
		total += v
	}
	return total
}

// Push feeds a new bar into the profiler. Returns a CandidateAnchor if a
// volume rotation breakout is confirmed, nil otherwise.
func (p *VolumeProfiler) Push(bar Bar) *CandidateAnchor {
	if p.bucketSize == 0 && bar.Close > 0 {
		p.bucketSize = bar.Close * p.bucketPct / 100.0
	}
	if p.bucketSize <= 0 {
		return nil
	}

	p.totalVolume += bar.Volume
	p.barCount++
	avgVolAllTime := p.totalVolume / float64(p.barCount)

	if p.count >= p.windowBars {
		oldIdx := p.head
		p.removeBar(p.bars[oldIdx])
		p.bars[oldIdx] = bar
		p.addBar(bar)
		p.head = (p.head + 1) % p.windowBars
	} else {
		slot := (p.head + p.count) % p.windowBars
		p.bars[slot] = bar
		p.addBar(bar)
		p.count++
	}

	if p.count < p.windowBars {
		return nil
	}

	if p.rotationDetected {
		outsideVA := bar.Close > p.rotationVAHigh || bar.Close < p.rotationVALow
		volumeSpike := bar.Volume > 2.0*p.rotationAvgVol

		if outsideVA && volumeSpike {
			midPrice := (p.rotationVALow + p.rotationVAHigh) / 2.0
			strength := bar.Volume / avgVolAllTime

			ca, err := NewCandidateAnchor(
				p.rotationLastBar.Time,
				midPrice,
				AnchorVolumeRotation,
				p.timeframe,
				strength,
			)
			if err != nil {
				p.rotationDetected = false
				return nil
			}

			result := ca.WithVolumeContext(VolumeRotationContext{
				RotationBars:   p.rotationBars,
				AvgVolume:      p.rotationAvgVol,
				BreakoutVolume: bar.Volume,
				PriceRange:     [2]float64{p.rotationVALow, p.rotationVAHigh},
			})

			p.rotationDetected = false
			return &result
		}

		if !outsideVA {
			p.rotationLastBar = bar
			p.rotationBars++
			return nil
		}

		p.rotationDetected = false
	}

	vaLow, vaHigh, vaWidth := p.valueArea()
	tightRange := vaWidth < 3.0*p.bucketSize
	windowVol := p.windowVolume()
	highVolume := windowVol > 1.5*float64(p.windowBars)*avgVolAllTime

	if tightRange && highVolume && avgVolAllTime > 0 {
		p.rotationDetected = true
		p.rotationVALow = vaLow
		p.rotationVAHigh = vaHigh
		p.rotationAvgVol = windowVol / float64(p.windowBars)
		p.rotationBars = p.windowBars
		p.rotationLastBar = bar
	}

	return nil
}

// Reset clears all internal state.
func (p *VolumeProfiler) Reset() {
	p.bars = make([]Bar, p.windowBars)
	p.head = 0
	p.count = 0
	p.histogram = make(map[int]float64)
	p.totalVolume = 0
	p.barCount = 0
	p.bucketSize = 0
	p.rotationDetected = false
}

// lastBarTime returns the time of the most recently pushed bar, for testing.
func (p *VolumeProfiler) lastBarTime() time.Time {
	if p.count == 0 {
		return time.Time{}
	}
	idx := (p.head + p.count - 1) % p.windowBars
	return p.bars[idx].Time
}
