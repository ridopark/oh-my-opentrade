package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// FundingRate represents a single funding rate observation for a perpetual
// contract on a derivatives venue.
type FundingRate struct {
	Venue         domain.Venue
	Symbol        domain.Symbol
	Timestamp     time.Time
	Rate          float64
	IntervalHours int
	MarkPrice     float64
	NextFundingAt time.Time
}

// FundingRatesPort provides access to perpetual funding rate data from
// derivatives venues. Implementations may source data from REST snapshots,
// WebSocket streams, or a combination of both.
type FundingRatesPort interface {
	// Latest returns the most recent funding rate for the given venue and symbol.
	Latest(ctx context.Context, venue domain.Venue, symbol domain.Symbol) (FundingRate, error)

	// History returns historical funding rate records in the [from, to) window.
	History(ctx context.Context, venue domain.Venue, symbol domain.Symbol, from, to time.Time) ([]FundingRate, error)

	// Stream returns a channel that receives funding rate updates in real time.
	// The channel is closed when ctx is canceled.
	Stream(ctx context.Context, venue domain.Venue, symbol domain.Symbol) (<-chan FundingRate, error)
}
