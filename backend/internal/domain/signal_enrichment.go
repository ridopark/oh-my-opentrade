package domain

// EnrichmentStatus indicates the outcome of the AI enrichment attempt.
type EnrichmentStatus string

const (
	// EnrichmentOK means the AI advisor returned a valid debate result.
	EnrichmentOK EnrichmentStatus = "ok"
	// EnrichmentTimeout means the AI advisor did not respond within the deadline.
	EnrichmentTimeout EnrichmentStatus = "timeout"
	// EnrichmentError means the AI advisor returned an error.
	EnrichmentError EnrichmentStatus = "error"
	// EnrichmentSkipped means the signal was not eligible for debate (e.g. exit signals).
	// Skipped signals proceed with original strategy logic (fallback behavior).
	EnrichmentSkipped EnrichmentStatus = "skipped"
	// EnrichmentVetoed means the signal was explicitly rejected by a pre-debate veto
	// (e.g. negative expectancy). Unlike EnrichmentSkipped, vetoed signals MUST NOT
	// proceed to order submission.
	EnrichmentVetoed EnrichmentStatus = "vetoed"
)

// SignalRef identifies the original strategy signal that triggered enrichment.
// It carries enough context for downstream consumers (RiskSizer) to reconstruct
// the OrderIntent without needing the full start.Signal (which lives in the
// strategy domain package, not the core domain).
type SignalRef struct {
	StrategyInstanceID string            `json:"strategyInstanceId"`
	Symbol             string            `json:"symbol"`
	SignalType         string            `json:"signalType"` // "entry", "exit", "adjust", "flat"
	Side               string            `json:"side"`       // "buy", "sell"
	Strength           float64           `json:"strength"`   // original signal strength [0,1]
	Tags               map[string]string `json:"tags"`       // original signal metadata
}

// RiskModifier is a structured enum output by the AI judge to influence risk sizing.
// Unlike free-text JudgeReasoning, this is a constrained enum safe for execution logic.
type RiskModifier string

const (
	RiskModifierTight  RiskModifier = "TIGHT"
	RiskModifierNormal RiskModifier = "NORMAL"
	RiskModifierWide   RiskModifier = "WIDE"
)

func NewRiskModifier(s string) RiskModifier {
	switch RiskModifier(s) {
	case RiskModifierTight, RiskModifierNormal, RiskModifierWide:
		return RiskModifier(s)
	default:
		return RiskModifierNormal
	}
}

// SignalEnrichment is the event payload for EventSignalEnriched.
// It always contains the original signal reference and enrichment status.
// When AI enrichment succeeds (Status == EnrichmentOK), the advisory fields
// are populated with bull/bear/judge reasoning and an adjusted confidence.
// When AI is unavailable, the fields fall back to the original signal values.
type SignalEnrichment struct {
	Signal         SignalRef        `json:"signal"`
	Status         EnrichmentStatus `json:"status"`
	Confidence     float64          `json:"confidence"`     // AI-adjusted or original strength
	Rationale      string           `json:"rationale"`      // AI rationale or generic signal string
	Direction      Direction        `json:"direction"`      // AI direction or derived from signal side
	BullArgument   string           `json:"bullArgument"`   // empty on fallback
	BearArgument   string           `json:"bearArgument"`   // empty on fallback
	JudgeReasoning string           `json:"judgeReasoning"` // empty on fallback
	RiskModifier   RiskModifier     `json:"riskModifier"`   // TIGHT, NORMAL, WIDE — empty defaults to NORMAL

	// News context — populated when recent news was found for the symbol.
	NewsHeadlines []string `json:"newsHeadlines,omitempty"`

	// Exit-signal P&L context (populated only for exit signals when position data is available).
	EntryPrice       float64 `json:"entryPrice,omitempty"`       // 0 when unavailable
	UnrealizedPnLPct float64 `json:"unrealizedPnlPct,omitempty"` // decimal (0.05 = 5%), 0 when unavailable
	UnrealizedPnLUSD float64 `json:"unrealizedPnlUsd,omitempty"` // dollar amount, 0 when unavailable
	HasPnL           bool    `json:"hasPnl,omitempty"`           // true when P&L fields are populated
}

// AIDirectionConflict returns true when the AI debate succeeded and recommended
// a direction that conflicts with the original strategy signal. This is used as
// a gate: the AI can veto a signal but cannot invent new trades.
//
// minConfidence sets the minimum AI confidence required for a veto. Below this
// threshold the AI opinion is considered too weak to override the strategy.
//
// Conflict matrix (entry signals only):
//
//	Signal Side "buy"  + AI Direction SHORT → conflict (if confidence >= minConfidence)
//	Signal Side "sell" + AI Direction LONG  → conflict (if confidence >= minConfidence)
//
// Returns false when the AI was skipped, timed out, errored, or when confidence
// is below minConfidence — in those cases the system falls back to the strategy's
// original signal.
func (e SignalEnrichment) AIDirectionConflict(minConfidence float64) bool {
	if e.Status != EnrichmentOK {
		return false
	}
	if e.Signal.SignalType != "entry" {
		return false
	}
	if e.Confidence < minConfidence {
		return false
	}
	switch e.Signal.Side {
	case "buy":
		return e.Direction == DirectionShort
	case "sell":
		return e.Direction == DirectionLong
	default:
		return false
	}
}

type SignalGatedPayload struct {
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	SignalType string  `json:"signalType"`
	Strategy   string  `json:"strategy"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}
