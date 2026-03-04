package domain

// AdvisoryDecision represents the output of the AI adversarial debate system.
type AdvisoryDecision struct {
	Direction      Direction
	Confidence     float64 // 0.0 – 1.0
	Rationale      string
	BullArgument   string
	BearArgument   string
	JudgeReasoning string

	// Options-specific fields — zero values are valid for equity debates.
	ContractSymbol string  // e.g. "AAPL240119C00190000" — empty for equity debates
	MaxLossUSD     float64 // > 0 required if ContractSymbol is set
	ExitRules      string  // e.g. "Exit at 2x premium or after 21 days" — empty for equity debates
}
