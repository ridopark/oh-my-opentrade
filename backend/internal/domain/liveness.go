package domain

import "time"

// DecisionReason captures the outcome of a single strategy evaluation.
// Emitted alongside liveness counters so the UI can show "why nothing is
// happening" beyond raw activity timestamps.
type DecisionReason struct {
	At      time.Time         `json:"at"`
	Outcome string            `json:"outcome"` // "HOLD" | "ENTRY" | "EXIT" | "SUPPRESSED"
	Summary string            `json:"summary"` // short human string
	Tags    map[string]string `json:"tags,omitempty"`
}

// SymbolLiveness is the per-(strategy,symbol) telemetry snapshot returned by
// the /api/strategies/{id}/liveness endpoint. All time fields are serialized
// as RFC3339 (nil time values marshal as the zero time but UI treats
// IsZero() as "never").
type SymbolLiveness struct {
	Symbol              string          `json:"symbol"`
	LastTickAt          time.Time       `json:"lastTickAt"`
	LastEvalAt          time.Time       `json:"lastEvalAt"`
	LastSignalAt        time.Time       `json:"lastSignalAt"`
	EvalCount           uint64          `json:"evalCount"`
	BarsToday           uint64          `json:"barsToday"`
	SignalCount         uint64          `json:"signalCount"`
	FillCount           uint64          `json:"fillCount"`
	FeedType            string          `json:"feedType"`
	FeedLastProcessedAt time.Time       `json:"feedLastProcessedAt"`
	FeedHealthy         bool            `json:"feedHealthy"`
	LastDecision        *DecisionReason `json:"lastDecision,omitempty"`
	// BarsPerMinute is a 60-slot rolling window of eval-count deltas,
	// ordered oldest -> newest relative to the tracker's most recent
	// rotation. Always serialized as a 60-element array; zeros mean "no
	// activity in that minute" or "tracker not yet rotated" (the UI
	// treats them identically).
	BarsPerMinute []uint32 `json:"barsPerMinute"`
}

// StrategyLiveness bundles all symbols tracked for a single strategy.
type StrategyLiveness struct {
	Strategy string           `json:"strategy"`
	Symbols  []SymbolLiveness `json:"symbols"`
	AsOf     time.Time        `json:"asOf"`
}

// StrategyEvaluationPayload is the payload for EventStrategyEvaluation,
// emitted by LivenessTracker.RecordEval at most once per second per
// (strategy, symbol). It carries enough context for the dashboard to pulse
// its liveness dot and update counters without re-polling the REST endpoint.
type StrategyEvaluationPayload struct {
	Strategy     string          `json:"strategy"`
	Symbol       string          `json:"symbol"`
	At           time.Time       `json:"at"`
	EvalCount    uint64          `json:"evalCount"`
	BarsToday    uint64          `json:"barsToday"`
	LastDecision *DecisionReason `json:"lastDecision,omitempty"`
}
