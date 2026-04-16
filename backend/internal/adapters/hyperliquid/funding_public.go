package hyperliquid

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// Compile-time interface compliance.
var _ ports.FundingRatesPort = (*PublicFundingAdapter)(nil)

// PublicFundingAdapter implements ports.FundingRatesPort using the public
// fundingHistory endpoint. Unlike FundingAdapter, it requires no wallet
// address and can be created with a read-only Hyperliquid client.
type PublicFundingAdapter struct {
	rest *RESTClient
	log  zerolog.Logger
}

// NewPublicFundingAdapter creates a FundingRatesPort backed by the public
// fundingHistory endpoint. Suitable for backfill and research pipelines
// where no trading wallet is configured.
func NewPublicFundingAdapter(rest *RESTClient, log zerolog.Logger) *PublicFundingAdapter {
	return &PublicFundingAdapter{
		rest: rest,
		log:  log.With().Str("component", "hyperliquid_public_funding").Logger(),
	}
}

// Latest returns the most recent funding rate from the public endpoint.
func (a *PublicFundingAdapter) Latest(ctx context.Context, _ domain.Venue, symbol domain.Symbol) (ports.FundingRate, error) {
	coin := symbolToCoin(symbol)
	now := time.Now().UTC()
	// Fetch last 24h to find the most recent record.
	snapshots, err := a.rest.GetPublicFundingHistory(ctx, coin, now.Add(-24*time.Hour), now)
	if err != nil {
		return ports.FundingRate{}, fmt.Errorf("hyperliquid: public funding latest: %w", err)
	}
	if len(snapshots) == 0 {
		return ports.FundingRate{}, fmt.Errorf("%w: no public funding data for %s", ErrAssetNotFound, coin)
	}
	// Return the newest entry.
	s := snapshots[len(snapshots)-1]
	return ports.FundingRate{
		Venue:         domain.VenueHyperliquid,
		Symbol:        symbol,
		Timestamp:     s.Time,
		Rate:          s.Rate,
		IntervalHours: 1, // Hyperliquid uses 1-hour funding intervals
	}, nil
}

// History returns historical funding rate records in the [from, to) window.
// The Hyperliquid API returns at most 500 records per call, so this method
// paginates forward when necessary.
func (a *PublicFundingAdapter) History(ctx context.Context, _ domain.Venue, symbol domain.Symbol, from, to time.Time) ([]ports.FundingRate, error) {
	coin := symbolToCoin(symbol)
	var result []ports.FundingRate
	cursor := from

	first := true
	for cursor.Before(to) {
		// Pace pagination requests to stay within Hyperliquid rate limits.
		if !first {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		first = false
		snapshots, err := a.rest.GetPublicFundingHistory(ctx, coin, cursor, to)
		if err != nil {
			return nil, fmt.Errorf("hyperliquid: public funding history: %w", err)
		}
		if len(snapshots) == 0 {
			break
		}

		for _, s := range snapshots {
			if s.Time.Before(from) || !s.Time.Before(to) {
				continue
			}
			result = append(result, ports.FundingRate{
				Venue:         domain.VenueHyperliquid,
				Symbol:        symbol,
				Timestamp:     s.Time,
				Rate:          s.Rate,
				IntervalHours: 1,
			})
		}

		// If we got fewer than 500 records, we have all data in this range.
		if len(snapshots) < 500 {
			break
		}

		// Advance cursor past the last returned record to paginate forward.
		cursor = snapshots[len(snapshots)-1].Time.Add(time.Millisecond)
	}

	return result, nil
}

// Stream returns ErrStreamNotSupported. Use the WebSocket-based FundingAdapter
// for real-time streaming.
func (a *PublicFundingAdapter) Stream(_ context.Context, _ domain.Venue, _ domain.Symbol) (<-chan ports.FundingRate, error) {
	return nil, ErrStreamNotSupported
}
