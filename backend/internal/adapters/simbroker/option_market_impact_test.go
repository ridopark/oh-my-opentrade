package simbroker

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockOptionBarVolume is a stub for ports.OptionBarVolumePort. Returns the
// canned value plus an optional canned error. callCount lets tests assert that
// the helper short-circuits when knobs are zero.
type mockOptionBarVolume struct {
	vol       int64
	err       error
	callCount atomic.Int64
}

func (m *mockOptionBarVolume) BarVolume(_ context.Context, _ domain.Symbol, _ time.Time, _ domain.Timeframe) (int64, error) {
	m.callCount.Add(1)
	return m.vol, m.err
}

// newImpactBroker returns a broker configured for impact testing. Caller picks
// scale_bps and max_part to drive the math; default-OFF is the zero values.
// Note: New() coerces SlippageBPS=0 to 5 (default), so callers must account
// for the 5bps slippage layer applied AFTER impact.
func newImpactBroker(scaleBps, maxPartPct float64) *Broker {
	b := New(Config{
		SlippageBPS:                  0, // -> default 5 bps
		DisableFillChan:              true,
		OptionEntrySpreadEnabled:     true,
		OptionImpactScaleBps:         scaleBps,
		OptionMaxParticipationPct:    maxPartPct,
	}, zerolog.Nop())
	return b
}

// slip5buy and slip5sell express the default 5bps slippage as a multiplier
// applied AFTER impact. Buys pay it, sells receive it adversely.
const (
	slip5buy  = 1.0 + 5.0/10000.0
	slip5sell = 1.0 - 5.0/10000.0
)

// TestOptionImpact_KnobsZeroIsByteIdentical pins the default-OFF guarantee:
// when both knobs are zero, the option fill path does not even consult the
// volume port and produces the same fill as today's tiered-spread output.
func TestOptionImpact_KnobsZeroIsByteIdentical(t *testing.T) {
	b := newImpactBroker(0, 0)
	mock := &mockOptionBarVolume{vol: 100}
	b.SetOptionBarVolume(mock)

	b.UpdatePrice(domain.Symbol("AAPL"), 150.0, time.Now())

	// Three premium tiers — cheap (0.50), standard (6.00), rich (15.00).
	for _, mid := range []float64{0.50, 6.00, 15.00} {
		intent := makeOptionEntryIntent(mid, false)
		intent.Quantity = 25 // would otherwise be 25% participation against vol=100

		_, err := b.SubmitOrder(context.Background(), intent)
		require.NoError(t, err)
	}

	require.Len(t, b.orders, 3)
	require.Equal(t, int64(0), mock.callCount.Load(),
		"port must not be consulted when both knobs are zero")

	expectedFills := map[float64]float64{
		0.50:  (0.50 + 0.50*0.015) * slip5buy,  // cheap tier 1.5% + 5bps slippage
		6.00:  (6.00 + 6.00*0.005) * slip5buy,  // standard tier 0.5% + 5bps
		15.00: (15.00 + 15.00*0.003) * slip5buy, // rich tier 0.3% + 5bps
	}
	for _, o := range b.orders {
		expected, ok := expectedFills[o.intent.LimitPrice]
		require.True(t, ok, "unexpected limit price %v", o.intent.LimitPrice)
		assert.InDelta(t, expected, o.fillPrice, 1e-9,
			"knobs-zero fill must equal pre-impact tiered-spread output")
	}
}

// TestOptionImpact_CapRejects pins that breaching MaxParticipationPct returns
// the typed ErrParticipationCap and records no order.
func TestOptionImpact_CapRejects(t *testing.T) {
	b := newImpactBroker(50, 2.0) // 2% cap
	b.SetOptionBarVolume(&mockOptionBarVolume{vol: 100})

	b.UpdatePrice(domain.Symbol("AAPL"), 150.0, time.Now())

	intent := makeOptionEntryIntent(6.00, false)
	intent.Quantity = 10 // 10 contracts vs 100 contract-bar = 1000% above 2%

	_, err := b.SubmitOrder(context.Background(), intent)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrParticipationCap),
		"cap breach must surface ErrParticipationCap")

	assert.Empty(t, b.orders, "rejected orders must not be recorded")
}

