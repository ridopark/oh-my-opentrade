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
	t.Helper()
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

func createBarOHLC(t *testing.T, symbol domain.Symbol, open, high, low, close, volume float64) domain.MarketBar {
	t.Helper()
	bar, err := domain.NewMarketBar(
		time.Now(),
		symbol,
		"1m",
		open, high, low, close,
		volume,
	)
	require.NoError(t, err)
	return bar
}

func TestZScoreFilter_PassesNormalBar(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	for i := 0; i < 5; i++ {
		suspect := filter.Check(createBar(t, sym, 10000.0, 10.0))
		assert.False(t, suspect)
	}

	// Use higher base prices so small moves stay under 1.5% deviation.
	filter = ingestion.NewZScoreFilter(5, 4.0)
	for i := 0; i < 5; i++ {
		// prices: 9998..10002 (mean 10000, stddev ~1.41)
		suspect := filter.Check(createBar(t, sym, 9998.0+float64(i), 10.0))
		assert.False(t, suspect)
	}

	// 10003: deviation from lastClose(10002) = 0.01%, z = 3/1.41 = 2.12 < 4.0
	suspect := filter.Check(createBar(t, sym, 10003.0, 10.0))
	assert.False(t, suspect, "Normal bar should pass")
}

func TestZScoreFilter_InsufficientData_NormalBar(t *testing.T) {
	filter := ingestion.NewZScoreFilter(100, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	for i := 0; i < 10; i++ {
		suspect := filter.Check(createBar(t, sym, 100.0, 10.0))
		assert.False(t, suspect, "Should pass when insufficient data")
	}

	// Small deviation (1%) passes during warmup — Layer 1 allows, Layer 2 inactive.
	suspect := filter.Check(createBar(t, sym, 101.0, 10.0))
	assert.False(t, suspect, "Small deviation should pass during warmup")
}

func TestZScoreFilter_DeviationRejectsDuringWarmup(t *testing.T) {
	filter := ingestion.NewZScoreFilter(100, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	filter.Check(createBar(t, sym, 100.0, 10.0))

	// Extreme outlier caught by bar-over-bar deviation even during warmup.
	suspect := filter.Check(createBar(t, sym, 200.0, 10.0))
	assert.True(t, suspect, "Extreme deviation should be rejected even during warmup")
}

func TestZScoreFilter_DetectsAnomaly(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	// Use prices 9800-9802 so a +6 move is only ~0.06% (passes Layer 1)
	// but 6/stddev(~1.41) = z≈4.24 > 4.0 triggers Layer 2.
	for i := 0; i < 5; i++ {
		suspect := filter.Check(createBar(t, sym, 9800.0+float64(i), 10.0))
		assert.False(t, suspect)
	}

	// Price 9808: diff=6 from mean 9802, z = 6/1.414 = 4.24 > 4.0. Volume z=0 < 2.0 → rejected.
	suspect := filter.Check(createBar(t, sym, 9808.0, 10.0))
	assert.True(t, suspect, "Anomalous bar without matching volume should be rejected")
}

func TestZScoreFilter_AnomalyWithMatchingVolumePasses(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	// Use prices 9800-9802 so a move to 9806 is only ~0.04% (passes Layer 1 at 1.5%)
	// but still triggers the Z-score (Layer 2).
	for i := 0; i < 5; i++ {
		suspect := filter.Check(createBar(t, sym, 9800.0+float64(i), 8.0+float64(i)))
		assert.False(t, suspect)
	}

	// Price 9808: z = 6/1.414 = 4.24 > 4.0, volume 16: z = 6/1.414 = 4.24 > 2.0.
	// Both price and volume anomalous → volume confirms the move → passes.
	suspect := filter.Check(createBar(t, sym, 9808.0, 16.0))
	assert.False(t, suspect, "Anomalous bar WITH matching volume should pass")
}

func TestZScoreFilter_RollingWindow(t *testing.T) {
	filter := ingestion.NewZScoreFilter(3, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	filter.Check(createBar(t, sym, 10000.0, 10.0))
	filter.Check(createBar(t, sym, 10000.0, 10.0))
	filter.Check(createBar(t, sym, 10000.0, 10.0))

	// stddev=0, any diff > 0 → infinite z-score. Also 10001 vs 10000 = 0.01% < 1.5%.
	// So this tests Layer 2 (Z-score) rejection with zero stddev.
	suspect := filter.Check(createBar(t, sym, 10001.0, 10.0))
	assert.True(t, suspect, "Non-zero diff with zero stddev should be rejected")
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

func TestZScoreFilter_DetectsHighWickAnomaly(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	for i := 0; i < 5; i++ {
		filter.Check(createBar(t, sym, 9998.0+float64(i), 8.0+float64(i)))
	}

	// High=10200 vs lastClose=10002: deviation 1.98% > 1.5% → caught by Layer 1.
	suspect := filter.Check(createBarOHLC(t, sym, 10000.0, 10200.0, 9999.0, 10000.0, 10.0))
	assert.True(t, suspect, "Bar with anomalous high wick should be rejected")
}

func TestZScoreFilter_DetectsLowWickAnomaly(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	for i := 0; i < 5; i++ {
		filter.Check(createBar(t, sym, 9998.0+float64(i), 8.0+float64(i)))
	}

	// Low=9800 vs lastClose=10002: deviation 2.02% > 1.5% → caught by Layer 1.
	suspect := filter.Check(createBarOHLC(t, sym, 10000.0, 10001.0, 9800.0, 10000.0, 10.0))
	assert.True(t, suspect, "Bar with anomalous low wick should be rejected")
}

func TestZScoreFilter_NormalWickPasses(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	for i := 0; i < 5; i++ {
		filter.Check(createBar(t, sym, 9998.0+float64(i), 10.0))
	}

	// OHLC all within 1.5% of lastClose(10002) AND within 4σ of close mean(10000, stddev≈1.41).
	// highZ = |10005-10000|/1.414 = 3.54 < 4.0, lowZ = |9995-10000|/1.414 = 3.54 < 4.0.
	suspect := filter.Check(createBarOHLC(t, sym, 10000.0, 10005.0, 9995.0, 10001.0, 10.0))
	assert.False(t, suspect, "Bar with normal wicks should pass")
}

func TestZScoreFilter_SeedEliminatesWarmupBlindSpot(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")
	filter.SetMaxDeviation(sym, ingestion.DeviationCrypto)

	seedBars := make([]domain.MarketBar, 5)
	for i := range seedBars {
		seedBars[i] = createBar(t, sym, 67000.0+float64(i), 100.0)
	}
	n := filter.Seed(sym, seedBars)
	assert.Equal(t, 5, n)

	// Phantom wick: high=$69,450 vs lastClose=67004 → deviation 3.65% > 3% (crypto).
	suspect := filter.Check(createBarOHLC(t, sym, 67005.0, 69450.0, 67000.0, 67005.0, 100.0))
	assert.True(t, suspect, "Phantom wick should be caught immediately after seeded restart")
}

func TestZScoreFilter_SeedPartialWindow(t *testing.T) {
	filter := ingestion.NewZScoreFilter(20, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")
	filter.SetMaxDeviation(sym, ingestion.DeviationCrypto)

	seedBars := make([]domain.MarketBar, 10)
	for i := range seedBars {
		seedBars[i] = createBar(t, sym, 67000.0+float64(i), 100.0)
	}
	n := filter.Seed(sym, seedBars)
	assert.Equal(t, 10, n)

	// Phantom: high=$69,450 vs lastClose=67009 → 3.65% > 3% (crypto) → caught.
	suspect := filter.Check(createBarOHLC(t, sym, 67010.0, 69450.0, 67005.0, 67010.0, 100.0))
	assert.True(t, suspect, "Layer 1 catches phantom even with partial Z-score window")

	suspect = filter.Check(createBar(t, sym, 67010.0, 100.0))
	assert.False(t, suspect, "Normal bar should pass after partial seed")
}

func TestZScoreFilter_SeedEmptyBars(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")

	n := filter.Seed(sym, nil)
	assert.Equal(t, 0, n)

	// No seed → no lastClose → Layer 1 inactive. First bar always passes.
	suspect := filter.Check(createBar(t, sym, 99999.0, 10.0))
	assert.False(t, suspect, "First bar with no seed should pass (no reference)")
}

func TestZScoreFilter_PerAssetClassDeviation(t *testing.T) {
	filter := ingestion.NewZScoreFilter(5, 4.0)
	btc, _ := domain.NewSymbol("BTC/USD")
	aapl, _ := domain.NewSymbol("AAPL")
	filter.SetMaxDeviation(btc, ingestion.DeviationCrypto)
	filter.SetMaxDeviation(aapl, ingestion.DeviationEquity)

	filter.Check(createBar(t, btc, 67000.0, 100.0))
	filter.Check(createBar(t, aapl, 200.0, 100.0))

	// BTC: 5% move ($70,350) → 3350/67000 = 5% > 3% (crypto) → rejected.
	suspect := filter.Check(createBar(t, btc, 70350.0, 100.0))
	assert.True(t, suspect, "5% BTC move should be rejected with crypto threshold")

	// AAPL: 5% move ($210) → 10/200 = 5% < 10% (equity) → passes Layer 1.
	suspect = filter.Check(createBar(t, aapl, 210.0, 100.0))
	assert.False(t, suspect, "5% AAPL move should pass with equity threshold")
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
