package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// cryptoBarsResponse is the JSON shape for the Alpaca crypto historical bars endpoint.
// Unlike equities ({"bars": [...]}), crypto bars are keyed by symbol: {"bars": {"BTC/USD": [...]}}.
type cryptoBarsResponse struct {
	Bars          map[string][]historicalBar `json:"bars"`
	NextPageToken string                     `json:"next_page_token"`
}

// cryptoSnapshotResponse is the JSON shape for the Alpaca crypto snapshot endpoint.
type cryptoSnapshotResponse struct {
	Snapshots map[string]cryptoSnapshotEntry `json:"snapshots"`
}

type cryptoSnapshotEntry struct {
	LatestBar *struct {
		T time.Time `json:"t"`
		O float64   `json:"o"`
		H float64   `json:"h"`
		L float64   `json:"l"`
		C float64   `json:"c"`
		V float64   `json:"v"`
	} `json:"latestBar"`
	LatestTrade *struct {
		P float64 `json:"p"`
	} `json:"latestTrade"`
	DailyBar *struct {
		V float64 `json:"v"`
	} `json:"dailyBar"`
	PrevDailyBar *struct {
		C float64 `json:"c"`
	} `json:"prevDailyBar"`
}

// GetCryptoHistoricalBars fetches historical OHLCV bars for a crypto symbol from the Alpaca data API.
// It paginates via next_page_token until all results are returned.
// Endpoint: /v1beta3/crypto/us/bars?symbols={sym}&timeframe={tf}&start={from}&end={to}
func (c *RESTClient) GetCryptoHistoricalBars(ctx context.Context, dataURL string, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	tf := toAlpacaTimeframe(string(timeframe))
	var bars []domain.MarketBar
	nextToken := ""

	symStr := symbol.String()

	for {
		path := fmt.Sprintf("/v1beta3/crypto/us/bars?symbols=%s&timeframe=%s&start=%s&end=%s&limit=1000",
			symStr, tf,
			from.UTC().Format(time.RFC3339),
			to.UTC().Format(time.RFC3339),
		)
		if nextToken != "" {
			path += "&page_token=" + nextToken
		}

		resp, err := c.doReqDataAPI(ctx, dataURL, http.MethodGet, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
		if err != nil {
			c.log.Error().Err(err).Str("symbol", symStr).Msg("crypto historical bars HTTP request failed")
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			c.log.Error().Int("status", resp.StatusCode).Str("symbol", symStr).Msg("crypto historical bars request failed")
			return nil, fmt.Errorf("alpaca: get crypto historical bars failed (status %d): %s", resp.StatusCode, string(body))
		}

		var page cryptoBarsResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("alpaca: failed to decode crypto historical bars: %w", err)
		}

		// Crypto response is keyed by symbol: {"bars": {"BTC/USD": [...]}}
		symBars, ok := page.Bars[symStr]
		if !ok {
			// Try no-slash variant (Alpaca may return either format)
			noSlash := string(symbol.ToNoSlashFormat())
			symBars = page.Bars[noSlash]
		}

		for _, b := range symBars {
			bar, err := domain.NewMarketBar(b.T, symbol, timeframe, b.O, b.H, b.L, b.C, b.V)
			if err != nil {
				c.log.Warn().Err(err).Str("symbol", symStr).Msg("skipping invalid crypto historical bar")
				continue
			}
			bars = append(bars, bar)
		}

		if page.NextPageToken == "" {
			break
		}
		nextToken = page.NextPageToken
	}

	c.log.Debug().
		Str("symbol", symStr).
		Int("count", len(bars)).
		Msg("crypto historical bars retrieved")
	return bars, nil
}

// GetCryptoSnapshot fetches snapshot data for crypto symbols from the Alpaca data API.
// Endpoint: /v1beta3/crypto/us/snapshots?symbols={sym1,sym2,...}
func (c *RESTClient) GetCryptoSnapshot(ctx context.Context, dataURL string, symbols []string) (map[string]ports.Snapshot, error) {
	if len(symbols) == 0 {
		return map[string]ports.Snapshot{}, nil
	}

	path := "/v1beta3/crypto/us/snapshots?symbols=" + strings.Join(symbols, ",")
	resp, err := c.doReqDataAPI(ctx, dataURL, http.MethodGet, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
	if err != nil {
		c.log.Error().Err(err).Msg("crypto snapshots HTTP request failed")
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alpaca: get crypto snapshots failed (status %d): %s", resp.StatusCode, string(body))
	}

	respBody := bytes.NewReader(body)
	var raw cryptoSnapshotResponse
	if err := json.NewDecoder(respBody).Decode(&raw); err != nil {
		return nil, fmt.Errorf("alpaca: decode crypto snapshots: %w", err)
	}

	out := make(map[string]ports.Snapshot, len(raw.Snapshots))
	for sym, entry := range raw.Snapshots {
		var lastTrade *float64
		if entry.LatestTrade != nil {
			lastTrade = &entry.LatestTrade.P
		}
		var prevClose *float64
		if entry.PrevDailyBar != nil {
			prevClose = &entry.PrevDailyBar.C
		}

		out[sym] = ports.Snapshot{
			Symbol:         sym,
			PrevClose:      prevClose,
			LastTradePrice: lastTrade,
		}
	}
	return out, nil
}
