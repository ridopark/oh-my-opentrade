package coinbase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// GetHistoricalBars fetches OHLCV bars from Coinbase for the given symbol,
// timeframe and [from, to] window. The returned slice is sorted ascending by
// time (oldest first) to match the other MarketDataFetcher adapters.
//
// Coinbase returns at most 300 candles per call and rejects ranges that imply
// more than 300 aggregations with a 400 error. To stay within that limit we
// walk the window forward in fixed steps of 300 × granularity seconds,
// issuing one request per step.
//
// The method satisfies backfill.MarketDataFetcher.
func (c *Client) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	product, err := toCoinbaseProduct(symbol)
	if err != nil {
		return nil, err
	}
	granularity, err := toCoinbaseGranularity(timeframe)
	if err != nil {
		return nil, err
	}

	if !to.After(from) {
		return nil, fmt.Errorf("coinbase: invalid window: to (%s) must be after from (%s)", to.Format(time.RFC3339), from.Format(time.RFC3339))
	}

	var bars []domain.MarketBar
	windowStart := from.UTC()
	windowEnd := to.UTC()
	step := time.Duration(granularity*maxCandlesPerRequest) * time.Second

	pageStart := windowStart
	for pageStart.Before(windowEnd) {
		pageEnd := pageStart.Add(step)
		if pageEnd.After(windowEnd) {
			pageEnd = windowEnd
		}

		page, _, err := c.fetchCandlePage(ctx, product, symbol, timeframe, granularity, pageStart, pageEnd)
		if err != nil {
			return nil, err
		}
		bars = append(bars, page...)

		pageStart = pageEnd
	}

	// Filter to the requested window (pagination can include bars outside
	// [from, to] due to granularity rounding) and sort ascending.
	filtered := bars[:0]
	for _, b := range bars {
		if b.Time.Before(windowStart) || b.Time.After(to.UTC()) {
			continue
		}
		filtered = append(filtered, b)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Time.Before(filtered[j].Time) })

	c.log.Debug().
		Str("symbol", symbol.String()).
		Str("timeframe", timeframe.String()).
		Int("count", len(filtered)).
		Msg("coinbase historical bars retrieved")
	return filtered, nil
}

// fetchCandlePage issues a single /candles call, parses the response, and
// returns validated MarketBars together with the oldest bar timestamp seen
// (used to step pagination backward). A response of len(bars) < 300 signals
// to the caller that no further pages are needed for this window.
func (c *Client) fetchCandlePage(ctx context.Context, product string, symbol domain.Symbol, timeframe domain.Timeframe, granularity int, from, to time.Time) ([]domain.MarketBar, time.Time, error) {
	path := fmt.Sprintf("/products/%s/candles", url.PathEscape(product))
	q := url.Values{}
	q.Set("start", from.Format(time.RFC3339))
	q.Set("end", to.Format(time.RFC3339))
	q.Set("granularity", strconv.Itoa(granularity))
	reqURL := c.baseURL + path + "?" + q.Encode()

	body, err := c.doWithRetry(ctx, reqURL, symbol)
	if err != nil {
		return nil, time.Time{}, err
	}

	// Response shape: [[time, low, high, open, close, volume], ...]
	// Each candle is an array of 6 numbers; time is unix-seconds (int).
	var raw [][]float64
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, time.Time{}, fmt.Errorf("coinbase: decode candles for %s: %w", symbol.String(), err)
	}

	bars := make([]domain.MarketBar, 0, len(raw))
	oldest := time.Time{}
	for _, row := range raw {
		if len(row) < 6 {
			c.log.Warn().Str("symbol", symbol.String()).Int("len", len(row)).Msg("skipping malformed candle row")
			continue
		}
		// Guard against NaN/Inf from upstream.
		hasBadFloat := false
		for _, v := range row {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				hasBadFloat = true
				break
			}
		}
		if hasBadFloat {
			c.log.Warn().Str("symbol", symbol.String()).Msg("skipping candle row with NaN/Inf")
			continue
		}

		t := time.Unix(int64(row[0]), 0).UTC()
		low := row[1]
		high := row[2]
		open := row[3]
		closePx := row[4]
		volume := row[5]

		bar, err := domain.NewMarketBar(t, symbol, timeframe, open, high, low, closePx, volume)
		if err != nil {
			c.log.Warn().
				Err(err).
				Str("symbol", symbol.String()).
				Time("time", t).
				Msg("skipping invalid coinbase bar")
			continue
		}
		bars = append(bars, bar)

		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	return bars, oldest, nil
}

// doWithRetry performs an HTTP GET against the Coinbase API, respecting the
// rate limiter, retrying on 429 and 5xx responses with exponential backoff
// (honoring Retry-After when present), and translating 404 into the sentinel
// ErrSymbolUnknown. Returns the raw response body on success.
func (c *Client) doWithRetry(ctx context.Context, reqURL string, symbol domain.Symbol) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("coinbase: rate limiter wait: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("coinbase: build request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "oh-my-opentrade/coinbase")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("coinbase: http do: %w", err)
			c.log.Warn().Err(err).Str("symbol", symbol.String()).Int("attempt", attempt+1).Msg("coinbase request failed, retrying")
			sleepBackoff(ctx, attempt, 0)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("coinbase: read body: %w", readErr)
			sleepBackoff(ctx, attempt, 0)
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil

		case resp.StatusCode == http.StatusNotFound:
			// 404 means Coinbase does not know this product id. No retry.
			c.log.Warn().
				Str("symbol", symbol.String()).
				Str("body", truncate(string(body), 200)).
				Msg("coinbase returned 404 for product")
			return nil, fmt.Errorf("%w: %s", ErrSymbolUnknown, symbol.String())

		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			lastErr = fmt.Errorf("coinbase: status %d: %s", resp.StatusCode, truncate(string(body), 200))
			c.log.Warn().
				Int("status", resp.StatusCode).
				Str("symbol", symbol.String()).
				Int("attempt", attempt+1).
				Msg("coinbase request retrying after transient failure")
			sleepBackoff(ctx, attempt, retryAfter)
			continue

		default:
			return nil, fmt.Errorf("coinbase: request failed (status %d): %s", resp.StatusCode, truncate(string(body), 200))
		}
	}
	if lastErr == nil {
		lastErr = errors.New("coinbase: exhausted retries without a response")
	}
	return nil, lastErr
}

// sleepBackoff pauses for either the Retry-After hint (when non-zero) or an
// exponential jittered backoff keyed on attempt. It honors ctx cancellation.
func sleepBackoff(ctx context.Context, attempt int, retryAfter time.Duration) {
	var d time.Duration
	switch {
	case retryAfter > 0:
		d = retryAfter
	default:
		// 200ms * 2^attempt, capped at 2s. Deterministic — good enough for
		// the retry budget of 3 and avoids flaky tests.
		d = time.Duration(200*(1<<attempt)) * time.Millisecond
		if d > 2*time.Second {
			d = 2 * time.Second
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// parseRetryAfter parses the Retry-After header, which may be either a number
// of seconds or an HTTP-date. Returns 0 when the header is missing/invalid so
// the caller falls back to exponential backoff.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// truncate returns s clipped to max runes, for log lines that should not spew
// a full response body.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
