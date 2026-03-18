package strategy

import (
	"math"
	"time"
)

type AnchorPoint struct {
	Name       string
	AnchorTime time.Time
	Price      float64
}

type AnchoredVWAPState struct {
	CumPV float64
	CumV  float64
	M2    float64 `json:"m2"` // Welford's online variance accumulator (volume-weighted)
}

func (s AnchoredVWAPState) Value() float64 {
	if s.CumV == 0 {
		return 0
	}
	return s.CumPV / s.CumV
}

// Variance returns the volume-weighted population variance of typical prices.
// Returns 0 if insufficient data (CumV == 0) or M2 not yet populated (backward compat).
func (s AnchoredVWAPState) Variance() float64 {
	if s.CumV == 0 || s.M2 == 0 {
		return 0
	}
	return s.M2 / s.CumV
}

// SD returns the volume-weighted standard deviation of typical prices around VWAP.
// Returns 0 if insufficient data or M2 not yet populated.
func (s AnchoredVWAPState) SD() float64 {
	v := s.Variance()
	if v <= 0 {
		return 0
	}
	return math.Sqrt(v)
}

type AnchoredVWAPCalc struct {
	anchors map[string]*anchoredVWAPEntry
}

const minBarsForSD = 10

type anchoredVWAPEntry struct {
	AnchorPoint
	state    AnchoredVWAPState
	active   bool
	barCount int
}

func NewAnchoredVWAPCalc() *AnchoredVWAPCalc {
	return &AnchoredVWAPCalc{anchors: make(map[string]*anchoredVWAPEntry)}
}

func (c *AnchoredVWAPCalc) AddAnchor(ap AnchorPoint) {
	if c.anchors == nil {
		c.anchors = make(map[string]*anchoredVWAPEntry)
	}
	c.anchors[ap.Name] = &anchoredVWAPEntry{AnchorPoint: ap}
}

func (c *AnchoredVWAPCalc) AnchorPoints() map[string]AnchorPoint {
	out := make(map[string]AnchorPoint)
	if c == nil {
		return out
	}
	for name, e := range c.anchors {
		if e == nil {
			continue
		}
		out[name] = e.AnchorPoint
	}
	return out
}

func (c *AnchoredVWAPCalc) RemoveAnchor(name string) bool {
	if c == nil || c.anchors == nil {
		return false
	}
	_, existed := c.anchors[name]
	delete(c.anchors, name)
	return existed
}

func (c *AnchoredVWAPCalc) Update(barTime time.Time, high, low, close_, volume float64) {
	if c == nil || len(c.anchors) == 0 {
		return
	}
	if volume <= 0 {
		for _, e := range c.anchors {
			if !e.active && !barTime.Before(e.AnchorTime) {
				e.active = true
			}
		}
		return
	}

	tp := (high + low + close_) / 3.0
	pv := tp * volume

	for _, e := range c.anchors {
		if !e.active {
			if barTime.Before(e.AnchorTime) {
				continue
			}
			e.active = true
		}
		oldVWAP := e.state.Value()
		e.state.CumPV += pv
		e.state.CumV += volume
		newVWAP := e.state.Value()
		e.state.M2 += volume * (tp - oldVWAP) * (tp - newVWAP)
		e.barCount++
	}
}

// SDBands returns VWAP ± (level × SD) for the named anchor.
// Returns (upper, lower, true) if the anchor exists and has valid SD data.
// Returns (0, 0, false) if anchor not found, not active, or M2 not populated.
func (c *AnchoredVWAPCalc) SDBands(name string, level float64) (upper, lower float64, ok bool) {
	if c == nil {
		return 0, 0, false
	}
	e, exists := c.anchors[name]
	if !exists || e == nil || !e.active || e.barCount < minBarsForSD {
		return 0, 0, false
	}
	sd := e.state.SD()
	if sd == 0 {
		return 0, 0, false
	}
	vwap := e.state.Value()
	offset := level * sd
	return vwap + offset, vwap - offset, true
}

// AllSDBands returns SD bands at the given level for all active anchors.
// Keys are anchor names, values are {upper, lower} pairs.
func (c *AnchoredVWAPCalc) AllSDBands(level float64) map[string][2]float64 {
	out := make(map[string][2]float64)
	if c == nil {
		return out
	}
	for name, e := range c.anchors {
		if !e.active || e.barCount < minBarsForSD {
			continue
		}
		sd := e.state.SD()
		if sd == 0 {
			continue
		}
		vwap := e.state.Value()
		offset := level * sd
		out[name] = [2]float64{vwap + offset, vwap - offset}
	}
	return out
}

func (c *AnchoredVWAPCalc) Values() map[string]float64 {
	out := make(map[string]float64)
	if c == nil {
		return out
	}
	for name, e := range c.anchors {
		if !e.active {
			continue
		}
		out[name] = e.state.Value()
	}
	return out
}

func (c *AnchoredVWAPCalc) Value(name string) (float64, bool) {
	if c == nil {
		return 0, false
	}
	e, ok := c.anchors[name]
	if !ok || e == nil || !e.active {
		return 0, false
	}
	return e.state.Value(), true
}

func (c *AnchoredVWAPCalc) States() map[string]AnchoredVWAPState {
	out := make(map[string]AnchoredVWAPState)
	if c == nil {
		return out
	}
	for name, e := range c.anchors {
		if e == nil {
			continue
		}
		out[name] = e.state
	}
	return out
}

func (c *AnchoredVWAPCalc) Restore(points []AnchorPoint, states map[string]AnchoredVWAPState) {
	if c.anchors == nil {
		c.anchors = make(map[string]*anchoredVWAPEntry)
	}

	for _, ap := range points {
		e := &anchoredVWAPEntry{AnchorPoint: ap}
		if st, ok := states[ap.Name]; ok {
			e.state = st
			e.active = true
		}
		c.anchors[ap.Name] = e
	}

	for name, st := range states {
		if _, ok := c.anchors[name]; ok {
			continue
		}
		e := &anchoredVWAPEntry{AnchorPoint: AnchorPoint{Name: name}, state: st, active: true}
		c.anchors[name] = e
	}
}
