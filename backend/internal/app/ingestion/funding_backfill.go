package ingestion

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// FundingBackfillOption configures optional parameters on FundingBackfill.
type FundingBackfillOption func(*FundingBackfill)

// WithChunkSize overrides the default 24h fetch chunk. Larger chunks reduce
// API calls when the source supports wide date ranges (e.g., Hyperliquid).
func WithChunkSize(d time.Duration) FundingBackfillOption {
	return func(b *FundingBackfill) { b.chunkSize = d }
}

// WithRequestDelay adds a pause between API requests to respect rate limits.
func WithRequestDelay(d time.Duration) FundingBackfillOption {
	return func(b *FundingBackfill) { b.requestDelay = d }
}

// FundingBackfill fetches historical funding rates from a venue adapter and
// persists them to the funding_rates hypertable. Used for initial data
// loading and gap filling.
type FundingBackfill struct {
	source       ports.FundingRatesPort
	repo         *timescaledb.FundingRepo
	log          zerolog.Logger
	chunkSize    time.Duration
	requestDelay time.Duration
}

// NewFundingBackfill creates a backfill job for the given source and repo.
func NewFundingBackfill(source ports.FundingRatesPort, repo *timescaledb.FundingRepo, log zerolog.Logger, opts ...FundingBackfillOption) *FundingBackfill {
	b := &FundingBackfill{
		source:    source,
		repo:      repo,
		log:       log.With().Str("component", "funding_backfill").Logger(),
		chunkSize: 24 * time.Hour,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Run backfills funding rates for the given symbols from `from` to `to`.
// It fetches in chunks and inserts in batches to avoid memory pressure
// on large date ranges.
func (b *FundingBackfill) Run(ctx context.Context, venue domain.Venue, symbols []domain.Symbol, from, to time.Time) error {
	chunkSize := b.chunkSize

	for _, sym := range symbols {
		b.log.Info().
			Str("venue", string(venue)).
			Str("symbol", string(sym)).
			Time("from", from).
			Time("to", to).
			Msg("starting funding rate backfill")

		var totalInserted int
		cursor := from

		for cursor.Before(to) {
			chunkEnd := cursor.Add(chunkSize)
			if chunkEnd.After(to) {
				chunkEnd = to
			}

			if b.requestDelay > 0 {
				select {
				case <-time.After(b.requestDelay):
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			rates, err := b.source.History(ctx, venue, sym, cursor, chunkEnd)
			if err != nil {
				return fmt.Errorf("funding_backfill: history %s %s [%s, %s): %w",
					venue, sym, cursor.Format(time.RFC3339), chunkEnd.Format(time.RFC3339), err)
			}

			if len(rates) > 0 {
				if err := b.repo.Insert(ctx, rates); err != nil {
					return fmt.Errorf("funding_backfill: insert %s %s: %w", venue, sym, err)
				}
				totalInserted += len(rates)
			}

			cursor = chunkEnd
		}

		b.log.Info().
			Str("venue", string(venue)).
			Str("symbol", string(sym)).
			Int("total_inserted", totalInserted).
			Msg("funding rate backfill complete")
	}

	return nil
}
