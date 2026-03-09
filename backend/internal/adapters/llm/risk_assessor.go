package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type riskAssessmentResult struct {
	ThesisStatus    string  `json:"thesis_status"`
	Action          string  `json:"action"`
	Confidence      float64 `json:"confidence"`
	Reasoning       string  `json:"reasoning"`
	UpdatedModifier string  `json:"updated_modifier"`
	ScaleOutPct     float64 `json:"scale_out_pct"`
}

// RiskAssessor evaluates open positions via an OpenAI-compatible LLM endpoint.
type RiskAssessor struct {
	baseURL     string
	model       string
	apiKey      string
	httpClient  *http.Client
	minInterval time.Duration
	provider    *providerRouting
	mu          sync.Mutex
	lastCall    time.Time
}

type RiskAssessorOption func(*RiskAssessor)

func WithRiskAssessorMinInterval(d time.Duration) RiskAssessorOption {
	return func(r *RiskAssessor) { r.minInterval = d }
}

func WithRiskAssessorProviderRouting(sort string, preferredMaxLatency map[string]float64) RiskAssessorOption {
	return func(r *RiskAssessor) {
		r.provider = &providerRouting{
			Sort:                sort,
			PreferredMaxLatency: preferredMaxLatency,
		}
	}
}

func NewRiskAssessor(baseURL, model, apiKey string, httpClient *http.Client, opts ...RiskAssessorOption) *RiskAssessor {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if model == "" {
		model = "anthropic/claude-sonnet-4"
	}
	r := &RiskAssessor{
		baseURL:    baseURL,
		model:      model,
		apiKey:     apiKey,
		httpClient: httpClient,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *RiskAssessor) AssessPosition(
	ctx context.Context,
	position domain.MonitoredPosition,
	indicators domain.IndicatorSnapshot,
	regime domain.MarketRegime,
) (*domain.RiskRevaluation, error) {
	if r.minInterval > 0 {
		r.mu.Lock()
		elapsed := time.Since(r.lastCall)
		if !r.lastCall.IsZero() && elapsed < r.minInterval {
			r.mu.Unlock()
			return nil, fmt.Errorf("risk_assessor: rate limit — next call allowed in %s", r.minInterval-elapsed)
		}
		r.lastCall = time.Now()
		r.mu.Unlock()
	}

	systemPrompt := `You are a Risk Monitoring Officer evaluating an open trading position.
Compare the original entry thesis against current market conditions.
Determine if the thesis is still valid, degrading, or invalidated.
Recommend: HOLD (thesis intact), TIGHTEN (increase caution), SCALE_OUT (reduce exposure), or EXIT (thesis broken).
Set updated_modifier: TIGHT (reduce risk), NORMAL (maintain), or WIDE (give room).
CONSTRAINT: If action is TIGHTEN, updated_modifier MUST be TIGHT. WIDE is only valid with HOLD.
scale_out_pct is 0.0 unless action is SCALE_OUT (then 0.25–0.75).
IMPORTANT: Factor in volume and liquidity conditions. During low-volume periods (off-peak hours, weekends), prefer HOLD with WIDE modifier during low liquidity unless thesis is clearly invalidated.
Respond ONLY with valid JSON — no markdown fences, no extra text.`

	userPrompt := buildRiskAssessmentPrompt(position, indicators, regime)

	reqBody, err := json.Marshal(chatRequest{
		Model: r.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Provider: r.provider,
	})
	if err != nil {
		return nil, fmt.Errorf("risk_assessor: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("risk_assessor: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	req.Header.Set("HTTP-Referer", "https://github.com/oh-my-opentrade")
	req.Header.Set("X-Title", "oh-my-opentrade")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("risk_assessor: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("risk_assessor: endpoint returned non-2xx status: %d", resp.StatusCode)
	}

	var completion chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		return nil, fmt.Errorf("risk_assessor: parse completion response: %w", err)
	}
	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("risk_assessor: completion response contained no choices")
	}

	var result riskAssessmentResult
	if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &result); err != nil {
		return nil, fmt.Errorf("risk_assessor: parse assessment JSON: %w", err)
	}

	thesisStatus := parseThesisStatus(result.ThesisStatus)
	action := parseRiskAction(result.Action)

	return &domain.RiskRevaluation{
		Symbol:          position.Symbol,
		ThesisStatus:    thesisStatus,
		Action:          action,
		Confidence:      result.Confidence,
		Reasoning:       result.Reasoning,
		UpdatedModifier: domain.NewRiskModifier(result.UpdatedModifier),
		ScaleOutPct:     result.ScaleOutPct,
		EvaluatedAt:     time.Now(),
	}, nil
}

func parseThesisStatus(s string) domain.ThesisStatus {
	switch domain.ThesisStatus(strings.ToUpper(s)) {
	case domain.ThesisIntact, domain.ThesisDegrading, domain.ThesisInvalidated:
		return domain.ThesisStatus(strings.ToUpper(s))
	default:
		return domain.ThesisIntact
	}
}

func parseRiskAction(s string) domain.RiskAction {
	switch domain.RiskAction(strings.ToUpper(s)) {
	case domain.RiskActionHold, domain.RiskActionTighten, domain.RiskActionScaleOut, domain.RiskActionExit:
		return domain.RiskAction(strings.ToUpper(s))
	default:
		return domain.RiskActionHold
	}
}

