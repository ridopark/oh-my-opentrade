package positionmonitor

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPriceCacheClock verifies that PriceCache uses the injected clock function
// instead of wall-clock time when recording observed prices.
func TestPriceCacheClock(t *testing.T) {
	// Fixed mock time
	mockTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	mockClock := func() time.Time { return mockTime }

	// Create PriceCache with mock clock
	pc := NewPriceCache(zerolog.Nop(), WithClock(mockClock))

	// Create a mock event bus
	bus := &mockEventBus{}

	// Subscribe PriceCache to MarketBarSanitized events
	ctx := context.Background()
	err := pc.Start(ctx, bus)
	require.NoError(t, err)

	// Publish a MarketBarSanitized event
	bar := domain.MarketBar{
		Symbol: domain.Symbol("AAPL"),
		Time:   time.Date(2025, 1, 1, 11, 59, 0, 0, time.UTC),
		Open:   150.0,
		High:   151.0,
		Low:    149.5,
		Close:  150.5,
		Volume: 1000000,
	}

	event := domain.Event{
		Type:    domain.EventMarketBarSanitized,
		Payload: bar,
	}

	err = bus.Publish(ctx, event)
	require.NoError(t, err)

	// Verify the price was cached with the mock clock time
	snap, ok := pc.LatestPrice(domain.Symbol("AAPL"))
	require.True(t, ok, "price should be cached")
	assert.Equal(t, 150.5, snap.Price)
	assert.Equal(t, mockTime, snap.ObservedAt, "ObservedAt should use mock clock, not wall-clock")
}

// TestPriceCacheDefaultClock verifies that PriceCache uses time.Now by default
// when no clock is injected.
func TestPriceCacheDefaultClock(t *testing.T) {
	// Create PriceCache without injecting a clock (should default to time.Now)
	pc := NewPriceCache(zerolog.Nop())

	// Create a mock event bus
	bus := &mockEventBus{}

	// Subscribe PriceCache to MarketBarSanitized events
	ctx := context.Background()
	err := pc.Start(ctx, bus)
	require.NoError(t, err)

	// Record wall-clock time before publishing
	beforeTime := time.Now()

	// Publish a MarketBarSanitized event
	bar := domain.MarketBar{
		Symbol: domain.Symbol("TSLA"),
		Time:   time.Now().Add(-1 * time.Minute),
		Open:   250.0,
		High:   251.0,
		Low:    249.5,
		Close:  250.5,
		Volume: 500000,
	}

	event := domain.Event{
		Type:    domain.EventMarketBarSanitized,
		Payload: bar,
	}

	err = bus.Publish(ctx, event)
	require.NoError(t, err)

	// Record wall-clock time after publishing
	afterTime := time.Now()

	// Verify the price was cached with a time close to now
	snap, ok := pc.LatestPrice(domain.Symbol("TSLA"))
	require.True(t, ok, "price should be cached")
	assert.Equal(t, 250.5, snap.Price)
	assert.True(t, snap.ObservedAt.After(beforeTime) || snap.ObservedAt.Equal(beforeTime),
		"ObservedAt should be >= beforeTime")
	assert.True(t, snap.ObservedAt.Before(afterTime) || snap.ObservedAt.Equal(afterTime),
		"ObservedAt should be <= afterTime")
}
