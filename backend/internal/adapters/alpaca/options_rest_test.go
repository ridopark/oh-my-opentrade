package alpaca

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
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
	chain, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, expiry, domain.OptionRightCall)

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
	chain, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, expiry, domain.OptionRightCall)

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
	_, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, expiry, domain.OptionRightCall)

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

	_, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, expiry, domain.OptionRightCall)
	require.NoError(t, err)
	assert.Equal(t, "call", capturedType)

	_, err = client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, expiry, domain.OptionRightPut)
	require.NoError(t, err)
	assert.Equal(t, "put", capturedType)
}

func TestGetOptionChain_EmptyUnderlying(t *testing.T) {
	limiter := NewRateLimiter(200)
	client := NewRESTClient("http://localhost:9999", "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	_, err := client.GetOptionChain(context.Background(), "http://localhost:9998", domain.Symbol(""), expiry, expiry, domain.OptionRightCall)

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
	chain, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, expiry, domain.OptionRightCall)

	require.NoError(t, err)
	assert.Len(t, chain, 1)
	assert.Equal(t, domain.Symbol("AAPL270119C00190000"), chain[0].ContractSymbol)
}

// TestGetOptionChain_RetryOnTransientStatus verifies that the snapshot fetch
// path retries on transient HTTP 5xx responses and ultimately succeeds.
// Counterpart to the production bug: a single TCP RST on data.alpaca.markets
// dropped the AFRM signal upstream of the broker. The same retry path covers
// 502/503/504; this test exercises the 503 branch deterministically.
func TestGetOptionChain_RetryOnTransientStatus(t *testing.T) {
	contractsJSON := `{
		"option_contracts": [
			{"symbol": "AAPL270119C00190000", "underlying_symbol": "AAPL", "expiration_date": "2027-01-19",
			 "strike_price": "190", "type": "call", "style": "american", "multiplier": "100",
			 "open_interest": "500", "tradable": true, "status": "active"}
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

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(contractsJSON))
	}))
	defer brokerServer.Close()

	var calls atomic.Int32
	dataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error": "service unavailable"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(snapshotsJSON))
	}))
	defer dataServer.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(brokerServer.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	chain, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, expiry, domain.OptionRightCall)

	require.NoError(t, err)
	require.Len(t, chain, 1)
	assert.Equal(t, int32(3), calls.Load(), "expected 2 retries before 3rd-attempt success")
}

// TestGetOptionChain_GivesUpAfterMaxRetries verifies the helper does not retry
// indefinitely: on persistent transient failures it returns the final error
// after the configured backoff schedule (3 retries / 4 attempts).
func TestGetOptionChain_GivesUpAfterMaxRetries(t *testing.T) {
	contractsJSON := `{
		"option_contracts": [
			{"symbol": "AAPL270119C00190000", "underlying_symbol": "AAPL", "expiration_date": "2027-01-19",
			 "strike_price": "190", "type": "call", "style": "american", "multiplier": "100",
			 "open_interest": "500", "tradable": true, "status": "active"}
		],
		"next_page_token": null
	}`

	brokerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(contractsJSON))
	}))
	defer brokerServer.Close()

	var calls atomic.Int32
	dataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error": "bad gateway"}`))
	}))
	defer dataServer.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(brokerServer.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	expiry := time.Date(2027, 1, 19, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	_, err := client.GetOptionChain(context.Background(), dataServer.URL, sym, expiry, expiry, domain.OptionRightCall)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
	assert.Equal(t, int32(len(transientRetryBackoffs)+1), calls.Load(), "expected attempts = backoffs + 1")
}

// TestIsTransientError covers the network-error classification for the cases
// the production bug surfaced: ECONNRESET and io.EOF wrapped inside net.OpError.
func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"econnreset bare", syscall.ECONNRESET, true},
		{"io.EOF", io.EOF, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"net.OpError wrapped", &net.OpError{Op: "read", Err: errors.New("read tcp: connection reset by peer")}, true},
		{"unrelated error", errors.New("unparseable response"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTransientError(tt.err))
		})
	}
}

