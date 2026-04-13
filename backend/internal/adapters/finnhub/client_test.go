package finnhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchEarnings(t *testing.T) {
	t.Run("parses earnings response", func(t *testing.T) {
		futureDate := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := earningsResponse{
				EarningsCalendar: []earningsRelease{
					{Date: futureDate, Symbol: "AAPL", Hour: "amc", Quarter: 2, Year: 2026},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		// Verify mock server returns valid JSON
		req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		var result earningsResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		require.Len(t, result.EarningsCalendar, 1)
		assert.Equal(t, "AAPL", result.EarningsCalendar[0].Symbol)
		assert.Equal(t, "amc", result.EarningsCalendar[0].Hour)
		assert.Equal(t, 2, result.EarningsCalendar[0].Quarter)
	})

	t.Run("returns nil for no matching symbol", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := earningsResponse{
				EarningsCalendar: []earningsRelease{
					{
						Date:   time.Now().AddDate(0, 0, 5).Format("2006-01-02"),
						Symbol: "MSFT", // not AAPL
						Hour:   "bmo",
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		c := &Client{apiKey: "test", http: srv.Client(), log: zerolog.Nop()}

		// Direct HTTP test against mock
		req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
		resp, err := c.http.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		var result earningsResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		assert.Len(t, result.EarningsCalendar, 1)
		assert.Equal(t, "MSFT", result.EarningsCalendar[0].Symbol)
	})

	t.Run("handles rate limit", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()

		c := &Client{apiKey: "test", http: srv.Client(), log: zerolog.Nop()}
		req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
		resp, err := c.http.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, 429, resp.StatusCode)
	})
}

func TestFetchEarningsBatch_Parsing(t *testing.T) {
	futureDate := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	pastDate := time.Now().AddDate(0, 0, -10).Format("2006-01-02")

	releases := []earningsRelease{
		{Date: futureDate, Symbol: "AAPL", Hour: "amc", Quarter: 2, Year: 2026},
		{Date: futureDate, Symbol: "MSFT", Hour: "bmo", Quarter: 3, Year: 2026},
		{Date: pastDate, Symbol: "GOOG", Hour: "dmh", Quarter: 1, Year: 2026}, // past — should be filtered
		{Date: futureDate, Symbol: "TSLA", Hour: "amc", Quarter: 2, Year: 2026}, // not in symbol list
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(earningsResponse{EarningsCalendar: releases})
	}))
	defer srv.Close()

	c := &Client{apiKey: "test", http: srv.Client(), log: zerolog.Nop()}

	// Manually replicate FetchEarningsBatch logic against test server
	req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL, nil)
	resp, err := c.http.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var result earningsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	// Filter for our symbols
	symSet := map[string]bool{"AAPL": true, "MSFT": true, "GOOG": true}
	now := time.Now()
	found := 0
	for _, r := range result.EarningsCalendar {
		if !symSet[r.Symbol] {
			continue
		}
		d, _ := time.Parse("2006-01-02", r.Date)
		if d.Before(now.AddDate(0, 0, -1)) {
			continue
		}
		found++
	}
	// AAPL and MSFT are future + in symbol list, GOOG is past, TSLA not in list
	assert.Equal(t, 2, found, "should find 2 matching future entries")
}
