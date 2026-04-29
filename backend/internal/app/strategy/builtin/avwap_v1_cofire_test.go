package builtin

import (
	"testing"
	"time"

	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
)

// Internal tests for the co-fire veto. Uses package builtin (not builtin_test)
// so that unexported helpers computeCofireVeto / updateCofireVetoState /
// applyCofireVeto are directly accessible without widening the API surface.

func cofireBaseCfg() AVWAPConfig {
	return AVWAPConfig{
		CofireVetoEnabled:             true,
		CofireVetoShadow:              false,
		CofireVetoLongOnly:            true,
		CofireVetoStretchZMin:         2.0,
		CofireVetoStretchZMax:         3.0,
		CofireVetoVolShiftMax:         -0.5,
		CofireVetoSessionSigmaMinBars: 6,
	}
}

// cofireReadyState returns a state where all four veto preconditions are
// satisfied and stretchZ / volShift land inside the firing band. The exact
// numbers are chosen to produce stretchZ ~ +2.5 and volShift ~ -1.0 with the
// default cfg thresholds.
func cofireReadyState() *AVWAPState {
	s := &AVWAPState{}
	s.LastInducementSignal = &strat.InducementSignal{
		Strength:  strat.InducementStrong,
		Score:     5,
		Direction: strat.SideSell,
		Tag:       "inducement_strong_short",
	}
	// Session returns: alternating ±0.004 → stdev ~ 0.00438, sigma * close = 0.438.
	s.CofireSessionReturns = []float64{0.004, -0.004, 0.004, -0.004, 0.004, -0.004}
	// Session VWAP: Num/Den = 99.0. stretchZ = (100-99)/0.438 ~ 2.28.
	s.CofireSessionVWAPNum = 99.0 * 1000
	s.CofireSessionVWAPDen = 1000
	// Bucketed z history: last 3 strongly negative, prior 5 strongly positive
	// → volShift = -1.0 - 1.0 = -2.0, well below the -0.5 threshold.
	s.CofireBucketedZHist = []float64{1.0, 1.0, 1.0, 1.0, 1.0, -1.0, -1.0, -1.0}
	return s
}

func cofireBar() strat.Bar {
	// 2026-04-20 is a Monday. computeCofireVeto doesn't check weekday (only
	// updateCofireVetoState does), but keeping the test date on a weekday
	// prevents the fixture from looking wrong if it's ever reused for the
	// state-update tests.
	return strat.Bar{
		Time:   time.Date(2026, 4, 20, 14, 30, 0, 0, time.UTC),
		Open:   99.9,
		High:   100.1,
		Low:    99.8,
		Close:  100.0,
		Volume: 5000,
	}
}

func TestCofireVeto_AllConditionsFire(t *testing.T) {
	s := cofireReadyState()
	cfg := cofireBaseCfg()
	veto, stretchZ, volShift := s.computeCofireVeto(cofireBar(), cfg)
	assert.True(t, veto, "veto must fire when all conditions satisfied")
	assert.InDelta(t, 2.28, stretchZ, 0.05, "stretchZ should land near 2.28")
	assert.Less(t, volShift, cfg.CofireVetoVolShiftMax, "volShift should be below threshold")
}

func TestCofireVeto_NoInducement(t *testing.T) {
	s := cofireReadyState()
	s.LastInducementSignal = nil
	veto, _, _ := s.computeCofireVeto(cofireBar(), cofireBaseCfg())
	assert.False(t, veto, "veto must not fire without inducement")
}

func TestCofireVeto_SessionSigmaNotReady(t *testing.T) {
	s := cofireReadyState()
	s.CofireSessionReturns = []float64{0.004, -0.004, 0.004} // len=3 < min 6
	veto, _, _ := s.computeCofireVeto(cofireBar(), cofireBaseCfg())
	assert.False(t, veto, "veto must not fire before session sigma has enough samples")
}

func TestCofireVeto_BucketedZHistTooShort(t *testing.T) {
	s := cofireReadyState()
	s.CofireBucketedZHist = []float64{-1.0, -1.0, -1.0} // len=3 < 8
	veto, _, _ := s.computeCofireVeto(cofireBar(), cofireBaseCfg())
	assert.False(t, veto, "veto must not fire before 8 bucketed z-scores are collected")
}

