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
var _ ports.FundingRatesPort = (*FundingAdapter)(nil)

// FundingAdapter implements ports.FundingRatesPort for Hyperliquid.
type FundingAdapter struct {
	client *Client
	rest   *RESTClient
	ws     *WSSubscriber
	log    zerolog.Logger
}

// NewFundingAdapter creates a FundingRatesPort backed by the given client.
func NewFundingAdapter(client *Client, rest *RESTClient, ws *WSSubscriber, log zerolog.Logger) *FundingAdapter {
	return &FundingAdapter{
		client: client,
		rest:   rest,
		ws:     ws,
		log:    log.With().Str("component", "hyperliquid_funding").Logger(),
	}
}

// Latest returns the most recent funding rate for the given venue and symbol.
func (f *FundingAdapter) Latest(ctx context.Context, venue domain.Venue, symbol domain.Symbol) (ports.FundingRate, error) {
	coin := symbolToCoin(symbol)
	rates, err := f.rest.GetFundingRates(ctx)
	if err != nil {
		return ports.FundingRate{}, fmt.Errorf("hyperliquid: funding latest: %w", err)
	}

	for _, r := range rates {
		if r.Coin == coin {
			return ports.FundingRate{
				Venue:         domain.VenueHyperliquid,
				Symbol:        symbol,
				Timestamp:     time.Now(),
				Rate:          r.Rate,
				IntervalHours: 1, // Hyperliquid uses 1-hour funding intervals
				MarkPrice:     r.MarkPrice,
				NextFundingAt: nextFundingTime(),
			}, nil
		}
	}

	return ports.FundingRate{}, fmt.Errorf("%w: funding rate for %s", ErrAssetNotFound, coin)
}

// History returns historical funding rate records in the [from, to) window.
// Hyperliquid's userFunding endpoint returns per-user funding payments.
// For general funding rate history, we approximate from the payments.
func (f *FundingAdapter) History(ctx context.Context, venue domain.Venue, symbol domain.Symbol, from, to time.Time) ([]ports.FundingRate, error) {
	payments, err := f.rest.GetFundingHistory(ctx, f.client.Address(), from, to)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: funding history: %w", err)
	}

	coin := symbolToCoin(symbol)
	var result []ports.FundingRate
	for _, p := range payments {
		if p.Coin != coin {
			continue
		}
		result = append(result, ports.FundingRate{
			Venue:         domain.VenueHyperliquid,
			Symbol:        symbol,
			Timestamp:     p.Time,
			Rate:          p.Rate,
			IntervalHours: 1,
		})
	}
	return result, nil
}

// Stream returns a channel that receives funding rate updates in real time
// via the WebSocket activeAssetCtx channel.
func (f *FundingAdapter) Stream(ctx context.Context, venue domain.Venue, symbol domain.Symbol) (<-chan ports.FundingRate, error) {
	coin := symbolToCoin(symbol)
	ch := make(chan ports.FundingRate, 16)

	f.ws.SubscribeFunding(coin)
	f.ws.OnFunding(func(wsf WSFunding) {
		if wsf.Coin != coin {
			return
		}
		fr := ports.FundingRate{
			Venue:         domain.VenueHyperliquid,
			Symbol:        symbol,
			Timestamp:     wsf.Time,
			Rate:          wsf.Rate,
			IntervalHours: 1,
			NextFundingAt: nextFundingTime(),
		}
		select {
		case ch <- fr:
		case <-ctx.Done():
		default:
			// Drop if consumer is slow.
		}
	})

	go func() {
		<-ctx.Done()
		close(ch)
	}()

	return ch, nil
}

// nextFundingTime returns the next hourly funding boundary.
// Hyperliquid settles funding every hour on the hour.
func nextFundingTime() time.Time {
	now := time.Now().UTC()
	return now.Truncate(time.Hour).Add(time.Hour)
}
