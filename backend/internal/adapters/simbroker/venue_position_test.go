package simbroker_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newVenueIntent(sym domain.Symbol, dir domain.Direction, qty float64, venue domain.Venue) domain.OrderIntent {
	return domain.OrderIntent{
		ID:             uuid.New(),
		TenantID:       "tenant-1",
		EnvMode:        domain.EnvModePaper,
		Symbol:         sym,
		Direction:      dir,
		Quantity:       qty,
		Venue:          venue,
		IdempotencyKey: "idem-" + uuid.NewString(),
	}
}

func TestDualVenuePositions_SeparateTracking(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{
		SlippageBPS:     0,
		DisableFillChan: true,
	}, log)

	sym := domain.Symbol("BTC/USD")
	price := 50000.0
	barTime := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	b.UpdatePrice(sym, price, barTime)

	ctx := context.Background()

	// Open long on Coinbase
	_, err := b.SubmitOrder(ctx, newVenueIntent(sym, domain.DirectionLong, 1.0, domain.VenueCoinbase))
	require.NoError(t, err)

	// Open short on Hyperliquid
	_, err = b.SubmitOrder(ctx, newVenueIntent(sym, domain.DirectionShort, 2.0, domain.VenueHyperliquid))
	require.NoError(t, err)

	// GetPositions should return both positions
	positions, err := b.GetPositions(ctx, "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	assert.Len(t, positions, 2, "should have two separate positions for the same symbol on different venues")

	// Verify venues are distinct
	venues := map[domain.Venue]domain.Trade{}
	for _, p := range positions {
		venues[p.Venue] = p
	}
	coinbasePos, ok := venues[domain.VenueCoinbase]
	require.True(t, ok, "should have a Coinbase position")
	assert.Equal(t, "buy", coinbasePos.Side)
	assert.Equal(t, 1.0, coinbasePos.Quantity)

	hlPos, ok := venues[domain.VenueHyperliquid]
	require.True(t, ok, "should have a Hyperliquid position")
	assert.Equal(t, "sell", hlPos.Side)
	assert.Equal(t, 2.0, hlPos.Quantity)
}

func TestDualVenuePositions_BackwardCompatible(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{
		SlippageBPS:     0,
		DisableFillChan: true,
	}, log)

	sym := domain.Symbol("AAPL")
	price := 150.0
	barTime := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	b.UpdatePrice(sym, price, barTime)

	ctx := context.Background()

	// Submit with empty venue (classic equity path)
	_, err := b.SubmitOrder(ctx, newVenueIntent(sym, domain.DirectionLong, 10, domain.VenueUnspecified))
	require.NoError(t, err)

	// GetPosition (symbol-only) should work
	qty, err := b.GetPosition(ctx, sym)
	require.NoError(t, err)
	assert.Equal(t, 10.0, qty)

	// GetPositions should include it
	positions, err := b.GetPositions(ctx, "tenant-1", domain.EnvModePaper)
	require.NoError(t, err)
	assert.Len(t, positions, 1)
	assert.True(t, positions[0].Venue.IsUnspecified(), "equity positions should have empty venue")
}

func TestGetPositionsByVenue(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{
		SlippageBPS:     0,
		DisableFillChan: true,
	}, log)

	barTime := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	b.UpdatePrice(domain.Symbol("BTC/USD"), 50000, barTime)
	b.UpdatePrice(domain.Symbol("ETH/USD"), 3000, barTime)

	ctx := context.Background()

	// Two positions on Coinbase
	_, err := b.SubmitOrder(ctx, newVenueIntent("BTC/USD", domain.DirectionLong, 1.0, domain.VenueCoinbase))
	require.NoError(t, err)
	_, err = b.SubmitOrder(ctx, newVenueIntent("ETH/USD", domain.DirectionLong, 5.0, domain.VenueCoinbase))
	require.NoError(t, err)

	// One position on Hyperliquid
	_, err = b.SubmitOrder(ctx, newVenueIntent("BTC/USD", domain.DirectionShort, 0.5, domain.VenueHyperliquid))
	require.NoError(t, err)

	// Filter by Coinbase
	cbPositions, err := b.GetPositionsByVenue(ctx, domain.VenueCoinbase)
	require.NoError(t, err)
	assert.Len(t, cbPositions, 2, "should have 2 Coinbase positions")

	// Filter by Hyperliquid
	hlPositions, err := b.GetPositionsByVenue(ctx, domain.VenueHyperliquid)
	require.NoError(t, err)
	assert.Len(t, hlPositions, 1, "should have 1 Hyperliquid position")
	assert.Equal(t, "sell", hlPositions[0].Side)

	// Filter by unknown venue
	unknownPositions, err := b.GetPositionsByVenue(ctx, domain.Venue("deribit"))
	require.NoError(t, err)
	assert.Len(t, unknownPositions, 0, "should have 0 positions for unknown venue")
}

func TestDualVenuePositions_CloseOneVenueDoesNotAffectOther(t *testing.T) {
	log := zerolog.Nop()
	b := simbroker.New(simbroker.Config{
		SlippageBPS:     0,
		DisableFillChan: true,
	}, log)

	sym := domain.Symbol("BTC/USD")
	price := 50000.0
	barTime := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	b.UpdatePrice(sym, price, barTime)

	ctx := context.Background()

	// Open long on Coinbase
	_, err := b.SubmitOrder(ctx, newVenueIntent(sym, domain.DirectionLong, 1.0, domain.VenueCoinbase))
	require.NoError(t, err)

	// Open long on Hyperliquid
	_, err = b.SubmitOrder(ctx, newVenueIntent(sym, domain.DirectionLong, 2.0, domain.VenueHyperliquid))
	require.NoError(t, err)

	// Close the Coinbase position by submitting a sell on the same venue
	_, err = b.SubmitOrder(ctx, newVenueIntent(sym, domain.DirectionShort, 1.0, domain.VenueCoinbase))
	require.NoError(t, err)

	// Coinbase position should be flat
	cbPositions, err := b.GetPositionsByVenue(ctx, domain.VenueCoinbase)
	require.NoError(t, err)
	openCB := 0
	for _, p := range cbPositions {
		if p.Quantity > 0 {
			openCB++
		}
	}
	assert.Equal(t, 0, openCB, "Coinbase position should be flat after closing sell")

	// Hyperliquid position should remain untouched
	hlPositions, err := b.GetPositionsByVenue(ctx, domain.VenueHyperliquid)
	require.NoError(t, err)
	require.Len(t, hlPositions, 1)
	assert.Equal(t, 2.0, hlPositions[0].Quantity, "Hyperliquid position should be unaffected")
	assert.Equal(t, "buy", hlPositions[0].Side)
}
