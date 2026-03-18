package strategy

// SwingDetector detects swing high/low pivot points using the Williams Fractal
// (N-bar pivot) algorithm on streaming bars. A swing high is confirmed when
// the center bar's High is strictly greater than the High of all N bars on
// each side. Swing lows are symmetric. Detection is lagged by N bars to
// guarantee no repainting.
type SwingDetector struct {
	n         int
	timeframe string
	buf       []Bar
	size      int // capacity = 2*n + 1
	count     int // bars received so far
}

func NewSwingDetector(n int, timeframe string) *SwingDetector {
	if n < 1 {
		n = 1
	}
	size := 2*n + 1
	return &SwingDetector{
		n:         n,
		timeframe: timeframe,
		buf:       make([]Bar, 0, size),
		size:      size,
	}
}

// Push feeds a new bar and returns any newly confirmed swing points.
// Returns at most 2 candidates (one swing high + one swing low) per call.
// Returns nil during the warmup period (first 2*N bars).
func (d *SwingDetector) Push(bar Bar) []CandidateAnchor {
	d.count++

	if len(d.buf) < d.size {
		d.buf = append(d.buf, bar)
		if len(d.buf) < d.size {
			return nil
		}
	} else {
		copy(d.buf, d.buf[1:])
		d.buf[d.size-1] = bar
	}

	center := d.buf[d.n]
	var results []CandidateAnchor

	if d.isSwingHigh(center.High) {
		strength := d.swingStrength(true)
		ca, err := NewCandidateAnchor(center.Time, center.High, AnchorSwingHigh, d.timeframe, strength)
		if err == nil {
			results = append(results, ca)
		}
	}

	if d.isSwingLow(center.Low) {
		strength := d.swingStrength(false)
		ca, err := NewCandidateAnchor(center.Time, center.Low, AnchorSwingLow, d.timeframe, strength)
		if err == nil {
			results = append(results, ca)
		}
	}

	return results
}

func (d *SwingDetector) isSwingHigh(centerHigh float64) bool {
	for i := 0; i < d.size; i++ {
		if i == d.n {
			continue
		}
		if d.buf[i].High >= centerHigh {
			return false
		}
	}
	return true
}

func (d *SwingDetector) isSwingLow(centerLow float64) bool {
	for i := 0; i < d.size; i++ {
		if i == d.n {
			continue
		}
		if d.buf[i].Low <= centerLow {
			return false
		}
	}
	return true
}

// swingStrength returns base N plus the count of additional confirming bars.
// In the basic implementation, strength equals N since we only have the
// exact window. Extended history scanning can be added later.
func (d *SwingDetector) swingStrength(isHigh bool) float64 {
	return float64(d.n)
}

// N returns the lookback parameter.
func (d *SwingDetector) N() int {
	return d.n
}

// WarmupBars returns the minimum number of bars needed before the first
// possible swing detection (2*N).
func (d *SwingDetector) WarmupBars() int {
	return 2 * d.n
}
