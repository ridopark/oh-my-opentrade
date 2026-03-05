package screener

import "time"

type TickedPayload struct {
	AsOf time.Time `json:"as_of"`
}

type RankedSymbol struct {
	Symbol     string   `json:"symbol"`
	GapPct     *float64 `json:"gap_pct,omitempty"`
	RVOL       *float64 `json:"rvol,omitempty"`
	NewsScore  *float64 `json:"news_score,omitempty"`
	TotalScore float64  `json:"total_score"`
}

type CompletedPayload struct {
	RunID    string         `json:"run_id"`
	AsOf     time.Time      `json:"as_of"`
	Universe int            `json:"universe"`
	TopN     int            `json:"top_n"`
	Ranked   []RankedSymbol `json:"ranked"`
}

type EffectiveSymbolsUpdatedPayload struct {
	StrategyKey string    `json:"strategy_key"`
	RunID       string    `json:"run_id"`
	AsOf        time.Time `json:"as_of"`
	Mode        string    `json:"mode"`
	Source      string    `json:"source"`
	Symbols     []string  `json:"symbols"`
}