// TestRetryTransient_RespectsContextCancel verifies the helper aborts cleanly
// when the caller's context is cancelled mid-retry-wait, returning ctx.Err().
func TestRetryTransient_RespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	fn := func() (*http.Response, error) {
		calls.Add(1)
		// Cancel the context after the first call so the retry-wait aborts.
		cancel()
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody}, nil
	}

	_, err := retryTransient(ctx, zerolog.Nop(), "test_op", fn)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "expected context.Canceled, got %v", err)
	assert.Equal(t, int32(1), calls.Load(), "should not have retried after ctx cancel")
}

// --- ListOptionContractsAsOf ---

func TestListOptionContractsAsOf_PaginatedAndPastDateActiveQuirk(t *testing.T) {
	// Two pages. The second page returns a contract whose expiry already
	// passed (status=active is the Alpaca-API quirk for past-listed
	// contracts) — proves we don't filter on tradable/status and instead
	// pass through whatever the API surfaces.
	page1 := `{
		"option_contracts": [
			{"symbol": "AAPL240119C00190000", "underlying_symbol": "AAPL", "expiration_date": "2024-01-19",
			 "strike_price": "190", "type": "call", "style": "american", "multiplier": "100",
			 "open_interest": "500", "tradable": false, "status": "active"}
		],
		"next_page_token": "PAGE2"
	}`
	page2 := `{
		"option_contracts": [
			{"symbol": "AAPL240119P00185000", "underlying_symbol": "AAPL", "expiration_date": "2024-01-19",
			 "strike_price": "185", "type": "put", "style": "american", "multiplier": "100",
			 "open_interest": "200", "tradable": false, "status": "active"}
		],
		"next_page_token": null
	}`

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/v2/options/contracts")
		// expiration_date_gte must equal asOf, expiration_date_lte must
		// equal asOf+dteRange — pin the URL contract here.
		q := r.URL.Query()
		assert.Equal(t, "2024-01-15", q.Get("expiration_date_gte"))
		assert.Equal(t, "2024-03-15", q.Get("expiration_date_lte"))
		assert.Equal(t, "active", q.Get("status"))

		w.WriteHeader(http.StatusOK)
		switch hits.Add(1) {
		case 1:
			assert.Empty(t, q.Get("page_token"), "first request must not carry a page_token")
			io.WriteString(w, page1)
		case 2:
			assert.Equal(t, "PAGE2", q.Get("page_token"), "second request must echo prior next_page_token")
			io.WriteString(w, page2)
		default:
			t.Fatalf("unexpected third request — pagination didn't terminate")
		}
	}))
	defer srv.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(srv.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	asOf := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	sym, _ := domain.NewSymbol("AAPL")
	contracts, err := client.ListOptionContractsAsOf(context.Background(), sym, asOf, 60)
	require.NoError(t, err)
	require.Len(t, contracts, 2)
	assert.Equal(t, domain.Symbol("AAPL240119C00190000"), contracts[0].ContractSymbol)
	assert.Equal(t, domain.OptionRightCall, contracts[0].Right)
	assert.Equal(t, 190.0, contracts[0].Strike)
	assert.Equal(t, domain.Symbol("AAPL240119P00185000"), contracts[1].ContractSymbol)
	assert.Equal(t, domain.OptionRightPut, contracts[1].Right)
}

func TestListOptionContractsAsOf_EmptyUnderlyingRejected(t *testing.T) {
	limiter := NewRateLimiter(200)
	client := NewRESTClient("http://unused", "test-key", "test-secret", limiter, zerolog.Nop())
	_, err := client.ListOptionContractsAsOf(context.Background(), domain.Symbol(""), time.Now(), 60)
	require.Error(t, err)
}

func TestListOptionContractsAsOf_HTTPErrorBubbles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(srv.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	sym, _ := domain.NewSymbol("AAPL")
	_, err := client.ListOptionContractsAsOf(context.Background(), sym, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), 60)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
}

// --- GetOptionDayBar ---

