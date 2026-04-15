// TimescaleDB-backed implementation of ports.GapDetector.
// Coalescing logic is exposed as a pure function for unit testing.
package timescaledb

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// GapDetector finds missing bars by diffing ExpectedBarTimestamps against
// timestamps persisted in market_bars.
type GapDetector struct {
	repo *Repository
}

// NewGapDetector wires the detector to an existing Repository, sharing its
// underlying *sql.DB / pgx pool — no new connections are opened.
func NewGapDetector(repo *Repository) *GapDetector {
	return &GapDetector{repo: repo}
}

const queryGapDetectorTimes = `SELECT time FROM market_bars WHERE symbol = $1 AND timeframe = $2 AND time >= $3 AND time < $4 ORDER BY time`

// FindMissingBars returns the ranges of expected timestamps not present in
// market_bars. Returns (nil, nil) when no bars are expected in the window.
func (g *GapDetector) FindMissingBars(ctx context.Context, sym domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]ports.GapRange, error) {
	expected := domain.ExpectedBarTimestamps(sym, tf, from, to)
	if len(expected) == 0 {
		return nil, nil
	}

	rows, err := g.repo.db.QueryContext(ctx, queryGapDetectorTimes, string(sym), string(tf), from, to)
	if err != nil {
		return nil, fmt.Errorf("timescaledb: gap detector query: %w", err)
	}
	defer rows.Close()

	actual := make([]time.Time, 0, len(expected))
	for rows.Next() {
		var t time.Time
		if scanErr := rows.Scan(&t); scanErr != nil {
			return nil, fmt.Errorf("timescaledb: gap detector scan: %w", scanErr)
		}
		actual = append(actual, t)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("timescaledb: gap detector iterate: %w", rowsErr)
	}

	return coalesceGaps(sym, tf, expected, actual), nil
}

// coalesceGaps diffs sorted expected vs sorted actual timestamps via two-pointer
// walk, then merges consecutive missing entries (gap == tf step) into ranges.
// The returned ranges use [Start, End) where End is the last-missing timestamp
// plus one step.
func coalesceGaps(sym domain.Symbol, tf domain.Timeframe, expected, actual []time.Time) []ports.GapRange {
	if len(expected) == 0 {
		return nil
	}
	step, ok := stepDurationFor(tf)

	missing := diffSortedTimes(expected, actual)
	if len(missing) == 0 {
		return nil
	}
	expCount := len(expected)
	actCount := len(actual)

	out := make([]ports.GapRange, 0, len(missing))
	rangeStart := missing[0]
	rangeLast := missing[0]
	for i := 1; i < len(missing); i++ {
		t := missing[i]
		if ok && t.Sub(rangeLast) == step {
			rangeLast = t
			continue
		}
		out = append(out, ports.GapRange{
			Symbol:        sym,
			Timeframe:     tf,
			Start:         rangeStart,
			End:           closeRange(rangeLast, tf, step, ok),
			ExpectedCount: expCount,
			ActualCount:   actCount,
		})
		rangeStart = t
		rangeLast = t
	}
	out = append(out, ports.GapRange{
		Symbol:        sym,
		Timeframe:     tf,
		Start:         rangeStart,
		End:           closeRange(rangeLast, tf, step, ok),
		ExpectedCount: expCount,
		ActualCount:   actCount,
	})
	return out
}

func diffSortedTimes(expected, actual []time.Time) []time.Time {
	out := make([]time.Time, 0, len(expected))
	i, j := 0, 0
	for i < len(expected) && j < len(actual) {
		switch {
		case expected[i].Equal(actual[j]):
			i++
			j++
		case expected[i].Before(actual[j]):
			out = append(out, expected[i])
			i++
		default:
			j++
		}
	}
	for ; i < len(expected); i++ {
		out = append(out, expected[i])
	}
	return out
}

func stepDurationFor(tf domain.Timeframe) (time.Duration, bool) {
	switch tf {
	case "1m":
		return time.Minute, true
	case "5m":
		return 5 * time.Minute, true
	case "15m":
		return 15 * time.Minute, true
	case "1h":
		return time.Hour, true
	}
	return 0, false
}

// closeRange returns the half-open End for a range whose final missing
// timestamp is last. For intraday tf, End is last+step. For 1d, the next
// trading day is not knowable here without a calendar; fall back to last+1ns
// so the range remains a half-open singleton encompassing the last missing day.
func closeRange(last time.Time, _ domain.Timeframe, step time.Duration, hasStep bool) time.Time {
	if hasStep {
		return last.Add(step)
	}
	return last.Add(time.Nanosecond)
}
