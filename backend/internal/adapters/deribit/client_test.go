package deribit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := NewClient(Config{
		BaseURL: srv.URL + "/",
		Assets:  []string{"BTC"},
	}, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

func TestGetInstruments(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/public/get_instruments", r.URL.Path)
		assert.Equal(t, "BTC", r.URL.Query().Get("currency"))
		assert.Equal(t, "option", r.URL.Query().Get("kind"))
		assert.Equal(t, "false", r.URL.Query().Get("expired"))

		resp := map[string]interface{}{
			"result": []map[string]interface{}{
				{
					"instrument_name":      "BTC-28MAR25-80000-C",
					"base_currency":        "BTC",
					"quote_currency":       "USD",
					"strike":               80000.0,
					"expiration_timestamp":  1743120000000, // 2025-03-28T00:00:00Z
					"option_type":          "call",
					"is_active":            true,
					"kind":                 "option",
				},
				{
					"instrument_name":      "BTC-28MAR25-80000-P",
					"base_currency":        "BTC",
					"quote_currency":       "USD",
					"strike":               80000.0,
					"expiration_timestamp":  1743120000000,
					"option_type":          "put",
					"is_active":            true,
					"kind":                 "option",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	c := newTestClient(t, handler)
	instruments, err := c.GetInstruments(context.Background(), "BTC")

	require.NoError(t, err)
	require.Len(t, instruments, 2)

	assert.Equal(t, "BTC-28MAR25-80000-C", instruments[0].InstrumentName)
	assert.Equal(t, "BTC", instruments[0].BaseCurrency)
	assert.Equal(t, 80000.0, instruments[0].Strike)
	assert.Equal(t, "call", instruments[0].OptionType)
	assert.True(t, instruments[0].IsActive)

	assert.Equal(t, "put", instruments[1].OptionType)
}

func TestGetTicker(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/public/ticker", r.URL.Path)
		assert.Equal(t, "BTC-28MAR25-80000-C", r.URL.Query().Get("instrument_name"))

		resp := map[string]interface{}{
			"result": map[string]interface{}{
				"instrument_name":  "BTC-28MAR25-80000-C",
				"mark_iv":          55.0,  // percentage
				"bid_iv":           54.0,
				"ask_iv":           56.0,
				"underlying_price": 82000.0,
				"mark_price":       0.045,
				"open_interest":    1200.0,
				"greeks": map[string]interface{}{
					"delta": 0.55,
					"gamma": 0.00001,
					"vega":  120.0,
					"theta": -50.0,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	c := newTestClient(t, handler)
	tk, err := c.GetTicker(context.Background(), "BTC-28MAR25-80000-C")

	require.NoError(t, err)
	assert.Equal(t, "BTC-28MAR25-80000-C", tk.InstrumentName)
	assert.InDelta(t, 0.55, tk.MarkIV, 0.001)  // 55% -> 0.55
	assert.InDelta(t, 0.54, tk.BidIV, 0.001)
	assert.InDelta(t, 0.56, tk.AskIV, 0.001)
	assert.InDelta(t, 0.55, tk.Delta, 0.001)
	assert.InDelta(t, 82000.0, tk.UnderlyingPrice, 0.01)
	assert.InDelta(t, 1200.0, tk.OpenInterest, 0.01)
}

func TestGetInstruments_RPCError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]interface{}{
			"error": map[string]interface{}{
				"code":    10001,
				"message": "invalid currency",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	c := newTestClient(t, handler)
	_, err := c.GetInstruments(context.Background(), "INVALID")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rpc error")
}

func TestRetryOn429(t *testing.T) {
	attempts := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("rate limited"))
			return
		}
		resp := map[string]interface{}{
			"result": []map[string]interface{}{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	c := newTestClient(t, handler)
	instruments, err := c.GetInstruments(context.Background(), "BTC")

	require.NoError(t, err)
	assert.Empty(t, instruments)
	assert.Equal(t, 3, attempts)
}

func TestRetryExhausted(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	})

	c := newTestClient(t, handler)
	_, err := c.GetInstruments(context.Background(), "BTC")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exhausted retries")
}
