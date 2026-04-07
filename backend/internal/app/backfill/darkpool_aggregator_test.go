package backfill

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDPAggregator_FiveMinuteBuckets(t *testing.T) {
	agg := NewDPAggregator("AAPL")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)

	// Two trades in the 14:00 bucket
	agg.AddTrade(base.Add(30*time.Second), "D", 150.0, 100)
	agg.AddTrade(base.Add(2*time.Minute), "Q", 150.5, 200) // lit

	// One trade in the 14:05 bucket
	agg.AddTrade(base.Add(5*time.Minute+10*time.Second), "D", 151.0, 50)

	bars := agg.Flush()
	require.Len(t, bars, 2)

	// Bars should be sorted by time.
	assert.Equal(t, base, bars[0].Time)
	assert.Equal(t, base.Add(5*time.Minute), bars[1].Time)

	// First bucket: 1 DP trade (100 shares) + 1 lit (200 shares)
	assert.Equal(t, "AAPL", string(bars[0].Symbol))
	assert.Equal(t, "5m", string(bars[0].Timeframe))
	assert.InDelta(t, 100.0, bars[0].DPVolume, 0.01)
	assert.Equal(t, 1, bars[0].DPTrades)
	assert.InDelta(t, 200.0, bars[0].LitVolume, 0.01)
	assert.InDelta(t, 300.0, bars[0].TotalVolume, 0.01)
	assert.InDelta(t, 100.0/300.0, bars[0].DPRatio, 0.001)
	assert.InDelta(t, 150.0, bars[0].DPVWAP, 0.01) // single DP trade

	// Second bucket: 1 DP trade
	assert.InDelta(t, 50.0, bars[1].DPVolume, 0.01)
	assert.Equal(t, 1, bars[1].DPTrades)
	assert.InDelta(t, 0.0, bars[1].LitVolume, 0.01)
	assert.InDelta(t, 50.0, bars[1].TotalVolume, 0.01)
	assert.InDelta(t, 1.0, bars[1].DPRatio, 0.001)
}

func TestDPAggregator_BuySellClassification(t *testing.T) {
	agg := NewDPAggregator("MSFT")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)

	// First DP trade at 100.0 — running VWAP starts at 0, so price >= VWAP → buy.
	agg.AddTrade(base, "D", 100.0, 100)
	// Second DP trade at 99.0 — running VWAP is 100.0, price < VWAP → sell.
	agg.AddTrade(base.Add(time.Second), "D", 99.0, 50)
	// Third DP trade at 100.5 — running VWAP ~99.67, price >= VWAP → buy.
	agg.AddTrade(base.Add(2*time.Second), "D", 100.5, 50)

	bars := agg.Flush()
	require.Len(t, bars, 1)

	assert.InDelta(t, 150.0, bars[0].BuyVolume, 0.01)  // first (100) + third (50)
	assert.InDelta(t, 50.0, bars[0].SellVolume, 0.01)   // second (50)
}

func TestDPAggregator_LargePrintDetection(t *testing.T) {
	agg := NewDPAggregator("SPY")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)

	// Large print: 500 * 500 = $250,000 >= $200K threshold
	agg.AddTrade(base, "D", 500.0, 500)
	// Small print: 500 * 100 = $50,000 < $200K threshold
	agg.AddTrade(base.Add(time.Second), "D", 500.0, 100)

	bars := agg.Flush()
	require.Len(t, bars, 1)

	assert.InDelta(t, 500.0, bars[0].LargePrintVolume, 0.01)
	assert.Equal(t, 1, bars[0].LargePrintCount)
	assert.InDelta(t, 500.0, bars[0].MaxPrintSize, 0.01)
}

func TestDPAggregator_EmptyFlush(t *testing.T) {
	agg := NewDPAggregator("TSLA")
	bars := agg.Flush()
	assert.Nil(t, bars)
}

func TestDPAggregator_NoDP(t *testing.T) {
	agg := NewDPAggregator("GOOG")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)

	// Only lit trades
	agg.AddTrade(base, "Q", 2800.0, 100)
	agg.AddTrade(base.Add(time.Second), "N", 2801.0, 50)

	bars := agg.Flush()
	require.Len(t, bars, 1)

	assert.InDelta(t, 0.0, bars[0].DPVolume, 0.01)
	assert.Equal(t, 0, bars[0].DPTrades)
	assert.InDelta(t, 150.0, bars[0].LitVolume, 0.01)
	assert.InDelta(t, 150.0, bars[0].TotalVolume, 0.01)
	assert.InDelta(t, 0.0, bars[0].DPRatio, 0.001)
	assert.InDelta(t, 0.0, bars[0].DPVWAP, 0.01)
}

func TestDPAggregator_FlushResetsState(t *testing.T) {
	agg := NewDPAggregator("NVDA")

	base := time.Date(2025, 6, 2, 14, 0, 0, 0, time.UTC)
	agg.AddTrade(base, "D", 300.0, 100)

	bars1 := agg.Flush()
	require.Len(t, bars1, 1)

	// After flush, new trades should start fresh.
	agg.AddTrade(base.Add(10*time.Minute), "D", 305.0, 200)
	bars2 := agg.Flush()
	require.Len(t, bars2, 1)

	assert.Equal(t, base.Add(10*time.Minute), bars2[0].Time)
	assert.InDelta(t, 200.0, bars2[0].DPVolume, 0.01)
}
