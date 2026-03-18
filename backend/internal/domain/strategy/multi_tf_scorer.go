package strategy

import (
	"math"
	"sort"
	"time"
)

var timeframeWeight = map[string]float64{
	"1m":  0.5,
	"5m":  1.0,
	"15m": 1.5,
	"1h":  2.0,
	"4h":  2.5,
	"1d":  3.0,
}

func tfWeight(tf string) float64 {
	if w, ok := timeframeWeight[tf]; ok {
		return w
	}
	return 1.0
}

// tfWindowDuration returns the approximate time span covered by N bars at the
// given timeframe. Used to determine whether two candidates from different
// timeframes represent the same structural point.
func tfWindowDuration(tf string, n int) time.Duration {
	var barDur time.Duration
	switch tf {
	case "1m":
		barDur = time.Minute
	case "5m":
		barDur = 5 * time.Minute
	case "15m":
		barDur = 15 * time.Minute
	case "1h":
		barDur = time.Hour
	case "4h":
		barDur = 4 * time.Hour
	case "1d":
		barDur = 24 * time.Hour
	default:
		barDur = 5 * time.Minute
	}
	return barDur * time.Duration(n)
}

const confluenceWindowBars = 5

// MultiTimeframeScorer enriches CandidateAnchor strength based on
// cross-timeframe confluence. If two candidates from different timeframes
// are close enough in time to represent the same structural level, both
// receive a strength bonus.
type MultiTimeframeScorer struct{}

func NewMultiTimeframeScorer() *MultiTimeframeScorer {
	return &MultiTimeframeScorer{}
}

// Score returns a copy of candidates with Strength adjusted for:
// 1. Timeframe weight (higher TF = more significant)
// 2. Confluence bonus (same level visible on multiple timeframes)
//
// The original slice is not modified.
func (s *MultiTimeframeScorer) Score(candidates []CandidateAnchor) []CandidateAnchor {
	if len(candidates) == 0 {
		return nil
	}

	scored := make([]CandidateAnchor, len(candidates))
	copy(scored, candidates)

	for i := range scored {
		scored[i].Strength = scored[i].Strength * tfWeight(scored[i].Timeframe)
	}

	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[i].Timeframe == scored[j].Timeframe {
				continue
			}
			if scored[i].Type != scored[j].Type {
				continue
			}

			higherTF := scored[i].Timeframe
			if tfWeight(scored[j].Timeframe) > tfWeight(scored[i].Timeframe) {
				higherTF = scored[j].Timeframe
			}
			tolerance := tfWindowDuration(higherTF, confluenceWindowBars)

			timeDiff := scored[i].Time.Sub(scored[j].Time)
			if timeDiff < 0 {
				timeDiff = -timeDiff
			}

			if timeDiff <= tolerance {
				priceDiff := math.Abs(scored[i].Price-scored[j].Price) / math.Max(scored[i].Price, scored[j].Price)
				if priceDiff < 0.02 {
					scored[i].Strength += 2.0
					scored[j].Strength += 2.0
				}
			}
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Strength > scored[j].Strength
	})

	return scored
}
