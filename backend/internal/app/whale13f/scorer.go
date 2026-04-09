package whale13f

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// filerAction describes what a single filer did with a ticker between quarters.
type filerAction struct {
	CIK       string  `json:"cik"`
	Name      string  `json:"name"`
	Action    string  `json:"action"` // "new", "add_50", "add_25", "hold", "reduce"
	PctChange float64 `json:"pct_change"`
	Tier      int     `json:"tier"`
	Score     int     `json:"score"`
}

// prevKey builds a lookup key for the previous-quarter holdings map.
func prevKey(cik, ticker string) string {
	return cik + "|" + ticker
}

// ComputeAccumulation diffs current quarter holdings against previous quarter
// and produces per-ticker accumulation scores.
//
// Scoring rules per filer:
//
//	New position:       +3 (Tier 1), +1 (Tier 2)
//	Increased >50%:     +2 (Tier 1), +1 (Tier 2)
//	Increased >25%:     +1 (Tier 1), +0 (Tier 2)
//	Decreased:          -1 (any tier)
func ComputeAccumulation(current, previous []domain.WhaleFiling, quarterEnd time.Time) []domain.WhaleAccumulation {
	// Build previous-quarter lookup: cik|ticker -> total share count.
	prevMap := make(map[string]int64, len(previous))
	for _, f := range previous {
		if f.Ticker == "" {
			continue
		}
		key := prevKey(f.FilerCIK, f.Ticker)
		prevMap[key] += f.ShareCount
	}

	// Group current filings by ticker, aggregating share count per cik+ticker.
	type filerHolding struct {
		cik        string
		name       string
		tier       int
		shareCount int64
	}
	tickerFilers := make(map[string]map[string]*filerHolding) // ticker -> cik -> holding

	for _, f := range current {
		if f.Ticker == "" {
			continue
		}
		byFiler, ok := tickerFilers[f.Ticker]
		if !ok {
			byFiler = make(map[string]*filerHolding)
			tickerFilers[f.Ticker] = byFiler
		}
		h, ok := byFiler[f.FilerCIK]
		if !ok {
			h = &filerHolding{cik: f.FilerCIK, name: f.FilerName, tier: f.FilerTier}
			byFiler[f.FilerCIK] = h
		}
		h.shareCount += f.ShareCount
	}

	results := make([]domain.WhaleAccumulation, 0, len(tickerFilers))

	for ticker, filers := range tickerFilers {
		var (
			totalScore    int
			newPositions  int
			additions50   int
			additions25   int
			reductions    int
			actions       []filerAction
		)

		for _, h := range filers {
			key := prevKey(h.cik, ticker)
			prevShares, hadPrev := prevMap[key]

			var act filerAction
			act.CIK = h.cik
			act.Name = h.name
			act.Tier = h.tier

			switch {
			case !hadPrev || prevShares == 0:
				// New position.
				act.Action = "new"
				act.PctChange = 100.0
				if h.tier == 1 {
					act.Score = 3
				} else {
					act.Score = 1
				}
				newPositions++

			case h.shareCount > prevShares:
				pctChange := float64(h.shareCount-prevShares) / float64(prevShares) * 100.0
				act.PctChange = pctChange

				switch {
				case pctChange > 50.0:
					act.Action = "add_50"
					if h.tier == 1 {
						act.Score = 2
					} else {
						act.Score = 1
					}
					additions50++
				case pctChange > 25.0:
					act.Action = "add_25"
					if h.tier == 1 {
						act.Score = 1
					}
					additions25++
				default:
					act.Action = "hold"
				}

			case h.shareCount < prevShares:
				pctChange := float64(h.shareCount-prevShares) / float64(prevShares) * 100.0
				act.Action = "reduce"
				act.PctChange = pctChange
				act.Score = -1
				reductions++

			default:
				// Unchanged position.
				act.Action = "hold"
			}

			totalScore += act.Score
			actions = append(actions, act)
		}

		// Build top filer detail: top 5 by absolute score, then alphabetical.
		sort.Slice(actions, func(i, j int) bool {
			ai, aj := abs(actions[i].Score), abs(actions[j].Score)
			if ai != aj {
				return ai > aj
			}
			return actions[i].Name < actions[j].Name
		})
		topN := 5
		if len(actions) < topN {
			topN = len(actions)
		}
		topFilerJSON, _ := json.Marshal(actions[:topN])

		results = append(results, domain.WhaleAccumulation{
			QuarterEnd:     quarterEnd,
			Ticker:         ticker,
			Score:          totalScore,
			NewPositions:   newPositions,
			Additions50Pct: additions50,
			Additions25Pct: additions25,
			Reductions:     reductions,
			TotalFilers:    len(filers),
			TopFilerDetail: topFilerJSON,
		})
	}

	// Sort output by ticker for deterministic results.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Ticker < results[j].Ticker
	})

	return results
}

// abs returns the absolute value of an integer.
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
