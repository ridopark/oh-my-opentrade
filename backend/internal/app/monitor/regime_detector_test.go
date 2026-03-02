package monitor_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createSnapshot(sym domain.Symbol, rsi, stochK, stochD, ema9, ema21, vwap float64) domain.IndicatorSnapshot {
	snap, _ := domain.NewIndicatorSnapshot(
		time.Now(),
		sym,
		"1m",
		rsi, stochK, stochD, ema9, ema21, vwap, 1000, 1000,
	)
	return snap
}

func TestRegimeDetector_Trend(t *testing.T) {
	rd := monitor.NewRegimeDetector()
	sym, _ := domain.NewSymbol("BTC/USD")

	// EMA9 > EMA21 significantly -> TREND
	snap := createSnapshot(sym, 60.0, 80.0, 75.0, 105.0, 100.0, 102.0)
	regime, changed := rd.Detect(snap)

	assert.True(t, changed, "should detect initial regime change")
	assert.Equal(t, domain.RegimeTrend, regime.Type)
	assert.Greater(t, regime.Strength, 0.5, "trend strength should be > 0.5")
	assert.LessOrEqual(t, regime.Strength, 1.0)
}

func TestRegimeDetector_Balance(t *testing.T) {
	rd := monitor.NewRegimeDetector()
	sym, _ := domain.NewSymbol("BTC/USD")

	// EMA9 ≈ EMA21 -> BALANCE
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

	// RSI > 70 + Stoch divergence (StochK crossing below StochD at extremes) -> REVERSAL
	snap := createSnapshot(sym, 75.0, 80.0, 85.0, 105.0, 100.0, 102.0)
	regime, changed := rd.Detect(snap)

	assert.True(t, changed)
	assert.Equal(t, domain.RegimeReversal, regime.Type)
	assert.GreaterOrEqual(t, regime.Strength, 0.0)
	assert.LessOrEqual(t, regime.Strength, 1.0)
}

func TestRegimeDetector_Transition(t *testing.T) {
	rd := monitor.NewRegimeDetector()
	sym, _ := domain.NewSymbol("BTC/USD")

	// 1. Start with TREND
	snap1 := createSnapshot(sym, 60.0, 80.0, 75.0, 105.0, 100.0, 102.0)
	regime1, changed1 := rd.Detect(snap1)
	require.True(t, changed1)
	require.Equal(t, domain.RegimeTrend, regime1.Type)

	// 2. Stay in TREND
	snap2 := createSnapshot(sym, 62.0, 82.0, 78.0, 106.0, 100.5, 103.0)
	regime2, changed2 := rd.Detect(snap2)
	assert.False(t, changed2, "should not emit change if regime remains TREND")
	assert.Equal(t, domain.RegimeTrend, regime2.Type)

	// 3. Transition to BALANCE
	snap3 := createSnapshot(sym, 50.0, 50.0, 50.0, 101.0, 100.8, 101.0)
	regime3, changed3 := rd.Detect(snap3)
	assert.True(t, changed3, "should emit change when transitioning from TREND to BALANCE")
	assert.Equal(t, domain.RegimeBalance, regime3.Type)
}
