package onchain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testWalletTags returns a small set of wallet tags for testing.
func testWalletTags() map[string]WalletTag {
	return map[string]WalletTag{
		"0xBinanceHot": {Address: "0xBinanceHot", Tag: "binance_hot", Entity: "Binance", Category: CategoryExchangeHot},
		"0xCoinbaseHot": {Address: "0xCoinbaseHot", Tag: "coinbase_hot", Entity: "Coinbase", Category: CategoryExchangeHot},
		"0xBlackRock":   {Address: "0xBlackRock", Tag: "blackrock_custody", Entity: "BlackRock", Category: CategoryETFCustodian},
		"0xKraken":      {Address: "0xKraken", Tag: "kraken_hot", Entity: "Kraken", Category: CategoryExchangeHot},
	}
}

// newTestTracker creates a FlowTracker backed by the given httptest server.
func newTestTracker(t *testing.T, srv *httptest.Server, queryIDs map[string]int) *FlowTracker {
	t.Helper()
	dune := NewDuneClientWithHTTP("test-key", srv.URL+"/", srv.Client(), zerolog.Nop())
	return newFlowTrackerForTest(dune, testWalletTags(), queryIDs, time.Minute, zerolog.Nop())
}

func TestNetFlow_PositiveInflow(t *testing.T) {
	// Simulate transfers TO exchange (selling pressure).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(latestResultResponse{
			State: "QUERY_STATE_COMPLETED",
			Result: &struct {
				Rows []map[string]any `json:"rows"`
			}{
				Rows: []map[string]any{
					// Unknown wallet -> Binance hot (inflow).
					{"from_address": "0xWhale1", "to_address": "0xBinanceHot", "amount_usd": 5000000.0, "amount": 50.0},
					// Unknown wallet -> Coinbase hot (inflow).
					{"from_address": "0xWhale2", "to_address": "0xCoinbaseHot", "amount_usd": 3000000.0, "amount": 30.0},
				},
			},
		})
	}))
	defer srv.Close()

	ft := newTestTracker(t, srv, map[string]int{"BTC": 100})

	result, err := ft.NetFlow(context.Background(), "BTC", 24)
	require.NoError(t, err)

	assert.Equal(t, "BTC", result.Asset)
	assert.Equal(t, 24, result.WindowHrs)
	assert.Equal(t, 8000000.0, result.InFlowUSD)
	assert.Equal(t, 0.0, result.OutFlowUSD)
	assert.Equal(t, 8000000.0, result.NetFlowUSD, "positive = net inflow to exchanges")
	assert.Equal(t, 2, result.LargeCount)
}

func TestNetFlow_NegativeOutflow(t *testing.T) {
	// Simulate transfers FROM exchange (accumulation).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(latestResultResponse{
			State: "QUERY_STATE_COMPLETED",
			Result: &struct {
				Rows []map[string]any `json:"rows"`
			}{
				Rows: []map[string]any{
					// Binance hot -> unknown wallet (outflow).
					{"from_address": "0xBinanceHot", "to_address": "0xColdStorage", "amount_usd": 10000000.0, "amount": 100.0},
				},
			},
		})
	}))
	defer srv.Close()

	ft := newTestTracker(t, srv, map[string]int{"BTC": 100})

	result, err := ft.NetFlow(context.Background(), "BTC", 24)
	require.NoError(t, err)

	assert.Equal(t, 0.0, result.InFlowUSD)
	assert.Equal(t, 10000000.0, result.OutFlowUSD)
	assert.Equal(t, -10000000.0, result.NetFlowUSD, "negative = net outflow from exchanges")
}

func TestNetFlow_MixedFlows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(latestResultResponse{
			State: "QUERY_STATE_COMPLETED",
			Result: &struct {
				Rows []map[string]any `json:"rows"`
			}{
				Rows: []map[string]any{
					// Inflow: whale -> exchange.
					{"from_address": "0xWhale", "to_address": "0xBinanceHot", "amount_usd": 7000000.0, "amount": 70.0},
					// Outflow: exchange -> cold storage.
					{"from_address": "0xCoinbaseHot", "to_address": "0xCold", "amount_usd": 3000000.0, "amount": 30.0},
					// Exchange -> exchange (ignored in net calculation).
					{"from_address": "0xBinanceHot", "to_address": "0xCoinbaseHot", "amount_usd": 2000000.0, "amount": 20.0},
					// Small transfer (under $1M).
					{"from_address": "0xSmall", "to_address": "0xKraken", "amount_usd": 500000.0, "amount": 5.0},
				},
			},
		})
	}))
	defer srv.Close()

	ft := newTestTracker(t, srv, map[string]int{"ETH": 200})

	result, err := ft.NetFlow(context.Background(), "ETH", 4)
	require.NoError(t, err)

	assert.Equal(t, 7500000.0, result.InFlowUSD, "inflow = 7M (whale->binance) + 500K (small->kraken)")
	assert.Equal(t, 3000000.0, result.OutFlowUSD, "outflow = 3M (coinbase->cold)")
	assert.Equal(t, 4500000.0, result.NetFlowUSD, "net = 7.5M - 3M = 4.5M")
	assert.Equal(t, 3, result.LargeCount, "3 transfers > $1M")
}

