package strategy

import "time"

type AnchorPoint struct {
	Name       string
	AnchorTime time.Time
	Price      float64
}

type AnchoredVWAPState struct {
	CumPV float64
	CumV  float64
}

func (s AnchoredVWAPState) Value() float64 {
	if s.CumV == 0 {
		return 0
	}
	return s.CumPV / s.CumV
}

type AnchoredVWAPCalc struct {
	anchors map[string]*anchoredVWAPEntry
}

type anchoredVWAPEntry struct {
	AnchorPoint
	state  AnchoredVWAPState
	active bool
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
		e.state.CumPV += pv
		e.state.CumV += volume
	}
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
