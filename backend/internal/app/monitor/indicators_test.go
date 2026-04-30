package monitor_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestIndicators_RSI_AllUp(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	var snap domain.IndicatorSnapshot
	// 14 up closes
	for i := 0; i < 15; i++ {
		snap = calc.Update(createBar(t, sym, 100.0+float64(i), 10.0))
	}

	assert.InDelta(t, 100.0, snap.RSI, 0.1, "14 up closes should give RSI near 100")
}

func TestIndicators_RSI_AllDown(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	var snap domain.IndicatorSnapshot
	// 14 down closes
	for i := 0; i < 15; i++ {
		snap = calc.Update(createBar(t, sym, 100.0-float64(i), 10.0))
	}

	assert.InDelta(t, 0.0, snap.RSI, 0.1, "14 down closes should give RSI near 0")
}

func TestIndicators_RSI_Mixed(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	var snap domain.IndicatorSnapshot
	// alternating up and down
	price := 100.0
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			price += 1.0
		} else {
			price -= 1.0
		}
		snap = calc.Update(createBar(t, sym, price, 10.0))
	}

	assert.InDelta(t, 50.0, snap.RSI, 0.1, "mixed closes should give RSI near 50")
}

func TestIndicators_RSI_InsufficientData(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	snap := calc.Update(createBar(t, sym, 100.0, 10.0))
	assert.Equal(t, 0.0, snap.RSI, "insufficient data should return 0")
}

// TestIndicators_Update_DedupsReplayBars covers the M3 bridge double-feed
// fix. The backtest and omo-replay paths feed the first ~50 replay bars
// to Service.WarmUp (which calls calc.Update) and then re-feed the same
// bars through the runtime path. Without dedup, every incremental
// accumulator (volumes, vwap, RSI window, MACD/ADX) double-counts. This
// test asserts that feeding the same time-ordered sequence twice produces
// the same snapshot as a single pass.
func TestIndicators_Update_DedupsReplayBars(t *testing.T) {
	sym, _ := domain.NewSymbol("BTC/USD")

	// Build a deterministic sequence of bars with monotonic times so the
	// replay leg uses identical (sym, tf, time) keys to the warmup leg.
	const n = 30
	base := time.Date(2026, 4, 29, 14, 0, 0, 0, time.UTC)
	bars := make([]domain.MarketBar, n)
	for i := 0; i < n; i++ {
		bar, err := domain.NewMarketBar(
			base.Add(time.Duration(i)*time.Minute),
			sym,
			"1m",
			100.0+float64(i),
			101.0+float64(i),
			99.0+float64(i),
			100.5+float64(i),
			10.0,
		)
		if err != nil {
			t.Fatalf("createBar: %v", err)
		}
		bars[i] = bar
	}

	// Single-pass baseline.
	single := monitor.NewIndicatorCalculator()
	var singleSnap domain.IndicatorSnapshot
	for _, b := range bars {
		singleSnap = single.Update(b)
	}

	// Bridge + runtime: feed the first 10 bars (the bridge), then feed
	// all 30 bars (the runtime replay). The first 10 overlap.
	bridged := monitor.NewIndicatorCalculator()
	for _, b := range bars[:10] {
		bridged.Update(b)
	}
	var bridgedSnap domain.IndicatorSnapshot
	for _, b := range bars {
		bridgedSnap = bridged.Update(b)
	}

	// Snapshots from both paths must match — if dedup is missing, the
	// bridged path double-counts the first 10 bars and produces drift in
	// every accumulator-derived field.
	assert.InDelta(t, singleSnap.VWAP, bridgedSnap.VWAP, 1e-9, "VWAP must match")
	assert.InDelta(t, singleSnap.RSI, bridgedSnap.RSI, 1e-9, "RSI must match")
	assert.InDelta(t, singleSnap.EMA9, bridgedSnap.EMA9, 1e-9, "EMA9 must match")
	assert.InDelta(t, singleSnap.EMA21, bridgedSnap.EMA21, 1e-9, "EMA21 must match")
	assert.InDelta(t, singleSnap.MACDLine, bridgedSnap.MACDLine, 1e-9, "MACD line must match")
	assert.InDelta(t, singleSnap.MACDSignal, bridgedSnap.MACDSignal, 1e-9, "MACD signal must match")
	assert.InDelta(t, singleSnap.ATR, bridgedSnap.ATR, 1e-9, "ATR must match")
	assert.InDelta(t, singleSnap.VolumeSMA, bridgedSnap.VolumeSMA, 1e-9, "VolumeSMA must match")
	assert.InDelta(t, singleSnap.BBUpper, bridgedSnap.BBUpper, 1e-9, "BB upper must match")
	assert.InDelta(t, singleSnap.BBLower, bridgedSnap.BBLower, 1e-9, "BB lower must match")
}

