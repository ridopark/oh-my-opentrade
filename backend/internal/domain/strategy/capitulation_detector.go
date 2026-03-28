package strategy

// CapitulationDetector detects panic sell-offs and blow-off tops on daily bars.
// Conditions:
//   - Daily range > 2.0 * ATR14
//   - Day volume > 2.5 * 20-day volume SMA
//   - Day volume is highest in 60+ trading days ("some of the largest volume ever")
//   - Capitulation low: close recovered into upper 70% of range AND made new 20-day low
//   - Blow-off top: close dropped into lower 70% of range AND made new 20-day high
//
// Strength = (range/ATR) * (volume_ratio) * 0.5
type CapitulationDetector struct {
	timeframe string
	bars      []Bar
	maxBars   int // 65 bars for ATR14 + vol SMA20 + 20-day extremes + 60-day volume rank
}

func NewCapitulationDetector(timeframe string) *CapitulationDetector {
	return &CapitulationDetector{
		timeframe: timeframe,
		maxBars:   65,
	}
}

func (d *CapitulationDetector) Push(bar Bar) *CandidateAnchor {
	d.bars = append(d.bars, bar)
	if len(d.bars) > d.maxBars {
		d.bars = d.bars[len(d.bars)-d.maxBars:]
	}
	if len(d.bars) < 21 {
		return nil
	}

	curr := d.bars[len(d.bars)-1]
	dailyRange := curr.High - curr.Low
	if dailyRange <= 0 {
		return nil
	}

	// ATR14
	atr := d.computeATR(14)
	if atr <= 0 || dailyRange < 2.0*atr {
		return nil
	}

	// Volume check
	volSMA := d.computeVolSMA(20)
	if volSMA <= 0 {
		return nil
	}
	volRatio := curr.Volume / volSMA
	if volRatio < 2.5 {
		return nil
	}

	// Volume must be the highest in at least 60 trading days (~3 months).
	// Research: "some of the largest volume EVER" — not just 2.5× recent average.
	// With fewer bars available, require highest in whatever history we have (min 20).
	if !d.isHighestVolume(curr.Volume, 60) {
		return nil
	}

	// 20-day high/low (excluding current bar)
	low20, high20 := d.computeExtremes(20)

	closePosition := (curr.Close - curr.Low) / dailyRange // 0=closed at low, 1=closed at high

	strength := (dailyRange / atr) * volRatio * 0.5

	// Capitulation low: closed in upper 70% + new 20-day low
	if closePosition > 0.3 && curr.Low < low20 {
		anchor, err := NewCandidateAnchor(
			curr.Time,
			curr.Low, // anchor at the panic low
			AnchorCapitulation,
			d.timeframe,
			strength,
		)
		if err != nil {
			return nil
		}
		return &anchor
	}

	// Blow-off top: closed in lower 70% + new 20-day high
	if closePosition < 0.7 && curr.High > high20 {
		anchor, err := NewCandidateAnchor(
			curr.Time,
			curr.High, // anchor at the blow-off high
			AnchorCapitulation,
			d.timeframe,
			strength,
		)
		if err != nil {
			return nil
		}
		return &anchor
	}

	return nil
}

func (d *CapitulationDetector) computeATR(period int) float64 {
	n := len(d.bars)
	if n < period+1 {
		return 0
	}
	var sum float64
	for i := n - period; i < n; i++ {
		tr := d.bars[i].High - d.bars[i].Low
		if i > 0 {
			prev := d.bars[i-1].Close
			hc := d.bars[i].High - prev
			if hc < 0 {
				hc = -hc
			}
			cl := d.bars[i].Low - prev
			if cl < 0 {
				cl = -cl
			}
			if hc > tr {
				tr = hc
			}
			if cl > tr {
				tr = cl
			}
		}
		sum += tr
	}
	return sum / float64(period)
}

func (d *CapitulationDetector) computeVolSMA(period int) float64 {
	n := len(d.bars)
	if n < period+1 {
		return 0
	}
	var sum float64
	for i := n - period - 1; i < n-1; i++ {
		sum += d.bars[i].Volume
	}
	return sum / float64(period)
}

func (d *CapitulationDetector) computeExtremes(period int) (low, high float64) {
	n := len(d.bars)
	if n < period+1 {
		return 0, 0
	}
	low = d.bars[n-period-1].Low
	high = d.bars[n-period-1].High
	for i := n - period - 1; i < n-1; i++ { // exclude current bar
		if d.bars[i].Low < low {
			low = d.bars[i].Low
		}
		if d.bars[i].High > high {
			high = d.bars[i].High
		}
	}
	return low, high
}

// isHighestVolume returns true if vol is the highest volume in the last `period`
// bars (excluding current). If fewer bars are available, checks all history (min 20).
func (d *CapitulationDetector) isHighestVolume(vol float64, period int) bool {
	n := len(d.bars)
	if n < 21 { // not enough data
		return false
	}
	lookback := period
	if n-1 < lookback {
		lookback = n - 1 // use all available history
	}
	for i := n - lookback - 1; i < n-1; i++ { // exclude current bar
		if i < 0 {
			continue
		}
		if d.bars[i].Volume >= vol {
			return false // found a day with equal or higher volume
		}
	}
	return true
}

func (d *CapitulationDetector) WarmupBars() int {
	return d.maxBars
}
