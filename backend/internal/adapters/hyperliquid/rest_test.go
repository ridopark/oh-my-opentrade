package hyperliquid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRESTClient(t *testing.T, handler http.Handler) (*RESTClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := newTestClient(t, srv.URL)
	return NewRESTClient(c), srv
}

func TestGetAccountState(t *testing.T) {
	resp := `{
		"marginSummary": {
			"accountValue": "10000.50",
			"totalNtlPos": "5000.25",
			"totalRawUsd": "9000.00",
			"totalMarginUsed": "1000.00"
		},
		"assetPositions": [
			{
				"position": {
					"coin": "BTC",
					"szi": "0.5",
					"entryPx": "50000.00",
					"positionValue": "25000.00",
					"unrealizedPnl": "500.00",
					"leverage": {"type": "cross", "value": 5},
					"liquidationPx": "40000.00",
					"marginUsed": "5000.00",
					"returnOnEquity": "0.10"
				}
			},
			{
				"position": {
					"coin": "ETH",
					"szi": "-2.0",
					"entryPx": "3000.00",
					"positionValue": "6000.00",
					"unrealizedPnl": "-100.00",
					"leverage": {"type": "cross", "value": 3},
					"liquidationPx": "3500.00",
					"marginUsed": "2000.00",
					"returnOnEquity": "-0.05"
				}
			}
		]
	}`

	rest, srv := newTestRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	state, err := rest.GetAccountState(context.Background(), "0xabc")
	require.NoError(t, err)

	assert.InDelta(t, 10000.50, state.MarginSummary.AccountValue, 0.01)
	assert.InDelta(t, 5000.25, state.MarginSummary.TotalNtlPos, 0.01)
	require.Len(t, state.Positions, 2)

	btc := state.Positions[0].Position
	assert.Equal(t, "BTC", btc.Coin)
	assert.InDelta(t, 0.5, btc.Szi, 0.001)
	assert.InDelta(t, 50000.0, btc.EntryPx, 0.01)
	assert.InDelta(t, 500.0, btc.UnrealizedPnl, 0.01)

	eth := state.Positions[1].Position
	assert.Equal(t, "ETH", eth.Coin)
	assert.InDelta(t, -2.0, eth.Szi, 0.001)
	assert.InDelta(t, -100.0, eth.UnrealizedPnl, 0.01)
}

func TestGetFundingRates(t *testing.T) {
	// metaAndAssetCtxs returns a 2-element array: [meta, [assetCtx...]]
	resp := `[
		{"universe": [
			{"name": "BTC", "szDecimals": 5},
			{"name": "ETH", "szDecimals": 4}
		]},
		[
			{"funding": "0.00015", "markPx": "50000.0", "openInterest": "1200.5", "oraclePx": "50010.0", "premium": "0.0001"},
			{"funding": "0.00020", "markPx": "3000.0", "openInterest": "8500.0", "oraclePx": "3001.0", "premium": "0.00012"}
		]
	]`

	rest, srv := newTestRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	rates, err := rest.GetFundingRates(context.Background())
	require.NoError(t, err)
	require.Len(t, rates, 2)

	assert.Equal(t, "BTC", rates[0].Coin)
	assert.InDelta(t, 0.00015, rates[0].Rate, 1e-8)
	assert.InDelta(t, 50000.0, rates[0].MarkPrice, 0.01)
	assert.InDelta(t, 1200.5, rates[0].OpenInterest, 0.01)

	assert.Equal(t, "ETH", rates[1].Coin)
	assert.InDelta(t, 0.00020, rates[1].Rate, 1e-8)
}

func TestGetCandles(t *testing.T) {
	resp := `[
		{"t": 1700000000000, "o": "50000.0", "h": "50500.0", "l": "49500.0", "c": "50200.0", "v": "100.5", "n": 250},
		{"t": 1700003600000, "o": "50200.0", "h": "50800.0", "l": "50100.0", "c": "50700.0", "v": "80.2", "n": 180}
	]`

	rest, srv := newTestRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	candles, err := rest.GetCandles(context.Background(), "BTC", "1h",
		time.UnixMilli(1700000000000), time.UnixMilli(1700007200000))
	require.NoError(t, err)
	require.Len(t, candles, 2)

	assert.Equal(t, time.UnixMilli(1700000000000).UTC(), candles[0].Time.UTC())
	assert.InDelta(t, 50000.0, candles[0].Open, 0.01)
	assert.InDelta(t, 50500.0, candles[0].High, 0.01)
	assert.InDelta(t, 49500.0, candles[0].Low, 0.01)
	assert.InDelta(t, 50200.0, candles[0].Close, 0.01)
	assert.InDelta(t, 100.5, candles[0].Volume, 0.01)
	assert.Equal(t, 250, candles[0].Trades)
}

