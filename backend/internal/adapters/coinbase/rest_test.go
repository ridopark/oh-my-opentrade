package coinbase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// newTestClient builds a Coinbase client pointing at the supplied test server
// URL with a very permissive rate limit so tests are not slowed by throttling.
func newTestClient(baseURL string) *Client {
	return NewClient(config.CoinbaseConfig{
		BaseURL:        baseURL,
		RateLimitRPS:   1000,
		TimeoutSeconds: 5,
	}, zerolog.Nop())
}

// candleRow is the Coinbase wire format: [time, low, high, open, close, volume].
func candleRow(t time.Time, low, high, open, closePx, volume float64) []float64 {
	return []float64{float64(t.Unix()), low, high, open, closePx, volume}
}

func TestGetHistoricalBars_SinglePage(t *testing.T) {
	// Three bars, newest-first (how Coinbase actually returns them).
	t0 := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Minute)
	t2 := t0.Add(10 * time.Minute)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/products/BTC-USD/candles", r.URL.Path)
		assert.Equal(t, "300", r.URL.Query().Get("granularity"))
		assert.NotEmpty(t, r.URL.Query().Get("start"))
		assert.NotEmpty(t, r.URL.Query().Get("end"))

		resp := [][]float64{
			candleRow(t2, 65900, 66100, 66000, 66050, 3.1),
			candleRow(t1, 65800, 66000, 65900, 66000, 2.2),
			candleRow(t0, 65700, 65900, 65800, 65900, 1.1),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := newTestClient(ts.URL)
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("5m")

	from := t0.Add(-time.Minute)
	to := t2.Add(time.Minute)

	bars, err := client.GetHistoricalBars(context.Background(), sym, tf, from, to)
	require.NoError(t, err)
	require.Len(t, bars, 3)

	// Must come back ascending (oldest first).
	assert.True(t, bars[0].Time.Equal(t0), "bar[0] time = %s, want %s", bars[0].Time, t0)
	assert.True(t, bars[1].Time.Equal(t1))
	assert.True(t, bars[2].Time.Equal(t2))

	// OHLCV decoded from [time, low, high, open, close, volume] — verify the
	// slightly tricky reordering.
	assert.Equal(t, 65800.0, bars[0].Open)
	assert.Equal(t, 65900.0, bars[0].High)
	assert.Equal(t, 65700.0, bars[0].Low)
	assert.Equal(t, 65900.0, bars[0].Close)
	assert.Equal(t, 1.1, bars[0].Volume)
	assert.Equal(t, "BTC/USD", bars[0].Symbol.String())
	assert.Equal(t, "5m", bars[0].Timeframe.String())
}

func TestGetHistoricalBars_Pagination(t *testing.T) {
	// Window covers 450 minutes at 1m granularity. Adapter walks forward in
	// 300-minute steps, so we expect exactly 2 pages: [from, from+300min) and
	// [from+300min, from+450min). Server fills each requested range densely
	// in Coinbase's newest-first order.
	from := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	to := from.Add(450 * time.Minute)

	var callCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")

		startStr := r.URL.Query().Get("start")
		endStr := r.URL.Query().Get("end")
		start, _ := time.Parse(time.RFC3339, startStr)
		end, _ := time.Parse(time.RFC3339, endStr)

		var page [][]float64
		for t := end.Add(-time.Minute); !t.Before(start); t = t.Add(-time.Minute) {
			page = append(page, candleRow(t, 100, 101, 100.5, 100.8, 0.5))
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer ts.Close()

	client := newTestClient(ts.URL)
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("1m")

	bars, err := client.GetHistoricalBars(context.Background(), sym, tf, from, to)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount), "adapter should issue one request per 300-bar chunk")
	assert.Equal(t, 450, len(bars))

	// Verify the result is ascending.
	for i := 1; i < len(bars); i++ {
		assert.True(t, bars[i-1].Time.Before(bars[i].Time), "bars should be sorted ascending")
	}
}