// TestOptionImpact_SqrtAtKnownParticipation pins the sqrt math at 1%/5%/10%
// participation. scale_bps=50, qty=1, vol={10000, 2000, 1000} produce the
// canonical adverse offsets 5/11.18/15.81 bps over the post-half-spread base.
func TestOptionImpact_SqrtAtKnownParticipation(t *testing.T) {
	cases := []struct {
		name        string
		barVol      int64
		expectedBps float64 // 50 * sqrt(participation)
	}{
		{"1pct participation", 10000, 50.0 * math.Sqrt(0.01)},  // ~5.000
		{"5pct participation", 2000, 50.0 * math.Sqrt(0.05)},   // ~11.180
		{"10pct participation", 1000, 50.0 * math.Sqrt(0.10)},  // ~15.811
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newImpactBroker(50, 100.0) // cap 100% so it never trips
			b.SetOptionBarVolume(&mockOptionBarVolume{vol: tc.barVol})
			b.UpdatePrice(domain.Symbol("AAPL"), 150.0, time.Now())

			mid := 6.00
			intent := makeOptionEntryIntent(mid, false)
			intent.Quantity = 1

			_, err := b.SubmitOrder(context.Background(), intent)
			require.NoError(t, err)
			require.Len(t, b.orders, 1)

			base := mid + mid*0.005 // standard tier
			postImpact := base + (tc.expectedBps/10000.0)*base
			expected := postImpact * slip5buy
			for _, o := range b.orders {
				assert.InDelta(t, expected, o.fillPrice, 1e-6,
					"impact must equal scale_bps * sqrt(participation) over base, then slippage")
			}
		})
	}
}

// TestOptionImpact_DirectionSymmetry pins that on a SELL-to-open the impact
// is subtracted from the bid, not added. Direction polarity matches the
// existing half-spread treatment.
func TestOptionImpact_DirectionSymmetry(t *testing.T) {
	b := newImpactBroker(50, 100.0)
	b.SetOptionBarVolume(&mockOptionBarVolume{vol: 1000}) // 10% participation

	b.UpdatePrice(domain.Symbol("AAPL"), 150.0, time.Now())

	mid := 6.00
	intent := makeOptionEntryIntent(mid, true) // SHORT
	intent.Quantity = 1

	_, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, b.orders, 1)

	base := mid - mid*0.005 // SELL receives bid (mid - half_spread)
	impactBps := 50.0 * math.Sqrt(0.10)
	postImpact := base - (impactBps/10000.0)*base
	expected := postImpact * slip5sell
	for _, o := range b.orders {
		assert.InDelta(t, expected, o.fillPrice, 1e-6,
			"SELL entry must have impact subtracted, then 5bps slippage adverse")
	}
}

// TestOptionImpact_VolumeFloor pins that a sub-floor bar volume is clipped
// to the floor when computing participation. Floor=50 means vol=5 with qty=2
// produces participation = 2*100/50 = 400%, NOT 2*100/5 = 4000%.
//
// Cap is intentionally disabled (max=0) so the test isolates floor math.
// In production, the cap WOULD fire on raw vol=5 first — that's the design:
// cap engages on truly thin bars, impact uses floored denominator to avoid
// pathological infinite impact when the cap is loose or absent.
func TestOptionImpact_VolumeFloor(t *testing.T) {
	b := newImpactBroker(50, 0) // cap disabled, impact only
	b.SetOptionBarVolume(&mockOptionBarVolume{vol: 5})

	b.UpdatePrice(domain.Symbol("AAPL"), 150.0, time.Now())

	mid := 6.00
	intent := makeOptionEntryIntent(mid, false)
	intent.Quantity = 2

	_, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, b.orders, 1)

	// participation against floor=50 = 2*100/50 = 4.0 (400%)
	// NOT against vol=5 = 40.0 (4000%)
	participation := 2.0 * 100.0 / 50.0
	base := mid + mid*0.005
	impactBps := 50.0 * math.Sqrt(participation)
	postImpact := base + (impactBps/10000.0)*base
	expected := postImpact * slip5buy
	for _, o := range b.orders {
		assert.InDelta(t, expected, o.fillPrice, 1e-6,
			"sub-floor volume must be clipped to vol_floor=50 for participation math")
	}
}

// TestOptionImpact_BarVolumeUnavailableIsNoOp pins that a port error or
// zero-volume return makes the helper short-circuit to base price (no impact,
// accepted=true). Documented failure mode in the plan.
func TestOptionImpact_BarVolumeUnavailableIsNoOp(t *testing.T) {
	b := newImpactBroker(50, 10.0)
	b.SetOptionBarVolume(&mockOptionBarVolume{vol: 0, err: errors.New("alpaca down")})

	b.UpdatePrice(domain.Symbol("AAPL"), 150.0, time.Now())

	mid := 6.00
	intent := makeOptionEntryIntent(mid, false)
	intent.Quantity = 100 // would breach 10% cap on any sane volume

	_, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err, "port error must not surface — helper no-ops")
	require.Len(t, b.orders, 1)

	base := mid + mid*0.005
	expected := base * slip5buy
	for _, o := range b.orders {
		assert.InDelta(t, expected, o.fillPrice, 1e-9,
			"no impact applied when port returns error (slippage still applies)")
	}
}

