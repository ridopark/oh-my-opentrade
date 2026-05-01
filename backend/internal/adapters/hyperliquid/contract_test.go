package hyperliquid

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports/brokerporttest"
)

// TestBrokerPortContract_Hyperliquid runs the shared BrokerPort
// contract suite against the Hyperliquid adapter via an httptest server
// that answers the four request shapes the harness will issue:
//
//   - POST /exchange : SubmitOrder issues an order action; returns
//     a successful resting order with a fixed oid.
//   - POST /info type=clearinghouseState : GetPositions and (cache-miss)
//     GetPosition both query account state; we return zero positions.
//   - POST /info type=openOrders : GetOrderStatus queries open orders
//     to determine whether an order is still working; we return [],
//     which the adapter interprets as "closed" (filled/canceled/expired).
//   - POST /info type=meta : the SDK calls this once on startup to
//     populate the asset map; we pre-seed the map so this isn't needed,
//     but the handler returns an empty meta to be safe.
//
// Internal test package (`package hyperliquid`) is required to access
// newTestBroker, which constructs the Broker against a custom HTTP
// server. brokerporttest doesn't depend on hyperliquid, so no circular
// import.
func TestBrokerPortContract_Hyperliquid(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/exchange") {
			// SubmitOrder + CancelOrder land here. Return a resting
			// order id so SubmitOrder succeeds with a non-empty
			// orderID for the harness's assertion.
			_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":99999}}]}}}`))
			return
		}

		// /info — discriminate by the JSON body's "type".
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		switch req["type"] {
		case "clearinghouseState":
			// Empty positions; satisfies "GetPositions on fresh
			// adapter returns empty" and "GetPosition for unknown
			// symbol returns (0, nil)".
			_, _ = w.Write([]byte(`{"assetPositions":[],"crossMaintenanceMarginUsed":"0","crossMarginSummary":{"accountValue":"100000","totalNtlPos":"0","totalRawUsd":"100000","totalMarginUsed":"0"},"marginSummary":{"accountValue":"100000","totalNtlPos":"0","totalRawUsd":"100000","totalMarginUsed":"0"},"time":1700000000,"withdrawable":"100000"}`))
		case "openOrders":
			// Empty open orders; GetOrderStatus interprets a missing
			// order as "closed" — repeated calls return the same value
			// (idempotent).
			_, _ = w.Write([]byte(`[]`))
		case "meta":
			// SDK-startup asset-map probe (per the file header). The
			// adapter pre-seeds its asset map so this is rarely hit,
			// but model it explicitly so it doesn't trip the default
			// guard below.
			_, _ = w.Write([]byte(`{"universe":[]}`))
		default:
			t.Errorf("unexpected /info type: %v — extend the test fixture", req["type"])
		}
	})

	b, srv := newTestBroker(t, handler)
	defer srv.Close()

	syms := []domain.Symbol{"BTC/USD", "ETH/USD"}
	prices := map[domain.Symbol]float64{
		"BTC/USD": 50000.0,
		"ETH/USD": 3000.0,
	}

	env := &brokerporttest.Env{
		TestSymbols:  syms,
		InitialPrice: prices,
		TestTenantID: "tenant-1",
		TestEnvMode:  domain.EnvModePaper,
	}

	brokerporttest.RunBrokerPortContract(t, b, nil, env)
}
