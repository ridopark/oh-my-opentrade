package monitor_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createSnapshot builds a test snapshot. EMA21 and EMA50 are used for regime detection.
func createSnapshot(sym domain.Symbol, rsi, stochK, stochD, ema21, ema50, vwap float64) domain.IndicatorSnapshot {
	snap, _ := domain.NewIndicatorSnapshot(
		time.Now(),
		sym,
		"1m",
		rsi, stochK, stochD, ema21, ema21, vwap, 1000, 1000,
	)
	snap.EMA50 = ema50
	return snap
}

func TestRegimeDetector_Trend(t *testing.T) {
	rd := monitor.NewRegimeDetector()
	sym, _ := domain.NewSymbol("BTC/USD")

	// EMA21=102 vs EMA50=100 → 2% divergence > 0.3% threshold → TREND
	snap := createSnapshot(sym, 60.0, 80.0, 75.0, 102.0, 100.0, 101.0)
	regime, changed := rd.Detect(snap)

	assert.True(t, changed, "should detect initial regime change")
	assert.Equal(t, domain.RegimeTrend, regime.Type)
	assert.Greater(t, regime.Strength, 0.5, "trend strength should be > 0.5")
	assert.LessOrEqual(t, regime.Strength, 1.0)
}

func TestRegimeDetector_Balance(t *testing.T) {
	rd := monitor.NewRegimeDetector()
	sym, _ := domain.NewSymbol("BTC/USD")

	// EMA21 ≈ EMA50 (0.1% divergence < 0.3% threshold) → BALANCE
	snap := createSnapshot(sym, 50.0, 50.0, 50.0, 100.1, 100.0, 100.05)
	regime, changed := rd.Detect(snap)

	assert.True(t, changed)
	assert.Equal(t, domain.RegimeBalance, regime.Type)
	assert.GreaterOrEqual(t, regime.Strength, 0.0)
	assert.LessOrEqual(t, regime.Strength, 1.0)
}

func TestRegimeDetector_Reversal(t *testing.T) {
	rd := monitor.NewRegimeDetector()
	sym, _ := domain.NewSymbol("BTC/USD")

	// RSI > 70 + StochK < StochD → REVERSAL (even with EMA divergence)
	snap := createSnapshot(sym, 75.0, 80.0, 85.0, 102.0, 100.0, 101.0)
	regime, changed := rd.Detect(snap)

	assert.True(t, changed)
	assert.Equal(t, domain.RegimeReversal, regime.Type)
	assert.GreaterOrEqual(t, regime.Strength, 0.0)
	assert.LessOrEqual(t, regime.Strength, 1.0)
}

func TestRegimeDetector_Transition(t *testing.T) {
	rd := monitor.NewRegimeDetector()
	sym, _ := domain.NewSymbol("BTC/USD")

	// 1. Start with TREND (EMA21=102 vs EMA50=100 → 2% > 0.3%)
	snap1 := createSnapshot(sym, 60.0, 80.0, 75.0, 102.0, 100.0, 101.0)
	regime1, changed1 := rd.Detect(snap1)
	require.True(t, changed1)
	require.Equal(t, domain.RegimeTrend, regime1.Type)

	// 2. Stay in TREND (EMA21=103 vs EMA50=100.5 → 2.5% > 0.3%)
	snap2 := createSnapshot(sym, 62.0, 82.0, 78.0, 103.0, 100.5, 102.0)
	regime2, changed2 := rd.Detect(snap2)
	assert.False(t, changed2, "should not emit change if regime remains TREND")
	assert.Equal(t, domain.RegimeTrend, regime2.Type)

	// 3. Transition to BALANCE (EMA21=100.1 vs EMA50=100.0 → 0.1% < 0.3%)
	snap3 := createSnapshot(sym, 50.0, 50.0, 50.0, 100.1, 100.0, 100.05)
	regime3, changed3 := rd.Detect(snap3)
	assert.True(t, changed3, "should emit change when transitioning from TREND to BALANCE")
	assert.Equal(t, domain.RegimeBalance, regime3.Type)
}
