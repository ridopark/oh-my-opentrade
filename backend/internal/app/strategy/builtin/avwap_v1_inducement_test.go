package builtin_test

import (
	"testing"
	"time"

	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Inducement tests exercise the pure domain detector directly. This keeps
// the tests independent of the AVWAP strategy's larger bar pipeline — the
// detector is the interesting unit.

func inducementDefaultCfg() strat.InducementConfig {
	return strat.InducementConfig{
		BreachMinBPS:   5,
		BreachMaxBPS:   80,
		ReversalBars:   3,
		VolumeMinRatio: 1.2,
		MaxAgeBars:     60,
	}
}

func baseTime() time.Time {
	return time.Date(2026, 4, 18, 14, 30, 0, 0, time.UTC)
}

// A swing high at 100.00 with a tight breach (~50bps = 0.50) that closes
// back inside and has confirming volume should fire a strong bearish signal.
func TestInducement_SameBarReversal_HighSwept_StrongShort(t *testing.T) {
	sw := strat.SwingLevel{
		Time:   baseTime().Add(-30 * time.Minute),
		Price:  100.00,
		Side:   strat.InducementSwingHigh,
		BarAge: 6,
	}
	bar := strat.Bar{
		Time:   baseTime(),
		Open:   99.80,
		High:   100.50, // 50bps breach above
		Low:    99.70,
		Close:  99.90, // closes inside => same-bar reversal
		Volume: 2000,
	}
	cfg := inducementDefaultCfg()
	sig, pending := strat.DetectInducement(bar, []strat.SwingLevel{sw}, nil, nil, cfg, 1000)
	require.NotNil(t, sig)
	assert.Nil(t, pending)
	assert.Equal(t, strat.InducementStrong, sig.Strength)
	assert.Equal(t, 5, sig.Score)
	assert.Equal(t, strat.SideSell, sig.Direction)
	assert.Contains(t, sig.Tag, "strong")
}

// A swing low that gets swept with same-bar reversal and confirming volume
// fires a strong bullish signal.
func TestInducement_SameBarReversal_LowSwept_StrongLong(t *testing.T) {
	sw := strat.SwingLevel{
		Time:   baseTime().Add(-30 * time.Minute),
		Price:  50.00,
		Side:   strat.InducementSwingLow,
		BarAge: 6,
	}
	bar := strat.Bar{
		Time:   baseTime(),
		Open:   50.05,
		High:   50.20,
		Low:    49.80, // 40bps breach below
		Close:  50.10, // closes inside
		Volume: 2500,
	}
	cfg := inducementDefaultCfg()
	sig, pending := strat.DetectInducement(bar, nil, []strat.SwingLevel{sw}, nil, cfg, 1000)
	require.NotNil(t, sig)
	assert.Nil(t, pending)
	assert.Equal(t, strat.InducementStrong, sig.Strength)
	assert.Equal(t, strat.SideBuy, sig.Direction)
}

// Multi-bar reversal: bar 1 breaches and closes outside (pending created).
// Bar 2 closes back inside — fires moderate (score 3).
func TestInducement_MultiBarReversal_Moderate(t *testing.T) {
	sw := strat.SwingLevel{
		Time:   baseTime().Add(-30 * time.Minute),
		Price:  100.00,
		Side:   strat.InducementSwingHigh,
		BarAge: 6,
	}
	bar1 := strat.Bar{
		Time:   baseTime(),
		Open:   99.95,
		High:   100.50,
		Low:    99.95,
		Close:  100.30, // breach, close OUTSIDE => pending
		Volume: 2000,
	}
	cfg := inducementDefaultCfg()
	sig1, pending := strat.DetectInducement(bar1, []strat.SwingLevel{sw}, nil, nil, cfg, 1000)
	assert.Nil(t, sig1, "bar1 should not fire (close outside)")
	require.NotNil(t, pending, "bar1 should create pending")
	assert.Equal(t, cfg.ReversalBars, pending.BarsRemaining)

	bar2 := strat.Bar{
		Time:   baseTime().Add(5 * time.Minute),
		Open:   100.20,
		High:   100.00, // does NOT breach swing again (avoid retriggering same-bar)
		Low:    99.70,
		Close:  99.80, // closes INSIDE
		Volume: 1800,
	}
	// Swept highs slice must still show the same swing (age+1).
	sw2 := sw
	sw2.BarAge++
	sig2, pending2 := strat.DetectInducement(bar2, []strat.SwingLevel{sw2}, nil, pending, cfg, 1000)
	require.NotNil(t, sig2)
	assert.Equal(t, strat.InducementModerate, sig2.Strength)
	assert.Equal(t, 3, sig2.Score)
	assert.Equal(t, strat.SideSell, sig2.Direction)
	assert.Nil(t, pending2, "pending should clear after firing")
}

// Volume below the configured ratio downgrades same-bar reversal to weak.
func TestInducement_SameBarReversal_VolumeGateFail_Weak(t *testing.T) {
	sw := strat.SwingLevel{
		Time:   baseTime().Add(-30 * time.Minute),
		Price:  100.00,
		Side:   strat.InducementSwingHigh,
		BarAge: 6,
	}
	bar := strat.Bar{
		Time:   baseTime(),
		High:   100.50,
		Low:    99.70,
		Open:   99.80,
		Close:  99.90, // same-bar reversal
		Volume: 900,   // below 1.2x VolumeSMA=1000 => 0.9 ratio
	}
	cfg := inducementDefaultCfg()
	sig, _ := strat.DetectInducement(bar, []strat.SwingLevel{sw}, nil, nil, cfg, 1000)
	require.NotNil(t, sig)
	assert.Equal(t, strat.InducementWeak, sig.Strength)
	assert.Equal(t, 2, sig.Score)
}

// Swings older than MaxAgeBars are ignored.
func TestInducement_AgeCapExpiry_Ignored(t *testing.T) {
	cfg := inducementDefaultCfg()
	sw := strat.SwingLevel{
		Time:   baseTime().Add(-10 * time.Hour),
		Price:  100.00,
		Side:   strat.InducementSwingHigh,
		BarAge: cfg.MaxAgeBars + 5, // stale
	}
	bar := strat.Bar{
		Time:   baseTime(),
		High:   100.50,
		Low:    99.70,
		Open:   99.80,
		Close:  99.90,
		Volume: 2000,
	}
	sig, pending := strat.DetectInducement(bar, []strat.SwingLevel{sw}, nil, nil, cfg, 1000)
	assert.Nil(t, sig)
	assert.Nil(t, pending)
}

// Breach below BreachMinBPS does not qualify.
func TestInducement_BreachBelowMin_Ignored(t *testing.T) {
	cfg := inducementDefaultCfg()
	cfg.BreachMinBPS = 20 // raise floor so a 10bps wick doesn't count
	sw := strat.SwingLevel{
		Time:   baseTime().Add(-30 * time.Minute),
		Price:  100.00,
		Side:   strat.InducementSwingHigh,
		BarAge: 6,
	}
	bar := strat.Bar{
		Time:   baseTime(),
		High:   100.10, // 10bps — below new min
		Low:    99.80,
		Open:   99.85,
		Close:  99.90,
		Volume: 2000,
	}
	sig, pending := strat.DetectInducement(bar, []strat.SwingLevel{sw}, nil, nil, cfg, 1000)
	assert.Nil(t, sig, "breach below min should not fire")
	assert.Nil(t, pending, "breach below min should not create pending")
}

// Breach above BreachMaxBPS is treated as a real breakout, not a sweep.
func TestInducement_BreachAboveMax_Ignored(t *testing.T) {
	cfg := inducementDefaultCfg() // max = 80 bps
	sw := strat.SwingLevel{
		Time:   baseTime().Add(-30 * time.Minute),
		Price:  100.00,
		Side:   strat.InducementSwingHigh,
		BarAge: 6,
	}
	bar := strat.Bar{
		Time:   baseTime(),
		High:   101.50, // 150bps breach, way past max
		Low:    99.80,
		Open:   99.85,
		Close:  99.90,
		Volume: 2000,
	}
	sig, pending := strat.DetectInducement(bar, []strat.SwingLevel{sw}, nil, nil, cfg, 1000)
	assert.Nil(t, sig)
	assert.Nil(t, pending)
}

// Both swing high AND low swept on the same bar => ambiguous, returns nil.
func TestInducement_BothSidesSwept_ReturnsNil(t *testing.T) {
	swHigh := strat.SwingLevel{
		Time: baseTime().Add(-30 * time.Minute), Price: 100.00,
		Side: strat.InducementSwingHigh, BarAge: 6,
	}
	swLow := strat.SwingLevel{
		Time: baseTime().Add(-30 * time.Minute), Price: 99.00,
		Side: strat.InducementSwingLow, BarAge: 6,
	}
	bar := strat.Bar{
		Time:   baseTime(),
		Open:   99.50,
		High:   100.30, // sweeps high
		Low:    98.70,  // sweeps low
		Close:  99.50,
		Volume: 2500,
	}
	cfg := inducementDefaultCfg()
	sig, pending := strat.DetectInducement(bar, []strat.SwingLevel{swHigh}, []strat.SwingLevel{swLow}, nil, cfg, 1000)
	assert.Nil(t, sig, "ambiguous bar should return no signal")
	assert.Nil(t, pending, "ambiguous bar should drop any pending too")
}

// A pending inducement that is still active gets overwritten when a fresh
// sweep fires on the new bar.
func TestInducement_PendingReplacedByNewSweep(t *testing.T) {
	cfg := inducementDefaultCfg()
	prevPending := &strat.PendingInducement{
		Swing: strat.SwingLevel{
			Time: baseTime().Add(-20 * time.Minute), Price: 99.00,
			Side: strat.InducementSwingLow, BarAge: 4,
		},
		BreachBar:     baseTime().Add(-5 * time.Minute),
		BreachBPS:     20,
		VolumeRatio:   1.5,
		BarsRemaining: 2,
	}
	// New bar sweeps a different swing high with same-bar reversal.
	freshSwing := strat.SwingLevel{
		Time: baseTime().Add(-15 * time.Minute), Price: 100.00,
		Side: strat.InducementSwingHigh, BarAge: 3,
	}
	bar := strat.Bar{
		Time:   baseTime(),
		Open:   99.95,
		High:   100.40,
		Low:    99.80,
		Close:  99.85,
		Volume: 2000,
	}
	sig, pending := strat.DetectInducement(
		bar,
		[]strat.SwingLevel{freshSwing},
		[]strat.SwingLevel{prevPending.Swing},
		prevPending, cfg, 1000,
	)
	require.NotNil(t, sig, "fresh sweep should fire")
	assert.Equal(t, strat.SideSell, sig.Direction, "fresh sweep direction wins")
	assert.Equal(t, strat.InducementStrong, sig.Strength)
	assert.Nil(t, pending, "pending is replaced by the completed fresh signal")
}

// Pending countdown expires when ReversalBars elapses without a close back
// inside — no signal, pending cleared.
func TestInducement_PendingExpiresAfterReversalBars(t *testing.T) {
	cfg := inducementDefaultCfg()
	cfg.ReversalBars = 2
	pending := &strat.PendingInducement{
		Swing: strat.SwingLevel{
			Time: baseTime().Add(-20 * time.Minute), Price: 100.00,
			Side: strat.InducementSwingHigh, BarAge: 4,
		},
		BreachBar:     baseTime().Add(-5 * time.Minute),
		BreachBPS:     30,
		VolumeRatio:   1.5,
		BarsRemaining: 1, // last bar before expiry
	}
	// Close stays above swing => no reversal.
	bar := strat.Bar{
		Time:   baseTime(),
		Open:   100.20,
		High:   100.30,
		Low:    100.10,
		Close:  100.25,
		Volume: 1500,
	}
	sig, nextPending := strat.DetectInducement(bar, nil, nil, pending, cfg, 1000)
	assert.Nil(t, sig)
	assert.Nil(t, nextPending, "countdown hit zero => pending dropped")
}
