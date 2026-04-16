package bybit

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// Compile-time interface compliance.
var _ ports.FundingRatesPort = (*FundingAdapter)(nil)

// FundingAdapter implements ports.FundingRatesPort for Bybit linear perpetuals.
type FundingAdapter struct {
	client *Client
	log    zerolog.Logger
}

// NewFundingAdapter creates a FundingRatesPort backed by the Bybit REST API.
func NewFundingAdapter(client *Client, log zerolog.Logger) *FundingAdapter {
	return &FundingAdapter{
		client: client,
		log:    log.With().Str("component", "bybit_funding").Logger(),
	}
}

// tickerResult represents the result envelope for /v5/market/tickers.
type tickerResult struct {
	List []tickerEntry `json:"list"`
}

type tickerEntry struct {
	Symbol          string `json:"symbol"`
	FundingRate     string `json:"fundingRate"`
	NextFundingTime string `json:"nextFundingTime"`
	MarkPrice       string `json:"markPrice"`
}

// Latest returns the most recent funding rate for the given venue and symbol.
func (f *FundingAdapter) Latest(ctx context.Context, _ domain.Venue, symbol domain.Symbol) (ports.FundingRate, error) {
	bs, err := toBybitSymbol(string(symbol))
	if err != nil {
		return ports.FundingRate{}, err
	}

	path := fmt.Sprintf("/v5/market/tickers?category=linear&symbol=%s", bs)
	resp, err := f.client.get(ctx, path)
	if err != nil {
		return ports.FundingRate{}, fmt.Errorf("bybit: funding latest: %w", err)
	}

	var result tickerResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return ports.FundingRate{}, fmt.Errorf("%w: parse ticker: %w", ErrInvalidResponse, err)
	}
	if len(result.List) == 0 {
		return ports.FundingRate{}, fmt.Errorf("%w: no ticker data for %s", ErrSymbolNotFound, bs)
	}

	entry := result.List[0]
	fundingRate, _ := strconv.ParseFloat(entry.FundingRate, 64)
	markPrice, _ := strconv.ParseFloat(entry.MarkPrice, 64)

	var nextFundingAt time.Time
	if ms, err := strconv.ParseInt(entry.NextFundingTime, 10, 64); err == nil {
		nextFundingAt = time.UnixMilli(ms)
	}

	return ports.FundingRate{
		Venue:         domain.VenueBybit,
		Symbol:        symbol,
		Timestamp:     time.Now().UTC(),
		Rate:          fundingRate,
		IntervalHours: 8,
		MarkPrice:     markPrice,
		NextFundingAt: nextFundingAt,
	}, nil
}

// fundingHistoryResult represents the result envelope for /v5/market/funding/history.
type fundingHistoryResult struct {
	List []fundingHistoryEntry `json:"list"`
}

type fundingHistoryEntry struct {
	Symbol               string `json:"symbol"`
	FundingRate          string `json:"fundingRate"`
	FundingRateTimestamp string `json:"fundingRateTimestamp"`
}

// History returns historical funding rate records in the [from, to) window.
// Bybit limits responses to 200 records per page, so this method paginates
// backward from `to` until all records in the window are fetched.
func (f *FundingAdapter) History(ctx context.Context, _ domain.Venue, symbol domain.Symbol, from, to time.Time) ([]ports.FundingRate, error) {
	bs, err := toBybitSymbol(string(symbol))
	if err != nil {
		return nil, err
	}

	var all []ports.FundingRate
	endTime := to

	for {
		path := fmt.Sprintf("/v5/market/funding/history?category=linear&symbol=%s&startTime=%d&endTime=%d&limit=200",
			bs, from.UnixMilli(), endTime.UnixMilli())

		resp, err := f.client.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("bybit: funding history: %w", err)
		}

		var result fundingHistoryResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return nil, fmt.Errorf("%w: parse funding history: %w", ErrInvalidResponse, err)
		}

		if len(result.List) == 0 {
			break
		}

		var oldest time.Time
		for _, entry := range result.List {
			rate, _ := strconv.ParseFloat(entry.FundingRate, 64)
			ms, _ := strconv.ParseInt(entry.FundingRateTimestamp, 10, 64)
			ts := time.UnixMilli(ms)

			if ts.Before(from) {
				continue
			}

			all = append(all, ports.FundingRate{
				Venue:         domain.VenueBybit,
				Symbol:        symbol,
				Timestamp:     ts,
				Rate:          rate,
				IntervalHours: 8,
			})

			if oldest.IsZero() || ts.Before(oldest) {
				oldest = ts
			}
		}

		// Bybit returns newest first; paginate backward.
		if len(result.List) < 200 {
			break
		}
		// Move endTime before the oldest record to avoid duplicates.
		endTime = oldest.Add(-time.Millisecond)
		if !endTime.After(from) {
			break
		}
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}

	return all, nil
}

// Stream returns ErrStreamNotSupported. The read-only Bybit adapter does not
// implement WebSocket streaming; use polling via the ingestion layer instead.
func (f *FundingAdapter) Stream(_ context.Context, _ domain.Venue, _ domain.Symbol) (<-chan ports.FundingRate, error) {
	return nil, ErrStreamNotSupported
}
