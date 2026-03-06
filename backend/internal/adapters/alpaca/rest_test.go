package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestRESTClient_SubmitOrder_Success(t *testing.T) {
	// Arrange
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/orders", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "test-key", r.Header.Get("APCA-API-KEY-ID"))
		assert.Equal(t, "test-secret", r.Header.Get("APCA-API-SECRET-KEY"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req map[string]interface{}
		err = json.Unmarshal(body, &req)
		require.NoError(t, err)

		assert.Equal(t, "AAPL", req["symbol"])
		// Alpaca accepts qty as string
		assert.Equal(t, "10", req["qty"])
		assert.Equal(t, "buy", req["side"])
		assert.Equal(t, "stop_limit", req["type"])
		assert.Equal(t, "gtc", req["time_in_force"])
		assert.Equal(t, 150.0, req["limit_price"])
		assert.Equal(t, 145.0, req["stop_price"])

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "order-uuid-123", "status": "new", "symbol": "AAPL", "qty": "10", "side": "buy", "type": "limit", "time_in_force": "gtc"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	sym, _ := domain.NewSymbol("AAPL")
	dir, _ := domain.NewDirection("LONG")
	intent, _ := domain.NewOrderIntent(
		uuid.New(), "tenant-1", domain.EnvModePaper,
		sym, dir, 150.0, 145.0, 10, 10.0, "strat", "rat", 0.9, "idempotent-key",
	)

	// Act
	orderID, err := client.SubmitOrder(context.Background(), intent)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "order-uuid-123", orderID)
}

func TestRESTClient_SubmitOrder_RoundsSubPennyPrices(t *testing.T) {
	// Arrange — verify sub-penny prices are rounded to 2 decimal places
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req map[string]interface{}
		err = json.Unmarshal(body, &req)
		require.NoError(t, err)

		// Sub-penny prices must be rounded to 2 decimal places
		assert.Equal(t, 260.25, req["limit_price"])
		assert.Equal(t, 255.16, req["stop_price"])

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "rounded-order-123"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	// Use sub-penny prices that Alpaca would reject
	intent := domain.OrderIntent{
		Symbol:     "AAPL",
		Direction:  domain.DirectionLong,
		Quantity:   10,
		LimitPrice: 260.25005999999996, // actual sub-penny value from production
		StopLoss:   255.1599,           // another sub-penny value
	}

	// Act
	orderID, err := client.SubmitOrder(context.Background(), intent)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "rounded-order-123", orderID)
}

func TestRESTClient_SubmitOrder_LimitNoStopPrice(t *testing.T) {
	// Arrange — verify limit orders (StopLoss=0) do NOT include stop_price
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req map[string]interface{}
		err = json.Unmarshal(body, &req)
		require.NoError(t, err)

		assert.Equal(t, "limit", req["type"])
		_, hasStopPrice := req["stop_price"]
		assert.False(t, hasStopPrice, "limit orders must not include stop_price")

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "limit-order-123"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	// OrderIntent with StopLoss = 0 (no stop)
	intent := domain.OrderIntent{
		Symbol:     "AAPL",
		Direction:  domain.DirectionLong,
		Quantity:   5,
		LimitPrice: 150.0,
		StopLoss:   0, // no stop
	}

	// Act
	orderID, err := client.SubmitOrder(context.Background(), intent)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "limit-order-123", orderID)
}

func TestRESTClient_SubmitOrder_FractionalQtyUsesDayTIF(t *testing.T) {
	// Arrange — verify fractional quantities use "day" TIF instead of "gtc"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req map[string]interface{}
		err = json.Unmarshal(body, &req)
		require.NoError(t, err)

		// Fractional qty should use "day" TIF
		assert.Equal(t, "10.5", req["qty"])
		assert.Equal(t, "day", req["time_in_force"])

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "fractional-order-123"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	// OrderIntent with fractional quantity
	intent := domain.OrderIntent{
		Symbol:     "AAPL",
		Direction:  domain.DirectionLong,
		Quantity:   10.5,
		LimitPrice: 150.0,
		StopLoss:   0,
	}

	// Act
	orderID, err := client.SubmitOrder(context.Background(), intent)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "fractional-order-123", orderID)
}

func TestRESTClient_SubmitOrder_Error(t *testing.T) {
	// Arrange
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message": "insufficient buying power"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	sym, _ := domain.NewSymbol("AAPL")
	dir, _ := domain.NewDirection("LONG")
	intent, _ := domain.NewOrderIntent(
		uuid.New(), "tenant-1", domain.EnvModePaper,
		sym, dir, 150.0, 145.0, 10, 10.0, "strat", "rat", 0.9, "idempotent-key",
	)

	// Act
	_, err := client.SubmitOrder(context.Background(), intent)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "422")
}

