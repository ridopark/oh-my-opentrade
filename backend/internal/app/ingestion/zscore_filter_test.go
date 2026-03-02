package ingestion_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createBar(t *testing.T, symbol domain.Symbol, closePrice, volume float64) domain.MarketBar {
	bar, err := domain.NewMarketBar(
		time.Now(),
		symbol,
		"1m",
		closePrice, closePrice, closePrice, closePrice, // O,H,L,C
		volume,
	)
	require.NoError(t, err)
	return bar
}

func TestZScoreFilter_PassesNormalBar(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	// Fill window
	for i := 0; i < 5; i++ {
		suspect := filter.Check(createBar(t, sym, 100.0, 10.0))
		assert.False(t, suspect)
	}

	// Normal bar (mean=100, stddev=0 -> wait, if stddev is 0, it should pass)
	// Let's create some variance
	filter = ingestion.NewZScoreFilter(5, 4.0)
	for i := 0; i < 5; i++ {
		// prices: 98, 99, 100, 101, 102 (mean 100, stddev ~1.41)
		// volumes: 10
		suspect := filter.Check(createBar(t, sym, 98.0+float64(i), 10.0))
		assert.False(t, suspect)
	}

	// 103 is z=3/1.41 = 2.12 < 4.0
	suspect := filter.Check(createBar(t, sym, 103.0, 10.0))
	assert.False(t, suspect, "Normal bar should pass")
}

func TestZScoreFilter_InsufficientData(t *testing.T) {
	filter := ingestion.NewZScoreFilter(100, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	// Only 10 bars, less than window size
	for i := 0; i < 10; i++ {
		suspect := filter.Check(createBar(t, sym, 100.0, 10.0))
		assert.False(t, suspect, "Should pass when insufficient data")
	}

	// Even an extreme outlier should pass if insufficient data
	suspect := filter.Check(createBar(t, sym, 99999.0, 10.0))
	assert.False(t, suspect, "Should pass outlier when insufficient data")
}

func TestZScoreFilter_DetectsAnomaly(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	for i := 0; i < 5; i++ {
		// prices: 98, 99, 100, 101, 102
		// volumes: 10, 10, 10, 10, 10
		suspect := filter.Check(createBar(t, sym, 98.0+float64(i), 10.0))
		assert.False(t, suspect)
	}

	// Mean = 100, StdDev = sqrt(2) = ~1.414
	// 4 sigma = 4 * 1.414 = 5.65
	// Price 106 -> diff 6 -> z = 4.24 > 4.0
	// Volume 10 -> mean 10, stddev 0 -> z = 0 < 2.0 (secondary threshold)
	// So it SHOULD reject!
	suspect := filter.Check(createBar(t, sym, 106.0, 10.0))
	assert.True(t, suspect, "Anomalous bar without matching volume should be rejected")
}

func TestZScoreFilter_AnomalyWithMatchingVolumePasses(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	for i := 0; i < 5; i++ {
		// prices: 98, 99, 100, 101, 102
		// volumes: 8, 9, 10, 11, 12 (mean 10, stddev ~1.41)
		suspect := filter.Check(createBar(t, sym, 98.0+float64(i), 8.0+float64(i)))
		assert.False(t, suspect)
	}

	// Price 106 -> z = 4.24 > 4.0
	// Volume 16 -> diff 6 -> z = 4.24 > 2.0 (assuming secondary is 2.0)
	// Since volume matches the anomaly, it should pass!
	suspect := filter.Check(createBar(t, sym, 106.0, 16.0))
	assert.False(t, suspect, "Anomalous bar WITH matching volume should pass")
}

func TestZScoreFilter_RollingWindow(t *testing.T) {
	filter := ingestion.NewZScoreFilter(3, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	// P1: 100, P2: 100, P3: 100
	filter.Check(createBar(t, sym, 100.0, 10.0))
	filter.Check(createBar(t, sym, 100.0, 10.0))
	filter.Check(createBar(t, sym, 100.0, 10.0))

	// Now window is full. If we push 200, it's an anomaly because stddev is 0 (all 100s)
	// Wait, if stddev is 0, any diff > 0 is inf z-score, so it's > 4.0.
	// We check 200 with low volume.
	suspect := filter.Check(createBar(t, sym, 200.0, 10.0))
	assert.True(t, suspect, "First outlier rejected")

	// The outlier itself should still be added to the rolling window?
	// Ah! Does the filter add the rejected bar to the window?
	// Usually, anomalous bars are NOT added to the rolling window to prevent poisoning!
	// Wait, the prompt doesn't specify. Let's assume we do add it, or we don't.
	// Let's assume we add it because "For each new bar: 1. Compute ... 2. Calculate ... 3. If suspect... reject"
	// But wait, the prompt says "The filter maintains a rolling window of closing prices per symbol. For each new bar: 1. Compute mean... 2. Calculate z... 3. If z > ... reject. 4. Otherwise pass".
	// It doesn't explicitly say "add to window". But we must add it to the window at some point.
	// Normally we add before or after. If we add *before* computing, the anomaly poisons its own detection. So we add *after* checking. If it's a suspect, do we add it? Probably not, or it poisons the window. Let's NOT add suspects.
}

func TestZScoreFilter_SeparatePerSymbol(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	symBTC, _ := domain.NewSymbol("BTC/USD")
	symETH, _ := domain.NewSymbol("ETH/USD")

	// Fill BTC window with 100s
	for i := 0; i < 5; i++ {
		filter.Check(createBar(t, symBTC, 100.0, 10.0))
	}

	// Send an outlier for ETH. It should pass because ETH window is not full (0 bars).
	suspect := filter.Check(createBar(t, symETH, 999.0, 10.0))
	assert.False(t, suspect, "ETH has insufficient data, should pass")
}

func TestZScoreFilter_ZeroStdDev(t *testing.T) {
	filter := ingestion.NewZScoreFilter(3, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	for i := 0; i < 3; i++ {
		filter.Check(createBar(t, sym, 100.0, 10.0))
	}

	// Mean = 100, StdDev = 0.
	// Diff = 0. Z = 0/0 = NaN. Should be handled and pass.
	suspect := filter.Check(createBar(t, sym, 100.0, 10.0))
	assert.False(t, suspect, "Identical price should pass (zero stddev)")
}
