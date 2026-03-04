package alpaca

import (
"context"
"net/http"
"net/http/httptest"
"testing"
"time"

"github.com/rs/zerolog"
"github.com/oh-my-opentrade/backend/internal/domain"
"github.com/stretchr/testify/assert"
"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────
// FormatOCCSymbol helper
// ─────────────────────────────────────────────

func TestFormatOCCSymbol_Call(t *testing.T) {
	result := FormatOCCSymbol("AAPL", time.Date(2024, 1, 19, 0, 0, 0, 0, time.UTC), domain.OptionRightCall, 190.0)
	assert.Equal(t, "AAPL240119C00190000", result)
}

func TestFormatOCCSymbol_Put(t *testing.T) {
	result := FormatOCCSymbol("MSFT", time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC), domain.OptionRightPut, 375.5)
	assert.Equal(t, "MSFT240315P00375500", result)
}

// ─────────────────────────────────────────────
// GetOptionChain
// ─────────────────────────────────────────────

func TestGetOptionChain_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/v2/options/contracts")
		assert.Equal(t, "AAPL", r.URL.Query().Get("underlying_symbols"))
		assert.Equal(t, "call", r.URL.Query().Get("type"))

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"snapshots": {
				"AAPL270119C00190000": {
					"greeks": {"delta": 0.52, "gamma": 0.04, "theta": -0.12, "vega": 0.18, "rho": 0.03},
					"impliedVolatility": 0.32,
					"latestQuote": {"bp": 3.10, "ap": 3.20, "c": 3.15}
				},
				"AAPL270119C00195000": {
					"greeks": {"delta": 0.45, "gamma": 0.04, "theta": -0.10, "vega": 0.17, "rho": 0.02},
					"impliedVolatility": 0.30,
					"latestQuote": {"bp": 2.50, "ap": 2.60, "c": 2.55}
				},
				"AAPL270119C00200000": {
					"greeks": {"delta": 0.38, "gamma": 0.03, "theta": -0.09, "vega": 0.16, "rho": 0.02},
					"impliedVolatility": 0.28,
					"latestQuote": {"bp": 2.00, "ap": 2.10, "c": 2.05}
				}
			}
		}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	chain, err := client.GetOptionChain(context.Background(), sym, expiry, domain.OptionRightCall)

	require.NoError(t, err)
	assert.Len(t, chain, 3)
}

func TestGetOptionChain_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"snapshots": {}}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	chain, err := client.GetOptionChain(context.Background(), sym, expiry, domain.OptionRightCall)

	require.NoError(t, err)
	assert.Empty(t, chain)
}

func TestGetOptionChain_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message": "internal error"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	_, err := client.GetOptionChain(context.Background(), sym, expiry, domain.OptionRightCall)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestGetOptionChain_FilterByRight(t *testing.T) {
	// Verify that "call" is sent when OptionRightCall is passed
	var capturedType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedType = r.URL.Query().Get("type")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"snapshots": {}}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")

	_, err := client.GetOptionChain(context.Background(), sym, expiry, domain.OptionRightCall)
	require.NoError(t, err)
	assert.Equal(t, "call", capturedType)

	_, err = client.GetOptionChain(context.Background(), sym, expiry, domain.OptionRightPut)
	require.NoError(t, err)
	assert.Equal(t, "put", capturedType)
}

func TestGetOptionChain_EmptyUnderlying(t *testing.T) {
	limiter := NewRateLimiter(200)
	client := NewRESTClient("http://localhost:9999", "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	_, err := client.GetOptionChain(context.Background(), domain.Symbol(""), expiry, domain.OptionRightCall)

	require.Error(t, err)
}
