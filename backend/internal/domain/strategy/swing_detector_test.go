package strategy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func swingBar(t0 time.Time, offsetMin int, high, low float64) Bar {
	return Bar{
		Time:   t0.Add(time.Duration(offsetMin) * time.Minute),
		Open:   (high + low) / 2,
		High:   high,
		Low:    low,
		Close:  (high + low) / 2,
		Volume: 1000,
	}
}

func TestSwingDetector_BasicSwingHigh(t *testing.T) {
	det := NewSwingDetector(2, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// bars: 10, 12, 15, 12, 10 — peak at index 2
	bars := []Bar{
		swingBar(t0, 0, 10, 9),
		swingBar(t0, 5, 12, 11),
		swingBar(t0, 10, 15, 14), // swing high
		swingBar(t0, 15, 12, 11),
		swingBar(t0, 20, 10, 9),
	}

	var results []CandidateAnchor
	for _, b := range bars {
		results = append(results, det.Push(b)...)
	}

	require.Len(t, results, 1)
	assert.Equal(t, AnchorSwingHigh, results[0].Type)
	assert.Equal(t, 15.0, results[0].Price)
	assert.Equal(t, bars[2].Time, results[0].Time)
}

func TestSwingDetector_BasicSwingLow(t *testing.T) {
	det := NewSwingDetector(2, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// bars: 10, 8, 5, 8, 10 — trough at index 2
	bars := []Bar{
		swingBar(t0, 0, 11, 10),
		swingBar(t0, 5, 9, 8),
		swingBar(t0, 10, 6, 5), // swing low
		swingBar(t0, 15, 9, 8),
		swingBar(t0, 20, 11, 10),
	}

	var results []CandidateAnchor
	for _, b := range bars {
		results = append(results, det.Push(b)...)
	}

	require.Len(t, results, 1)
	assert.Equal(t, AnchorSwingLow, results[0].Type)
	assert.Equal(t, 5.0, results[0].Price)
}

func TestSwingDetector_NoSwingMonotonic(t *testing.T) {
	det := NewSwingDetector(2, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	var results []CandidateAnchor
	for i := 0; i < 10; i++ {
		price := 100.0 + float64(i)
		results = append(results, det.Push(swingBar(t0, i*5, price+1, price))...)
	}

	assert.Empty(t, results, "monotonically increasing should have no swings")
}

func TestSwingDetector_MultipleSwings(t *testing.T) {
	det := NewSwingDetector(2, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// V shape then inverted V: 10, 12, 15, 12, 10, 12, 15
	highs := []float64{10, 12, 15, 12, 10, 12, 15, 12, 10}
	lows := []float64{9, 11, 14, 11, 9, 11, 14, 11, 9}

	var results []CandidateAnchor
	for i, h := range highs {
		results = append(results, det.Push(swingBar(t0, i*5, h, lows[i]))...)
	}

	highCount := 0
	lowCount := 0
	for _, r := range results {
		if r.Type == AnchorSwingHigh {
			highCount++
		} else if r.Type == AnchorSwingLow {
			lowCount++
		}
	}

	assert.True(t, highCount >= 1, "should detect at least one swing high")
	assert.True(t, lowCount >= 1, "should detect at least one swing low")
}

func TestSwingDetector_EqualHighsNoSwing(t *testing.T) {
	det := NewSwingDetector(2, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// center bar has same high as a neighbor — NOT a swing (must be strictly greater)
	bars := []Bar{
		swingBar(t0, 0, 10, 9),
		swingBar(t0, 5, 15, 11), // same high as center
		swingBar(t0, 10, 15, 14),
		swingBar(t0, 15, 12, 11),
		swingBar(t0, 20, 10, 9),
	}

	var results []CandidateAnchor
	for _, b := range bars {
		results = append(results, det.Push(b)...)
	}

	highResults := filterByType(results, AnchorSwingHigh)
	assert.Empty(t, highResults, "equal highs should not produce a swing high")
}

func TestSwingDetector_EqualLowsNoSwing(t *testing.T) {
	det := NewSwingDetector(2, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	bars := []Bar{
		swingBar(t0, 0, 11, 10),
		swingBar(t0, 5, 9, 5), // same low as center
		swingBar(t0, 10, 6, 5),
		swingBar(t0, 15, 9, 8),
		swingBar(t0, 20, 11, 10),
	}

	var results []CandidateAnchor
	for _, b := range bars {
		results = append(results, det.Push(b)...)
	}

	lowResults := filterByType(results, AnchorSwingLow)
	assert.Empty(t, lowResults, "equal lows should not produce a swing low")
}

func TestSwingDetector_WarmupPeriod(t *testing.T) {
	det := NewSwingDetector(3, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// First 2*N = 6 bars should never produce results
	for i := 0; i < 6; i++ {
		result := det.Push(swingBar(t0, i*5, float64(10+i), float64(9+i)))
		assert.Nil(t, result, "bar %d should return nil during warmup", i)
	}
}

func TestSwingDetector_StrengthEqualsN(t *testing.T) {
	det := NewSwingDetector(5, "1h")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// Build a clear swing high at the center with N=5
	for i := 0; i < 5; i++ {
		det.Push(swingBar(t0, i*60, float64(10+i), float64(9+i)))
	}
	det.Push(swingBar(t0, 5*60, 20, 19)) // center = peak
	for i := 1; i <= 5; i++ {
		results := det.Push(swingBar(t0, (5+i)*60, float64(20-i), float64(19-i)))
		if len(results) > 0 {
			assert.Equal(t, 5.0, results[0].Strength)
			return
		}
	}
	t.Fatal("expected swing high to be detected")
}

func TestSwingDetector_DifferentN(t *testing.T) {
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// With N=1, this sequence has a swing: 10, 15, 10
	// With N=3, it needs 3 bars on each side — won't detect in just 3 bars
	barsSmall := []Bar{
		swingBar(t0, 0, 10, 9),
		swingBar(t0, 5, 15, 14),
		swingBar(t0, 10, 10, 9),
	}

	det1 := NewSwingDetector(1, "5m")
	var results1 []CandidateAnchor
	for _, b := range barsSmall {
		results1 = append(results1, det1.Push(b)...)
	}
	assert.NotEmpty(t, results1, "N=1 should detect swing in 3 bars")

	det3 := NewSwingDetector(3, "5m")
	var results3 []CandidateAnchor
	for _, b := range barsSmall {
		results3 = append(results3, det3.Push(b)...)
	}
	assert.Empty(t, results3, "N=3 should NOT detect swing in only 3 bars")
}

func TestSwingDetector_BothHighAndLowOnSameBar(t *testing.T) {
	det := NewSwingDetector(2, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	// Center bar has highest high AND lowest low (wide range candle)
	bars := []Bar{
		swingBar(t0, 0, 12, 8),
		swingBar(t0, 5, 13, 7),
		{Time: t0.Add(10 * time.Minute), Open: 10, High: 20, Low: 3, Close: 10, Volume: 1000},
		swingBar(t0, 15, 13, 7),
		swingBar(t0, 20, 12, 8),
	}

	var results []CandidateAnchor
	for _, b := range bars {
		results = append(results, det.Push(b)...)
	}

	types := map[CandidateAnchorType]bool{}
	for _, r := range results {
		types[r.Type] = true
	}
	assert.True(t, types[AnchorSwingHigh], "should detect swing high")
	assert.True(t, types[AnchorSwingLow], "should detect swing low")
}

func TestSwingDetector_NonRepainting(t *testing.T) {
	det := NewSwingDetector(2, "5m")
	t0 := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	bars := []Bar{
		swingBar(t0, 0, 10, 9),
		swingBar(t0, 5, 12, 11),
		swingBar(t0, 10, 15, 14), // potential swing
		swingBar(t0, 15, 12, 11),
		swingBar(t0, 20, 10, 9), // confirms swing at index 2
	}

	var confirmed []CandidateAnchor
	for _, b := range bars {
		confirmed = append(confirmed, det.Push(b)...)
	}

	// The swing is confirmed on bar[4] (the 5th bar), but the swing time is bar[2]
	require.Len(t, confirmed, 1)
	assert.Equal(t, bars[2].Time, confirmed[0].Time, "swing time must be the center bar, not the confirming bar")
}

func filterByType(candidates []CandidateAnchor, t CandidateAnchorType) []CandidateAnchor {
	var out []CandidateAnchor
	for _, c := range candidates {
		if c.Type == t {
			out = append(out, c)
		}
	}
	return out
}
