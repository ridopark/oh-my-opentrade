package domain

import "time"

// MarketBarEnvelope carries per-event metadata that subscribers and downstream
// emitters need when an indicator Update fires HTF callbacks. Lifted from
// domain.Event so consumer-side code (monitor's HTF callback, runner's HTF
// dispatch) can build derived events without re-parsing the parent event.
type MarketBarEnvelope struct {
	TenantID   string
	EnvMode    EnvMode
	IdemKey    string
	OccurredAt time.Time
}

// EnvelopeFromEvent extracts the envelope fields from a market-bar event.
func EnvelopeFromEvent(ev Event) MarketBarEnvelope {
	return MarketBarEnvelope{
		TenantID:   ev.TenantID,
		EnvMode:    ev.EnvMode,
		IdemKey:    ev.IdempotencyKey,
		OccurredAt: ev.OccurredAt,
	}
}
