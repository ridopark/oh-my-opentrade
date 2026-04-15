package strategy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// barBuilder is a small helper so test intent reads cleanly.
type barSpec struct {
	offsetMin           int
	open, high, low, cl float64
	vol                 float64
}

func buildBars(start time.Time, specs []barSpec) []Bar {
	out := make([]Bar, len(specs))
	for i, s := range specs {
		out[i] = Bar{
			Time:   start.Add(time.Duration(s.offsetMin) * time.Minute),
			Open:   s.open,
			High:   s.high,
			Low:    s.low,
			Close:  s.cl,
			Volume: s.vol,
		}
	}
	return out
}

// makeQuietHistory fills N quiet 5m bars around price p so session HOD/LOD
// and swing detection have material to work with. We deliberately keep the
// range narrow so test-specified sweep targets dominate.
func makeQuietHistory(start time.Time, n int, p float64, vol float64) []Bar {
	out := make([]Bar, n)
	for i := 0; i < n; i++ {
		out[i] = Bar{
			Time:   start.Add(time.Duration(i*5) * time.Minute),
			Open:   p,
			High:   p + 1,
			Low:    p - 1,
			Close:  p,
			Volume: vol,
		}
	}
	return out
}

func TestCryptoInducement_SweepAboveHOD(t *testing.T) {
	start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	// 20 quiet bars around 50000 establish session HOD at 50001.
	hist := makeQuietHistory(start, 20, 50000, 100)
	// Inject a mid-session peak at 50100 so HOD moves up.
	hist[10].High = 50100
	sweep := Bar{
		Time:   start.Add(100 * time.Minute),
		Open:   50050,
		High:   50180, // overshoots HOD 50100 by 80, ~16 bps
		Low:    50040,
		Close:  50060, // closes back under HOD
		Volume: 500,   // 5x SMA of 100
	}
	bars := append(hist, sweep)

	det := NewCryptoInducement(DefaultCryptoInducementConfig())
	res := det.Detect("BTC/USD", bars, nil)

	require.True(t, res.Detected, "expected HOD sweep")
	require.Equal(t, CryptoInducementBearish, res.Direction)
	require.Equal(t, CryptoInducementLevelHOD, res.LevelType)
	require.InDelta(t, 50100.0, res.ReferenceLevel, 0.01)
	require.Greater(t, res.BreachBPS, 10.0)
	require.Less(t, res.BreachBPS, 100.0)
}

func TestCryptoInducement_SweepBelowLOD(t *testing.T) {
	start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	hist := makeQuietHistory(start, 20, 50000, 100)
	hist[8].Low = 49900 // LOD = 49900
	sweep := Bar{
		Time:   start.Add(100 * time.Minute),
		Open:   49950,
		High:   49960,
		Low:    49820, // undercuts LOD by 80 => ~16 bps
		Close:  49940,
		Volume: 500,
	}
	bars := append(hist, sweep)

	det := NewCryptoInducement(DefaultCryptoInducementConfig())
	res := det.Detect("BTC/USD", bars, nil)

	require.True(t, res.Detected)
	require.Equal(t, CryptoInducementBullish, res.Direction)
	require.Equal(t, CryptoInducementLevelLOD, res.LevelType)
	require.InDelta(t, 49900.0, res.ReferenceLevel, 0.01)
}

func TestCryptoInducement_RoundNumberSweep(t *testing.T) {
	start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	// Position history so session HOD/LOD do NOT overlap the round level.
	// Price lives well below 50000, with history extrema at 49500/49495.
	hist := makeQuietHistory(start, 20, 49497, 100)
	sweep := Bar{
		Time:   start.Add(100 * time.Minute),
		Open:   49940,
		High:   50060, // pokes above round 50000 by 60 => 12 bps
		Low:    49930,
		Close:  49950, // closes back under 50000
		Volume: 500,
	}
	bars := append(hist, sweep)

	det := NewCryptoInducement(DefaultCryptoInducementConfig())
	res := det.Detect("BTC/USD", bars, nil)

	require.True(t, res.Detected)
	require.Equal(t, CryptoInducementLevelRound, res.LevelType)
	require.InDelta(t, 50000.0, res.ReferenceLevel, 0.01)
	require.Equal(t, CryptoInducementBearish, res.Direction)
}

