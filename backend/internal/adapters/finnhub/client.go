// Package finnhub provides a client for the Finnhub API to fetch
// earnings calendar data.
package finnhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const baseURL = "https://finnhub.io/api/v1"

// Client fetches earnings calendar data from Finnhub.
type Client struct {
	apiKey string
	http   *http.Client
	log    zerolog.Logger
}

// NewClient creates a new Finnhub client with the given API key.
func NewClient(apiKey string, log zerolog.Logger) *Client {
	return &Client{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 30 * time.Second},
		log:    log.With().Str("component", "finnhub").Logger(),
	}
}

// earningsResponse mirrors Finnhub's earnings calendar JSON.
type earningsResponse struct {
	EarningsCalendar []earningsRelease `json:"earningsCalendar"`
}

type earningsRelease struct {
	Date        string  `json:"date"`
	Symbol      string  `json:"symbol"`
	Hour        string  `json:"hour"`         // "bmo", "amc", "dmh"
	Quarter     int     `json:"quarter"`
	Year        int     `json:"year"`
	EPSEstimate float64 `json:"epsEstimate"`
	EPSActual   float64 `json:"epsActual"`
}

// FetchEarnings fetches earnings dates for a symbol within a date range.
// Returns the next upcoming earnings entry, or nil if none found.
func (c *Client) FetchEarnings(ctx context.Context, symbol string, from, to time.Time) (*ports.EarningsEntry, error) {
	url := fmt.Sprintf("%s/calendar/earnings?symbol=%s&from=%s&to=%s&token=%s",
		baseURL, symbol, from.Format("2006-01-02"), to.Format("2006-01-02"), c.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("finnhub: create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("finnhub: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("finnhub: rate limited (429)")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("finnhub: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var result earningsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("finnhub: decode response: %w", err)
	}

	// Find the earliest future earnings for this symbol
	now := time.Now()
	var best *ports.EarningsEntry
	for _, r := range result.EarningsCalendar {
		if r.Symbol != symbol {
			continue
		}
		d, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			continue
		}
		if d.Before(now.AddDate(0, 0, -1)) {
			continue // skip past dates
		}
		if best == nil || d.Before(best.EarningsDate) {
			best = &ports.EarningsEntry{
				Symbol:       r.Symbol,
				EarningsDate: d,
				Hour:         r.Hour,
				Quarter:      r.Quarter,
				Year:         r.Year,
			}
		}
	}

	return best, nil
}

// FetchEarningsBatch fetches earnings for multiple symbols by querying the
// date range without a symbol filter (returns all symbols for the range).
// This is more efficient than per-symbol queries for large universes.
func (c *Client) FetchEarningsBatch(ctx context.Context, symbols []string, from, to time.Time) ([]ports.EarningsEntry, error) {
	url := fmt.Sprintf("%s/calendar/earnings?from=%s&to=%s&token=%s",
		baseURL, from.Format("2006-01-02"), to.Format("2006-01-02"), c.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("finnhub: create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("finnhub: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("finnhub: rate limited (429)")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("finnhub: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var result earningsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("finnhub: decode response: %w", err)
	}

	// Build symbol lookup set
	symSet := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		symSet[s] = true
	}

	// Keep only matching symbols, earliest date per symbol
	bestBySymbol := make(map[string]ports.EarningsEntry)
	now := time.Now()
	for _, r := range result.EarningsCalendar {
		if !symSet[r.Symbol] {
			continue
		}
		d, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			continue
		}
		if d.Before(now.AddDate(0, 0, -1)) {
			continue
		}
		existing, ok := bestBySymbol[r.Symbol]
		if !ok || d.Before(existing.EarningsDate) {
			bestBySymbol[r.Symbol] = ports.EarningsEntry{
				Symbol:       r.Symbol,
				EarningsDate: d,
				Hour:         r.Hour,
				Quarter:      r.Quarter,
				Year:         r.Year,
			}
		}
	}

	entries := make([]ports.EarningsEntry, 0, len(bestBySymbol))
	for _, e := range bestBySymbol {
		entries = append(entries, e)
	}

	c.log.Info().Int("symbols_found", len(entries)).Int("symbols_queried", len(symbols)).Msg("earnings dates fetched")
	return entries, nil
}
