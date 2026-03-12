package alpaca

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatOCCSymbol_Call(t *testing.T) {
	result := FormatOCCSymbol("AAPL", time.Date(2024, 1, 19, 0, 0, 0, 0, time.UTC), domain.OptionRightCall, 190.0)
	assert.Equal(t, "AAPL240119C00190000", result)
}

func TestFormatOCCSymbol_Put(t *testing.T) {
	result := FormatOCCSymbol("MSFT", time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC), domain.OptionRightPut, 375.5)
	assert.Equal(t, "MSFT240315P00375500", result)
}

// makeOptionChainServers creates two test HTTP servers that together simulate the
// Alpaca broker API (contract listing) and data API (snapshots with greeks).
// Returns brokerServer, dataServer — caller must close both.
func makeOptionChainServers(t *testing.T, contractsJSON, snapshotsJSON string) (*httptest.Server, *httptest.Server) {
	t.Helper()

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/v2/options/contracts")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(contractsJSON))
	}))

	dataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/v1beta1/options/snapshots")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(snapshotsJSON))
	}))

	return brokerServer, dataServer
}

func TestGetOptionChain_HappyPath(t *testing.T) {
	contractsJSON := `{
		"option_contracts": [
			{"symbol": "AAPL270119C00190000", "underlying_symbol": "AAPL", "expiration_date": "2027-01-19",
			 "strike_price": "190", "type": "call", "style": "american", "multiplier": "100",
			 "open_interest": "500", "tradable": true, "status": "active"},
			{"symbol": "AAPL270119C00195000", "underlying_symbol": "AAPL", "expiration_date": "2027-01-19",
			 "strike_price": "195", "type": "call", "style": "american", "multiplier": "100",
			 "open_interest": "300", "tradable": true, "status": "active"},
			{"symbol": "AAPL270119C00200000", "underlying_symbol": "AAPL", "expiration_date": "2027-01-19",
			 "strike_price": "200", "type": "call", "style": "american", "multiplier": "100",
			 "open_interest": "200", "tradable": true, "status": "active"}
		],
		"next_page_token": null
	}`

	snapshotsJSON := `{
		"snapshots": {
			"AAPL270119C00190000": {
				"greeks": {"delta": 0.52, "gamma": 0.04, "theta": -0.12, "vega": 0.18, "rho": 0.03},
				"impliedVolatility": 0.32,
				"latestQuote": {"bp": 3.10, "ap": 3.20, "c": 3.15},
				"openInterest": 500
			},
			"AAPL270119C00195000": {
				"greeks": {"delta": 0.45, "gamma": 0.04, "theta": -0.10, "vega": 0.17, "rho": 0.02},
				"impliedVolatility": 0.30,
				"latestQuote": {"bp": 2.50, "ap": 2.60, "c": 2.55},
				"openInterest": 300
			},
			"AAPL270119C00200000": {
				"greeks": {"delta": 0.38, "gamma": 0.03, "theta": -0.09, "vega": 0.16, "rho": 0.02},
				"impliedVolatility": 0.28,
				"latestQuote": {"bp": 2.00, "ap": 2.10, "c": 2.05},
				"openInterest": 200
			}
		},
		"next_page_token": null
	}`

	brokerServer, dataServer := makeOptionChainServers(t, contractsJSON, snapshotsJSON)
	defer brokerServer.Close()
	defer dataServer.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(brokerServer.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	chain, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, domain.OptionRightCall)

	require.NoError(t, err)
	assert.Len(t, chain, 3)
	for _, c := range chain {
		assert.NotZero(t, c.Bid)
		assert.NotZero(t, c.Ask)
		assert.NotZero(t, c.Delta)
	}
}

func TestGetOptionChain_EmptyContractList(t *testing.T) {
	contractsJSON := `{"option_contracts": [], "next_page_token": null}`
	snapshotsJSON := `{"snapshots": {}, "next_page_token": null}`

	brokerServer, dataServer := makeOptionChainServers(t, contractsJSON, snapshotsJSON)
	defer brokerServer.Close()
	defer dataServer.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(brokerServer.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	chain, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, domain.OptionRightCall)

	require.NoError(t, err)
	assert.Empty(t, chain)
}

func TestGetOptionChain_BrokerHTTPError(t *testing.T) {
	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message": "internal error"}`))
	}))
	defer brokerServer.Close()

	dataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"snapshots": {}}`))
	}))
	defer dataServer.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(brokerServer.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	_, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, domain.OptionRightCall)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestGetOptionChain_FilterByRight(t *testing.T) {
	var capturedType string
	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedType = r.URL.Query().Get("type")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"option_contracts": [], "next_page_token": null}`))
	}))
	defer brokerServer.Close()

	dataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"snapshots": {}}`))
	}))
	defer dataServer.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(brokerServer.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")

	_, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, domain.OptionRightCall)
	require.NoError(t, err)
	assert.Equal(t, "call", capturedType)

	_, err = client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, domain.OptionRightPut)
	require.NoError(t, err)
	assert.Equal(t, "put", capturedType)
}

func TestGetOptionChain_EmptyUnderlying(t *testing.T) {
	limiter := NewRateLimiter(200)
	client := NewRESTClient("http://localhost:9999", "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	_, err := client.GetOptionChain(context.Background(), "http://localhost:9998", domain.Symbol(""), expiry, domain.OptionRightCall)

	require.Error(t, err)
}

func TestGetOptionChain_SkipsNonTradableContracts(t *testing.T) {
	contractsJSON := `{
		"option_contracts": [
			{"symbol": "AAPL270119C00190000", "underlying_symbol": "AAPL", "expiration_date": "2027-01-19",
			 "strike_price": "190", "type": "call", "style": "american", "multiplier": "100",
			 "open_interest": "500", "tradable": true, "status": "active"},
			{"symbol": "AAPL270119C00195000", "underlying_symbol": "AAPL", "expiration_date": "2027-01-19",
			 "strike_price": "195", "type": "call", "style": "american", "multiplier": "100",
			 "open_interest": "0", "tradable": false, "status": "inactive"}
		],
		"next_page_token": null
	}`
	snapshotsJSON := `{
		"snapshots": {
			"AAPL270119C00190000": {
				"greeks": {"delta": 0.52, "gamma": 0.04, "theta": -0.12, "vega": 0.18, "rho": 0.03},
				"impliedVolatility": 0.32,
				"latestQuote": {"bp": 3.10, "ap": 3.20, "c": 3.15},
				"openInterest": 500
			}
		},
		"next_page_token": null
	}`

	brokerServer, dataServer := makeOptionChainServers(t, contractsJSON, snapshotsJSON)
	defer brokerServer.Close()
	defer dataServer.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(brokerServer.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	chain, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, domain.OptionRightCall)

	require.NoError(t, err)
	assert.Len(t, chain, 1)
	assert.Equal(t, domain.Symbol("AAPL270119C00190000"), chain[0].ContractSymbol)
}