func TestGetOpenInterest(t *testing.T) {
	resp := `[
		{"universe": [
			{"name": "BTC", "szDecimals": 5},
			{"name": "ETH", "szDecimals": 4}
		]},
		[
			{"funding": "0.00015", "markPx": "50000.0", "openInterest": "1200.5", "oraclePx": "50010.0", "premium": "0.0001"},
			{"funding": "0.00020", "markPx": "3000.0", "openInterest": "8500.0", "oraclePx": "3001.0", "premium": "0.00012"}
		]
	]`

	rest, srv := newTestRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	oi, err := rest.GetOpenInterest(context.Background(), "ETH")
	require.NoError(t, err)
	assert.Equal(t, "ETH", oi.Coin)
	assert.InDelta(t, 8500.0, oi.OI, 0.01)
	assert.InDelta(t, 25500000.0, oi.OIUsd, 100.0) // 8500 * 3000
	assert.InDelta(t, 3000.0, oi.MarkPrice, 0.01)
}

func TestGetOpenInterest_NotFound(t *testing.T) {
	resp := `[
		{"universe": [{"name": "BTC", "szDecimals": 5}]},
		[{"funding": "0.00015", "markPx": "50000.0", "openInterest": "1200.5", "oraclePx": "50010.0", "premium": "0.0001"}]
	]`

	rest, srv := newTestRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	_, err := rest.GetOpenInterest(context.Background(), "DOGE")
	assert.ErrorIs(t, err, ErrAssetNotFound)
}

func TestRESTGetOpenOrders(t *testing.T) {
	resp := `[
		{"coin":"BTC","oid":12345,"side":"B","limitPx":"50000.00","sz":"0.1","timestamp":1700000000000},
		{"coin":"ETH","oid":12346,"side":"A","limitPx":"3100.00","sz":"2.0","timestamp":1700000001000}
	]`

	rest, srv := newTestRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	orders, err := rest.GetOpenOrders(context.Background(), "0xabc")
	require.NoError(t, err)
	require.Len(t, orders, 2)

	assert.Equal(t, "BTC", orders[0].Coin)
	assert.Equal(t, int64(12345), orders[0].OID)
	assert.Equal(t, "B", orders[0].Side)
	assert.Equal(t, "50000.00", orders[0].LimitPx)

	assert.Equal(t, "ETH", orders[1].Coin)
	assert.Equal(t, "A", orders[1].Side)
}

func TestGetFundingHistory(t *testing.T) {
	resp := `[
		{
			"time": 1700000000000,
			"coin": "BTC",
			"usds": "5.25",
			"hash": "0xabc",
			"delta": {
				"type": "funding",
				"coin": "BTC",
				"usds": "5.25",
				"fundingRate": "0.00015"
			}
		}
	]`

	rest, srv := newTestRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	payments, err := rest.GetFundingHistory(context.Background(), "0xabc",
		time.UnixMilli(1699999000000), time.UnixMilli(1700001000000))
	require.NoError(t, err)
	require.Len(t, payments, 1)

	assert.Equal(t, "BTC", payments[0].Coin)
	assert.InDelta(t, 5.25, payments[0].UsdSize, 0.01)
	assert.InDelta(t, 0.00015, payments[0].Rate, 1e-8)
	assert.Equal(t, time.UnixMilli(1700000000000).UTC(), payments[0].Time.UTC())
}

func TestParseFloat(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"50000.50", 50000.50},
		{"0.00015", 0.00015},
		{"-2.5", -2.5},
		{"", 0},
		{"invalid", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseFloat(tt.input)
			assert.InDelta(t, tt.want, got, 1e-10)
		})
	}
}
