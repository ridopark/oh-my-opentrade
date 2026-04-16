package flow

import (
	"sort"
	"time"
)

// LargePrint represents a detected institutional-sized trade.
type LargePrint struct {
	Venue     string
	Symbol    string
	Price     float64
	SizeUSD   float64
	TakerSide string
	Timestamp time.Time
}

// CoordinatedPrint represents large prints appearing on multiple venues
// within a short time window, suggesting institutional accumulation.
type CoordinatedPrint struct {
	Symbol    string
	Direction string // "buy" or "sell"
	Prints    []LargePrint
	TotalUSD  float64
	Timestamp time.Time // earliest print in the cluster
}

// DetectLargePrints scans recent trades for prints above the USD threshold.
// Returns prints from the last `window` duration.
func (a *Aggregator) DetectLargePrints(symbol string, window time.Duration) []LargePrint {
	a.mu.RLock()
	defer a.mu.RUnlock()

	now := a.nowFn()
	cutoff := now.Add(-window)

	venueMap, ok := a.trades[symbol]
	if !ok {
		return nil
	}

	var result []LargePrint
	for _, trades := range venueMap {
		for _, t := range trades {
			if t.Timestamp.Before(cutoff) {
				continue
			}
			usd := t.Price * t.Size
			if usd >= a.cfg.LargePrintUSD {
				result = append(result, LargePrint{
					Venue:     t.Venue,
					Symbol:    t.Symbol,
					Price:     t.Price,
					SizeUSD:   usd,
					TakerSide: t.TakerSide,
					Timestamp: t.Timestamp,
				})
			}
		}
	}

	// Sort by timestamp ascending.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp)
	})
	return result
}

// CoordinatedFlow detects when large prints appear on 2+ venues within
// a short time window, suggesting institutional accumulation.
// The clusterWindow controls how close in time prints must be to be considered
// coordinated. minVenues is the minimum number of distinct venues required.
func (a *Aggregator) CoordinatedFlow(symbol string, clusterWindow time.Duration, minVenues int) []CoordinatedPrint {
	// Get all large prints in the wider 60s window.
	prints := a.DetectLargePrints(symbol, a.cfg.Window60s)
	if len(prints) < minVenues {
		return nil
	}

	var results []CoordinatedPrint

	// Slide through prints and find clusters where multiple venues appear
	// within clusterWindow of each other, grouped by direction.
	for _, dir := range []string{"buy", "sell"} {
		// Filter to this direction.
		var directed []LargePrint
		for _, p := range prints {
			if p.TakerSide == dir {
				directed = append(directed, p)
			}
		}
		if len(directed) < minVenues {
			continue
		}

		// For each print, look ahead within clusterWindow and collect venues.
		used := make(map[int]bool)
		for i := 0; i < len(directed); i++ {
			if used[i] {
				continue
			}
			cluster := []LargePrint{directed[i]}
			venues := map[string]bool{directed[i].Venue: true}

			for j := i + 1; j < len(directed); j++ {
				if used[j] {
					continue
				}
				if directed[j].Timestamp.Sub(directed[i].Timestamp) > clusterWindow {
					break
				}
				// Only add if it brings a new venue.
				if !venues[directed[j].Venue] {
					cluster = append(cluster, directed[j])
					venues[directed[j].Venue] = true
					used[j] = true
				}
			}

			if len(venues) >= minVenues {
				used[i] = true
				var total float64
				for _, p := range cluster {
					total += p.SizeUSD
				}
				results = append(results, CoordinatedPrint{
					Symbol:    symbol,
					Direction: dir,
					Prints:    cluster,
					TotalUSD:  total,
					Timestamp: cluster[0].Timestamp,
				})
			}
		}
	}

	return results
}
