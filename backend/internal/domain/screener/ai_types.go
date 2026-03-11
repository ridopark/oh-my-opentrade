package screener

import "time"

// AIScreenerResult is the per-symbol, per-strategy AI evaluation persisted to TimescaleDB.
type AIScreenerResult struct {
	TenantID    string    `json:"tenant_id"`
	EnvMode     string    `json:"env_mode"`
	RunID       string    `json:"run_id"`
	AsOf        time.Time `json:"as_of"`
	StrategyKey string    `json:"strategy_key"`
	Symbol      string    `json:"symbol"`
	AnonID      string    `json:"anon_id"` // e.g. "ASSET_A" — prevents LLM brand bias
	Score       int       `json:"score"`   // 0-5 absolute scale (research: highest human-LLM alignment)
	Rationale   string    `json:"rationale"`
	Model       string    `json:"model"`
	LatencyMS   int64     `json:"latency_ms"`
	PromptHash  string    `json:"prompt_hash"` // SHA-256 for reproducibility audits
	CreatedAt   time.Time `json:"created_at"`
}

type AIRankedSymbol struct {
	Symbol    string `json:"symbol"`
	Score     int    `json:"score"` // 0-5
	Rationale string `json:"rationale"`
}

// AIScreenerCompletedPayload is published as EventAIScreenerCompleted.
type AIScreenerCompletedPayload struct {
	RunID       string           `json:"run_id"`
	AsOf        time.Time        `json:"as_of"`
	StrategyKey string           `json:"strategy_key"`
	Model       string           `json:"model"`
	Candidates  int              `json:"candidates"`
	Ranked      []AIRankedSymbol `json:"ranked"`
	LatencyMS   int64            `json:"latency_ms"`
}
