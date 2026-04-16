package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// OpenInterestSnapshot holds a point-in-time open interest reading for a
// perpetual contract on a derivatives venue.
type OpenInterestSnapshot struct {
	Venue     domain.Venue
	Symbol    domain.Symbol
	OI        float64
	OIUsd     float64
	MarkPrice float64
}

// OpenInterestPort provides access to open interest data from derivatives
// venues.
type OpenInterestPort interface {
	// OpenInterest returns the current open interest snapshot for the given
	// venue and symbol.
	OpenInterest(ctx context.Context, venue domain.Venue, symbol domain.Symbol) (OpenInterestSnapshot, error)
}