func TestCryptoInducement_Swing1hSweep(t *testing.T) {
	// 1h swing detection needs >= 2*SwingWindow+1 prior bars. Default is 12.
	start := time.Date(2026, 4, 14, 20, 0, 0, 0, time.UTC) // prior UTC day
	// Build 30 bars of flat-ish history with a clear local high at index 15
	// on the PRIOR UTC day so it doesn't collide with session HOD of the
	// sweep-day bar.
	specs := make([]barSpec, 30)
	for i := range specs {
		specs[i] = barSpec{offsetMin: i * 5, open: 3000, high: 3001, low: 2999, cl: 3000, vol: 100}
	}
	specs[15].high = 3050 // swing high
	hist := buildBars(start, specs)

	// Sweep bar on next UTC day (so session HOD/LOD won't shadow swing).
	sweepStart := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	sweep := Bar{
		Time:   sweepStart,
		Open:   3010,
		High:   3060, // overshoots swing 3050 by 10 => ~33 bps
		Low:    3005,
		Close:  3020,
		Volume: 500,
	}
	bars := append(hist, sweep)

	det := NewCryptoInducement(DefaultCryptoInducementConfig())
	res := det.Detect("ETH/USD", bars, nil)

	require.True(t, res.Detected)
	require.Equal(t, CryptoInducementBearish, res.Direction)
	// Swing high at 3050 should be the reference (round 3100 is too far,
	// and no same-day session extremum exists yet besides this bar itself).
	require.Equal(t, CryptoInducementLevelSwing1h, res.LevelType)
	require.InDelta(t, 3050.0, res.ReferenceLevel, 0.01)
}

func TestCryptoInducement_NoSweep(t *testing.T) {
	start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	hist := makeQuietHistory(start, 20, 50000, 100)
	// Bar that stays entirely inside prior extrema — no breach.
	quiet := Bar{
		Time:   start.Add(100 * time.Minute),
		Open:   50000,
		High:   50005,
		Low:    49995,
		Close:  50002,
		Volume: 500,
	}
	bars := append(hist, quiet)

	det := NewCryptoInducement(DefaultCryptoInducementConfig())
	res := det.Detect("BTC/USD", bars, nil)

	require.False(t, res.Detected)
}

func TestCryptoInducement_BreachTooLarge(t *testing.T) {
	start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	// Use a non-round price base and disable round-number levels so the
	// only candidate references are HOD/LOD and swings; then overshoot HOD
	// by far more than BreachMaxBPS.
	hist := makeQuietHistory(start, 20, 50237, 100)
	hist[10].High = 50240 // HOD = 50240
	// High = 51000 overshoots by 760 => ~151 bps > default 150 bps max.
	sweep := Bar{
		Time:   start.Add(100 * time.Minute),
		Open:   50240,
		High:   51000,
		Low:    50230,
		Close:  50235, // back under HOD
		Volume: 500,
	}
	bars := append(hist, sweep)

	cfg := DefaultCryptoInducementConfig()
	cfg.RoundIncrements = map[string]float64{} // isolate to HOD/LOD+swing
	det := NewCryptoInducement(cfg)
	res := det.Detect("BTC/USD", bars, nil)
	require.False(t, res.Detected, "breach beyond BreachMaxBPS should be rejected as real breakout")
}

