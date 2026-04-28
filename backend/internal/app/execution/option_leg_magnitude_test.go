package execution

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

// Magnitude guard regression tests. Pre-fix (2026-04-28) the LLY 850P
// incident recorded an option fill at $869.37 because the underlying spot
// leaked into intent.LimitPrice and then into the trade row, producing a
// fictitious +$84k P&L. The guard rejects any option fill whose price
// exceeds 5x its reference premium — well above realistic intraday option
// moves but well below the underlying-leak ratio.

func TestShouldBlockOptionLegMagnitude_NonOption_AlwaysAllowed(t *testing.T) {
	svc := &Service{}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:    "AAPL",
		Direction: domain.DirectionCloseLong,
		// No Instrument => non-option path.
	}}
	_, blocked := svc.shouldBlockOptionLegMagnitude(po, 99999)
	assert.False(t, blocked, "guard must skip non-option intents entirely")
}

func TestShouldBlockOptionLegMagnitude_NoReference_Allows(t *testing.T) {
	svc := &Service{}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:     "LLY260508P00850000",
		Direction:  domain.DirectionCloseLong,
		Instrument: &domain.Instrument{Type: domain.InstrumentTypeOption},
		Meta:       map[string]string{},
	}}
	// No positionLookup wired and no Meta["premium"] — guard cannot resolve
	// a reference, so it stays out of the way (best-effort safety net).
	_, blocked := svc.shouldBlockOptionLegMagnitude(po, 869.37)
	assert.False(t, blocked, "guard must allow legs when no reference premium is resolvable")
}

func TestShouldBlockOptionLegMagnitude_ZeroLegPrice_Allows(t *testing.T) {
	svc := &Service{positionLookup: &stubPositionLookup{
		positions: map[string]domain.MonitoredPosition{
			"LLY260508P00850000": {
				Symbol:      "LLY260508P00850000",
				CustomState: map[string]float64{"option_premium": 25.66},
			},
		},
	}}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:     "LLY260508P00850000",
		Direction:  domain.DirectionCloseLong,
		Instrument: &domain.Instrument{Type: domain.InstrumentTypeOption},
	}}
	// Zero legPrice is the "broker stream gave us nothing" case — handled
	// by handleStreamFill's defer path, not the magnitude guard.
	_, blocked := svc.shouldBlockOptionLegMagnitude(po, 0)
	assert.False(t, blocked)
}

func TestShouldBlockOptionLegMagnitude_LLYIncident_Blocked(t *testing.T) {
	// The 2026-04-28 incident: LLY 850P, entry premium $25.66, exit fill
	// price reported as $869.37 (the LLY underlying spot). 869.37 / 25.66
	// ≈ 33.9x — must be rejected.
	svc := &Service{positionLookup: &stubPositionLookup{
		positions: map[string]domain.MonitoredPosition{
			"LLY260508P00850000": {
				Symbol:      "LLY260508P00850000",
				CustomState: map[string]float64{"option_premium": 25.66},
			},
		},
	}}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:     "LLY260508P00850000",
		Direction:  domain.DirectionCloseLong,
		Instrument: &domain.Instrument{Type: domain.InstrumentTypeOption},
	}}
	reason, blocked := svc.shouldBlockOptionLegMagnitude(po, 869.37)
	assert.True(t, blocked, "33x reference premium must be rejected")
	assert.Contains(t, reason, "exceeds")
	assert.Contains(t, reason, "5x")
}

func TestShouldBlockOptionLegMagnitude_LegitimateMove_Allowed(t *testing.T) {
	// 4x intraday move is exceptional but plausible (event-day gamma squeeze,
	// 0DTE pin moves). Must NOT be rejected.
	svc := &Service{positionLookup: &stubPositionLookup{
		positions: map[string]domain.MonitoredPosition{
			"NVDA260508C00120000": {
				Symbol:      "NVDA260508C00120000",
				CustomState: map[string]float64{"option_premium": 1.50},
			},
		},
	}}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:     "NVDA260508C00120000",
		Direction:  domain.DirectionCloseLong,
		Instrument: &domain.Instrument{Type: domain.InstrumentTypeOption},
	}}
	_, blocked := svc.shouldBlockOptionLegMagnitude(po, 6.00) // 4x
	assert.False(t, blocked, "4x move on a low-priced option must be allowed")
}

func TestShouldBlockOptionLegMagnitude_EntryUsesIntentMeta(t *testing.T) {
	// Entry direction (BUY/SHORT) — no positionLookup hit; reference must
	// resolve from intent.Meta["premium"].
	svc := &Service{positionLookup: &stubPositionLookup{positions: map[string]domain.MonitoredPosition{}}}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:     "LLY260508P00850000",
		Direction:  domain.DirectionLong,
		Instrument: &domain.Instrument{Type: domain.InstrumentTypeOption},
		Meta:       map[string]string{"premium": "25.00"},
	}}
	reason, blocked := svc.shouldBlockOptionLegMagnitude(po, 200.0)
	assert.True(t, blocked, "8x entry-meta premium must be rejected")
	assert.Contains(t, reason, "exceeds")
}
