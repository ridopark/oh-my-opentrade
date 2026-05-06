package strategy

import "math"

// EMARolling implements an exponential moving average with the conventional
// alpha = 2/(period+1) smoothing constant and a simple-mean seed across the
// first `period` updates. NaN/Inf updates are skipped so degenerate inputs
// cannot poison the running value.
//
// NOT thread-safe; the caller serializes access.
type EMARolling struct {
	period int
	alpha  float64
	value  float64
	count  int
	seedSum float64
}

// NewEMARolling constructs an EMA configured for the given period.
func NewEMARolling(period int) *EMARolling {
	if period < 1 {
		period = 1
	}
	return &EMARolling{
		period: period,
		alpha:  2.0 / (float64(period) + 1.0),
	}
}

// Update ingests a new price observation and advances the EMA state.
func (e *EMARolling) Update(price float64) {
	if math.IsNaN(price) || math.IsInf(price, 0) {
		return
	}
	if e.count < e.period {
		e.seedSum += price
		e.count++
		if e.count == e.period {
			e.value = e.seedSum / float64(e.period)
		}
		return
	}
	e.value = e.alpha*price + (1-e.alpha)*e.value
}

// Value returns the current EMA. Returns 0 until IsReady() is true.
func (e *EMARolling) Value() float64 {
	if !e.IsReady() {
		return 0
	}
	return e.value
}

// IsReady reports whether the EMA has consumed at least `period` updates.
func (e *EMARolling) IsReady() bool { return e.count >= e.period }
