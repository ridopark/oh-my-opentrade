package domain

import (
	"testing"
	"time"
)

// BenchmarkBSMPrice measures the raw Black-Scholes price call on the hot path.
// This is invoked for every open option position on every bar during backtest.
func BenchmarkBSMPrice(b *testing.B) {
	const (
		s     = 550.0
		k     = 555.0
		t     = 14.0 / 365.25
		r     = 0.045
		sigma = 0.22
	)
	var sink float64
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sink += BSMPrice(s, k, t, r, sigma, true)
	}
	_ = sink
}

// BenchmarkEstimatedPremium_BSM measures the full EstimatedPremium path that
// the position monitor exercises per-bar, per-position. The BSM branch is
// the dominant path for options opened with full CustomState.
func BenchmarkEstimatedPremium_BSM(b *testing.B) {
	now := time.Date(2026, 4, 1, 14, 30, 0, 0, time.UTC)
	expiry := now.Add(14 * 24 * time.Hour)
	mp := MonitoredPosition{
		InstrumentType: InstrumentTypeOption,
		EntryPrice:     550.0,
		CustomState: map[string]float64{
			"option_premium": 6.50,
			"delta_at_entry": 0.52,
			"strike":         555.0,
			"expiry_unix":    float64(expiry.Unix()),
			"iv_at_entry":    0.22,
			"is_call":        1.0,
		},
	}
	underlying := 551.25
	var sink float64
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sink += mp.EstimatedPremium(underlying, now)
	}
	_ = sink
}

// BenchmarkEstimatedPremium_DeltaLinear measures the fallback path (no BSM
// inputs in CustomState). Kept for comparison with the BSM path to quantify
// the correctness-vs-speed trade-off from commit 2188f43.
func BenchmarkEstimatedPremium_DeltaLinear(b *testing.B) {
	now := time.Date(2026, 4, 1, 14, 30, 0, 0, time.UTC)
	mp := MonitoredPosition{
		InstrumentType: InstrumentTypeOption,
		EntryPrice:     550.0,
		CustomState: map[string]float64{
			"option_premium": 6.50,
			"delta_at_entry": 0.52,
		},
	}
	underlying := 551.25
	var sink float64
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sink += mp.EstimatedPremium(underlying, now)
	}
	_ = sink
}
