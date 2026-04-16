package flow

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAggregator(now time.Time) *Aggregator {
	cfg := DefaultAggregatorConfig()
	a := NewAggregator(cfg)
	a.nowFn = func() time.Time { return now }
	return a
}

func TestScore_KnownTrades(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// Inject trades: 3 buys of 1 BTC @ $60k, 1 sell of 1 BTC @ $60k
	// All within 10s window.
	for i := 0; i < 3; i++ {
		a.Ingest(VenueTrade{
			Venue: "binance", Symbol: "BTC/USD",
			Price: 60000, Size: 1, TakerSide: "buy",
			Timestamp: now.Add(-time.Duration(i) * time.Second),
		})
	}
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "sell",
		Timestamp: now.Add(-2 * time.Second),
	})

	score := a.Score("BTC/USD")

	// buyVol = 3 * 60000 = 180000, sellVol = 1 * 60000 = 60000
	// netFlow = (180000 - 60000) / (180000 + 60000) = 120000 / 240000 = 0.5
	assert.Equal(t, "BTC/USD", score.Symbol)
	assert.InDelta(t, 180000.0, score.BuyVol10s, 0.01)
	assert.InDelta(t, 60000.0, score.SellVol10s, 0.01)
	assert.InDelta(t, 0.5, score.NetFlow10s, 0.001)
	assert.InDelta(t, 0.5, score.NetFlow60s, 0.001)
}

func TestScore_WindowExpiry(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// Trade within 10s window.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "buy",
		Timestamp: now.Add(-5 * time.Second),
	})
	// Trade outside 10s but within 60s.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 2, TakerSide: "sell",
		Timestamp: now.Add(-30 * time.Second),
	})

	score := a.Score("BTC/USD")

	// 10s window: only the buy trade.
	assert.InDelta(t, 60000.0, score.BuyVol10s, 0.01)
	assert.InDelta(t, 0.0, score.SellVol10s, 0.01)
	assert.InDelta(t, 1.0, score.NetFlow10s, 0.001)

	// 60s window: buy + sell.
	assert.InDelta(t, 60000.0, score.BuyVol60s, 0.01)
	assert.InDelta(t, 120000.0, score.SellVol60s, 0.01)
	assert.InDelta(t, -1.0/3.0, score.NetFlow60s, 0.001) // (60k-120k)/180k
}

func TestScore_MultiVenueAgreement(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// All three venues show net buy in the 60s window.
	for _, venue := range []string{"binance", "coinbase", "hyperliquid"} {
		a.Ingest(VenueTrade{
			Venue: venue, Symbol: "BTC/USD",
			Price: 60000, Size: 1, TakerSide: "buy",
			Timestamp: now.Add(-5 * time.Second),
		})
	}

	score := a.Score("BTC/USD")
	assert.Equal(t, 3, score.VenueAgreement)
	assert.Equal(t, 3, score.TotalVenues)
}

func TestScore_EmptySymbol(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	score := a.Score("DOGE/USD")
	assert.Equal(t, "DOGE/USD", score.Symbol)
	assert.Equal(t, 0.0, score.NetFlow10s)
	assert.Equal(t, 0.0, score.NetFlow60s)
	assert.Equal(t, 0, score.VenueAgreement)
	assert.Equal(t, 0, score.TotalVenues)
}

func TestEvict_RemovesOldTrades(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// Old trade well outside the 60s window.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "buy",
		Timestamp: now.Add(-5 * time.Minute),
	})
	// Recent trade.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "sell",
		Timestamp: now.Add(-3 * time.Second),
	})

	a.Evict()

	// Recent trade should survive.
	score := a.Score("BTC/USD")
	assert.InDelta(t, 0.0, score.BuyVol60s, 0.01)
	assert.InDelta(t, 60000.0, score.SellVol60s, 0.01)

	// Verify the old trade is gone by checking internal state.
	a.mu.RLock()
	trades := a.trades["BTC/USD"]["binance"]
	a.mu.RUnlock()
	require.Len(t, trades, 1)
	assert.Equal(t, "sell", trades[0].TakerSide)
}

func TestEvict_CleanupEmptyMaps(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// Only old trades.
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "buy",
		Timestamp: now.Add(-5 * time.Minute),
	})

	a.Evict()

	a.mu.RLock()
	_, exists := a.trades["BTC/USD"]
	a.mu.RUnlock()
	assert.False(t, exists, "symbol entry should be cleaned up when all trades evicted")
}

func TestIngest_MaxTradesPerKeyCap(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	cfg := DefaultAggregatorConfig()
	cfg.MaxTradesPerKey = 100
	a := NewAggregator(cfg)
	a.nowFn = func() time.Time { return now }

	// Ingest 200 trades to exceed the cap.
	for i := 0; i < 200; i++ {
		a.Ingest(VenueTrade{
			Venue: "binance", Symbol: "BTC/USD",
			Price: 60000, Size: 1, TakerSide: "buy",
			Timestamp: now.Add(-time.Duration(200-i) * time.Millisecond),
		})
	}

	a.mu.RLock()
	count := len(a.trades["BTC/USD"]["binance"])
	a.mu.RUnlock()

	// After cap trimming, should be well under MaxTradesPerKey.
	assert.LessOrEqual(t, count, cfg.MaxTradesPerKey)
}

func TestConcurrentIngestAndScore(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	var wg sync.WaitGroup
	const writers = 10
	const readers = 5
	const tradesPerWriter = 100

	// Writers.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < tradesPerWriter; i++ {
				a.Ingest(VenueTrade{
					Venue: "binance", Symbol: "BTC/USD",
					Price: 60000, Size: 1, TakerSide: "buy",
					Timestamp: now.Add(-time.Duration(i) * time.Millisecond),
				})
			}
		}(w)
	}

	// Concurrent readers.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < tradesPerWriter; i++ {
				_ = a.Score("BTC/USD")
			}
		}()
	}

	// Concurrent eviction.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			a.Evict()
		}
	}()

	wg.Wait()
	// If we reach here without a race detector panic, the test passes.
}

func TestScore_LargePrintDetection(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	a := testAggregator(now)

	// One large buy (1 BTC @ $60k = $60k > $50k threshold).
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 1, TakerSide: "buy",
		Timestamp: now.Add(-5 * time.Second),
	})
	// One small sell (0.1 BTC @ $60k = $6k < threshold).
	a.Ingest(VenueTrade{
		Venue: "binance", Symbol: "BTC/USD",
		Price: 60000, Size: 0.1, TakerSide: "sell",
		Timestamp: now.Add(-3 * time.Second),
	})

	score := a.Score("BTC/USD")
	assert.Equal(t, 1, score.LargePrintCount)
	assert.InDelta(t, 1.0, score.LargePrintNetFlow, 0.001) // only buy large print
}
