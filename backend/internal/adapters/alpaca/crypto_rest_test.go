package alpaca

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

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func newTestRESTClient(baseURL, dataURL string) *RESTClient {
	limiter := NewPriorityRateLimiter(600, 200)
	return NewRESTClient(baseURL, "test-key", "test-secret", limiter, zerolog.Nop())
}

func TestGetCryptoHistoricalBars(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/v1beta3/crypto/us/bars")
		assert.Contains(t, r.URL.RawQuery, "symbols=BTC/USD")
		assert.Contains(t, r.URL.RawQuery, "timeframe=1Min")

		resp := map[string]interface{}{
			"bars": map[string]interface{}{
				"BTC/USD": []map[string]interface{}{
					{
						"t": "2025-03-05T12:00:00Z",
						"o": 60000.0,
						"h": 61000.0,
						"l": 59500.0,
						"c": 60500.0,
						"v": 1.5,
					},
					{
						"t": "2025-03-05T12:01:00Z",
						"o": 60500.0,
						"h": 60800.0,
						"l": 60200.0,
						"c": 60600.0,
						"v": 2.3,
					},
				},
			},
			"next_page_token": nil,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := newTestRESTClient(ts.URL, ts.URL)
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("1m")
	from := time.Date(2025, 3, 5, 12, 0, 0, 0, time.UTC)
	to := time.Date(2025, 3, 5, 13, 0, 0, 0, time.UTC)

	bars, err := client.GetCryptoHistoricalBars(context.Background(), ts.URL, sym, tf, from, to)
	require.NoError(t, err)
	require.Len(t, bars, 2)

	assert.Equal(t, "BTC/USD", bars[0].Symbol.String())
	assert.Equal(t, 60000.0, bars[0].Open)
	assert.Equal(t, 61000.0, bars[0].High)
	assert.Equal(t, 59500.0, bars[0].Low)
	assert.Equal(t, 60500.0, bars[0].Close)
	assert.Equal(t, 1.5, bars[0].Volume)

	assert.Equal(t, 60500.0, bars[1].Open)
	assert.Equal(t, 2.3, bars[1].Volume)
}

func TestGetCryptoHistoricalBars_Pagination(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			resp := map[string]interface{}{
				"bars": map[string]interface{}{
					"BTC/USD": []map[string]interface{}{
						{"t": "2025-03-05T12:00:00Z", "o": 60000.0, "h": 61000.0, "l": 59500.0, "c": 60500.0, "v": 1.5},
					},
				},
				"next_page_token": "page2",
			}
			json.NewEncoder(w).Encode(resp)
		} else {
			resp := map[string]interface{}{
				"bars": map[string]interface{}{
					"BTC/USD": []map[string]interface{}{
						{"t": "2025-03-05T12:01:00Z", "o": 60500.0, "h": 60800.0, "l": 60200.0, "c": 60600.0, "v": 2.3},
					},
				},
				"next_page_token": nil,
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer ts.Close()

	client := newTestRESTClient(ts.URL, ts.URL)
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("1m")
	from := time.Date(2025, 3, 5, 12, 0, 0, 0, time.UTC)
	to := time.Date(2025, 3, 5, 13, 0, 0, 0, time.UTC)

	bars, err := client.GetCryptoHistoricalBars(context.Background(), ts.URL, sym, tf, from, to)
	require.NoError(t, err)
	require.Len(t, bars, 2)
	assert.Equal(t, 2, callCount)
}

func TestGetCryptoHistoricalBars_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer ts.Close()

	client := newTestRESTClient(ts.URL, ts.URL)
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("1m")

	_, err := client.GetCryptoHistoricalBars(context.Background(), ts.URL, sym, tf, time.Now().Add(-time.Hour), time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
}

func TestGetCryptoSnapshot(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/v1beta3/crypto/us/snapshots")
		assert.Contains(t, r.URL.RawQuery, "symbols=BTC/USD")

		resp := map[string]interface{}{
			"snapshots": map[string]interface{}{
				"BTC/USD": map[string]interface{}{
					"latestTrade": map[string]interface{}{
						"p": 60500.0,
					},
					"prevDailyBar": map[string]interface{}{
						"c": 59800.0,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := newTestRESTClient(ts.URL, ts.URL)
	snaps, err := client.GetCryptoSnapshot(context.Background(), ts.URL, []string{"BTC/USD"})
	require.NoError(t, err)
	require.Len(t, snaps, 1)

	snap, ok := snaps["BTC/USD"]
	require.True(t, ok)
	assert.Equal(t, "BTC/USD", snap.Symbol)
	require.NotNil(t, snap.LastTradePrice)
	assert.Equal(t, 60500.0, *snap.LastTradePrice)
	require.NotNil(t, snap.PrevClose)
	assert.Equal(t, 59800.0, *snap.PrevClose)
}

func TestGetCryptoSnapshot_Empty(t *testing.T) {
	client := newTestRESTClient("http://unused", "http://unused")
	snaps, err := client.GetCryptoSnapshot(context.Background(), "http://unused", []string{})
	require.NoError(t, err)
	assert.Empty(t, snaps)
}
