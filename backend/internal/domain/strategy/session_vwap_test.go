package strategy

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mkBar(ts time.Time, o, h, l, c, v float64) Bar {
	return Bar{Time: ts, Open: o, High: h, Low: l, Close: c, Volume: v}
}

func TestSessionVWAP_NotReadyBeforeFirstBar(t *testing.T) {
	sv := NewSessionVWAP(DefaultSessionVWAPConfig())
	_, _, _, ok := sv.Update(mkBar(time.Date(2026, 4, 15, 0, 5, 0, 0, time.UTC), 100, 100, 100, 100, 0))
	assert.False(t, ok, "zero-volume first bar should not establish VWAP")
}

func TestSessionVWAP_BasicVWAPComputation(t *testing.T) {
	sv := NewSessionVWAP(SessionVWAPConfig{SigmaMethod: SigmaMethodRolling, SigmaLookbackBars: 4})
	base := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	// Two bars: (100, 10) and (110, 20). VWAP = (100*10 + 110*20)/30 = 106.666...
	sv.Update(mkBar(base, 100, 100, 100, 100, 10))
	vwap, _, _, _ := sv.Update(mkBar(base.Add(5*time.Minute), 110, 110, 110, 110, 20))
	assert.InDelta(t, 106.6667, vwap, 0.01)
}

func TestSessionVWAP_ResetsAtUTCMidnight(t *testing.T) {
	sv := NewSessionVWAP(SessionVWAPConfig{SigmaMethod: SigmaMethodRolling, SigmaLookbackBars: 4})
	d1 := time.Date(2026, 4, 15, 23, 55, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)

	sv.Update(mkBar(d1, 100, 100, 100, 100, 10))
	sv.Update(mkBar(d1.Add(2*time.Minute), 200, 200, 200, 200, 10)) // would pull vwap to 150

	vwap, _, _, _ := sv.Update(mkBar(d2, 50, 50, 50, 50, 10))
	assert.InDelta(t, 50.0, vwap, 1e-9, "session rollover should discard prior day's cumulative")
}

func TestSessionVWAP_DevZSignCorrectness(t *testing.T) {
	sv := NewSessionVWAP(SessionVWAPConfig{SigmaMethod: SigmaMethodRolling, SigmaLookbackBars: 10})
	base := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	// Drift up then shock down.
	prices := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 90}
	var lastDevZ float64
	var ok bool
	for i, p := range prices {
		_, _, lastDevZ, ok = sv.Update(mkBar(base.Add(time.Duration(i)*5*time.Minute), p, p, p, p, 10))
	}
	require.True(t, ok)
	assert.Less(t, lastDevZ, 0.0, "shock-down close below VWAP should yield negative z")
}

func TestSessionVWAP_AllSigmaMethodsProduceSensibleValues(t *testing.T) {
	base := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	prices := []float64{100, 101, 99, 102, 98, 103, 97, 104, 96, 105, 95, 90}

	for _, method := range []string{SigmaMethodSession, SigmaMethodRolling, SigmaMethodEWMA} {
		t.Run(method, func(t *testing.T) {
			sv := NewSessionVWAP(SessionVWAPConfig{SigmaMethod: method, SigmaLookbackBars: 6})
			var sigma float64
			var ok bool
			for i, p := range prices {
				_, sigma, _, ok = sv.Update(mkBar(base.Add(time.Duration(i)*5*time.Minute), p, p, p, p, 10))
			}
			require.True(t, ok, "method %s should be ready after %d bars", method, len(prices))
			assert.Greater(t, sigma, 0.0)
			assert.False(t, math.IsNaN(sigma))
			assert.False(t, math.IsInf(sigma, 0))
		})
	}
}

func TestSessionVWAP_RollingSigmaNeedsTwoSamples(t *testing.T) {
	sv := NewSessionVWAP(SessionVWAPConfig{SigmaMethod: SigmaMethodRolling, SigmaLookbackBars: 5})
	base := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	_, _, _, ok := sv.Update(mkBar(base, 100, 100, 100, 100, 10))
	assert.False(t, ok, "single bar should not yield sigma")
	_, _, _, ok = sv.Update(mkBar(base.Add(5*time.Minute), 101, 101, 101, 101, 10))
	assert.True(t, ok)
}

func TestSessionVWAP_CustomResetHour(t *testing.T) {
	sv := NewSessionVWAP(SessionVWAPConfig{SessionResetUTCHour: 22, SigmaMethod: SigmaMethodRolling, SigmaLookbackBars: 4})

	// Session anchored at 22:00 UTC. Bars at 21:55 and 22:05 should be in different sessions.
	b1 := time.Date(2026, 4, 15, 21, 55, 0, 0, time.UTC)
	b2 := time.Date(2026, 4, 15, 22, 5, 0, 0, time.UTC)

	sv.Update(mkBar(b1, 100, 100, 100, 100, 10))
	sv.Update(mkBar(b1.Add(2*time.Minute), 200, 200, 200, 200, 10)) // still session 1
	vwap, _, _, _ := sv.Update(mkBar(b2, 50, 50, 50, 50, 10))
	assert.InDelta(t, 50.0, vwap, 1e-9, "22:00 UTC rollover should discard prior cumulative")
}
