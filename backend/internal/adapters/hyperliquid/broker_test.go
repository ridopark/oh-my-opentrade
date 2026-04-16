package hyperliquid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

func newTestBroker(t *testing.T, handler http.Handler) (*Broker, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	cfg := config.HyperliquidConfig{
		Network:    "testnet",
		PrivateKey: testPrivateKey,
	}
	client, err := NewClient(cfg, zerolog.Nop())
	require.NoError(t, err)
	client.baseURL = srv.URL
	client.SetAssetMap(map[string]int{"BTC": 0, "ETH": 1, "SOL": 2})

	rest := NewRESTClient(client)
	ws := NewWSSubscriber(TestnetWSURL, client.Address(), zerolog.Nop())

	b := &Broker{
		client:    client,
		rest:      rest,
		ws:        ws,
		log:       zerolog.Nop(),
		positions: make(map[string]*trackedPosition),
	}
	return b, srv
}

func TestSubmitOrder_Long(t *testing.T) {
	var receivedBody map[string]any

	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "/exchange") {
			_ = json.Unmarshal(body, &receivedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":99999}}]}}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	intent := domain.OrderIntent{
		Symbol:     domain.Symbol("BTC/USD"),
		Direction:  domain.DirectionLong,
		LimitPrice: 50000.0,
		Quantity:   0.1,
		OrderType:  "limit",
		Venue:      domain.VenueHyperliquid,
	}

	oid, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	assert.Equal(t, "99999", oid)

	// Verify the action was sent.
	action, ok := receivedBody["action"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "order", action["type"])
	assert.Equal(t, "na", action["grouping"])
}

func TestSubmitOrder_Market(t *testing.T) {
	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/exchange") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"oid":88888}}]}}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	intent := domain.OrderIntent{
		Symbol:     domain.Symbol("BTC/USD"),
		Direction:  domain.DirectionLong,
		LimitPrice: 50000.0,
		Quantity:   0.05,
		OrderType:  "market",
	}

	oid, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	assert.Equal(t, "88888", oid)
}

func TestSubmitOrder_InsufficientMargin(t *testing.T) {
	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/exchange") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"insufficient margin"}]}}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	intent := domain.OrderIntent{
		Symbol:     domain.Symbol("BTC/USD"),
		Direction:  domain.DirectionLong,
		LimitPrice: 50000.0,
		Quantity:   100.0,
	}

	_, err := b.SubmitOrder(context.Background(), intent)
	assert.ErrorIs(t, err, ErrInsufficientMargin)
}

func TestCancelOrder(t *testing.T) {
	openOrdersResp := `[{"coin":"BTC","oid":12345,"side":"B","limitPx":"50000.00","sz":"0.1","timestamp":1700000000000}]`

	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "/exchange") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		// Info endpoint: check if it's openOrders.
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["type"] == "openOrders" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(openOrdersResp))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	err := b.CancelOrder(context.Background(), "12345")
	require.NoError(t, err)
}

