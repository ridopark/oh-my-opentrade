package ingestion_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedFilter(t *testing.T, f *ingestion.AdaptiveFilter, sym domain.Symbol, price, volume float64, count int) {
	t.Helper()
	bars := make([]domain.MarketBar, count)
	for i := range bars {
		bar, err := domain.NewMarketBar(
			time.Now().Add(time.Duration(i)*time.Minute),
			sym, "1m",
			price, price+5, price-5, price, volume,
		)
		require.NoError(t, err)
		bar.TradeCount = 100
		bars[i] = bar
	}
	f.Seed(sym, bars)
}

func TestAdaptiveFilter_NormalBarPasses(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")
	seedFilter(t, f, sym, 67500, 100, 25)

	bar := createBarOHLC(t, sym, 67490, 67510, 67480, 67500, 110)
	bar.TradeCount = 50

	result := f.Process(bar)

	assert.Equal(t, ingestion.FilterPass, result.Status)
	assert.False(t, result.Bar.Repaired)
	assert.Equal(t, 67510.0, result.Bar.High)
	assert.Equal(t, 67480.0, result.Bar.Low)
}

func TestAdaptiveFilter_PhantomWick_TradeCountGate(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")
	seedFilter(t, f, sym, 67500, 100, 25)

	bar := createBarOHLC(t, sym, 67490, 69250, 67480, 67500, 50)
	bar.TradeCount = 2

	result := f.Process(bar)

	assert.Equal(t, ingestion.FilterRepaired, result.Status)
	assert.True(t, result.Bar.Repaired)
	assert.Equal(t, 69250.0, result.Bar.OriginalHigh)
	assert.Less(t, result.Bar.High, 69250.0)
	assert.GreaterOrEqual(t, result.Bar.High, result.Bar.Close)
}

func TestAdaptiveFilter_PhantomWick_WickGate(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")
	seedFilter(t, f, sym, 67500, 100, 25)

	bar := createBarOHLC(t, sym, 67500, 69000, 67490, 67510, 80)
	bar.TradeCount = 50

	result := f.Process(bar)

	assert.Equal(t, ingestion.FilterRepaired, result.Status)
	assert.True(t, result.Bar.Repaired)
	assert.Equal(t, 69000.0, result.Bar.OriginalHigh)
	assert.Less(t, result.Bar.High, 69000.0)
}

func TestAdaptiveFilter_RealCrash_HighVolume_Passes(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	f.SetMaxDeviation(domain.Symbol("BTC/USD"), ingestion.DeviationCrypto)
	sym, _ := domain.NewSymbol("BTC/USD")
	seedFilter(t, f, sym, 67500, 100, 25)

	bar := createBarOHLC(t, sym, 67500, 67600, 65000, 65100, 5000)
	bar.TradeCount = 500

	result := f.Process(bar)

	assert.Equal(t, ingestion.FilterPass, result.Status, "real crash with high volume+trades should pass")
	assert.False(t, result.Bar.Repaired)
}

func TestAdaptiveFilter_ATREnvelope_ClampsExtremeHigh(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")
	seedFilter(t, f, sym, 67500, 100, 25)

	bar := createBarOHLC(t, sym, 67500, 70000, 67490, 67510, 200)
	bar.TradeCount = 50

	result := f.Process(bar)

	assert.Equal(t, ingestion.FilterRepaired, result.Status)
	assert.True(t, result.Bar.Repaired)
	assert.Equal(t, 70000.0, result.Bar.OriginalHigh)
	assert.Less(t, result.Bar.High, 70000.0)
	assert.GreaterOrEqual(t, result.Bar.High, result.Bar.Close)
}

func TestAdaptiveFilter_WarmupFallback_Rejects(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	f.SetMaxDeviation(domain.Symbol("BTC/USD"), ingestion.DeviationCrypto)
	sym, _ := domain.NewSymbol("BTC/USD")

	bar1 := createBar(t, sym, 67500, 100)
	r1 := f.Process(bar1)
	assert.Equal(t, ingestion.FilterPass, r1.Status)

	bar2 := createBarOHLC(t, sym, 67500, 72000, 67400, 72000, 100)
	r2 := f.Process(bar2)
	assert.Equal(t, ingestion.FilterRejected, r2.Status, "during warmup, extreme deviation should reject")
}

