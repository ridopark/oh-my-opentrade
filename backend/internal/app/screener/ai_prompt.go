package screener

import (
	"fmt"
	"strings"
	"time"
)

type CandidateData struct {
	AnonID     string
	Price      float64
	PrevClose  float64
	GapPct     float64
	PMVol      int64
	AvgVol     int64
	RVOL       float64
	ATRPct     float64 // daily ATR as % of price
	NR7        bool    // narrowest range in 7 sessions
	EMA200Bias string  // "BULLISH", "BEARISH", "NEUTRAL", ""
}

func BuildAIScreenerPrompt(strategyDesc string, candidates []CandidateData, asOf time.Time) string {
	var sb strings.Builder

	sb.WriteString("You are a Senior Quantitative Portfolio Manager. Score each candidate 0-5 for fit with the described strategy.\n\n")

	fmt.Fprintf(&sb, "Current Date/Time (ET): %s\n", asOf.Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&sb, "IMPORTANT: Do not use any information after this date.\n\n")

	fmt.Fprintf(&sb, "Strategy Description:\n%s\n\n", strategyDesc)

	sb.WriteString("Candidates:\n")
	sb.WriteString("ID            | Price      | PrevClose  | Gap%%      | PM_Vol     | AvgVol     | RVOL   | ATR%%   | NR7 | Bias\n")
	sb.WriteString("------------- | ---------- | ---------- | --------- | ---------- | ---------- | ------ | ------ | --- | --------\n")
	for _, c := range candidates {
		nr7Str := "-"
		if c.NR7 {
			nr7Str = "Y"
		}
		bias := c.EMA200Bias
		if bias == "" {
			bias = "-"
		}
		fmt.Fprintf(&sb, "%-13s | %10.2f | %10.2f | %+8.2f%% | %10s | %10s | %5.1fx | %5.1f%% | %3s | %s\n",
			c.AnonID, c.Price, c.PrevClose, c.GapPct,
			fmtVol(c.PMVol), fmtVol(c.AvgVol), c.RVOL,
			c.ATRPct, nr7Str, bias)
	}

	sb.WriteString("\nRespond ONLY with a JSON array. Each element: {\"id\": \"ASSET_X\", \"score\": 0-5, \"rationale\": \"brief reason\"}.\n")
	sb.WriteString("No markdown fences, no extra text. Score 0 means no fit, 5 means perfect fit.")

	return sb.String()
}

func fmtVol(v int64) string {
	if v <= 0 {
		return "-"
	}
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(v)/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("%.1fK", float64(v)/1_000)
	default:
		return fmt.Sprintf("%d", v)
	}
}
