package bybit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func newTestFundingAdapter(t *testing.T, handler http.Handler) (*FundingAdapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := NewClientWithHTTP(srv.URL, srv.Client(), zerolog.Nop())
	return NewFundingAdapter(c, zerolog.Nop()), srv
}

func TestFundingAdapter_Latest(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fa, srv := newTestFundingAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, "/v5/market/tickers")
			assert.Equal(t, "linear", r.URL.Query().Get("category"))
			assert.Equal(t, "BTCUSDT", r.URL.Query().Get("symbol"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"retCode": 0,
				"retMsg": "OK",
				"result": {
					"list": [{
						"symbol": "BTCUSDT",
						"fundingRate": "0.000150",
						"nextFundingTime": "1700064000000",
						"markPrice": "65432.10"
					}]
				}
			}`))
		}))
		defer srv.Close()

		fr, err := fa.Latest(context.Background(), domain.VenueBybit, domain.Symbol("BTC/USD"))
		require.NoError(t, err)
		assert.Equal(t, domain.VenueBybit, fr.Venue)
		assert.Equal(t, domain.Symbol("BTC/USD"), fr.Symbol)
		assert.InDelta(t, 0.000150, fr.Rate, 1e-8)
		assert.InDelta(t, 65432.10, fr.MarkPrice, 0.01)
		assert.Equal(t, 8, fr.IntervalHours)
		assert.Equal(t, time.UnixMilli(1700064000000), fr.NextFundingAt)
	})

	t.Run("empty list", func(t *testing.T) {
		fa, srv := newTestFundingAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[]}}`))
		}))
		defer srv.Close()

		_, err := fa.Latest(context.Background(), domain.VenueBybit, domain.Symbol("BTC/USD"))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSymbolNotFound)
	})

	t.Run("unmapped symbol", func(t *testing.T) {
		fa, srv := newTestFundingAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not reach server")
		}))
		defer srv.Close()

		_, err := fa.Latest(context.Background(), domain.VenueBybit, domain.Symbol("DOGE/USD"))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSymbolNotFound)
	})
}

func TestFundingAdapter_History(t *testing.T) {
	t.Run("single page", func(t *testing.T) {
		fa, srv := newTestFundingAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, "/v5/market/funding/history")
			assert.Equal(t, "ETHUSDT", r.URL.Query().Get("symbol"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"retCode": 0,
				"retMsg": "OK",
				"result": {
					"list": [
						{"symbol":"ETHUSDT","fundingRate":"0.000200","fundingRateTimestamp":"1700028800000"},
						{"symbol":"ETHUSDT","fundingRate":"0.000150","fundingRateTimestamp":"1700000000000"}
					]
				}
			}`))
		}))
		defer srv.Close()

		from := time.UnixMilli(1699999000000)
		to := time.UnixMilli(1700100000000)
		rates, err := fa.History(context.Background(), domain.VenueBybit, domain.Symbol("ETH/USD"), from, to)
		require.NoError(t, err)
		require.Len(t, rates, 2)
		// Results should be in chronological order (oldest first).
		assert.True(t, rates[0].Timestamp.Before(rates[1].Timestamp))
		assert.InDelta(t, 0.000150, rates[0].Rate, 1e-8)
		assert.InDelta(t, 0.000200, rates[1].Rate, 1e-8)
		assert.Equal(t, domain.VenueBybit, rates[0].Venue)
		assert.Equal(t, domain.Symbol("ETH/USD"), rates[0].Symbol)
	})

	t.Run("empty result", func(t *testing.T) {
		fa, srv := newTestFundingAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[]}}`))
		}))
		defer srv.Close()

		rates, err := fa.History(context.Background(), domain.VenueBybit, domain.Symbol("BTC/USD"),
			time.Now().Add(-time.Hour), time.Now())
		require.NoError(t, err)
		assert.Empty(t, rates)
	})

	t.Run("unmapped symbol", func(t *testing.T) {
		fa, srv := newTestFundingAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not reach server")
		}))
		defer srv.Close()

		_, err := fa.History(context.Background(), domain.VenueBybit, domain.Symbol("DOGE/USD"),
			time.Now().Add(-time.Hour), time.Now())
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSymbolNotFound)
	})
}

func TestFundingAdapter_Stream(t *testing.T) {
	fa, srv := newTestFundingAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach server")
	}))
	defer srv.Close()

	_, err := fa.Stream(context.Background(), domain.VenueBybit, domain.Symbol("BTC/USD"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStreamNotSupported)
}
