// Package indicatortest provides shared helpers for parity tests
// driven by domain.IndicatorSnapshot. Centralizing the field table
// here prevents drift between callers when IndicatorSnapshot grows
// new numeric fields.
package indicatortest

import (
	"math"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

var FloatFields = []struct {
	Name    string
	Extract func(domain.IndicatorSnapshot) float64
}{
	{"RSI", func(s domain.IndicatorSnapshot) float64 { return s.RSI }},
	{"StochK", func(s domain.IndicatorSnapshot) float64 { return s.StochK }},
	{"StochD", func(s domain.IndicatorSnapshot) float64 { return s.StochD }},
	{"EMA9", func(s domain.IndicatorSnapshot) float64 { return s.EMA9 }},
	{"EMA21", func(s domain.IndicatorSnapshot) float64 { return s.EMA21 }},
	{"EMA50", func(s domain.IndicatorSnapshot) float64 { return s.EMA50 }},
	{"EMA200", func(s domain.IndicatorSnapshot) float64 { return s.EMA200 }},
	{"EMAFast", func(s domain.IndicatorSnapshot) float64 { return s.EMAFast }},
	{"EMASlow", func(s domain.IndicatorSnapshot) float64 { return s.EMASlow }},
	{"VWAP", func(s domain.IndicatorSnapshot) float64 { return s.VWAP }},
	{"VWAPSD", func(s domain.IndicatorSnapshot) float64 { return s.VWAPSD }},
	{"Volume", func(s domain.IndicatorSnapshot) float64 { return s.Volume }},
	{"VolumeSMA", func(s domain.IndicatorSnapshot) float64 { return s.VolumeSMA }},
	{"ATR", func(s domain.IndicatorSnapshot) float64 { return s.ATR }},
	{"BBUpper", func(s domain.IndicatorSnapshot) float64 { return s.BBUpper }},
	{"BBMiddle", func(s domain.IndicatorSnapshot) float64 { return s.BBMiddle }},
	{"BBLower", func(s domain.IndicatorSnapshot) float64 { return s.BBLower }},
	{"BBPercentB", func(s domain.IndicatorSnapshot) float64 { return s.BBPercentB }},
	{"BBBandwidth", func(s domain.IndicatorSnapshot) float64 { return s.BBBandwidth }},
	{"MACDLine", func(s domain.IndicatorSnapshot) float64 { return s.MACDLine }},
	{"MACDSignal", func(s domain.IndicatorSnapshot) float64 { return s.MACDSignal }},
	{"MACDHistogram", func(s domain.IndicatorSnapshot) float64 { return s.MACDHistogram }},
	{"ADX", func(s domain.IndicatorSnapshot) float64 { return s.ADX }},
	{"RegimeScore", func(s domain.IndicatorSnapshot) float64 { return s.RegimeScore }},
}

// AssertSnapshotsBitEqual fails the test if any FloatField differs in
// IEEE 754 bit pattern between got and want.
func AssertSnapshotsBitEqual(t *testing.T, label string, got, want domain.IndicatorSnapshot, ctx string) {
	t.Helper()
	for _, f := range FloatFields {
		gv, wv := f.Extract(got), f.Extract(want)
		if math.Float64bits(gv) != math.Float64bits(wv) {
			t.Fatalf("%s [%s]: field %s diverged: got %x (%v) want %x (%v)",
				label, ctx, f.Name, math.Float64bits(gv), gv, math.Float64bits(wv), wv)
		}
	}
}

// MakeBars returns a deterministic 1m OHLC stream rooted at anchor.
// The price walk is sine + linear drift around basePrice, fully
// reproducible without math/rand state.
func MakeBars(sym domain.Symbol, basePrice float64, anchor time.Time, n int) []domain.MarketBar {
	bars := make([]domain.MarketBar, 0, n)
	for i := 0; i < n; i++ {
		ts := anchor.Add(time.Duration(i) * time.Minute)
		base := basePrice + 0.05*float64(i) + 3.0*math.Sin(float64(i)*0.1)
		open := base
		high := base + 0.5 + 0.25*math.Sin(float64(i)*0.37)
		low := base - 0.5 - 0.25*math.Cos(float64(i)*0.41)
		closePx := base + 0.1*math.Sin(float64(i)*0.23)
		if high < open {
			high = open + 0.1
		}
		if high < closePx {
			high = closePx + 0.1
		}
		if low > open {
			low = open - 0.1
		}
		if low > closePx {
			low = closePx - 0.1
		}
		volume := 1000.0 + 50.0*math.Abs(math.Sin(float64(i)*0.13))
		bars = append(bars, domain.MarketBar{
			Time:      ts,
			Symbol:    sym,
			Timeframe: domain.Timeframe("1m"),
			Open:      open,
			High:      high,
			Low:       low,
			Close:     closePx,
			Volume:    volume,
		})
	}
	return bars
}