func TestIndicators_Stochastic(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	var snap domain.IndicatorSnapshot
	// highest high scenario
	for i := 0; i < 14; i++ {
		snap = calc.Update(createBarDetailed(t, sym, 100, 100+float64(i), 100, 100+float64(i), 10))
	}
	assert.InDelta(t, 100.0, snap.StochK, 0.1, "highest high should give StochK near 100")

	// lowest low scenario
	calc2 := monitor.NewIndicatorCalculator()
	for i := 0; i < 14; i++ {
		snap = calc2.Update(createBarDetailed(t, sym, 100, 100, 100-float64(i), 100-float64(i), 10))
	}
	assert.InDelta(t, 0.0, snap.StochK, 0.1, "lowest low should give StochK near 0")

	// check D is 3-period SMA of K
	// Let's feed some steady values
	calc3 := monitor.NewIndicatorCalculator()
	for i := 0; i < 16; i++ {
		snap = calc3.Update(createBarDetailed(t, sym, 50, 100, 0, 50, 10)) // K will be 50 always
	}
	assert.InDelta(t, 50.0, snap.StochD, 0.1, "StochD should be SMA of StochK")
}

func TestIndicators_EMA(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 25; i++ {
		snap = calc.Update(createBar(t, sym, 50.0, 10.0))
	}
	assert.InDelta(t, 50.0, snap.EMA9, 0.1, "EMA of constant values equals constant")
	assert.InDelta(t, 50.0, snap.EMA21, 0.1, "EMA of constant values equals constant")

	// EMA reacts faster than SMA (in a trend, short EMA > long EMA)
	for i := 0; i < 25; i++ {
		snap = calc.Update(createBar(t, sym, 50.0+float64(i*10), 10.0))
	}
	assert.Greater(t, snap.EMA9, snap.EMA21, "EMA9 should react faster than EMA21 in uptrend")
}

func TestIndicators_VWAP(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	// Single bar
	snap := calc.Update(createBarDetailed(t, sym, 10, 10, 10, 10, 100))
	assert.InDelta(t, 10.0, snap.VWAP, 0.1, "single bar VWAP equals typical price")

	// Multiple bars: VWAP = (10*100 + 20*200) / (100 + 200) = 5000 / 300 = 16.666
	snap = calc.Update(createBarDetailed(t, sym, 20, 20, 20, 20, 200))
	assert.InDelta(t, 16.666, snap.VWAP, 0.01, "multiple bars VWAP cumulative")
}

func TestIndicators_ATR_InsufficientData(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	snap := calc.Update(createBarDetailed(t, sym, 100, 105, 95, 100, 10))
	assert.Equal(t, 0.0, snap.ATR, "ATR should be zero with insufficient data")

	for i := 1; i < 14; i++ {
		snap = calc.Update(createBarDetailed(t, sym, 100, 105, 95, 100, 10))
	}
	assert.Equal(t, 0.0, snap.ATR, "ATR should be zero before warmup (need 15 bars)")
}

func TestIndicators_ATR_AfterWarmup(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 16; i++ {
		snap = calc.Update(createBarDetailed(t, sym, 100, 110, 90, 100, 10))
	}
	assert.Greater(t, snap.ATR, 0.0, "ATR should be positive after warmup")
	assert.InDelta(t, 20.0, snap.ATR, 0.5, "ATR of constant H=110,L=90 bars should be ~20")
}

func TestIndicators_ATR_KnownValues(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	// 15 bars with High-Low=10 and no gaps → True Range = 10 for each → ATR = 10
	for i := 0; i < 15; i++ {
		base := 100.0 + float64(i)
		calc.Update(createBarDetailed(t, sym, base, base+5, base-5, base, 10))
	}
	snap := calc.Update(createBarDetailed(t, sym, 115, 120, 110, 115, 10))
	assert.InDelta(t, 10.0, snap.ATR, 1.0, "ATR of H-L=10 bars should be ~10")
}

