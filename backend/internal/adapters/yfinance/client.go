package yfinance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// Client fetches historical bar data from Yahoo Finance (no auth required).
type Client struct {
	client *http.Client
	log    zerolog.Logger
}

// NewClient creates a Yahoo Finance client.
func NewClient(log zerolog.Logger) *Client {
	return &Client{
		client: &http.Client{Timeout: 30 * time.Second},
		log:    log.With().Str("component", "yfinance").Logger(),
	}
}

// chartResponse mirrors the Yahoo Finance v8 chart JSON structure.
type chartResponse struct {
	Chart struct {
		Result []struct {
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open   []*float64 `json:"open"`
					High   []*float64 `json:"high"`
					Low    []*float64 `json:"low"`
					Close  []*float64 `json:"close"`
					Volume []*float64 `json:"volume"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// GetVIXBars fetches daily VIX bars from Yahoo Finance for the given date range.
func (c *Client) GetVIXBars(ctx context.Context, from, to time.Time) ([]domain.MarketBar, error) {
	url := fmt.Sprintf(
		"https://query1.finance.yahoo.com/v8/finance/chart/%%5EVIX?period1=%d&period2=%d&interval=1d",
		from.Unix(), to.Unix(),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("yfinance: create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yfinance: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("yfinance: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var chart chartResponse
	if err := json.NewDecoder(resp.Body).Decode(&chart); err != nil {
		return nil, fmt.Errorf("yfinance: decode response: %w", err)
	}

	if chart.Chart.Error != nil {
		return nil, fmt.Errorf("yfinance: API error: %s — %s", chart.Chart.Error.Code, chart.Chart.Error.Description)
	}

	if len(chart.Chart.Result) == 0 || len(chart.Chart.Result[0].Timestamp) == 0 {
		return nil, fmt.Errorf("yfinance: no data returned for VIX")
	}

	result := chart.Chart.Result[0]
	if len(result.Indicators.Quote) == 0 {
		return nil, fmt.Errorf("yfinance: no quote data in response")
	}

	q := result.Indicators.Quote[0]
	sym, _ := domain.NewSymbol("VIX")
	tf := domain.Timeframe("1d")

	var bars []domain.MarketBar
	for i, ts := range result.Timestamp {
		if i >= len(q.Open) || i >= len(q.High) || i >= len(q.Low) || i >= len(q.Close) {
			break
		}
		// Yahoo returns nil pointers for missing data points (holidays, etc.)
		if q.Open[i] == nil || q.High[i] == nil || q.Low[i] == nil || q.Close[i] == nil {
			continue
		}

		vol := 0.0
		if i < len(q.Volume) && q.Volume[i] != nil {
			vol = *q.Volume[i]
		}

		bar, err := domain.NewMarketBar(
			time.Unix(ts, 0).UTC(),
			sym, tf,
			*q.Open[i], *q.High[i], *q.Low[i], *q.Close[i],
			vol,
		)
		if err != nil {
			continue
		}
		bars = append(bars, bar)
	}

	c.log.Info().Int("bars", len(bars)).Msg("VIX bars fetched from Yahoo Finance")
	return bars, nil
}