func TestGetHistoricalBars_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal oops"))
	}))
	defer ts.Close()

	client := newTestClient(ts.URL)
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("5m")

	_, err := client.GetHistoricalBars(context.Background(),
		sym, tf,
		time.Now().Add(-time.Hour), time.Now(),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
}

func TestGetHistoricalBars_RateLimit429(t *testing.T) {
	t0 := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)

	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// Tell the client to wait 0 seconds — keeps the test fast.
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limit"}`))
			return
		}
		resp := [][]float64{
			candleRow(t0, 100, 101, 100.5, 100.8, 0.5),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := newTestClient(ts.URL)
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("5m")

	bars, err := client.GetHistoricalBars(context.Background(),
		sym, tf,
		t0.Add(-time.Minute), t0.Add(time.Minute),
	)
	require.NoError(t, err)
	require.Len(t, bars, 1)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "should have retried once after 429")
}

func TestGetHistoricalBars_UnknownProduct(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"NotFound"}`))
	}))
	defer ts.Close()

	client := newTestClient(ts.URL)
	sym, _ := domain.NewSymbol("XXX/USD")
	tf, _ := domain.NewTimeframe("5m")

	_, err := client.GetHistoricalBars(context.Background(),
		sym, tf,
		time.Now().Add(-time.Hour), time.Now(),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSymbolUnknown), "want ErrSymbolUnknown, got %v", err)
}

// TestGetHistoricalBars_SkipsInvalidBars verifies that a bar with high<low is
// logged+skipped rather than failing the whole request.
func TestGetHistoricalBars_SkipsInvalidBars(t *testing.T) {
	t0 := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Minute)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := [][]float64{
			candleRow(t1, 200, 100, 150, 150, 1.0),      // high<low -> invalid, should be skipped
			candleRow(t0, 100, 101, 100.5, 100.8, 0.5), // valid
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := newTestClient(ts.URL)
	sym, _ := domain.NewSymbol("BTC/USD")
	tf, _ := domain.NewTimeframe("5m")

	bars, err := client.GetHistoricalBars(context.Background(),
		sym, tf,
		t0.Add(-time.Minute), t1.Add(time.Minute),
	)
	require.NoError(t, err)
	require.Len(t, bars, 1)
	assert.True(t, bars[0].Time.Equal(t0))
}

// Sanity check that the granularity query param is formatted per our mapping.
func TestGetHistoricalBars_EndpointShape(t *testing.T) {
	t0 := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)

	var gotGranularity string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotGranularity = r.URL.Query().Get("granularity")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer ts.Close()

	client := newTestClient(ts.URL)
	sym, _ := domain.NewSymbol("ETH/USD")
	tf, _ := domain.NewTimeframe("1h")

	_, err := client.GetHistoricalBars(context.Background(), sym, tf, t0, t0.Add(time.Hour))
	require.NoError(t, err)
	require.NotEmpty(t, gotGranularity)
	n, _ := strconv.Atoi(gotGranularity)
	assert.Equal(t, 3600, n, "1h must map to 3600s")
}

// Compile-time assertion: Client satisfies the backfill.MarketDataFetcher
// signature shape (checked here without importing the package to keep the
// test package free of a reverse dep).
var _ interface {
	GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
} = (*Client)(nil)

// Catch a silly server handler mistake early with a readable failure.
func TestCandleRowHelper(t *testing.T) {
	t0 := time.Unix(1718416800, 0).UTC()
	row := candleRow(t0, 1, 2, 1.5, 1.8, 0.1)
	require.Len(t, row, 6)
	assert.Equal(t, float64(t0.Unix()), row[0])
}

// Sanity-check the fmt.Errorf wrapping pattern on rest.go: errors pass through
// errors.Is for ErrSymbolUnknown.
func TestErrSymbolUnknown_ErrorsIs(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", ErrSymbolUnknown)
	assert.True(t, errors.Is(wrapped, ErrSymbolUnknown))
}