func TestNetFlow_CacheHit(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(latestResultResponse{
			State: "QUERY_STATE_COMPLETED",
			Result: &struct {
				Rows []map[string]any `json:"rows"`
			}{
				Rows: []map[string]any{
					{"from_address": "0xW", "to_address": "0xBinanceHot", "amount_usd": 1000000.0, "amount": 10.0},
				},
			},
		})
	}))
	defer srv.Close()

	ft := newTestTracker(t, srv, map[string]int{"BTC": 100})

	// First call — hits Dune.
	_, err := ft.NetFlow(context.Background(), "BTC", 24)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	// Second call — should be served from cache.
	_, err = ft.NetFlow(context.Background(), "BTC", 24)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "second call should not hit Dune")
}

func TestNetFlow_NoQueryID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach Dune")
	}))
	defer srv.Close()

	ft := newTestTracker(t, srv, map[string]int{}) // no query IDs

	_, err := ft.NetFlow(context.Background(), "DOGE", 24)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no query ID configured")
}

func TestLargeTransfers_Filtering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(latestResultResponse{
			State: "QUERY_STATE_COMPLETED",
			Result: &struct {
				Rows []map[string]any `json:"rows"`
			}{
				Rows: []map[string]any{
					{"tx_hash": "0xtx1", "from_address": "0xWhale", "to_address": "0xBinanceHot", "amount_usd": 5000000.0, "amount": 50.0, "block_time": "2026-04-15T10:00:00Z"},
					{"tx_hash": "0xtx2", "from_address": "0xSmall", "to_address": "0xCoinbaseHot", "amount_usd": 500000.0, "amount": 5.0, "block_time": "2026-04-15T10:05:00Z"},
					{"tx_hash": "0xtx3", "from_address": "0xBinanceHot", "to_address": "0xBlackRock", "amount_usd": 20000000.0, "amount": 200.0, "block_time": "2026-04-15T10:10:00Z"},
				},
			},
		})
	}))
	defer srv.Close()

	ft := newTestTracker(t, srv, map[string]int{"BTC": 100})

	transfers, err := ft.LargeTransfers(context.Background(), "BTC", 24, 1000000.0)
	require.NoError(t, err)

	// Should filter out the 500K transfer.
	require.Len(t, transfers, 2)

	// First transfer.
	assert.Equal(t, "0xtx1", transfers[0].TxHash)
	assert.Equal(t, "BTC", transfers[0].Asset)
	assert.Equal(t, 5000000.0, transfers[0].AmountUSD)
	assert.Equal(t, "", transfers[0].FromTag, "unknown wallet has no tag")
	assert.Equal(t, "binance_hot", transfers[0].ToTag)
	assert.Equal(t, domain.VenueBinance, transfers[0].Venue)

	// Second transfer.
	assert.Equal(t, "0xtx3", transfers[1].TxHash)
	assert.Equal(t, "binance_hot", transfers[1].FromTag)
	assert.Equal(t, "blackrock_custody", transfers[1].ToTag)
	// Venue should be binance (from exchange tag).
	assert.Equal(t, domain.VenueBinance, transfers[1].Venue)
}

func TestLargeTransfers_CacheHit(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(latestResultResponse{
			State: "QUERY_STATE_COMPLETED",
			Result: &struct {
				Rows []map[string]any `json:"rows"`
			}{
				Rows: []map[string]any{
					{"tx_hash": "0xtx1", "from_address": "0xA", "to_address": "0xB", "amount_usd": 2000000.0, "amount": 20.0},
				},
			},
		})
	}))
	defer srv.Close()

	ft := newTestTracker(t, srv, map[string]int{"ETH": 200})

	_, err := ft.LargeTransfers(context.Background(), "ETH", 4, 1000000.0)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)

	_, err = ft.LargeTransfers(context.Background(), "ETH", 4, 1000000.0)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "cached result should avoid second Dune call")
}

func TestTagToVenue(t *testing.T) {
	tests := []struct {
		tag   string
		venue domain.Venue
	}{
		{"binance_hot", domain.VenueBinance},
		{"binance_cold", domain.VenueBinance},
		{"coinbase_hot", domain.VenueCoinbase},
		{"coinbase_cold", domain.VenueCoinbase},
		{"kraken_hot", domain.VenueKraken},
		{"blackrock_custody", domain.VenueUnspecified},
		{"unknown", domain.VenueUnspecified},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			assert.Equal(t, tt.venue, tagToVenue(tt.tag))
		})
	}
}