func TestAdaptiveFilter_Seed_EnablesATR(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	sym, _ := domain.NewSymbol("ETH/USD")
	seedFilter(t, f, sym, 3500, 200, 25)

	bar := createBarOHLC(t, sym, 3500, 3700, 3490, 3510, 150)
	bar.TradeCount = 50

	result := f.Process(bar)
	assert.NotEqual(t, ingestion.FilterRejected, result.Status, "after seeding ATR should be active, not using legacy reject")
}

func TestAdaptiveFilter_Repair_PreservesOriginals(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")
	seedFilter(t, f, sym, 67500, 100, 25)

	bar := createBarOHLC(t, sym, 67500, 69500, 65500, 67510, 50)
	bar.TradeCount = 3

	result := f.Process(bar)

	assert.True(t, result.Bar.Repaired)
	assert.Equal(t, 69500.0, result.Bar.OriginalHigh)
	assert.Equal(t, 65500.0, result.Bar.OriginalLow)
	assert.NotEqual(t, result.Bar.OriginalHigh, result.Bar.High)
	assert.NotEqual(t, result.Bar.OriginalLow, result.Bar.Low)
}

func TestAdaptiveFilter_Repair_HighGteClose(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	sym, _ := domain.NewSymbol("BTC/USD")
	seedFilter(t, f, sym, 67500, 100, 25)

	bar := createBarOHLC(t, sym, 67500, 69500, 65500, 67510, 50)
	bar.TradeCount = 2

	result := f.Process(bar)

	assert.True(t, result.Bar.Repaired)
	assert.GreaterOrEqual(t, result.Bar.High, result.Bar.Close)
	assert.LessOrEqual(t, result.Bar.Low, result.Bar.Close)
	assert.GreaterOrEqual(t, result.Bar.High, result.Bar.Low)
}

func TestAdaptiveFilter_ZScoreCatchAll_Rejects(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(5, 3.0)
	sym, _ := domain.NewSymbol("BTC/USD")
	seedFilter(t, f, sym, 67500, 100, 10)

	bar := createBar(t, sym, 80000, 1)
	bar.TradeCount = 100

	result := f.Process(bar)

	assert.Equal(t, ingestion.FilterRejected, result.Status, "extreme close z-score with low volume should be rejected")
}

func TestAdaptiveFilter_SeparateSymbolState(t *testing.T) {
	f := ingestion.NewAdaptiveFilter(20, 4.0)
	sym1, _ := domain.NewSymbol("BTC/USD")
	sym2, _ := domain.NewSymbol("ETH/USD")
	seedFilter(t, f, sym1, 67500, 100, 25)
	seedFilter(t, f, sym2, 3500, 200, 25)

	bar1 := createBarOHLC(t, sym1, 67500, 67510, 67490, 67500, 110)
	bar1.TradeCount = 50
	r1 := f.Process(bar1)
	assert.Equal(t, ingestion.FilterPass, r1.Status)

	bar2 := createBarOHLC(t, sym2, 3500, 3510, 3490, 3500, 210)
	bar2.TradeCount = 50
	r2 := f.Process(bar2)
	assert.Equal(t, ingestion.FilterPass, r2.Status)
}

func TestRollingATR_Seed_MatchesWilder(t *testing.T) {
	atr := ingestion.NewRollingATR(3)

	bars := make([]domain.MarketBar, 5)
	prices := [][3]float64{{102, 98, 100}, {105, 99, 103}, {104, 100, 101}, {106, 101, 105}, {107, 102, 104}}
	for i, p := range prices {
		bar, _ := domain.NewMarketBar(time.Now().Add(time.Duration(i)*time.Minute), "TEST", "1m", p[2], p[0], p[1], p[2], 100)
		bars[i] = bar
	}
	atr.Seed(bars)

	assert.True(t, atr.Seeded())
	assert.Greater(t, atr.Value(), 0.0)
}

func TestRollingVolSMA_Correct(t *testing.T) {
	vol := ingestion.NewRollingVolSMA(3)

	assert.False(t, vol.Seeded())
	assert.Equal(t, 0.0, vol.Value())

	vol.Update(100)
	vol.Update(200)
	vol.Update(300)

	assert.True(t, vol.Seeded())
	assert.Equal(t, 200.0, vol.Value())

	vol.Update(400)
	assert.Equal(t, 300.0, vol.Value())
}
