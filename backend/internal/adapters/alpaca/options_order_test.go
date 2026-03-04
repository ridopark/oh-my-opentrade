package alpaca

import (
"context"
"encoding/json"
"io"
"net/http"
"net/http/httptest"
"testing"

"github.com/google/uuid"
"github.com/rs/zerolog"
"github.com/oh-my-opentrade/backend/internal/domain"
"github.com/stretchr/testify/assert"
"github.com/stretchr/testify/require"
)

func makeOptionIntent(t *testing.T, dir domain.Direction) domain.OrderIntent {
	t.Helper()
	inst, err := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL270119C00190000", "AAPL")
	require.NoError(t, err)
	id := uuid.New()
	intent, err := domain.NewOptionOrderIntent(
		id, "tenant1", domain.EnvModePaper,
		inst, dir,
		3.20, 5.0,
		"test", "test", 0.8, "key-"+id.String(),
		200.0,
	)
	require.NoError(t, err)
	return intent
}

// ─────────────────────────────────────────────
// SubmitOptionOrder — happy path
// ─────────────────────────────────────────────

func TestRESTClient_SubmitOptionOrder_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/orders", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &req))

		// option order fields
		assert.Equal(t, "AAPL270119C00190000", req["symbol"])
		assert.Equal(t, "5", req["qty"])
		assert.Equal(t, "buy", req["side"])
		assert.Equal(t, "limit", req["type"])
		assert.Equal(t, "day", req["time_in_force"])
		assert.Equal(t, 3.20, req["limit_price"])
		// no stop_price
		_, hasStop := req["stop_price"]
		assert.False(t, hasStop, "option orders must not include stop_price")

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "opt-order-abc123"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	intent := makeOptionIntent(t, domain.DirectionLong)
	orderID, err := client.SubmitOptionOrder(context.Background(), intent)
	require.NoError(t, err)
	assert.Equal(t, "opt-order-abc123", orderID)
}

// ─────────────────────────────────────────────
// SubmitOptionOrder — short direction error
// ─────────────────────────────────────────────

func TestRESTClient_SubmitOptionOrder_ShortDirection(t *testing.T) {
	limiter := NewRateLimiter(200)
	client := NewRESTClient("http://localhost:9999", "test-key", "test-secret", limiter, zerolog.Nop())

	// Build intent manually because NewOptionOrderIntent validates direction
	inst, _ := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL270119C00190000", "AAPL")
	intent := domain.OrderIntent{
		Instrument: &inst,
		Symbol:     "AAPL270119C00190000",
		Direction:  domain.DirectionShort,
		LimitPrice: 3.20,
		StopLoss:   3.20,
		Quantity:   5.0,
		MaxLossUSD: 200.0,
	}

	_, err := client.SubmitOptionOrder(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MVP does not support selling options")
}

// ─────────────────────────────────────────────
// SubmitOptionOrder — non-2xx response
// ─────────────────────────────────────────────

func TestRESTClient_SubmitOptionOrder_Non2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message": "insufficient buying power"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	client := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())

	intent := makeOptionIntent(t, domain.DirectionLong)
	_, err := client.SubmitOptionOrder(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "422")
}

// ─────────────────────────────────────────────
// Adapter.SubmitOrder — option path dispatches to SubmitOptionOrder
// ─────────────────────────────────────────────

func TestAdapter_SubmitOrder_OptionDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)
		// should be "day" for options
		assert.Equal(t, "day", req["time_in_force"])
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "opt-dispatch-id"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	rest := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	adapter := &Adapter{rest: rest, ws: nil}

	intent := makeOptionIntent(t, domain.DirectionLong)
	orderID, err := adapter.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	assert.Equal(t, "opt-dispatch-id", orderID)
}

// ─────────────────────────────────────────────
// Adapter.SubmitOrder — nil instrument uses equity path (gtc)
// ─────────────────────────────────────────────

func TestAdapter_SubmitOrder_EquityPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)
		// equity path uses "gtc"
		assert.Equal(t, "gtc", req["time_in_force"])
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "equity-order-id"}`))
	}))
	defer server.Close()

	limiter := NewRateLimiter(200)
	rest := NewRESTClient(server.URL, "test-key", "test-secret", limiter, zerolog.Nop())
	adapter := &Adapter{rest: rest, ws: nil}

	sym, _ := domain.NewSymbol("AAPL")
	id := uuid.New()
	intent, _ := domain.NewOrderIntent(
		id, "tenant1", domain.EnvModePaper,
		sym, domain.DirectionLong,
		150.0, 145.0, 10, 10.0,
		"test", "test", 0.8, "key-eq-1",
	)
	orderID, err := adapter.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	assert.Equal(t, "equity-order-id", orderID)
}
