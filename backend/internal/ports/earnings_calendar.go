package ports

import (
	"context"
	"time"
)

// EarningsEntry represents a single symbol's next earnings date.
type EarningsEntry struct {
	Symbol       string
	EarningsDate time.Time
	Hour         string // "bmo", "amc", "dmh"
	Quarter      int
	Year         int
}

// EarningsCalendarPort provides access to upcoming earnings dates.
type EarningsCalendarPort interface {
	// GetNextEarnings returns the next earnings date for a symbol.
	// Returns nil if no upcoming earnings are known.
	GetNextEarnings(ctx context.Context, symbol string) (*EarningsEntry, error)

	// UpsertEarnings stores or updates an earnings entry.
	UpsertEarnings(ctx context.Context, entry EarningsEntry) error

	// UpsertBatch stores or updates multiple earnings entries.
	UpsertBatch(ctx context.Context, entries []EarningsEntry) error
}
