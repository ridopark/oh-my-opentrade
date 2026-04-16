package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectLargePrints_AboveThreshold(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// Large print: 1 BTC @ $60k = $60k.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "buy",
		Timestamp: now.Add(-5 * time.Second),
	})
	// Small print: 0.1 BTC @ $60k = $6k.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 0.1, TakerSide: "sell",
		Timestamp: now.Add(-3 * time.Second),
	})

	prints := a.DetectLargePrints("BTC/USD", 60*time.Second)
	require.Len(t, prints, 1)
	assert.InDelta(t, 60000.0, prints[0].SizeUSD, 0.01)
	assert.Equal(t, "buy", prints[0].TakerSide)
}

func TestDetectLargePrints_OutsideWindow(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// Large but old.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 2, TakerSide: "buy",
		Timestamp: now.Add(-5 * time.Minute),
	})

	prints := a.DetectLargePrints("BTC/USD", 60*time.Second)
	assert.Empty(t, prints)
}

func TestCoordinatedFlow_TwoVenues(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// Large buy on binance.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "buy",
		Timestamp: now.Add(-5 * time.Second),
	})
	// Large buy on coinbase within 3 seconds.
	a.Ingest(VenueTrade{
		Venue: "coinbase", Symbol: "BTC/USD",
		Price: 60000, Size: 1.2, TakerSide: "buy",
		Timestamp: now.Add(-3 * time.Second),
	})

	coordinated := a.CoordinatedFlow("BTC/USD", 5*time.Second, 2)
	require.Len(t, coordinated, 1)
	assert.Equal(t, "buy", coordinated[0].Direction)
	assert.Len(t, coordinated[0].Prints, 2)
	assert.InDelta(t, 132000.0, coordinated[0].TotalUSD, 0.01) // 60k + 72k
}

func TestCoordinatedFlow_InsufficientVenues(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// Only one venue has a large print.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "buy",
		Timestamp: now.Add(-5 * time.Second),
	})

	coordinated := a.CoordinatedFlow("BTC/USD", 5*time.Second, 2)
	assert.Empty(t, coordinated)
}

func TestCoordinatedFlow_TimeTooFarApart(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// Large buy on binance at -50s.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "buy",
		Timestamp: now.Add(-50 * time.Second),
	})
	// Large buy on coinbase at -5s (45 seconds apart, outside 5s cluster window).
	a.Ingest(VenueTrade{
		Venue: "coinbase", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "buy",
		Timestamp: now.Add(-5 * time.Second),
	})

	coordinated := a.CoordinatedFlow("BTC/USD", 5*time.Second, 2)
	assert.Empty(t, coordinated)
}
