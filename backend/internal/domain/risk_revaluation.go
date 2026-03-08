package domain

import "time"

// ThesisStatus indicates whether the original entry thesis is still valid.
type ThesisStatus string

const (
	ThesisIntact      ThesisStatus = "INTACT"
	ThesisDegrading   ThesisStatus = "DEGRADING"
	ThesisInvalidated ThesisStatus = "INVALIDATED"
)

// RiskAction describes what adjustment the Risk Agent recommends.
type RiskAction string

const (
	RiskActionHold     RiskAction = "HOLD"
	RiskActionTighten  RiskAction = "TIGHTEN"
	RiskActionScaleOut RiskAction = "SCALE_OUT"
	RiskActionExit     RiskAction = "EXIT"
)

// RiskRevaluation is the output of a periodic AI risk assessment on an open position.
type RiskRevaluation struct {
	Symbol          Symbol       `json:"symbol"`
	ThesisStatus    ThesisStatus `json:"thesisStatus"`
	Action          RiskAction   `json:"action"`
	Confidence      float64      `json:"confidence"`
	Reasoning       string       `json:"reasoning"`
	UpdatedModifier RiskModifier `json:"updatedModifier"`
	ScaleOutPct     float64      `json:"scaleOutPct,omitempty"`
	EvaluatedAt     time.Time    `json:"evaluatedAt"`
}

// RiskRevaluationEvent is the event payload emitted after a position risk re-evaluation.
type RiskRevaluationEvent struct {
	RiskRevaluation
	Strategy      string  `json:"strategy"`
	EntryPrice    float64 `json:"entryPrice"`
	CurrentPrice  float64 `json:"currentPrice"`
	UnrealizedPnL float64 `json:"unrealizedPnl"`
	HoldDuration  string  `json:"holdDuration"`
	TenantID      string  `json:"tenantId"`
	EnvMode       EnvMode `json:"envMode"`
}
