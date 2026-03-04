package backfill

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// ChunkWindow represents a [From, To) time range for a single API request.
type ChunkWindow struct {
	From time.Time
	To   time.Time
}

// chunkDuration returns the maximum calendar duration per API request for a given timeframe.
// Larger timeframes use bigger chunks to minimize the number of API calls.
func chunkDuration(tf domain.Timeframe) time.Duration {
	switch tf {
	case "1m":
		return 10 * 24 * time.Hour // 10 days
	case "5m":
		return 30 * 24 * time.Hour // 30 days
	case "15m":
		return 60 * 24 * time.Hour // 60 days
	case "1h":
		return 180 * 24 * time.Hour // 180 days
	case "1d":
		return 365 * 24 * time.Hour // 1 year
	default:
		return 10 * 24 * time.Hour
	}
}

// SplitTimeRange divides [from, to) into chunk windows appropriate for the given timeframe.
func SplitTimeRange(from, to time.Time, tf domain.Timeframe) []ChunkWindow {
	d := chunkDuration(tf)
	var chunks []ChunkWindow
	cursor := from
	for cursor.Before(to) {
		end := cursor.Add(d)
		if end.After(to) {
			end = to
		}
		chunks = append(chunks, ChunkWindow{From: cursor, To: end})
		cursor = end
	}
	return chunks
}