// makeFutureExitIntent builds an exit intent with an expiry safely in the
// future relative to barTime so BSM produces a positive premium.
func makeFutureExitIntent(barTime time.Time, exitReason string) (domain.OrderIntent, time.Time) {
	expiry := barTime.AddDate(0, 0, 30).Format("2006-01-02")
	meta := map[string]string{
		"strike":           "150.0",
		"expiry":           expiry,
		"option_right":     "CALL",
		"iv_at_entry":      "0.30",
		"premium":          "5.00",
		"entry_underlying": "150.0",
		"entry_date":       barTime.Format("2006-01-02"),
		"underlying":       "AAPL",
	}
	if exitReason != "" {
		meta["exit_reason"] = exitReason
	}
	intent := makeOptionExitIntent("AAPL260601C00150000", meta)
	intent.Quantity = 1
	return intent, barTime
}

// TestOptionImpact_ExitPathDefaultUrgent pins that exits without an
// explicit exit_reason are treated as urgent — impact is multiplied by
// the urgency factor (default 1.5x).
func TestOptionImpact_ExitPathDefaultUrgent(t *testing.T) {
	b := newImpactBroker(50, 1000.0)
	b.SetOptionBarVolume(&mockOptionBarVolume{vol: 1000}) // 10% at qty=1

	barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-15 14:30")
	b.UpdatePrice(domain.Symbol("AAPL"), 153.0, barTime) // ITM call -> positive premium

	b.positions[positionKey(domain.Symbol("AAPL260601C00150000"), "")] = &position{
		symbol:   domain.Symbol("AAPL260601C00150000"),
		side:     "buy",
		quantity: 1,
		avgCost:  6.00,
	}

	intent, _ := makeFutureExitIntent(barTime, "") // urgent default

	preFill := b.computeOptionExitPrice(intent, 153.0, barTime)
	require.Greater(t, preFill, 0.0, "BSM must produce positive premium for ITM call")

	_, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, b.orders, 1)

	// Expected: base = preFill; impact at 10% with 1.5x urgency subtracted
	// (sell), then 5bps slippage adverse.
	impactBps := 50.0 * math.Sqrt(0.10) * 1.5
	postImpact := preFill - (impactBps/10000.0)*preFill
	expected := postImpact * slip5sell
	for _, o := range b.orders {
		assert.InDelta(t, expected, o.fillPrice, 1e-6,
			"default exit must apply 1.5x urgency multiplier")
	}
}

// TestOptionImpact_ExitPathPatientTakeProfit pins the quant-analyst fix:
// exits tagged exit_reason="take_profit" are patient and pay 1.0x impact,
// not 1.5x. Without this gate, AVWAP_v4's take-profit tail (its actual
// edge) is systematically over-penalized.
func TestOptionImpact_ExitPathPatientTakeProfit(t *testing.T) {
	b := newImpactBroker(50, 1000.0)
	b.SetOptionBarVolume(&mockOptionBarVolume{vol: 1000}) // 10%

	barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-15 14:30")
	b.UpdatePrice(domain.Symbol("AAPL"), 153.0, barTime)

	b.positions[positionKey(domain.Symbol("AAPL260601C00150000"), "")] = &position{
		symbol:   domain.Symbol("AAPL260601C00150000"),
		side:     "buy",
		quantity: 1,
		avgCost:  6.00,
	}

	intent, _ := makeFutureExitIntent(barTime, "take_profit")

	preFill := b.computeOptionExitPrice(intent, 153.0, barTime)
	require.Greater(t, preFill, 0.0)

	_, err := b.SubmitOrder(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, b.orders, 1)

	// 1.0x — patient take-profit pays raw impact, not urgent-scaled.
	impactBps := 50.0 * math.Sqrt(0.10) * 1.0
	postImpact := preFill - (impactBps/10000.0)*preFill
	expected := postImpact * slip5sell
	for _, o := range b.orders {
		assert.InDelta(t, expected, o.fillPrice, 1e-6,
			"take_profit exit must apply 1.0x (patient), not the urgent multiplier")
	}
}