func buildRiskAssessmentPrompt(pos domain.MonitoredPosition, indicators domain.IndicatorSnapshot, regime domain.MarketRegime) string {
	responseTemplate := `{
  "thesis_status": "INTACT | DEGRADING | INVALIDATED",
  "action": "HOLD | TIGHTEN | SCALE_OUT | EXIT",
  "confidence": 0.85,
  "reasoning": "brief risk assessment explaining thesis validity",
  "updated_modifier": "TIGHT | NORMAL | WIDE",
  "scale_out_pct": 0.0
}`

	currentPrice := indicators.EMA9
	if currentPrice == 0 {
		currentPrice = pos.EntryPrice
	}
	unrealizedPnLPct := pos.UnrealizedPnLPct(currentPrice)
	drawdownPct := pos.DrawdownFromHighPct(currentPrice)
	holdDuration := time.Since(pos.EntryTime)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		`Evaluate this open position and respond ONLY with valid JSON matching this schema (no markdown, no extra text).
%s

Position Details:
  Symbol: %s
  Direction: LONG
  Entry Price: $%.2f
  Current Price: $%.2f
  Unrealized P&L: %+.2f%%
  Quantity: %.2f
  Hold Duration: %s
  High Water Mark: $%.2f
  Drawdown from HWM: %.2f%%
  Strategy: %s`,
		responseTemplate,
		pos.Symbol,
		pos.EntryPrice,
		currentPrice,
		unrealizedPnLPct*100,
		pos.Quantity,
		formatHoldDuration(holdDuration),
		pos.HighWaterMark,
		drawdownPct*100,
		pos.Strategy,
	))

	if pos.EntryThesis != nil {
		sb.WriteString(fmt.Sprintf(`

Entry Thesis (what we believed when entering):
  Direction: %s
  Confidence: %.0f%%
  Risk Modifier: %s
  Entry Regime: %s`,
			pos.EntryThesis.Direction,
			pos.EntryThesis.Confidence*100,
			pos.EntryThesis.RiskModifier,
			pos.EntryThesis.EntryRegime,
		))
		if pos.EntryThesis.BullArgument != "" {
			sb.WriteString(fmt.Sprintf("\n  Bull Thesis: %s", pos.EntryThesis.BullArgument))
		}
		if pos.EntryThesis.BearArgument != "" {
			sb.WriteString(fmt.Sprintf("\n  Bear Thesis: %s", pos.EntryThesis.BearArgument))
		}
		if pos.EntryThesis.JudgeReasoning != "" {
			sb.WriteString(fmt.Sprintf("\n  Judge Reasoning: %s", pos.EntryThesis.JudgeReasoning))
		}
	}

	sb.WriteString(fmt.Sprintf(`

Current Market State:
  Regime: %s (strength: %.2f)
  RSI: %.2f
  StochK: %.2f
  StochD: %.2f
  EMA9: %.2f
  EMA21: %.2f
  EMA Trend: %s
  VWAP: %.2f
  VWAP Position: %s`,
		regime.Type.String(),
		regime.Strength,
		indicators.RSI,
		indicators.StochK,
		indicators.StochD,
		indicators.EMA9,
		indicators.EMA21,
		emaTrend(indicators.EMA9, indicators.EMA21),
		indicators.VWAP,
		vwapPosition(indicators.EMA9, indicators.VWAP),
	))

	if pos.EntryThesis != nil && pos.EntryThesis.EntryRegime != "" {
		if pos.EntryThesis.EntryRegime != regime.Type.String() {
			sb.WriteString(fmt.Sprintf("\n  ⚠ REGIME CHANGED: %s → %s", pos.EntryThesis.EntryRegime, regime.Type.String()))
		}
	}

	rvol := 0.0
	if indicators.VolumeSMA > 0 {
		rvol = indicators.Volume / indicators.VolumeSMA
	}
	liqRegime := classifyLiquidityRegime(rvol, time.Now())
	sb.WriteString(fmt.Sprintf(`

Volume & Liquidity:
  Current Volume: %.2f
  Volume SMA(20): %.2f
  Relative Volume: %.2fx %s
  ATR(14): %.4f
  Liquidity Regime: %s`,
		indicators.Volume,
		indicators.VolumeSMA,
		rvol, rvolLabel(rvol),
		indicators.ATR,
		liqRegime,
	))
	if liqRegime == "LOW" {
		sb.WriteString("\n  ⚠ LOW LIQUIDITY: Tight stops risk triggering on noise/flash wicks. Consider wider stops or HOLD.")
	}

	return sb.String()
}

func formatHoldDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	totalMin := int(d.Minutes())
	if totalMin < 60 {
		return fmt.Sprintf("%dm", totalMin)
	}
	h := totalMin / 60
	m := totalMin % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

func rvolLabel(rvol float64) string {
	switch {
	case rvol >= 1.5:
		return "(HIGH)"
	case rvol >= 0.7:
		return "(NORMAL)"
	case rvol > 0:
		return "(LOW)"
	default:
		return "(N/A)"
	}
}

func classifyLiquidityRegime(rvol float64, now time.Time) string {
	hour := now.UTC().Hour()
	isWeekend := now.UTC().Weekday() == time.Saturday || now.UTC().Weekday() == time.Sunday

	if rvol > 0 && rvol < 0.5 {
		return "LOW"
	}

	if isWeekend || hour >= 22 || hour < 8 {
		if rvol > 0 && rvol >= 1.0 {
			return "MEDIUM"
		}
		return "LOW"
	}

	if hour >= 13 && hour <= 18 {
		return "HIGH"
	}

	return "MEDIUM"
}

// NoOpRiskAssessor implements ports.RiskAssessorPort but always returns nil.
type NoOpRiskAssessor struct{}

func NewNoOpRiskAssessor() *NoOpRiskAssessor {
	return &NoOpRiskAssessor{}
}

func (n *NoOpRiskAssessor) AssessPosition(
	_ context.Context,
	_ domain.MonitoredPosition,
	_ domain.IndicatorSnapshot,
	_ domain.MarketRegime,
) (*domain.RiskRevaluation, error) {
	return nil, nil
}
