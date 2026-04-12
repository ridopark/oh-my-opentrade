package domain

// TradeAttributionComponent is the JSON-serializable representation of a
// single confluence component's contribution to a trade decision. It mirrors
// strategy.ComponentScore but lives in domain for persistence/API use.
type TradeAttributionComponent struct {
	Name   string  `json:"name"`
	Group  string  `json:"group"`
	Weight int     `json:"weight"`
	Value  float64 `json:"value,omitempty"`
	Fired  bool    `json:"fired"`
}