func TestRESTClient_CancelOrder_Success(t *testing.T) {
	// Arrange
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/orders/order-uuid-123", r.URL.Path)
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	// Act
	err := client.CancelOrder(context.Background(), "order-uuid-123")

	// Assert
	require.NoError(t, err)
}

func TestRESTClient_GetOrderStatus_Success(t *testing.T) {
	// Arrange
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/orders/order-uuid-123", r.URL.Path)
		assert.Equal(t, "GET", r.Method)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "order-uuid-123", "status": "filled", "filled_avg_price": "150.25"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	// Act
	status, err := client.GetOrderStatus(context.Background(), "order-uuid-123")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "filled", status)
}

func TestRESTClient_GetPositions_Success(t *testing.T) {
	// Arrange
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/positions", r.URL.Path)
		assert.Equal(t, "GET", r.Method)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"symbol": "AAPL", "qty": "10", "side": "long", "avg_entry_price": "150.00", "current_price": "155.00"}]`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	// Act
	positions, err := client.GetPositions(context.Background(), "tenant-1", domain.EnvModePaper)

	// Assert
	require.NoError(t, err)
	require.Len(t, positions, 1)
	assert.Equal(t, "AAPL", positions[0].Symbol.String())
	assert.Equal(t, 10.0, positions[0].Quantity)
}

func TestRESTClient_GetQuote_Success(t *testing.T) {
	// Arrange
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/stocks/AAPL/quotes/latest", r.URL.Path)
		assert.Equal(t, "iex", r.URL.Query().Get("feed"))
		assert.Equal(t, "GET", r.Method)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"quote": {"bp": 150.10, "ap": 150.20, "bs": 100, "as": 200}}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	sym, _ := domain.NewSymbol("AAPL")

	// Act
	bid, ask, err := client.GetQuote(context.Background(), server.URL, sym)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, 150.10, bid)
	assert.Equal(t, 150.20, ask)
}

func TestRESTClient_GetQuote_Error(t *testing.T) {
	// Arrange
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	sym, _ := domain.NewSymbol("AAPL")

	// Act
	_, _, err := client.GetQuote(context.Background(), server.URL, sym)

	// Assert
	require.Error(t, err)
}

func TestRESTClient_UsesRateLimiter(t *testing.T) {
	// Arrange
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"quote": {"bp": 150.10, "ap": 150.20, "bs": 100, "as": 200}}`))
	}))
	defer server.Close()

	// Rate limiter allows 60 requests per minute -> 1 request per second
	limiter := NewRateLimiter(60)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	sym, _ := domain.NewSymbol("AAPL")
	ctx := context.Background()

	// Consume all burst tokens (60 total limit, burst=60)
	for i := 0; i < 60; i++ {
		err := limiter.Wait(ctx)
		require.NoError(t, err)
	}

	start := time.Now()
	// Next call should wait ~1s
	_, _, err := client.GetQuote(ctx, server.URL, sym)
	require.NoError(t, err)
	duration := time.Since(start)
	assert.GreaterOrEqual(t, duration.Milliseconds(), int64(900), "REST client should block due to rate limit")
}

func TestRESTClient_DoReq_Retry429_SucceedsSecondAttempt(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`"too many requests"`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"quote": {"bp": 150.10, "ap": 150.20}}`))
	}))
	defer server.Close()

	limiter := NewPriorityRateLimiter(10000, 10000)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	resp, err := client.doReqWithOpts(ctx, http.MethodGet, "/v2/account", nil, reqOpts{priority: PriorityTrading, maxRetries: 3})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, attempts)
}

func TestRESTClient_DoReq_Retry429_RespectsMaxRetries(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`"too many requests"`))
	}))
	defer server.Close()

	limiter := NewPriorityRateLimiter(10000, 10000)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	resp, err := client.doReqWithOpts(ctx, http.MethodGet, "/v2/account", nil, reqOpts{priority: PriorityTrading, maxRetries: 1})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, 2, attempts)
}

func TestRESTClient_DoReq_Non429NotRetried(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	limiter := NewPriorityRateLimiter(10000, 10000)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	resp, err := client.doReqWithOpts(ctx, http.MethodGet, "/v2/account", nil, reqOpts{priority: PriorityTrading, maxRetries: 3})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Equal(t, 1, attempts)
}

func TestRESTClient_DoReq_ContextCancellationDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(10*time.Second).Unix()))
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`"too many requests"`))
		cancel()
	}))
	defer server.Close()

	limiter := NewPriorityRateLimiter(10000, 10000)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	resp, err := client.doReqWithOpts(ctx, http.MethodGet, "/v2/account", nil, reqOpts{priority: PriorityTrading, maxRetries: 3})
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