func TestGetOptionDayBar_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/v1beta1/options/bars")
		q := r.URL.Query()
		assert.Equal(t, "AAPL240119C00190000", q.Get("symbols"))
		assert.Equal(t, "1Day", q.Get("timeframe"))
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{
			"bars": {
				"AAPL240119C00190000": [
					{"t": "2024-01-15T00:00:00Z", "o": 3.10, "h": 3.40, "l": 3.05, "c": 3.25, "v": 1234}
				]
			}
		}`)
	}))
	defer srv.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(srv.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	bar, err := client.GetOptionDayBar(
		context.Background(),
		srv.URL,
		domain.Symbol("AAPL240119C00190000"),
		time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)
	require.NotNil(t, bar)
	assert.Equal(t, 3.25, bar.Close)
	assert.Equal(t, domain.Timeframe("1d"), bar.Timeframe)
}

func TestGetOptionDayBar_NoBarReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"bars": {}}`)
	}))
	defer srv.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(srv.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	bar, err := client.GetOptionDayBar(
		context.Background(),
		srv.URL,
		domain.Symbol("AAPL240119C00190000"),
		time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)
	assert.Nil(t, bar, "no published bar must surface as nil, not a zero-valued bar")
}

func TestGetOptionDayBar_EmptyOCCRejected(t *testing.T) {
	limiter := NewRateLimiter(200)
	client := NewRESTClient("http://unused", "test-key", "test-secret", limiter, zerolog.Nop())
	_, err := client.GetOptionDayBar(context.Background(), "http://unused", domain.Symbol(""), time.Now())
	require.Error(t, err)
}

func TestGetOptionDayBars_ChunksAt100AndMergesResponses(t *testing.T) {
	// Build 250 OCC symbols. Server should see exactly 3 hits: 100 + 100 + 50.
	occs := make([]domain.Symbol, 250)
	for i := range occs {
		occs[i] = domain.Symbol(fmt.Sprintf("AAPL240119C%08d", (i+1)*1000))
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		assert.Contains(t, r.URL.Path, "/v1beta1/options/bars")
		q := r.URL.Query()
		assert.Equal(t, "1Day", q.Get("timeframe"))
		got := q.Get("symbols")
		// Echo a bar for the first symbol of the batch only — proves the
		// merge step keys by symbol from the response, not by request order.
		first := strings.SplitN(got, ",", 2)[0]
		nSymbols := len(strings.Split(got, ","))
		assert.LessOrEqual(t, nSymbols, 100, "no batch may exceed 100 OCCs per call")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"bars": {"%s": [{"t":"2024-01-15T00:00:00Z","o":1.0,"h":2.0,"l":0.5,"c":1.5,"v":100}]}}`, first)
	}))
	defer srv.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(srv.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	bars, err := client.GetOptionDayBars(context.Background(), srv.URL, occs, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, int32(3), hits.Load(), "250 OCCs / 100 per batch = 3 round trips")
	// 3 batches * 1 echoed-back bar each = 3 entries in the merged map.
	assert.Len(t, bars, 3)
}

func TestGetOptionDayBars_EmptyInputReturnsEmptyMap(t *testing.T) {
	limiter := NewRateLimiter(200)
	client := NewRESTClient("http://unused", "test-key", "test-secret", limiter, zerolog.Nop())
	bars, err := client.GetOptionDayBars(context.Background(), "http://unused", nil, time.Now())
	require.NoError(t, err)
	assert.Empty(t, bars)
}

func TestGetOptionDayBars_PartialMissingBarsAreOmittedFromMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Two requested OCCs, server only knows about one.
		io.WriteString(w, `{
			"bars": {
				"AAPL240119C00190000": [{"t":"2024-01-15T00:00:00Z","o":3.10,"h":3.40,"l":3.05,"c":3.25,"v":1234}]
			}
		}`)
	}))
	defer srv.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(srv.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	bars, err := client.GetOptionDayBars(context.Background(), srv.URL,
		[]domain.Symbol{"AAPL240119C00190000", "AAPL240119C00999000"},
		time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Len(t, bars, 1)
	require.NotNil(t, bars["AAPL240119C00190000"])
	assert.Equal(t, 3.25, bars["AAPL240119C00190000"].Close)
	assert.Nil(t, bars["AAPL240119C00999000"], "missing keys must read nil rather than a zero-valued bar")
}

func TestGetOptionDayBar_HTTPErrorBubbles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":"unavailable"}`)
	}))
	defer srv.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(srv.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	_, err := client.GetOptionDayBar(
		context.Background(),
		srv.URL,
		domain.Symbol("AAPL240119C00190000"),
		time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
	)
	require.Error(t, err)
}
