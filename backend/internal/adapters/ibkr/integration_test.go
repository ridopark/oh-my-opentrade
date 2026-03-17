//go:build ibkr

package ibkr_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func integrationConfig() config.IBKRConfig {
	host := os.Getenv("IB_GATEWAY_HOST")
	if host == "" {
		host = "localhost"
	}
	return config.IBKRConfig{Host: host, Port: 4002, ClientID: 99, PaperMode: true}
}

func TestIntegration_Connect(t *testing.T) {
	cfg := integrationConfig()
	a, err := ibkr.NewAdapter(cfg, zerolog.New(os.Stdout).With().Timestamp().Logger())
	require.NoError(t, err)
	assert.True(t, a.IsConnected())
	require.NoError(t, a.Close())
}

func TestIntegration_GetAccountEquity(t *testing.T) {
	a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
	require.NoError(t, err)
	defer a.Close()

	equity, err := a.GetAccountEquity(context.Background())
	require.NoError(t, err)
	assert.Greater(t, equity, float64(0))
}

func TestIntegration_GetPositions_ReturnsSlice(t *testing.T) {
	a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
	require.NoError(t, err)
	defer a.Close()

	positions, err := a.GetPositions(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)
	assert.NotNil(t, positions)
}

func TestIntegration_GetQuote_AAPL(t *testing.T) {
	a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
	require.NoError(t, err)
	defer a.Close()

	bid, ask, err := a.GetQuote(context.Background(), "AAPL")
	require.NoError(t, err)
	assert.Greater(t, bid, float64(0))
	assert.Greater(t, ask, float64(0))
	assert.LessOrEqual(t, bid, ask)
}

func TestIntegration_SubmitAndCancelOrder(t *testing.T) {
	a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
	require.NoError(t, err)
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	updateCh, err := a.SubscribeOrderUpdates(ctx)
	require.NoError(t, err)

	orderID, err := a.SubmitOrder(ctx, domain.OrderIntent{
		Symbol:     "AAPL",
		Direction:  domain.DirectionLong,
		Quantity:   1,
		OrderType:  "limit",
		LimitPrice: 1.00,
	})
	require.NoError(t, err)
	require.NotEmpty(t, orderID)
	t.Logf("submitted order: %s", orderID)

	select {
	case update := <-updateCh:
		assert.Contains(t, []string{"new", "accepted"}, update.Event)
		t.Logf("received event: %s for order %s", update.Event, update.BrokerOrderID)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for new order event")
	}

	err = a.CancelOrder(ctx, orderID)
	require.NoError(t, err)

	deadline := time.After(15 * time.Second)
	for {
		select {
		case update := <-updateCh:
			if update.Event == "canceled" && update.BrokerOrderID == orderID {
				t.Logf("cancel confirmed for order %s", orderID)
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for canceled event")
		case <-ctx.Done():
			t.Fatal("context canceled before cancel event received")
		}
	}
}

func TestIntegration_StreamBars_ReceivesBars(t *testing.T) {
	a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
	require.NoError(t, err)
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	barCh := make(chan domain.MarketBar, 10)
	err = a.StreamBars(ctx, []domain.Symbol{"AAPL"}, "1m",
		func(_ context.Context, bar domain.MarketBar) error {
			barCh <- bar
			return nil
		})
	require.NoError(t, err)

	t.Log("waiting for bar (requires active market or extended hours trading)...")
	select {
	case bar := <-barCh:
		assert.Equal(t, domain.Symbol("AAPL"), bar.Symbol)
		assert.Greater(t, bar.High, float64(0))
		t.Logf("received bar: %+v", bar)
	case <-ctx.Done():
		t.Skip("no bars received (possibly outside market hours) — skipping")
	}
}
