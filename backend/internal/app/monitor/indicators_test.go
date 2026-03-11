package monitor_test

import (
	"testing"

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
