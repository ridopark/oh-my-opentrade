package hyperliquid

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// Compile-time interface compliance.
var _ ports.OpenInterestPort = (*OpenInterestAdapter)(nil)

// OpenInterestAdapter implements ports.OpenInterestPort for Hyperliquid.
type OpenInterestAdapter struct {
	rest *RESTClient
	log  zerolog.Logger
}

// NewOpenInterestAdapter creates an OpenInterestPort backed by the given REST client.
func NewOpenInterestAdapter(rest *RESTClient, log zerolog.Logger) *OpenInterestAdapter {
	return &OpenInterestAdapter{
		rest: rest,
		log:  log.With().Str("component", "hyperliquid_oi").Logger(),
	}
}

// OpenInterest returns the current open interest snapshot for the given symbol.
func (a *OpenInterestAdapter) OpenInterest(ctx context.Context, venue domain.Venue, symbol domain.Symbol) (ports.OpenInterestSnapshot, error) {
	coin := symbolToCoin(symbol)
	oi, err := a.rest.GetOpenInterest(ctx, coin)
	if err != nil {
		return ports.OpenInterestSnapshot{}, fmt.Errorf("hyperliquid: open interest: %w", err)
	}
	return ports.OpenInterestSnapshot{
		Venue:     domain.VenueHyperliquid,
		Symbol:    symbol,
		OI:        oi.OI,
		OIUsd:     oi.OIUsd,
		MarkPrice: oi.MarkPrice,
	}, nil
}
