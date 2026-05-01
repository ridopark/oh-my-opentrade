package indicator_test

import (
	"math"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

const (
	testSymbol    domain.Symbol    = "TEST"
	testTimeframe domain.Timeframe = "1m"
	testBarCount                   = 250
)

// makeBars returns a deterministic 1m RTH bar stream so parity tests
// stay reproducible without math/rand state.
func makeBars(t *testing.T, n int) []domain.MarketBar {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	start := time.Date(2025, 1, 6, 9, 30, 0, 0, loc)
	bars := make([]domain.MarketBar, 0, n)
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(i) * time.Minute)
		base := 200.0 + 0.05*float64(i) + 3.0*math.Sin(float64(i)*0.1)
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
			Symbol:    testSymbol,
			Timeframe: testTimeframe,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     closePx,
			Volume:    volume,
		})
	}
	return bars
}

var floatFields = []struct {
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

func assertSnapshotsBitEqual(t *testing.T, label string, got, want domain.IndicatorSnapshot, barIdx int) {
	t.Helper()
	for _, f := range floatFields {
		gv, wv := f.extract(got), f.extract(want)
		if math.Float64bits(gv) != math.Float64bits(wv) {
			t.Fatalf("%s: bar %d: field %s diverged: got %x (%v) want %x (%v)",
				label, barIdx, f.name, math.Float64bits(gv), gv, math.Float64bits(wv), wv)
		}
	}
}

func TestService_UpdateParity(t *testing.T) {
	raw := monitor.NewIndicatorCalculator()
	raw.Label = "raw"
	svc := indicator.NewService("wrapped")

	bars := makeBars(t, testBarCount)
	for i, b := range bars {
		rawSnap := raw.Update(b)
		wrappedSnap := svc.Update(b)
		assertSnapshotsBitEqual(t, "Update", wrappedSnap, rawSnap, i)
	}
}

func TestService_LastSnapshotMatchesUpdate(t *testing.T) {
	svc := indicator.NewService("last")
	bars := makeBars(t, testBarCount)
	for i, b := range bars {
		updated := svc.Update(b)
		got, ok := svc.LastSnapshot(b.Symbol, b.Timeframe)
		if !ok {
			t.Fatalf("bar %d: LastSnapshot missing for %s/%s", i, b.Symbol, b.Timeframe)
		}
		assertSnapshotsBitEqual(t, "LastSnapshot", got, updated, i)
	}
}

func TestService_LastSnapshotMissingKey(t *testing.T) {
	svc := indicator.NewService("missing")
	if _, ok := svc.LastSnapshot("UNKNOWN", "1m"); ok {
		t.Fatalf("LastSnapshot for unseen key should report ok=false")
	}
}

func TestService_WarmUpEqualsSerialUpdate(t *testing.T) {
	bars := makeBars(t, testBarCount)

	serial := indicator.NewService("serial")
	var lastSerial domain.IndicatorSnapshot
	for _, b := range bars {
		lastSerial = serial.Update(b)
	}

	batch := indicator.NewService("batch")
	batch.WarmUp(bars)
	lastBatch, ok := batch.LastSnapshot(testSymbol, testTimeframe)
	if !ok {
		t.Fatalf("WarmUp: LastSnapshot missing after batch feed")
	}
	assertSnapshotsBitEqual(t, "WarmUp", lastBatch, lastSerial, len(bars)-1)
}
