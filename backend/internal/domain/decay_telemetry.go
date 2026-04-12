package domain

// RollingDecayPoint represents a single trade in the rolling decay series,
// including rolling profit factor and win rate computed over trailing windows.
type RollingDecayPoint struct {
	TradeSeq    int      `json:"tradeSeq"`
	PnL         float64  `json:"pnl"`
	RollingPF20  *float64 `json:"rollingPf20"`
	RollingPF60  *float64 `json:"rollingPf60"`
	RollingPF120 *float64 `json:"rollingPf120"`
	RollingWR60  *float64 `json:"rollingWr60"`
}

// ComponentAttribution captures PF-with vs PF-without for a single confluence
// component, enabling ablation analysis of scorer value.
type ComponentAttribution struct {
	Component  string   `json:"component"`
	Group      string   `json:"group"`
	NFired     int      `json:"nFired"`
	NNotFired  int      `json:"nNotFired"`
	PFFired    *float64 `json:"pfFired"`
	PFNotFired *float64 `json:"pfNotFired"`
	Marginal   *float64 `json:"marginal"`
}
