package strategy

import (
	"math"
	"sort"
	"time"
)

type AnchorPoint struct {
	Name       string
	AnchorTime time.Time
	Price      float64
	RTHOnly    bool // when true, Update skips bars outside 9:30-16:00 ET
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

// etLoc is America/New_York, used for RTH filtering (9:30-16:00 ET).
var etLoc *time.Location

func init() {
	var err error
	etLoc, err = time.LoadLocation("America/New_York")
	if err != nil {
		// Fallback: UTC-5 (EST). This should never happen in practice.
		etLoc = time.FixedZone("EST", -5*3600)
	}
}

// isRTH returns true if barTime falls within Regular Trading Hours (09:30-16:00 ET).
func isRTH(barTime time.Time) bool {
	et := barTime.In(etLoc)
	hhmm := et.Hour()*60 + et.Minute()
	return hhmm >= 9*60+30 && hhmm < 16*60
}

type AnchoredVWAPCalc struct {
	anchors     map[string]*anchoredVWAPEntry
	lastBarTime time.Time // prevents double-counting when both 1m and 5m bars call Update
}

const minBarsForSD = 10

type anchoredVWAPEntry struct {
	AnchorPoint
	state       AnchoredVWAPState
	active      bool
	barCount    int
	recentVWAPs [20]float64
	vwapIdx     int
	vwapCount   int
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

// SortedNames returns the names of all active anchors in sorted order
// for deterministic iteration. Go map iteration is random — without
// this, backtest results vary between runs.
func (c *AnchoredVWAPCalc) SortedNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.anchors))
	for name, e := range c.anchors {
		if e != nil && e.active {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
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
	// Skip bars at or before the last processed time to prevent double-counting
	// when both 1m UpdateCalc and 5m OnEvent call Update for overlapping data.
	if !c.lastBarTime.IsZero() && !barTime.After(c.lastBarTime) {
		return
	}
	c.lastBarTime = barTime
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

	rthOK := isRTH(barTime) // precompute once per bar

	for _, e := range c.anchors {
		if !e.active {
			if barTime.Before(e.AnchorTime) {
				continue
			}
			e.active = true
		}
		// Skip pre-market/after-hours bars for RTH-only anchors (pd_high, pd_low).
		if e.RTHOnly && !rthOK {
			continue
		}
		oldVWAP := e.state.Value()
		e.state.CumPV += pv
		e.state.CumV += volume
		newVWAP := e.state.Value()
		e.state.M2 += volume * (tp - oldVWAP) * (tp - newVWAP)
		e.barCount++

		// Push new VWAP into ring buffer for slope calculation
		e.recentVWAPs[e.vwapIdx] = newVWAP
		e.vwapIdx = (e.vwapIdx + 1) % 20
		if e.vwapCount < 20 {
			e.vwapCount++
		}
	}
}

// UpdateSingleAnchor feeds a bar into ONE specific anchor, bypassing the
// lastBarTime dedup guard. Used to replay previous-day bars into individual
// anchors (pd_high, pd_low) without affecting other anchors.
func (c *AnchoredVWAPCalc) UpdateSingleAnchor(name string, barTime time.Time, high, low, close_, volume float64) {
	if c == nil {
		return
	}
	e, ok := c.anchors[name]
	if !ok || e == nil {
		return
	}
	if !e.active {
		if barTime.Before(e.AnchorTime) {
			return
		}
		e.active = true
	}
	if volume <= 0 {
		return
	}
	tp := (high + low + close_) / 3.0
	pv := tp * volume
	oldVWAP := e.state.Value()
	e.state.CumPV += pv
	e.state.CumV += volume
	newVWAP := e.state.Value()
	e.state.M2 += volume * (tp - oldVWAP) * (tp - newVWAP)
	e.barCount++
	e.recentVWAPs[e.vwapIdx] = newVWAP
	e.vwapIdx = (e.vwapIdx + 1) % 20
	if e.vwapCount < 20 {
		e.vwapCount++
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

// Slope returns the AVWAP slope for the named anchor as bps per bar.
// Positive = uptrend, negative = downtrend. Returns (0, false) if
// insufficient data (< lookback bars).
func (c *AnchoredVWAPCalc) Slope(name string, lookback int) (float64, bool) {
	if c == nil {
		return 0, false
	}
	e, ok := c.anchors[name]
	if !ok || !e.active || e.vwapCount < lookback || lookback < 2 {
		return 0, false
	}

	// Simple linear regression slope over the last N values
	// Access ring buffer in chronological order
	start := (e.vwapIdx - lookback + 20) % 20

	var sumX, sumY, sumXY, sumX2 float64
	for i := 0; i < lookback; i++ {
		idx := (start + i) % 20
		x := float64(i)
		y := e.recentVWAPs[idx]
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	n := float64(lookback)
	denom := n*sumX2 - sumX*sumX
	if denom == 0 {
		return 0, false
	}
	slope := (n*sumXY - sumX*sumY) / denom

	// Normalize to bps per bar
	avgPrice := sumY / n
	if avgPrice <= 0 {
		return 0, false
	}
	slopeBPS := (slope / avgPrice) * 10000.0
	return slopeBPS, true
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

// AnchorSnapshot is one anchor's evaluation-time view: the running VWAP,
// the slope (bps per bar over the most recent ring-buffer fill), and the
// counters that drive readiness checks. Used by EntryGated diagnostics
// (Phase 2 of the parity plan) so a SQL diff can pinpoint per-anchor
// disagreements between live and backtest at the same bar.
type AnchorSnapshot struct {
	VWAP      float64
	SlopeBPS  float64
	BarCount  int
	VWAPCount int
	Active    bool
}

// Snapshot returns one AnchorSnapshot per registered anchor. The slope
// uses a fixed 5-bar lookback (matches the default cfg.SlopeLookback in
// shipped strategies); when fewer than 5 VWAP samples have accumulated
// or the regression is degenerate, SlopeBPS is 0.
func (c *AnchoredVWAPCalc) Snapshot() map[string]AnchorSnapshot {
	out := make(map[string]AnchorSnapshot)
	if c == nil {
		return out
	}
	const slopeLookback = 5
	for name, e := range c.anchors {
		if e == nil {
			continue
		}
		snap := AnchorSnapshot{
			VWAP:      e.state.Value(),
			BarCount:  e.barCount,
			VWAPCount: e.vwapCount,
			Active:    e.active,
		}
		if slope, ok := c.Slope(name, slopeLookback); ok {
			snap.SlopeBPS = slope
		}
		out[name] = snap
	}
	return out
}

// LastBarTime returns the most recent bar timestamp the calc has applied.
func (c *AnchoredVWAPCalc) LastBarTime() time.Time {
	if c == nil {
		return time.Time{}
	}
	return c.lastBarTime
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
