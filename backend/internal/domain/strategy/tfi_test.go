package strategy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTFI_EmptyWindow(t *testing.T) {
	tfi := NewTFI(DefaultTFIConfig())
	v, n := tfi.Value()
	assert.Equal(t, 0.0, v)
	assert.Equal(t, 0, n)
}

func TestTFI_AllBuy(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		tfi.UpdateTrade(MarketTradeLike{Time: ts.Add(time.Duration(i) * time.Minute), Size: 1, TakerSide: "buy"})
	}
	v, n := tfi.Value()
	assert.Equal(t, 1.0, v)
	assert.Equal(t, 5, n)
}

func TestTFI_AllSell(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		tfi.UpdateTrade(MarketTradeLike{Time: ts.Add(time.Duration(i) * time.Minute), Size: 2, TakerSide: "sell"})
	}
	v, _ := tfi.Value()
	assert.Equal(t, -1.0, v)
}

func TestTFI_MixedBuySell(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	tfi.UpdateTrade(MarketTradeLike{Time: ts, Size: 7, TakerSide: "buy"})
	tfi.UpdateTrade(MarketTradeLike{Time: ts.Add(1 * time.Minute), Size: 3, TakerSide: "sell"})
	v, n := tfi.Value()
	// (7 - 3)/(7 + 3) = 0.4
	assert.InDelta(t, 0.4, v, 1e-9)
	assert.Equal(t, 2, n)
}

func TestTFI_UnknownSideIgnored(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	tfi.UpdateTrade(MarketTradeLike{Time: ts, Size: 10, TakerSide: ""})
	v, n := tfi.Value()
	assert.Equal(t, 0.0, v)
	assert.Equal(t, 0, n)
}

func TestTFI_WindowEviction(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	// Old sell that should be evicted.
	tfi.UpdateTrade(MarketTradeLike{Time: ts, Size: 100, TakerSide: "sell"})
	// Newer buy 20 minutes later — window is 15m so the sell should be gone.
	tfi.UpdateTrade(MarketTradeLike{Time: ts.Add(20 * time.Minute), Size: 5, TakerSide: "buy"})

	v, n := tfi.Value()
	assert.Equal(t, 1.0, v, "only the recent buy should remain in window")
	assert.Equal(t, 1, n)
}

func TestTFI_BarFallbackPositive(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15, FallbackBarSign: true})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	tfi.UpdateBar(Bar{Time: ts, Open: 100, Close: 105, Volume: 10})
	v, n := tfi.Value()
	assert.Equal(t, 1.0, v)
	assert.Equal(t, 1, n)
}

func TestTFI_BarFallbackNegative(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15, FallbackBarSign: true})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	tfi.UpdateBar(Bar{Time: ts, Open: 100, Close: 95, Volume: 10})
	v, _ := tfi.Value()
	assert.Equal(t, -1.0, v)
}

func TestTFI_BarFallbackDisabled(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15, FallbackBarSign: false})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	tfi.UpdateBar(Bar{Time: ts, Open: 100, Close: 105, Volume: 10})
	v, n := tfi.Value()
	assert.Equal(t, 0.0, v)
	assert.Equal(t, 0, n)
}

func TestTFI_BarFallbackFlatBarIgnored(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15, FallbackBarSign: true})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	tfi.UpdateBar(Bar{Time: ts, Open: 100, Close: 100, Volume: 10})
	_, n := tfi.Value()
	assert.Equal(t, 0, n)
}

func TestTFI_MixedTickAndBarFallback(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15, FallbackBarSign: true})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	tfi.UpdateTrade(MarketTradeLike{Time: ts, Size: 4, TakerSide: "buy"})
	tfi.UpdateBar(Bar{Time: ts.Add(1 * time.Minute), Open: 100, Close: 98, Volume: 6})
	v, n := tfi.Value()
	// buy=4, sell=6 -> (4-6)/10 = -0.2
	assert.InDelta(t, -0.2, v, 1e-9)
	assert.Equal(t, 2, n)
}

func TestTFI_ZeroSizeIgnored(t *testing.T) {
	tfi := NewTFI(TFIConfig{WindowMinutes: 15})
	ts := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	tfi.UpdateTrade(MarketTradeLike{Time: ts, Size: 0, TakerSide: "buy"})
	_, n := tfi.Value()
	assert.Equal(t, 0, n)
}
