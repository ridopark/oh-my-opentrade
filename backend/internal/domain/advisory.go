package domain

// AdvisoryDecision represents the output of the AI adversarial debate system.
type AdvisoryDecision struct {
	Direction      Direction
	Confidence     float64 // 0.0 – 1.0
	Rationale      string
	BullArgument   string
	BearArgument   string
	JudgeReasoning string
}