func TestCryptoInducement_VolumeGate(t *testing.T) {
	start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	hist := makeQuietHistory(start, 20, 50000, 100)
	hist[10].High = 50100
	sweep := Bar{
		Time:   start.Add(100 * time.Minute),
		Open:   50050,
		High:   50180,
		Low:    50040,
		Close:  50060,
		Volume: 80, // below SMA (100) -> ratio 0.8 < 1.2
	}
	bars := append(hist, sweep)

	det := NewCryptoInducement(DefaultCryptoInducementConfig())
	res := det.Detect("BTC/USD", bars, nil)
	require.False(t, res.Detected, "low-volume sweep should be rejected")
}

func TestCryptoInducement_TakerFlowFilter(t *testing.T) {
	start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	hist := makeQuietHistory(start, 20, 50000, 100)
	hist[8].Low = 49900
	sweep := Bar{
		Time:   start.Add(100 * time.Minute),
		Open:   49950,
		High:   49960,
		Low:    49820,
		Close:  49940,
		Volume: 500,
	}
	bars := append(hist, sweep)

	cfg := DefaultCryptoInducementConfig()
	cfg.TakerFlowMinRatio = 0.6 // require 60% aggressive BUY on bullish sweep

	// Wrong-direction taker flow (sells dominate) -> reject.
	tradesSell := []CryptoTrade{
		{Time: sweep.Time, Price: 49830, Size: 10, TakerSide: "sell"},
		{Time: sweep.Time, Price: 49940, Size: 2, TakerSide: "buy"},
	}
	det := NewCryptoInducement(cfg)
	require.False(t, det.Detect("BTC/USD", bars, tradesSell).Detected)

	// Right-direction taker flow (buys absorb) -> accept.
	tradesBuy := []CryptoTrade{
		{Time: sweep.Time, Price: 49830, Size: 2, TakerSide: "sell"},
		{Time: sweep.Time, Price: 49940, Size: 10, TakerSide: "buy"},
	}
	res := det.Detect("BTC/USD", bars, tradesBuy)
	require.True(t, res.Detected)
	require.Greater(t, res.TakerFlowRatio, 0.6)
}

func TestCryptoInducement_RoundIncrementOverride(t *testing.T) {
	start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	hist := makeQuietHistory(start, 20, 147, 100)
	// Price under 150; sweep pokes above custom round 150 for "FOO/USD".
	sweep := Bar{
		Time:   start.Add(100 * time.Minute),
		Open:   148,
		High:   150.30,
		Low:    147.5,
		Close:  148.5,
		Volume: 500,
	}
	bars := append(hist, sweep)

	cfg := DefaultCryptoInducementConfig()
	cfg.RoundIncrements = map[string]float64{"FOO/USD": 50}
	det := NewCryptoInducement(cfg)
	res := det.Detect("FOO/USD", bars, nil)
	require.True(t, res.Detected)
	require.Equal(t, CryptoInducementLevelRound, res.LevelType)
	require.InDelta(t, 150.0, res.ReferenceLevel, 0.01)
}

func TestCryptoInducement_DirectionCorrectness(t *testing.T) {
	cases := []struct {
		name     string
		highMod  float64 // add to base high in history
		sweepHi  float64
		sweepLo  float64
		sweepCl  float64
		wantDir  CryptoInducementDirection
	}{
		{"bearish_hod_sweep", 100, 50180, 50040, 50060, CryptoInducementBearish},
		{"bullish_lod_sweep", 0, 49960, 49820, 49940, CryptoInducementBullish},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
			hist := makeQuietHistory(start, 20, 50000, 100)
			if tc.highMod > 0 {
				hist[10].High = 50000 + tc.highMod
			} else {
				hist[8].Low = 49900
			}
			sweep := Bar{
				Time:   start.Add(100 * time.Minute),
				Open:   50000,
				High:   tc.sweepHi,
				Low:    tc.sweepLo,
				Close:  tc.sweepCl,
				Volume: 500,
			}
			bars := append(hist, sweep)
			det := NewCryptoInducement(DefaultCryptoInducementConfig())
			res := det.Detect("BTC/USD", bars, nil)
			require.True(t, res.Detected)
			require.Equal(t, tc.wantDir, res.Direction)
		})
	}
}