func TestIndicators_ATR_ZeroVolatility(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 16; i++ {
		snap = calc.Update(createBarDetailed(t, sym, 100, 100, 100, 100, 10))
	}
	assert.InDelta(t, 0.0, snap.ATR, 1e-10, "ATR of zero-volatility bars should be zero")
}

func TestIndicators_EMA50_InitializesAtBar50(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 49; i++ {
		snap = calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	assert.Equal(t, 0.0, snap.EMA50, "EMA50 should be zero before 50 bars")

	snap = calc.Update(createBar(t, sym, 100.0, 10.0))
	assert.InDelta(t, 100.0, snap.EMA50, 0.01, "EMA50 should initialize to SMA at bar 50")
}

func TestIndicators_EMA50_ExponentialAfterInit(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	for i := 0; i < 50; i++ {
		calc.Update(createBar(t, sym, 100.0, 10.0))
	}

	var snap domain.IndicatorSnapshot
	for i := 0; i < 10; i++ {
		snap = calc.Update(createBar(t, sym, 200.0, 10.0))
	}
	assert.Greater(t, snap.EMA50, 100.0, "EMA50 should rise toward 200 in uptrend")
	assert.Less(t, snap.EMA50, 200.0, "EMA50 should lag behind close in uptrend")
}

func TestIndicators_EMA50_IndependentFromEMA9EMA21(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 60; i++ {
		snap = calc.Update(createBar(t, sym, 100.0+float64(i), 10.0))
	}
	assert.NotEqual(t, snap.EMA9, snap.EMA50)
	assert.NotEqual(t, snap.EMA21, snap.EMA50)
	assert.Greater(t, snap.EMA9, snap.EMA50, "EMA9 reacts faster in uptrend")
}

func TestComputeStaticEMA_InsufficientData(t *testing.T) {
	assert.Equal(t, 0.0, monitor.ComputeStaticEMA(nil, 10))
	assert.Equal(t, 0.0, monitor.ComputeStaticEMA([]float64{1, 2}, 10))
	assert.Equal(t, 0.0, monitor.ComputeStaticEMA([]float64{1}, 0))
}

func TestComputeStaticEMA_ConstantPrice(t *testing.T) {
	closes := make([]float64, 200)
	for i := range closes {
		closes[i] = 50.0
	}
	assert.InDelta(t, 50.0, monitor.ComputeStaticEMA(closes, 200), 0.001)
}

func TestComputeStaticEMA_ExactPeriod(t *testing.T) {
	closes := make([]float64, 9)
	for i := range closes {
		closes[i] = float64(i + 1)
	}
	assert.InDelta(t, 5.0, monitor.ComputeStaticEMA(closes, 9), 0.001)
}

func TestComputeStaticEMA_UpTrend(t *testing.T) {
	closes := make([]float64, 50)
	for i := range closes {
		closes[i] = float64(i + 1)
	}
	ema := monitor.ComputeStaticEMA(closes, 10)
	assert.Greater(t, ema, 0.0)
	assert.Less(t, ema, 50.0)
	assert.Greater(t, ema, 25.0, "EMA should track recent prices in uptrend")
}

func TestComputeStaticEMA_MatchesStreaming(t *testing.T) {
	closes := make([]float64, 50)
	for i := range closes {
		closes[i] = 100.0 + float64(i)
	}
	staticEMA := monitor.ComputeStaticEMA(closes, 50)

	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")
	var snap domain.IndicatorSnapshot
	for _, c := range closes {
		snap = calc.Update(createBar(t, sym, c, 10.0))
	}
	assert.InDelta(t, snap.EMA50, staticEMA, 0.001,
		"ComputeStaticEMA should match streaming EMA50 for identical input")
}

func TestIndicators_VolumeSMA(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("BTC/USD")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 20; i++ {
		snap = calc.Update(createBar(t, sym, 100.0, float64((i+1)*10)))
	}
	// volumes: 10, 20, ..., 200. Sum = 2100. Mean = 105
	assert.InDelta(t, 105.0, snap.VolumeSMA, 0.1, "VolumeSMA returns mean of last 20 volumes")
}

// ---------------------------------------------------------------------------
// Bollinger Bands
// ---------------------------------------------------------------------------

func TestIndicators_BB_ConstantPrice(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 20; i++ {
		snap = calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	// Constant price → stddev = 0 → bands collapse to SMA
	assert.InDelta(t, 100.0, snap.BBMiddle, 0.01, "BB middle = SMA(20)")
	assert.InDelta(t, 100.0, snap.BBUpper, 0.01, "BB upper = middle when stddev=0")
	assert.InDelta(t, 100.0, snap.BBLower, 0.01, "BB lower = middle when stddev=0")
	assert.InDelta(t, 0.0, snap.BBBandwidth, 0.01, "bandwidth=0 when price constant")
}

func TestIndicators_BB_KnownValues(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	// 20 bars: 10 at 90, 10 at 110 → SMA = 100
	var snap domain.IndicatorSnapshot
	for i := 0; i < 10; i++ {
		calc.Update(createBar(t, sym, 90.0, 10.0))
	}
	for i := 0; i < 10; i++ {
		snap = calc.Update(createBar(t, sym, 110.0, 10.0))
	}
	// SMA(20) = (10*90 + 10*110)/20 = 100
	assert.InDelta(t, 100.0, snap.BBMiddle, 0.01)
	// Sample StdDev = sqrt((10*100 + 10*100)/19) = sqrt(2000/19) ≈ 10.2598
	assert.InDelta(t, 120.5196, snap.BBUpper, 0.01, "upper = 100 + 2*10.2598")
	assert.InDelta(t, 79.4804, snap.BBLower, 0.01, "lower = 100 - 2*10.2598")
	// %B = (close - lower) / (upper - lower) = (110 - 79.48) / 41.04 ≈ 0.7437
	assert.InDelta(t, 0.7437, snap.BBPercentB, 0.01)
	// Bandwidth = (upper - lower) / middle ≈ 41.04 / 100 ≈ 0.4104
	assert.InDelta(t, 0.4104, snap.BBBandwidth, 0.01)
}

func TestIndicators_BB_InsufficientData(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 19; i++ {
		snap = calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	assert.Equal(t, 0.0, snap.BBMiddle, "BB should be zero before 20 bars")
	assert.Equal(t, 0.0, snap.BBUpper)
	assert.Equal(t, 0.0, snap.BBLower)
}

func TestIndicators_BB_PercentB_AboveUpperBand(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	// Build 19 bars at 100, then spike to 200
	for i := 0; i < 19; i++ {
		calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	snap := calc.Update(createBar(t, sym, 200.0, 10.0))
	// Close is well above upper band → %B > 1.0
	assert.Greater(t, snap.BBPercentB, 1.0, "%%B > 1 when close above upper band")
}

// ---------------------------------------------------------------------------
// MACD
// ---------------------------------------------------------------------------

func TestIndicators_MACD_InsufficientData(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 25; i++ {
		snap = calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	// Need 26 bars for EMA(26) to initialize
	assert.Equal(t, 0.0, snap.MACDLine, "MACD should be zero before EMA26 initializes")
}

func TestIndicators_MACD_ConstantPrice(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 40; i++ {
		snap = calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	// Constant price → EMA12 = EMA26 = 100 → MACD line = 0
	assert.InDelta(t, 0.0, snap.MACDLine, 0.01, "MACD line = 0 for constant price")
	assert.InDelta(t, 0.0, snap.MACDSignal, 0.01, "MACD signal = 0 for constant price")
	assert.InDelta(t, 0.0, snap.MACDHistogram, 0.01, "MACD histogram = 0 for constant price")
}

func TestIndicators_MACD_Uptrend(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	// Seed at 100, then strong uptrend
	for i := 0; i < 26; i++ {
		calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	for i := 0; i < 20; i++ {
		snap = calc.Update(createBar(t, sym, 100.0+float64(i+1)*2.0, 10.0))
	}
	// In uptrend, EMA12 reacts faster → MACD line > 0
	assert.Greater(t, snap.MACDLine, 0.0, "MACD line positive in uptrend")
	assert.Greater(t, snap.MACDHistogram, 0.0, "MACD histogram positive in accelerating uptrend")
}

func TestIndicators_MACD_Downtrend(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 26; i++ {
		calc.Update(createBar(t, sym, 200.0, 10.0))
	}
	for i := 0; i < 20; i++ {
		snap = calc.Update(createBar(t, sym, 200.0-float64(i+1)*2.0, 10.0))
	}
	// In downtrend, EMA12 drops faster → MACD line < 0
	assert.Less(t, snap.MACDLine, 0.0, "MACD line negative in downtrend")
}

func TestIndicators_MACD_SignalCrossover(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	// Phase 1: flat → MACD ~ 0
	for i := 0; i < 35; i++ {
		calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	// Phase 2: sharp uptrend → MACD line rises above signal
	var snap domain.IndicatorSnapshot
	for i := 0; i < 15; i++ {
		snap = calc.Update(createBar(t, sym, 100.0+float64(i+1)*5.0, 10.0))
	}
	assert.Greater(t, snap.MACDLine, snap.MACDSignal,
		"MACD line should be above signal in strong uptrend (bullish crossover)")
}

// ---------------------------------------------------------------------------
// ADX
// ---------------------------------------------------------------------------

func TestIndicators_ADX_InsufficientData(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 14; i++ {
		snap = calc.Update(createBarDetailed(t, sym, 100, 110, 90, 100, 10))
	}
	assert.Equal(t, 0.0, snap.ADX, "ADX should be zero before warmup")
}

func TestIndicators_ADX_StrongUptrend(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	// Strong uptrend: steadily rising highs and lows
	for i := 0; i < 40; i++ {
		base := 100.0 + float64(i)*2.0
		snap = calc.Update(createBarDetailed(t, sym, base, base+5, base-2, base+3, 10))
	}
	assert.Greater(t, snap.ADX, 20.0, "ADX should be high (>20) in strong trend")
}

func TestIndicators_ADX_Choppy(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	// Choppy: alternating up/down with overlapping ranges
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			snap = calc.Update(createBarDetailed(t, sym, 100, 105, 95, 103, 10))
		} else {
			snap = calc.Update(createBarDetailed(t, sym, 103, 108, 98, 100, 10))
		}
	}
	// In a range-bound market, ADX should be relatively low
	assert.Less(t, snap.ADX, 30.0, "ADX should be moderate/low in choppy market")
}

func TestIndicators_ADX_Positive(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 30; i++ {
		base := 100.0 + float64(i)
		snap = calc.Update(createBarDetailed(t, sym, base, base+5, base-5, base, 10))
	}
	assert.Greater(t, snap.ADX, 0.0, "ADX should always be positive after warmup")
}

// ---------------------------------------------------------------------------
// Regime Score
// ---------------------------------------------------------------------------

func TestIndicators_RegimeScore_Range(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	// Feed enough bars for all regime factors to be active
	for i := 0; i < 80; i++ {
		base := 100.0 + float64(i)*0.5
		snap = calc.Update(createBarDetailed(t, sym, base, base+3, base-3, base+1, 10))
	}
	assert.GreaterOrEqual(t, snap.RegimeScore, 0.0, "regime score >= 0")
	assert.LessOrEqual(t, snap.RegimeScore, 1.0, "regime score <= 1")
}

func TestIndicators_RegimeScore_ZeroBeforeWarmup(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	snap := calc.Update(createBar(t, sym, 100.0, 10.0))
	assert.Equal(t, 0.0, snap.RegimeScore, "regime score = 0 with no factors active")
}

func TestIndicators_RegimeScore_StrongTrend(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	// Strong uptrend with expanding volatility → all 3 factors should fire
	for i := 0; i < 80; i++ {
		base := 100.0 + float64(i)*3.0 // steep trend
		spread := 2.0 + float64(i)*0.1  // expanding volatility
		snap = calc.Update(createBarDetailed(t, sym, base, base+spread, base-spread, base+spread*0.5, 10))
	}
	// With strong trend + high ADX + expanding BB bandwidth + steep EMA50 slope
	// regime score should be high
	assert.GreaterOrEqual(t, snap.RegimeScore, 0.5, "regime score should be high in strong trend")
}

// ---------------------------------------------------------------------------
// Custom EMA (RegisterEMAConfig)
// ---------------------------------------------------------------------------

func TestIndicators_CustomEMA_Basic(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	calc.RegisterEMAConfig("AAPL", "1m", 5, 15)
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	// Feed enough bars for both periods to initialize
	for i := 0; i < 20; i++ {
		snap = calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	assert.InDelta(t, 100.0, snap.EMAFast, 0.01, "custom EMA fast = price when constant")
	assert.InDelta(t, 100.0, snap.EMASlow, 0.01, "custom EMA slow = price when constant")
	assert.Equal(t, 5, snap.EMAFastPeriod)
	assert.Equal(t, 15, snap.EMASlowPeriod)
}

func TestIndicators_CustomEMA_FastReactsFaster(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	calc.RegisterEMAConfig("AAPL", "1m", 5, 15)
	sym, _ := domain.NewSymbol("AAPL")

	// Seed at 100
	for i := 0; i < 15; i++ {
		calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	// Uptrend
	var snap domain.IndicatorSnapshot
	for i := 0; i < 10; i++ {
		snap = calc.Update(createBar(t, sym, 100.0+float64(i+1)*5.0, 10.0))
	}
	assert.Greater(t, snap.EMAFast, snap.EMASlow, "fast EMA reacts quicker in uptrend")
}

func TestIndicators_CustomEMA_InvalidConfig(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	// fast >= slow should be rejected
	calc.RegisterEMAConfig("AAPL", "1m", 15, 5)
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 20; i++ {
		snap = calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	// Should remain zero since config was rejected
	assert.Equal(t, 0.0, snap.EMAFast, "invalid config should not set EMAFast")
	assert.Equal(t, 0.0, snap.EMASlow, "invalid config should not set EMASlow")
}

func TestIndicators_CustomEMA_NotSetWithoutConfig(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 20; i++ {
		snap = calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	assert.Equal(t, 0.0, snap.EMAFast, "EMAFast = 0 without RegisterEMAConfig")
	assert.Equal(t, 0.0, snap.EMASlow, "EMASlow = 0 without RegisterEMAConfig")
	assert.Equal(t, 0, snap.EMAFastPeriod)
	assert.Equal(t, 0, snap.EMASlowPeriod)
}

// ---------------------------------------------------------------------------
// VWAP Session Reset
// ---------------------------------------------------------------------------

func TestIndicators_VWAP_SessionReset(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	// Session 1: bar at price 100, volume 100
	calc.Update(createBarDetailed(t, sym, 100, 100, 100, 100, 100))
	// Bar at price 200, volume 100 → VWAP = (100*100 + 200*100)/200 = 150
	snap := calc.Update(createBarDetailed(t, sym, 200, 200, 200, 200, 100))
	assert.InDelta(t, 150.0, snap.VWAP, 0.01, "VWAP before reset")

	// Reset session
	calc.ResetSession("AAPL", "1m")

	// Session 2: bar at price 50 → VWAP should be 50 (fresh start)
	snap = calc.Update(createBarDetailed(t, sym, 50, 50, 50, 50, 100))
	assert.InDelta(t, 50.0, snap.VWAP, 0.01, "VWAP should restart after session reset")
}

func TestIndicators_VWAP_SessionReset_PreservesOtherIndicators(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	// Build up enough bars for EMA9 to initialize
	for i := 0; i < 12; i++ {
		calc.Update(createBar(t, sym, 100.0, 10.0))
	}
	calc.ResetSession("AAPL", "1m")

	snap := calc.Update(createBar(t, sym, 100.0, 10.0))
	// EMA9 should still be valid after VWAP reset
	assert.InDelta(t, 100.0, snap.EMA9, 0.1, "EMA9 survives VWAP session reset")
}

// ---------------------------------------------------------------------------
// VWAP SD
// ---------------------------------------------------------------------------

func TestIndicators_VWAPSD_PositiveWithVariance(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	// Feed bars with varying prices to create variance
	var snap domain.IndicatorSnapshot
	prices := []float64{100, 102, 98, 104, 96, 106, 94, 108, 92, 110}
	for _, p := range prices {
		snap = calc.Update(createBarDetailed(t, sym, p, p+1, p-1, p, 100))
	}
	assert.Greater(t, snap.VWAPSD, 0.0, "VWAP SD should be positive with price variance")
}

func TestIndicators_VWAPSD_ZeroForConstantPrice(t *testing.T) {
	calc := monitor.NewIndicatorCalculator()
	sym, _ := domain.NewSymbol("AAPL")

	var snap domain.IndicatorSnapshot
	for i := 0; i < 10; i++ {
		snap = calc.Update(createBarDetailed(t, sym, 100, 100, 100, 100, 100))
	}
	assert.InDelta(t, 0.0, snap.VWAPSD, 0.01, "VWAP SD = 0 for constant typical price")
}

// ---------------------------------------------------------------------------
// ComputeNR7
// ---------------------------------------------------------------------------

func TestComputeNR7_NarrowestLast(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	bars := make([]domain.MarketBar, 7)
	for i := 0; i < 6; i++ {
		bars[i] = createBarDetailed(t, sym, 100, 110, 90, 100, 10) // range = 20
	}
	bars[6] = createBarDetailed(t, sym, 100, 101, 99, 100, 10) // range = 2
	assert.True(t, monitor.ComputeNR7(bars), "last bar has narrowest range")
}

func TestComputeNR7_NotNarrowest(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	bars := make([]domain.MarketBar, 7)
	for i := 0; i < 7; i++ {
		bars[i] = createBarDetailed(t, sym, 100, 110, 90, 100, 10) // all same range
	}
	assert.False(t, monitor.ComputeNR7(bars), "equal ranges → not NR7")
}

func TestComputeNR7_InsufficientBars(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	bars := make([]domain.MarketBar, 5)
	for i := 0; i < 5; i++ {
		bars[i] = createBarDetailed(t, sym, 100, 110, 90, 100, 10)
	}
	assert.False(t, monitor.ComputeNR7(bars), "need 7 bars minimum")
}

// ---------------------------------------------------------------------------
// ComputeDailyATR
// ---------------------------------------------------------------------------

func TestComputeDailyATR_KnownValues(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	// 15 bars with H-L=10, no gaps
	bars := make([]domain.MarketBar, 15)
	for i := 0; i < 15; i++ {
		base := 100.0 + float64(i)
		bars[i] = createBarDetailed(t, sym, base, base+5, base-5, base, 10)
	}
	atr := monitor.ComputeDailyATR(bars, 14)
	assert.InDelta(t, 10.0, atr, 1.0, "daily ATR with H-L=10 bars")
}

func TestComputeDailyATR_InsufficientData(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	bars := make([]domain.MarketBar, 5)
	for i := 0; i < 5; i++ {
		bars[i] = createBarDetailed(t, sym, 100, 110, 90, 100, 10)
	}
	assert.Equal(t, 0.0, monitor.ComputeDailyATR(bars, 14))
}

// ---------------------------------------------------------------------------
// ComputeRealizedVol
// ---------------------------------------------------------------------------

func TestComputeRealizedVol_ZeroForConstantPrice(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	bars := make([]domain.MarketBar, 25)
	for i := 0; i < 25; i++ {
		bars[i] = createBarDetailed(t, sym, 100, 100, 100, 100, 10)
	}
	assert.InDelta(t, 0.0, monitor.ComputeRealizedVol(bars, 20), 0.01)
}

func TestComputeRealizedVol_PositiveForVolatilePrice(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	bars := make([]domain.MarketBar, 25)
	for i := 0; i < 25; i++ {
		// Oscillating prices for volatility
		price := 100.0
		if i%2 == 0 {
			price = 105.0
		} else {
			price = 95.0
		}
		bars[i] = createBarDetailed(t, sym, price, price+1, price-1, price, 10)
	}
	rv := monitor.ComputeRealizedVol(bars, 20)
	assert.Greater(t, rv, 0.0, "realized vol should be positive for oscillating prices")
}

func TestComputeRealizedVol_InsufficientData(t *testing.T) {
	sym, _ := domain.NewSymbol("AAPL")
	bars := make([]domain.MarketBar, 10)
	for i := 0; i < 10; i++ {
		bars[i] = createBarDetailed(t, sym, 100, 105, 95, 100, 10)
	}
	assert.Equal(t, 0.0, monitor.ComputeRealizedVol(bars, 20))
}
