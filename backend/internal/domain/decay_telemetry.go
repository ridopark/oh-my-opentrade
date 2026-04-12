package domain

// RollingDecayPoint is a single data point on the rolling PF/WR curve.
type RollingDecayPoint struct {
	TradeSeq    int      `json:"tradeSeq"`
	PnL         float64  `json:"pnl"`
	RollingPF20  *float64 `json:"rollingPf20"`
	RollingPF60  *float64 `json:"rollingPf60"`
	RollingPF120 *float64 `json:"rollingPf120"`
	RollingWR60  *float64 `json:"rollingWr60"`
}

// ComponentAttribution captures the marginal PF contribution of a single
// confluence component, computed via conditional ablation.
type ComponentAttribution struct {
	Component  string   `json:"component"`
	Group      string   `json:"group"`
	NFired     int      `json:"nFired"`
	NNotFired  int      `json:"nNotFired"`
	PFFired    *float64 `json:"pfFired"`
	PFNotFired *float64 `json:"pfNotFired"`
	Marginal   *float64 `json:"marginal"`
}
