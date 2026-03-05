//go:build smoke

package alpaca

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSmoke_AlpacaPaperAccount verifies connectivity to the Alpaca paper trading API
// by fetching account equity.
//
// Run with: go test ./internal/adapters/alpaca/ -tags smoke -run TestSmoke_AlpacaPaper -v -timeout 30s
//
// Requires: APCA_API_KEY_ID, APCA_API_SECRET_KEY, APCA_API_BASE_URL set in environment.
func TestSmoke_AlpacaPaperAccount(t *testing.T) {
	client := newSmokeClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	equity, err := client.GetAccountEquity(ctx)
	require.NoError(t, err, "failed to get account equity")
	require.Greater(t, equity, 0.0, "account equity should be positive for paper account")

	t.Logf("Paper account equity: $%.2f", equity)
}

// TestSmoke_AlpacaPaperOrderLifecycle submits a limit order at a very low price
// (will not fill), verifies status, then cancels it.
func TestSmoke_AlpacaPaperOrderLifecycle(t *testing.T) {
	client := newSmokeClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Step 1: Submit a limit order at $1.00 for AAPL — will never fill.
	intent := domain.OrderIntent{
		Symbol:     "AAPL",
		Direction:  domain.DirectionLong,
		Quantity:   1,
		LimitPrice: 1.00,
	}

	orderID, err := client.SubmitOrder(ctx, intent)
	require.NoError(t, err, "submit order failed")
	require.NotEmpty(t, orderID, "order ID should not be empty")
	t.Logf("Submitted order ID: %s", orderID)

	// Step 2: Verify order status is "new" or "accepted"
	time.Sleep(1 * time.Second) // brief pause for Alpaca to process
	status, err := client.GetOrderStatus(ctx, orderID)
	require.NoError(t, err, "get order status failed")
	assert.Contains(t, []string{"new", "accepted", "pending_new"}, status,
		"expected order to be new/accepted/pending_new, got: %s", status)
	t.Logf("Order status: %s", status)

	// Step 3: Cancel the order
	err = client.CancelOrder(ctx, orderID)
	require.NoError(t, err, "cancel order failed")
	t.Logf("Order %s cancelled successfully", orderID)

	// Step 4: Verify order is now canceled
	time.Sleep(1 * time.Second)
	status, err = client.GetOrderStatus(ctx, orderID)
	require.NoError(t, err, "get order status after cancel failed")
	assert.Contains(t, []string{"canceled", "cancelled", "pending_cancel"}, status,
		"expected order to be canceled, got: %s", status)
	t.Logf("Final order status: %s", status)
}

// TestSmoke_AlpacaPaperGetQuote verifies we can fetch a live quote.
func TestSmoke_AlpacaPaperGetQuote(t *testing.T) {
	apiKey := os.Getenv("APCA_API_KEY_ID")
	apiSecret := os.Getenv("APCA_API_SECRET_KEY")
	if apiKey == "" || apiSecret == "" {
		t.Skip("APCA_API_KEY_ID / APCA_API_SECRET_KEY not set — skipping smoke test")
	}

	// Quotes go to data.alpaca.markets, not paper-api
	dataURL := os.Getenv("APCA_DATA_URL")
	if dataURL == "" {
		dataURL = "https://data.alpaca.markets"
	}

	log := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	limiter := NewRateLimiter(200)
	// Use data URL for quote endpoint
	client := NewRESTClient(dataURL, apiKey, apiSecret, limiter, log)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bid, ask, err := client.GetQuote(ctx, dataURL, "AAPL")
	require.NoError(t, err, "get quote failed")
	require.Greater(t, bid, 0.0, "bid should be positive")
	require.Greater(t, ask, 0.0, "ask should be positive")
	require.GreaterOrEqual(t, ask, bid, "ask should be >= bid")

	t.Logf("AAPL quote: bid=$%.2f ask=$%.2f spread=$%.4f", bid, ask, ask-bid)
}

func newSmokeClient(t *testing.T) *RESTClient {
	t.Helper()
	apiKey := os.Getenv("APCA_API_KEY_ID")
	apiSecret := os.Getenv("APCA_API_SECRET_KEY")
	baseURL := os.Getenv("APCA_API_BASE_URL")

	if apiKey == "" || apiSecret == "" {
		t.Skip("APCA_API_KEY_ID / APCA_API_SECRET_KEY not set — skipping smoke test")
	}
	if baseURL == "" {
		baseURL = "https://paper-api.alpaca.markets"
	}

	log := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	limiter := NewRateLimiter(200)
	return NewRESTClient(baseURL, apiKey, apiSecret, limiter, log)
}
