package domain

import "encoding/json"

// TradeAttribution captures per-component confluence data for decay telemetry.
// Stored in the trades.thesis JSONB column alongside the existing EntryThesis
// format. The "v" field distinguishes the two schemas.
type TradeAttribution struct {
	V          int                        `json:"v"`
	Confluence TradeAttributionConfluence `json:"confluence"`
	Regime     string                     `json:"regime,omitempty"`
	VIXBucket  string                     `json:"vixBucket,omitempty"`
	Gates      []TradeGate                `json:"gates,omitempty"`
}

// TradeAttributionConfluence holds the scored components.
type TradeAttributionConfluence struct {
	Score      int                          `json:"score"`
	Components []TradeAttributionComponent  `json:"components"`
}

// TradeAttributionComponent is the serializable form of strategy.ComponentScore.
type TradeAttributionComponent struct {
	Name   string  `json:"name"`
	Group  string  `json:"group"`
	Weight int     `json:"weight"`
	Value  float64 `json:"value,omitempty"`
	Fired  bool    `json:"fired"`
}

// TradeGate captures an entry gate's pass/fail status.
type TradeGate struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

// ParseThesisJSON deserializes the trades.thesis JSONB column, routing to the
// correct struct based on the presence of a "v" key.
//
// Returns (entryThesis, attribution, error). Exactly one of the first two
// return values will be non-nil on success.
func ParseThesisJSON(data json.RawMessage) (*EntryThesis, *TradeAttribution, error) {
	if len(data) == 0 {
		return nil, nil, nil
	}

	var probe struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, nil, err
	}

	if probe.V != nil {
		var attr TradeAttribution
		if err := json.Unmarshal(data, &attr); err != nil {
			return nil, nil, err
		}
		return nil, &attr, nil
	}

	var thesis EntryThesis
	if err := json.Unmarshal(data, &thesis); err != nil {
		return nil, nil, err
	}
	return &thesis, nil, nil
}
