package alpaca

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------
func mockTradeServer(t *testing.T, handler func(*websocket.Conn)) DialFn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		handler(c)
	}))
	t.Cleanup(srv.Close)
	return func(ctx context.Context, _ string) (*websocket.Conn, error) {
		wsURL := "ws" + srv.URL[len("http"):]
		c, _, err := websocket.Dial(ctx, wsURL, nil)
		return c, err
	}
}

func tsAuth(conn *websocket.Conn, authorized bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, _ = conn.Read(ctx)

	status := "authorized"
	msg := ""
	if !authorized {
		status = "unauthorized"
		msg = "invalid credentials"
	}
	resp, _ := json.Marshal(map[string]any{
		"stream": "authorization",
		"data":   map[string]string{"status": status, "message": msg},
	})
	_ = conn.Write(ctx, websocket.MessageText, resp)
}

func tsSubscribe(conn *websocket.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, _ = conn.Read(ctx)
}

func tsSendUpdate(conn *websocket.Conn, event, orderID, qty, price string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msg, _ := json.Marshal(map[string]any{
		"stream": "trade_updates",
		"data": map[string]any{
			"event":     event,
			"price":     price,
			"qty":       qty,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"order": map[string]string{
				"id":               orderID,
				"client_order_id":  "client-" + orderID,
				"filled_qty":       qty,
				"filled_avg_price": price,
				"status":           event,
				"symbol":           "AAPL",
				"side":             "buy",
			},
		},
	})
	_ = conn.Write(ctx, websocket.MessageText, msg)
}

func tsClient(dialFn DialFn) *TradeStreamClient {
	return &TradeStreamClient{
		apiKey:    "test-key",
		apiSecret: "test-secret",
		log:       zerolog.Nop(),
		dialFn:    dialFn,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestTradeStream_AuthenticateAndReceiveFill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dial := mockTradeServer(t, func(c *websocket.Conn) {
		defer c.CloseNow()
		tsAuth(c, true)
		tsSubscribe(c)
		tsSendUpdate(c, "fill", "order-123", "10", "150.50")
		<-ctx.Done()
	})

	ch := make(chan ports.OrderUpdate, 10)
	go tsClient(dial).Run(ctx, ch)

	select {
	case u := <-ch:
		assert.Equal(t, "order-123", u.BrokerOrderID)
		assert.Equal(t, "fill", u.Event)
		assert.Equal(t, 10.0, u.FilledQty)
		assert.Equal(t, 150.50, u.FilledAvgPrice)
		assert.False(t, u.FilledAt.IsZero(), "FilledAt should be set")
	case <-ctx.Done():
		t.Fatal("timeout waiting for fill update")
	}
}

func TestTradeStream_AuthFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	authAttempted := make(chan struct{}, 5)
	dial := mockTradeServer(t, func(c *websocket.Conn) {
		defer c.CloseNow()
		tsAuth(c, false)
		authAttempted <- struct{}{}
		<-ctx.Done()
	})

	ch := make(chan ports.OrderUpdate, 10)
	done := make(chan struct{})
	go func() {
		tsClient(dial).Run(ctx, ch)
		close(done)
	}()

	select {
	case <-authAttempted:
	case <-ctx.Done():
		t.Fatal("timeout waiting for auth attempt")
	}

	select {
	case u := <-ch:
		t.Fatalf("unexpected update on auth failure: %+v", u)
	default:
	}

	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}

func TestTradeStream_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connected := make(chan struct{}, 1)
	dial := mockTradeServer(t, func(c *websocket.Conn) {
		defer c.CloseNow()
		tsAuth(c, true)
		tsSubscribe(c)
		select {
		case connected <- struct{}{}:
		default:
		}
		<-ctx.Done()
	})

	client := tsClient(dial)
	ch := make(chan ports.OrderUpdate, 10)
	done := make(chan struct{})
	go func() {
		client.Run(ctx, ch)
		close(done)
	}()

	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("timeout waiting for connection")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after context cancel")
}

func TestTradeStream_ParseVariousEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dial := mockTradeServer(t, func(c *websocket.Conn) {
		defer c.CloseNow()
		tsAuth(c, true)
		tsSubscribe(c)
		tsSendUpdate(c, "fill", "ord-1", "100", "150.00")
		tsSendUpdate(c, "partial_fill", "ord-2", "50", "200.00")
		tsSendUpdate(c, "canceled", "ord-3", "0", "0")
		<-ctx.Done()
	})

	ch := make(chan ports.OrderUpdate, 10)
	go tsClient(dial).Run(ctx, ch)

	var got []ports.OrderUpdate
	for i := 0; i < 3; i++ {
		select {
		case u := <-ch:
			got = append(got, u)
		case <-ctx.Done():
			t.Fatalf("timeout after receiving %d of 3 updates", len(got))
		}
	}

	require.Len(t, got, 3)

	assert.Equal(t, "fill", got[0].Event)
	assert.Equal(t, "ord-1", got[0].BrokerOrderID)
	assert.Equal(t, 100.0, got[0].FilledQty)
	assert.Equal(t, 150.0, got[0].FilledAvgPrice)

	assert.Equal(t, "partial_fill", got[1].Event)
	assert.Equal(t, "ord-2", got[1].BrokerOrderID)
	assert.Equal(t, 50.0, got[1].FilledQty)
	assert.Equal(t, 200.0, got[1].FilledAvgPrice)

	assert.Equal(t, "canceled", got[2].Event)
	assert.Equal(t, "ord-3", got[2].BrokerOrderID)
	assert.Equal(t, 0.0, got[2].FilledQty)
	assert.Equal(t, 0.0, got[2].FilledAvgPrice)
}

func TestTradeStream_ReconnectOnDisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var connCount atomic.Int32
	dial := mockTradeServer(t, func(c *websocket.Conn) {
		defer c.CloseNow()
		n := connCount.Add(1)
		tsAuth(c, true)
		tsSubscribe(c)

		switch {
		case n == 1:
			tsSendUpdate(c, "fill", "order-1", "1", "100.00")
			return
		default:
			tsSendUpdate(c, "fill", "order-2", "1", "200.00")
			<-ctx.Done()
		}
	})

	ch := make(chan ports.OrderUpdate, 10)
	go tsClient(dial).Run(ctx, ch)

	select {
	case u := <-ch:
		assert.Equal(t, "order-1", u.BrokerOrderID)
	case <-ctx.Done():
		t.Fatal("timeout waiting for first fill")
	}
	select {
	case u := <-ch:
		assert.Equal(t, "order-2", u.BrokerOrderID)
	case <-ctx.Done():
		t.Fatal("timeout waiting for second fill after reconnect")
	}

	assert.GreaterOrEqual(t, int(connCount.Load()), 2, "client should have reconnected")
}