func TestCofireVeto_StretchZBelowBand(t *testing.T) {
	s := cofireReadyState()
	// Move VWAP very close to bar close → tiny stretchZ.
	s.CofireSessionVWAPNum = 99.95 * 1000
	veto, stretchZ, _ := s.computeCofireVeto(cofireBar(), cofireBaseCfg())
	assert.False(t, veto, "veto must not fire when |stretchZ| < min")
	assert.Less(t, stretchZ, 2.0)
}

func TestCofireVeto_StretchZAboveBand(t *testing.T) {
	s := cofireReadyState()
	// Move VWAP far away → large stretchZ.
	s.CofireSessionVWAPNum = 95.0 * 1000 // stretchZ ~ (100-95)/0.438 ~ 11.4
	veto, stretchZ, _ := s.computeCofireVeto(cofireBar(), cofireBaseCfg())
	assert.False(t, veto, "veto must not fire when |stretchZ| > max")
	assert.Greater(t, stretchZ, 3.0)
}

func TestCofireVeto_VolShiftAboveThreshold(t *testing.T) {
	s := cofireReadyState()
	// Flat history → volShift = 0, not < -0.5.
	s.CofireBucketedZHist = []float64{0, 0, 0, 0, 0, 0, 0, 0}
	veto, _, volShift := s.computeCofireVeto(cofireBar(), cofireBaseCfg())
	assert.False(t, veto, "veto must not fire when volShift >= threshold")
	assert.GreaterOrEqual(t, volShift, -0.5)
}

// updateCofireVetoState tests: session boundary reset and RTH gating.

func TestUpdateCofireVetoState_ResetsOnNewSession(t *testing.T) {
	if etLocation == nil {
		t.Skip("etLocation not initialized")
	}
	s := &AVWAPState{}
	// Seed day-1 close during RTH. 2026-04-20 is a Monday.
	day1 := time.Date(2026, 4, 20, 14, 30, 0, 0, etLocation) // Monday 14:30 ET
	bar1 := strat.Bar{Time: day1, Open: 100, High: 100, Low: 100, Close: 100, Volume: 1000}
	s.updateCofireVetoState(bar1)
	// Day-2 bar during RTH should reset session state.
	day2 := time.Date(2026, 4, 21, 10, 0, 0, 0, etLocation) // Tuesday 10:00 ET
	bar2 := strat.Bar{Time: day2, Open: 101, High: 101, Low: 101, Close: 101, Volume: 1000}
	s.updateCofireVetoState(bar2)
	assert.Equal(t, day2.Format("2006-01-02"), s.CofireSessionDate,
		"session date must advance on new ET day")
	// Session returns should not span the boundary.
	assert.LessOrEqual(t, len(s.CofireSessionReturns), 1,
		"session returns must not carry over across days")
}

func TestUpdateCofireVetoState_SkipsZeroVolumeBar(t *testing.T) {
	if etLocation == nil {
		t.Skip("etLocation not initialized")
	}
	s := &AVWAPState{}
	// Pre-populate a bucket with a real sample so we can verify a zero-volume
	// bar doesn't displace it.
	s.CofireTODBuckets = map[string][]float64{"14:30": {1000, 1100, 1050}}
	bar := strat.Bar{
		Time:   time.Date(2026, 4, 20, 14, 30, 0, 0, etLocation), // Monday RTH
		Open:   100, High: 100.5, Low: 99.8, Close: 100.0,
		Volume: 0, // halt / illiquid print
	}
	s.updateCofireVetoState(bar)
	assert.Equal(t, []float64{1000, 1100, 1050}, s.CofireTODBuckets["14:30"],
		"zero-volume bar must not append to TOD bucket")
	assert.Zero(t, s.CofireSessionVWAPDen,
		"zero-volume bar must not accumulate VWAP denominator")
}

func TestUpdateCofireVetoState_SkipsExtendedHours(t *testing.T) {
	if etLocation == nil {
		t.Skip("etLocation not initialized")
	}
	s := &AVWAPState{}
	// 08:00 ET — pre-market. Monday 2026-04-20 to avoid weekend short-circuit.
	pre := time.Date(2026, 4, 20, 8, 0, 0, 0, etLocation)
	bar := strat.Bar{Time: pre, Open: 100, High: 100, Low: 100, Close: 100, Volume: 1000}
	s.updateCofireVetoState(bar)
	assert.Empty(t, s.CofireSessionDate, "extended-hours bars must not set session state")
	assert.Zero(t, s.CofireSessionVWAPDen, "extended-hours bars must not accumulate VWAP")
}
