package monitor_test

import (
	"math"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

var parityFloatFields = []struct {
	name    string
	extract func(domain.IndicatorSnapshot) float64
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

func assertParitySnapshotsBitEqual(t *testing.T, label string, got, want domain.IndicatorSnapshot, sym domain.Symbol, dateIdx, barIdx int) {
	t.Helper()
	for _, f := range parityFloatFields {
		gv, wv := f.extract(got), f.extract(want)
		if math.Float64bits(gv) != math.Float64bits(wv) {
			t.Fatalf("%s: sym=%s date=%d bar=%d field=%s diverged: got %x (%v) want %x (%v)",
				label, sym, dateIdx, barIdx, f.name, math.Float64bits(gv), gv, math.Float64bits(wv), wv)
		}
	}
}

func makeParityBars(sym domain.Symbol, basePrice float64, anchorDate time.Time, n int) []domain.MarketBar {
	bars := make([]domain.MarketBar, 0, n)
	for i := 0; i < n; i++ {
		ts := anchorDate.Add(time.Duration(i) * time.Minute)
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

func TestService_IndicatorShadowParity_BitEqual(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}

	type symSpec struct {
		sym       domain.Symbol
		basePrice float64
	}
	symbols := []symSpec{
		{"AAPL", 200.0},
		{"MU", 120.0},
		{"SPY", 540.0},
	}
	dates := []time.Time{
		time.Date(2026, 4, 15, 9, 30, 0, 0, loc),
		time.Date(2026, 4, 20, 9, 30, 0, 0, loc),
		time.Date(2026, 4, 25, 9, 30, 0, 0, loc),
	}
	const barsPerDate = 200

	idx := indicator.NewService("shadow_parity")

	calc := monitor.NewIndicatorCalculator()
	calc.Label = "monitor_parity_under_test"

	for d, anchor := range dates {
		for _, ss := range symbols {
			bars := makeParityBars(ss.sym, ss.basePrice, anchor, barsPerDate)
			for i, b := range bars {
				monSnap := calc.Update(b)
				idxSnap := idx.Update(b)
				assertParitySnapshotsBitEqual(t, "Update parity", idxSnap, monSnap, b.Symbol, d, i)

				lastIdx, ok := idx.LastSnapshot(b.Symbol, b.Timeframe)
				if !ok {
					t.Fatalf("indicator.LastSnapshot missing for %s/%s after bar %d", b.Symbol, b.Timeframe, i)
				}
				assertParitySnapshotsBitEqual(t, "LastSnapshot parity (indicator)", lastIdx, idxSnap, b.Symbol, d, i)

				calcSnap, ok := calc.SnapshotForTest(b.Symbol, b.Timeframe)
				if !ok {
					t.Fatalf("monitor.SnapshotForTest missing for %s/%s after bar %d", b.Symbol, b.Timeframe, i)
				}
				assertParitySnapshotsBitEqual(t, "SnapshotForTest parity (monitor)", calcSnap, monSnap, b.Symbol, d, i)
			}
		}
	}
}

func TestService_WithIndicatorShadow_OptionWiring(t *testing.T) {
	bus := memory.NewBus()
	idx := indicator.NewService("shadow_wire")
	svc := monitor.NewService(bus, &mockRepository{}, zerolog.Nop(), monitor.WithIndicatorShadow(idx))
	if svc == nil {
		t.Fatal("NewService with WithIndicatorShadow returned nil")
	}
}
