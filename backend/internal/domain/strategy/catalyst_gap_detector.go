package strategy

// CatalystGapDetector detects earnings/news gaps on daily bars.
// A catalyst gap is confirmed when:
//   - Gap up: open > prior day high, OR gap down: open < prior day low
//   - Gap size > 1.5 * ATR14
//   - Day volume > 2.0 * 20-day volume SMA
//
// Anchor is placed at the gap day's open price.
// Strength = gap_atr_mult * volume_ratio.
type CatalystGapDetector struct {
	timeframe string
	bars      []Bar // rolling window of daily bars
	maxBars   int   // 25 bars for ATR14 + volume SMA20
}

func NewCatalystGapDetector(timeframe string) *CatalystGapDetector {
	return &CatalystGapDetector{
		timeframe: timeframe,
		maxBars:   25,
	}
}

func (d *CatalystGapDetector) Push(bar Bar) *CandidateAnchor {
	d.bars = append(d.bars, bar)
	if len(d.bars) > d.maxBars {
		d.bars = d.bars[len(d.bars)-d.maxBars:]
	}
	if len(d.bars) < 21 {
		return nil // need enough history for ATR14 + vol SMA20
	}

	curr := d.bars[len(d.bars)-1]
	prior := d.bars[len(d.bars)-2]

	// Check for gap
	gapUp := curr.Open > prior.High
	gapDown := curr.Open < prior.Low
	if !gapUp && !gapDown {
		return nil
	}

	gapSize := curr.Open - prior.Close
	if gapSize < 0 {
		gapSize = -gapSize
	}

	// Compute ATR14
	atr := d.computeATR(14)
	if atr <= 0 {
		return nil
	}

	gapATRMult := gapSize / atr
	if gapATRMult < 1.5 {
		return nil
	}

	// Compute 20-day volume SMA
	volSMA := d.computeVolSMA(20)
	if volSMA <= 0 {
		return nil
	}

	volRatio := curr.Volume / volSMA
	if volRatio < 2.0 {
		return nil
	}

	strength := gapATRMult * volRatio

	anchor, err := NewCandidateAnchor(
		curr.Time,
		curr.Open,
		AnchorCatalystGap,
		d.timeframe,
		strength,
	)
	if err != nil {
		return nil
	}
	return &anchor
}

func (d *CatalystGapDetector) computeATR(period int) float64 {
	n := len(d.bars)
	if n < period+1 {
		return 0
	}
	var sum float64
	for i := n - period; i < n; i++ {
		tr := d.bars[i].High - d.bars[i].Low
		// True range includes gap from prior close
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

func (d *CatalystGapDetector) computeVolSMA(period int) float64 {
	n := len(d.bars)
	if n < period+1 { // +1 because we exclude current bar
		return 0
	}
	var sum float64
	// Use bars [n-period-1 .. n-2] (exclude current bar)
	for i := n - period - 1; i < n-1; i++ {
		sum += d.bars[i].Volume
	}
	return sum / float64(period)
}

func (d *CatalystGapDetector) WarmupBars() int {
	return d.maxBars
}