func TestCancelOrder_NotFound(t *testing.T) {
	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`)) // No open orders.
	}))
	defer srv.Close()

	err := b.CancelOrder(context.Background(), "99999")
	assert.ErrorIs(t, err, ErrOrderNotFound)
}

func TestGetPositions(t *testing.T) {
	resp := `{
		"marginSummary": {"accountValue":"10000","totalNtlPos":"5000","totalRawUsd":"9000","totalMarginUsed":"1000"},
		"assetPositions": [
			{"position":{"coin":"BTC","szi":"0.5","entryPx":"50000.00","positionValue":"25000","unrealizedPnl":"500","leverage":{"type":"cross","value":5},"liquidationPx":"40000","marginUsed":"5000","returnOnEquity":"0.1"}},
			{"position":{"coin":"ETH","szi":"0.0","entryPx":"0","positionValue":"0","unrealizedPnl":"0","leverage":{"type":"cross","value":3},"liquidationPx":"0","marginUsed":"0","returnOnEquity":"0"}}
		]
	}`

	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	trades, err := b.GetPositions(context.Background(), "test-tenant", domain.EnvModePaper)
	require.NoError(t, err)
	// Only non-zero positions are returned.
	require.Len(t, trades, 1)

	assert.Equal(t, domain.Symbol("BTC/USD"), trades[0].Symbol)
	assert.Equal(t, "buy", trades[0].Side)
	assert.InDelta(t, 0.5, trades[0].Quantity, 0.001)
	assert.InDelta(t, 50000.0, trades[0].Price, 0.01)
	assert.Equal(t, domain.VenueHyperliquid, trades[0].Venue)

	// Check position cache was updated.
	b.posMu.RLock()
	tp, ok := b.positions["BTC"]
	b.posMu.RUnlock()
	require.True(t, ok)
	assert.InDelta(t, 0.5, tp.Qty, 0.001)
}

func TestGetPosition_CacheMiss(t *testing.T) {
	resp := `{
		"marginSummary": {"accountValue":"10000","totalNtlPos":"5000","totalRawUsd":"9000","totalMarginUsed":"1000"},
		"assetPositions": [
			{"position":{"coin":"SOL","szi":"-10.0","entryPx":"100.00","positionValue":"1000","unrealizedPnl":"-50","leverage":{"type":"cross","value":5},"liquidationPx":"120","marginUsed":"200","returnOnEquity":"-0.05"}}
		]
	}`

	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	qty, err := b.GetPosition(context.Background(), domain.Symbol("SOL/USD"))
	require.NoError(t, err)
	assert.InDelta(t, -10.0, qty, 0.001)
}

func TestGetPosition_CacheHit(t *testing.T) {
	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call API when cache is populated")
	}))
	defer srv.Close()

	b.posMu.Lock()
	b.positions["BTC"] = &trackedPosition{Coin: "BTC", Qty: 1.5, AvgEntryPx: 50000}
	b.posMu.Unlock()

	qty, err := b.GetPosition(context.Background(), domain.Symbol("BTC/USD"))
	require.NoError(t, err)
	assert.InDelta(t, 1.5, qty, 0.001)
}

func TestGetOpenOrders(t *testing.T) {
	resp := `[
		{"coin":"BTC","oid":111,"side":"B","limitPx":"49000.00","sz":"0.2","timestamp":1700000000000},
		{"coin":"ETH","oid":222,"side":"A","limitPx":"3200.00","sz":"5.0","timestamp":1700000001000}
	]`

	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	orders, err := b.GetOpenOrders(context.Background())
	require.NoError(t, err)
	require.Len(t, orders, 2)

	assert.Equal(t, "111", orders[0].BrokerOrderID)
	assert.Equal(t, "BTC", orders[0].Symbol)
	assert.Equal(t, "buy", orders[0].Side)
	assert.InDelta(t, 49000.0, orders[0].LimitPrice, 0.01)

	assert.Equal(t, "222", orders[1].BrokerOrderID)
	assert.Equal(t, "sell", orders[1].Side)
}

func TestCancelAllOpenOrders(t *testing.T) {
	callCount := 0
	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)

		if strings.Contains(r.URL.Path, "/exchange") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		// Return open orders.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"coin":"BTC","oid":111,"side":"B","limitPx":"49000","sz":"0.1","timestamp":1700000000000}]`))
	}))
	defer srv.Close()

	count, err := b.CancelAllOpenOrders(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCancelAllOpenOrders_Empty(t *testing.T) {
	b, srv := newTestBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	count, err := b.CancelAllOpenOrders(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestSymbolToCoin(t *testing.T) {
	tests := []struct {
		symbol domain.Symbol
		want   string
	}{
		{"BTC/USD", "BTC"},
		{"ETH/USDT", "ETH"},
		{"SOL-PERP", "SOL"},
		{"BTC-USD", "BTC"},
		{"DOGE", "DOGE"},
	}
	for _, tt := range tests {
		t.Run(string(tt.symbol), func(t *testing.T) {
			assert.Equal(t, tt.want, symbolToCoin(tt.symbol))
		})
	}
}

func TestCoinToSymbol(t *testing.T) {
	assert.Equal(t, domain.Symbol("BTC/USD"), coinToSymbol("BTC"))
	assert.Equal(t, domain.Symbol("ETH/USD"), coinToSymbol("ETH"))
}

func TestHlSideToDomain(t *testing.T) {
	assert.Equal(t, "buy", hlSideToDomain("B"))
	assert.Equal(t, "sell", hlSideToDomain("A"))
	assert.Equal(t, "unknown", hlSideToDomain("unknown"))
}

func TestBuildOrderType(t *testing.T) {
	tests := []struct {
		name      string
		intent    domain.OrderIntent
		wantTif   string
		isMarket  bool
	}{
		{
			name:     "default limit GTC",
			intent:   domain.OrderIntent{},
			wantTif:  "Gtc",
		},
		{
			name:     "explicit IOC",
			intent:   domain.OrderIntent{TimeInForce: "ioc"},
			wantTif:  "Ioc",
		},
		{
			name:     "market becomes IOC",
			intent:   domain.OrderIntent{OrderType: "market"},
			wantTif:  "Ioc",
			isMarket: true,
		},
		{
			name:     "ALO post-only",
			intent:   domain.OrderIntent{TimeInForce: "alo"},
			wantTif:  "Alo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ot := buildOrderType(tt.intent)
			require.NotNil(t, ot.Limit)
			assert.Equal(t, tt.wantTif, ot.Limit.Tif)
		})
	}
}

func TestParseOrderResponse(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantOID string
		wantErr error
	}{
		{
			name:    "resting order",
			raw:     `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":12345}}]}}}`,
			wantOID: "12345",
		},
		{
			name:    "filled order",
			raw:     `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"oid":67890}}]}}}`,
			wantOID: "67890",
		},
		{
			name:    "insufficient margin",
			raw:     `{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"insufficient margin"}]}}}`,
			wantErr: ErrInsufficientMargin,
		},
		{
			name:    "bad status",
			raw:     `{"status":"error","response":{}}`,
			wantErr: ErrInvalidResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oid, err := parseOrderResponse([]byte(tt.raw))
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantOID, oid)
			}
		})
	}
}

func TestFormatPrice(t *testing.T) {
	assert.Equal(t, "50000.00", formatPrice(50000.0))
	assert.Equal(t, "0.001234", formatPrice(0.001234))
	assert.Equal(t, "100.50", formatPrice(100.50))
}

func TestFormatSize(t *testing.T) {
	assert.Equal(t, "0.1", formatSize(0.1))
	assert.Equal(t, "100", formatSize(100))
}
