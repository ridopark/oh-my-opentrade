package strategy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiTimeframeScorer_SingleTimeframe(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	ca5m, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 5.0)
	ca1h, _ := NewCandidateAnchor(ts.Add(2*time.Hour), 105.0, AnchorSwingHigh, "1h", 5.0)

	scored := scorer.Score([]CandidateAnchor{ca5m, ca1h})
	require.Len(t, scored, 2)

	var s5m, s1h CandidateAnchor
	for _, s := range scored {
		if s.Timeframe == "5m" {
			s5m = s
		} else {
			s1h = s
		}
	}

	assert.Equal(t, 5.0*1.0, s5m.Strength, "5m weight = 1.0")
	assert.Equal(t, 5.0*2.0, s1h.Strength, "1h weight = 2.0")
}

func TestMultiTimeframeScorer_Confluence(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	ca5m, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 5.0)
	ca1h, _ := NewCandidateAnchor(ts.Add(10*time.Minute), 100.5, AnchorSwingHigh, "1h", 5.0)

	scored := scorer.Score([]CandidateAnchor{ca5m, ca1h})
	require.Len(t, scored, 2)

	var s5m, s1h CandidateAnchor
	for _, s := range scored {
		if s.Timeframe == "5m" {
			s5m = s
		} else {
			s1h = s
		}
	}

	assert.Equal(t, 5.0*1.0+2.0, s5m.Strength, "5m gets +2 confluence bonus")
	assert.Equal(t, 5.0*2.0+2.0, s1h.Strength, "1h gets +2 confluence bonus")
}

func TestMultiTimeframeScorer_NoConfluenceFarApart(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	ca5m, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 5.0)
	ca1h, _ := NewCandidateAnchor(ts.Add(24*time.Hour), 100.0, AnchorSwingHigh, "1h", 5.0)

	scored := scorer.Score([]CandidateAnchor{ca5m, ca1h})

	var s5m CandidateAnchor
	for _, s := range scored {
		if s.Timeframe == "5m" {
			s5m = s
		}
	}

	assert.Equal(t, 5.0*1.0, s5m.Strength, "no confluence when far apart in time")
}

func TestMultiTimeframeScorer_NoConfluenceDifferentTypes(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	caHigh, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 5.0)
	caLow, _ := NewCandidateAnchor(ts.Add(time.Minute), 100.0, AnchorSwingLow, "1h", 5.0)

	scored := scorer.Score([]CandidateAnchor{caHigh, caLow})

	var sHigh CandidateAnchor
	for _, s := range scored {
		if s.Type == AnchorSwingHigh {
			sHigh = s
		}
	}

	assert.Equal(t, 5.0*1.0, sHigh.Strength, "no confluence for different types")
}

func TestMultiTimeframeScorer_NoConfluenceDifferentPriceLevels(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	ca5m, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 5.0)
	ca1h, _ := NewCandidateAnchor(ts.Add(time.Minute), 110.0, AnchorSwingHigh, "1h", 5.0)

	scored := scorer.Score([]CandidateAnchor{ca5m, ca1h})

	var s5m CandidateAnchor
	for _, s := range scored {
		if s.Timeframe == "5m" {
			s5m = s
		}
	}

	assert.Equal(t, 5.0*1.0, s5m.Strength, "no confluence when prices >2% apart")
}

func TestMultiTimeframeScorer_DailyWeightsHighest(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	ca5m, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 3.0)
	ca1d, _ := NewCandidateAnchor(ts.Add(48*time.Hour), 200.0, AnchorSwingHigh, "1d", 5.0)

	scored := scorer.Score([]CandidateAnchor{ca5m, ca1d})
	require.Len(t, scored, 2)

	// 1d: 5*3.0=15, 5m: 3*1.0=3 — daily ranks first due to weight
	assert.Equal(t, "1d", scored[0].Timeframe)
	assert.Equal(t, 15.0, scored[0].Strength)
	assert.Equal(t, 3.0, scored[1].Strength)
}

func TestMultiTimeframeScorer_SortedByStrength(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	ca1, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 2.0)
	ca2, _ := NewCandidateAnchor(ts.Add(time.Hour), 105.0, AnchorSwingLow, "1h", 3.0)
	ca3, _ := NewCandidateAnchor(ts.Add(2*time.Hour), 110.0, AnchorSwingHigh, "1d", 1.0)

	scored := scorer.Score([]CandidateAnchor{ca1, ca2, ca3})
	require.Len(t, scored, 3)

	assert.True(t, scored[0].Strength >= scored[1].Strength, "sorted descending")
	assert.True(t, scored[1].Strength >= scored[2].Strength, "sorted descending")
}

func TestMultiTimeframeScorer_EmptyInput(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	assert.Nil(t, scorer.Score(nil))
	assert.Nil(t, scorer.Score([]CandidateAnchor{}))
}

func TestMultiTimeframeScorer_DoesNotMutateOriginal(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	ca, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 5.0)
	origStrength := ca.Strength
	input := []CandidateAnchor{ca}

	scorer.Score(input)

	assert.Equal(t, origStrength, input[0].Strength, "original must not be mutated")
}

func TestMultiTimeframeScorer_TripleConfluence(t *testing.T) {
	scorer := NewMultiTimeframeScorer()
	ts := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)

	ca5m, _ := NewCandidateAnchor(ts, 100.0, AnchorSwingHigh, "5m", 5.0)
	ca1h, _ := NewCandidateAnchor(ts.Add(5*time.Minute), 100.2, AnchorSwingHigh, "1h", 5.0)
	ca1d, _ := NewCandidateAnchor(ts.Add(10*time.Minute), 100.1, AnchorSwingHigh, "1d", 5.0)

	scored := scorer.Score([]CandidateAnchor{ca5m, ca1h, ca1d})
	require.Len(t, scored, 3)

	var s5m, s1h, s1d CandidateAnchor
	for _, s := range scored {
		switch s.Timeframe {
		case "5m":
			s5m = s
		case "1h":
			s1h = s
		case "1d":
			s1d = s
		}
	}

	// 5m: base 5*1.0 + confluence with 1h (+2) + confluence with 1d (+2) = 9
	assert.Equal(t, 5.0*1.0+2.0+2.0, s5m.Strength)
	// 1h: base 5*2.0 + confluence with 5m (+2) + confluence with 1d (+2) = 14
	assert.Equal(t, 5.0*2.0+2.0+2.0, s1h.Strength)
	// 1d: base 5*3.0 + confluence with 5m (+2) + confluence with 1h (+2) = 19
	assert.Equal(t, 5.0*3.0+2.0+2.0, s1d.Strength)
}
