package hyperliquid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestWSServer creates a WebSocket test server that sends the given messages
// on connect, then closes.
func newTestWSServer(t *testing.T, msgs []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("ws accept error: %v", err)
			return
		}
		defer conn.CloseNow()

		// Read subscription messages (ignore them).
		ctx := r.Context()
		go func() {
			for {
				_, _, err := conn.Read(ctx)
				if err != nil {
					return
				}
			}
		}()

		// Send test messages.
		for _, msg := range msgs {
			if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
				return
			}
		}
		// Keep connection open briefly so messages can be read.
		time.Sleep(100 * time.Millisecond)
		conn.Close(websocket.StatusNormalClosure, "done")
	}))
}

func TestWSSubscriber_DispatchTrades(t *testing.T) {
	tradeMsg := `{
		"channel": "trades",
		"data": [
			{"coin": "BTC", "side": "B", "px": "50000.0", "sz": "0.5", "time": 1700000000000, "hash": "0xabc"}
		]
	}`

	srv := newTestWSServer(t, []string{tradeMsg})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws := NewWSSubscriber(wsURL, "0xtest", zerolog.Nop())
	ws.SubscribeTrades("BTC")

	var mu sync.Mutex
	var received []WSTrade
	ws.OnTrade(func(trade WSTrade) {
		mu.Lock()
		received = append(received, trade)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run in a goroutine; it will reconnect after server closes.
	go func() { _ = ws.Run(ctx) }()

	// Wait for the trade to be dispatched.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "BTC", received[0].Coin)
	assert.Equal(t, "B", received[0].Side)
	assert.InDelta(t, 50000.0, received[0].Price, 0.01)
	assert.InDelta(t, 0.5, received[0].Size, 0.001)
	assert.Equal(t, "0xabc", received[0].Hash)
}

func TestWSSubscriber_DispatchL2Book(t *testing.T) {
	bookMsg := `{
		"channel": "l2Book",
		"data": {
			"coin": "ETH",
			"time": 1700000000000,
			"levels": [
				[{"px": "3000.0", "sz": "10.0", "n": 5}, {"px": "2999.0", "sz": "20.0", "n": 3}],
				[{"px": "3001.0", "sz": "8.0", "n": 4}, {"px": "3002.0", "sz": "15.0", "n": 2}]
			]
		}
	}`

	srv := newTestWSServer(t, []string{bookMsg})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws := NewWSSubscriber(wsURL, "0xtest", zerolog.Nop())
	ws.SubscribeL2Book("ETH")

	var mu sync.Mutex
	var received []WSL2Book
	ws.OnL2Book(func(book WSL2Book) {
		mu.Lock()
		received = append(received, book)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = ws.Run(ctx) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	book := received[0]
	assert.Equal(t, "ETH", book.Coin)
	require.Len(t, book.Bids, 2)
	require.Len(t, book.Asks, 2)
	assert.InDelta(t, 3000.0, book.Bids[0].Price, 0.01)
	assert.InDelta(t, 10.0, book.Bids[0].Size, 0.01)
	assert.Equal(t, 5, book.Bids[0].Count)
	assert.InDelta(t, 3001.0, book.Asks[0].Price, 0.01)
}

func TestWSSubscriber_DispatchFunding(t *testing.T) {
	fundingMsg := `{
		"channel": "activeAssetCtx",
		"data": {
			"coin": "BTC",
			"ctx": {
				"funding": "0.00015",
				"markPx": "50000.0",
				"openInterest": "1200",
				"oraclePx": "50010",
				"premium": "0.0001"
			}
		}
	}`

	srv := newTestWSServer(t, []string{fundingMsg})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws := NewWSSubscriber(wsURL, "0xtest", zerolog.Nop())
	ws.SubscribeFunding("BTC")

	var mu sync.Mutex
	var received []WSFunding
	ws.OnFunding(func(f WSFunding) {
		mu.Lock()
		received = append(received, f)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = ws.Run(ctx) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "BTC", received[0].Coin)
	assert.InDelta(t, 0.00015, received[0].Rate, 1e-8)
	assert.InDelta(t, 0.0001, received[0].Premium, 1e-8)
}

func TestWSSubscriber_DispatchUserEvents(t *testing.T) {
	userMsg := `{
		"channel": "userEvents",
		"data": {
			"fills": [
				{"coin":"BTC","side":"B","px":"50000.0","sz":"0.1","time":1700000000000,"oid":12345,"hash":"0xfill","startPosition":"0.0","closedPnl":"0","fee":"1.25"}
			]
		}
	}`

	srv := newTestWSServer(t, []string{userMsg})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws := NewWSSubscriber(wsURL, "0xtest", zerolog.Nop())
	ws.SubscribeUserEvents()

	var mu sync.Mutex
	var received []WSUserEvent
	ws.OnUserEvent(func(evt WSUserEvent) {
		mu.Lock()
		received = append(received, evt)
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = ws.Run(ctx) }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received[0].Fills, 1)
	fill := received[0].Fills[0]
	assert.Equal(t, "BTC", fill.Coin)
	assert.Equal(t, "B", fill.Side)
	assert.InDelta(t, 50000.0, fill.Price, 0.01)
	assert.InDelta(t, 0.1, fill.Size, 0.001)
	assert.Equal(t, int64(12345), fill.OID)
	assert.InDelta(t, 1.25, fill.Fee, 0.01)
}

func TestWSSubscriber_IgnoresUnknownChannel(t *testing.T) {
	msg := `{"channel": "unknown_channel", "data": {}}`
	srv := newTestWSServer(t, []string{msg})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws := NewWSSubscriber(wsURL, "0xtest", zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	// Should not panic or error on unknown channels.
	_ = ws.Run(ctx)
}

func TestWSSubscriber_IgnoresPong(t *testing.T) {
	msg := `{"method": "pong"}`
	srv := newTestWSServer(t, []string{msg})
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws := NewWSSubscriber(wsURL, "0xtest", zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	// Should not panic on pong messages.
	_ = ws.Run(ctx)
}

func TestDispatch_RawParsing(t *testing.T) {
	ws := NewWSSubscriber("ws://unused", "0xtest", zerolog.Nop())

	// Test that dispatch handles malformed JSON gracefully.
	ws.dispatch([]byte(`not json`))
	ws.dispatch([]byte(`{}`))
	ws.dispatch([]byte(`{"channel":"trades","data":"bad"}`))

	// Register a handler that tracks calls.
	var called bool
	ws.OnTrade(func(trade WSTrade) {
		called = true
	})

	// Valid trade data.
	validTrade, _ := json.Marshal(wsEnvelope{
		Channel: "trades",
		Data:    json.RawMessage(`[{"coin":"BTC","side":"B","px":"100","sz":"1","time":1000,"hash":"0x1"}]`),
	})
	ws.dispatch(validTrade)
	assert.True(t, called)
}
