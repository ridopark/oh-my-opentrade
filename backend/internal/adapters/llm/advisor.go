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
	"github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// chatMessage is a single message in the OpenAI-compatible chat format.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// providerRouting configures provider-level routing options for OpenRouter.
// See https://openrouter.ai/docs/features/provider-routing
// Fields are omitempty so the struct is a no-op for non-OpenRouter endpoints.
type providerRouting struct {
	Sort                string             `json:"sort,omitempty"`
	PreferredMaxLatency map[string]float64 `json:"preferred_max_latency,omitempty"`
}

// chatRequest is the JSON body sent to the /v1/chat/completions endpoint.
type chatRequest struct {
	Model    string           `json:"model"`
	Messages []chatMessage    `json:"messages"`
	Provider *providerRouting `json:"provider,omitempty"`
}

// chatChoice is a single choice in the OpenAI-compatible response.
type chatChoice struct {
	Message chatMessage `json:"message"`
}

// chatCompletionResponse is the OpenAI-compatible response shape.
type chatCompletionResponse struct {
	Choices []chatChoice `json:"choices"`
}

type debateResult struct {
	Direction      string  `json:"direction"`
	Confidence     float64 `json:"confidence"`
	Rationale      string  `json:"rationale"`
	BullArgument   string  `json:"bull_argument"`
	BearArgument   string  `json:"bear_argument"`
	JudgeReasoning string  `json:"judge_reasoning"`
	RiskModifier   string  `json:"risk_modifier"`
	ContractSymbol string  `json:"contract_symbol"`
	MaxLossUSD     float64 `json:"max_loss_usd"`
	ExitRules      string  `json:"exit_rules"`
}

type responseTemplate struct {
	Direction      string  `json:"direction"`
	Confidence     float64 `json:"confidence"`
	Rationale      string  `json:"rationale"`
	BullArgument   string  `json:"bull_argument"`
	BearArgument   string  `json:"bear_argument"`
	JudgeReasoning string  `json:"judge_reasoning"`
	RiskModifier   string  `json:"risk_modifier"`
	ContractSymbol string  `json:"contract_symbol,omitempty"`
	MaxLossUSD     float64 `json:"max_loss_usd,omitempty"`
	ExitRules      string  `json:"exit_rules,omitempty"`
}

func buildResponseTemplate(withOptions bool) string {
	tmpl := responseTemplate{
		Direction:      "LONG | SHORT | NEUTRAL",
		Confidence:     0.85,
		Rationale:      "concise risk-adjusted reasoning",
		BullArgument:   "key bullish thesis with supporting data",
		BearArgument:   "key bearish thesis with supporting data",
		JudgeReasoning: "risk-reward verdict weighing both sides and worst-case scenario",
		RiskModifier:   "TIGHT | NORMAL | WIDE",
	}
	if withOptions {
		tmpl.ContractSymbol = "AAPL240119C00190000"
		tmpl.MaxLossUSD = 320.0
		tmpl.ExitRules = "Exit at 2x premium ($640) or 21 days before expiry, whichever comes first"
	}
	b, _ := json.MarshalIndent(tmpl, "", "  ")
	return string(b)
}

// ─────────────────────────────────────────────
// Option chain types (public market data only)
// ─────────────────────────────────────────────

// OptionChainSummary is a condensed view of top option contract candidates for the LLM prompt.
// It MUST NOT include proprietary signals or strategy parameters.
// Only public market data (delta, IV, bid/ask, OI, DTE, symbol) is permitted here.
type OptionChainSummary struct {
	Candidates []OptionCandidate
}

// OptionCandidate holds the public market data for a single option contract candidate.
type OptionCandidate struct {
	ContractSymbol string
	Delta          float64
	IV             float64
	Bid            float64
	Ask            float64
	OpenInterest   int
	DTE            int
}

// ─────────────────────────────────────────────
// debateRequest — internal carrier for RequestDebate options
// ─────────────────────────────────────────────

type debateRequest struct {
	optionChain   *OptionChainSummary
	signalContext *signalContext
	perfSummary   *domain.StrategyPerformanceSummary
	newsItems     []domain.NewsItem
}

func (dr *debateRequest) SetStrategyPerformance(summary *domain.StrategyPerformanceSummary) {
	dr.perfSummary = summary
}

func (dr *debateRequest) SetNews(items []domain.NewsItem) {
	dr.newsItems = items
}

// signalContext carries strategy signal metadata for enriched prompts.
type signalContext struct {
	tags       map[string]string
	signalType string  // "entry", "exit", "adjust", "flat"
	side       string  // "buy", "sell"
	strength   float64 // [0,1]
}

// ─────────────────────────────────────────────
// DebateOption functional options
// ─────────────────────────────────────────────

// DebateOption is a functional option for RequestDebate.
// It is a type alias for ports.DebateOption so callers can use either package.
type DebateOption = ports.DebateOption

// WithOptionChain attaches an option chain summary to the debate request.
// When present, the prompt will include chain data and require contract selection output.
func WithOptionChain(chain OptionChainSummary) DebateOption {
	return func(raw any) {
		if dr, ok := raw.(*debateRequest); ok {
			dr.optionChain = &chain
		}
	}
}

// WithSignalContext attaches strategy signal metadata to the debate request.
// When present, the prompt will include a "Signal Context" section with signal type,
// side, strength, and any associated tags (e.g. AVWAP setup name, reference price).
// PRIVACY BOUNDARY: tags come from the strategy pipeline and contain only public-facing
// metadata (setup name, reference price, regime). No DNA parameters or proprietary logic.
func WithSignalContext(tags map[string]string, signalType, side string, strength float64) DebateOption {
	return func(raw any) {
		if dr, ok := raw.(*debateRequest); ok {
			dr.signalContext = &signalContext{
				tags:       tags,
				signalType: signalType,
				side:       side,
				strength:   strength,
			}
		}
	}
}

// ─────────────────────────────────────────────
// Advisor
// ─────────────────────────────────────────────

// Advisor is an HTTP-based implementation of ports.AIAdvisorPort.
// It calls any OpenAI-compatible /v1/chat/completions endpoint
// (OpenAI, Ollama, LM Studio, vLLM, OpenRouter, etc.) with a structured Bull/Bear/Judge
// prompt and parses the JSON embedded in the assistant reply.
// No external SDK dependency — pure net/http.
type Advisor struct {
	baseURL     string
	model       string
	apiKey      string // optional — sent as Authorization: Bearer <key> when non-empty
	httpClient  *http.Client
	minInterval time.Duration    // 0 means no rate limiting
	provider    *providerRouting // optional — OpenRouter provider routing config
	mu          sync.Mutex
	lastCall    time.Time
}

// AdvisorOption is a functional option for Advisor.
type AdvisorOption func(*Advisor)

// WithMinInterval sets the minimum time between consecutive RequestDebate calls.
// Calls made before the interval has elapsed return an error immediately without
// hitting the endpoint. Use this to stay within free-tier rate limits.
func WithMinInterval(d time.Duration) AdvisorOption {
	return func(a *Advisor) { a.minInterval = d }
}

// WithProviderRouting sets OpenRouter provider-level routing options.
// This is a no-op for non-OpenRouter endpoints because the field is omitempty.
// Example: WithProviderRouting("latency", nil) sorts providers by latency.
// Example: WithProviderRouting("", map[string]float64{"p90": 2.0}) sets preferred max latency.
func WithProviderRouting(sort string, preferredMaxLatency map[string]float64) AdvisorOption {
	return func(a *Advisor) {
		a.provider = &providerRouting{
			Sort:                sort,
			PreferredMaxLatency: preferredMaxLatency,
		}
	}
}

// NewAdvisor creates a new Advisor targeting the given base URL.
// model is the LLM model name to request (e.g. "anthropic/claude-sonnet-4", "openai/gpt-4o", "meta-llama/llama-3-70b-instruct").
// apiKey is optional — set it for OpenAI, OpenRouter, or any authenticated endpoint.
// Pass nil for httpClient to use http.DefaultClient.
// opts are functional options (e.g. WithMinInterval).
func NewAdvisor(baseURL, model, apiKey string, httpClient *http.Client, opts ...AdvisorOption) *Advisor {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if model == "" {
		model = "anthropic/claude-sonnet-4"
	}
	a := &Advisor{
		baseURL:    baseURL,
		model:      model,
		apiKey:     apiKey,
		httpClient: httpClient,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// RequestDebate POSTs a structured adversarial debate prompt to /v1/chat/completions
// and parses the JSON embedded in the assistant's reply into an AdvisoryDecision.
// Returns an error if the HTTP call fails, the response is not 2xx,
// or the JSON cannot be parsed.
// The variadic opts allow optional context (e.g. WithOptionChain) without breaking
// existing callers — passing no opts gives identical behavior to the prior signature.
func (a *Advisor) RequestDebate(
	ctx context.Context,
	symbol domain.Symbol,
	regime domain.MarketRegime,
	indicators domain.IndicatorSnapshot,
	opts ...DebateOption,
) (*domain.AdvisoryDecision, error) {
	// Rate-limit guard: reject calls that arrive too soon after the previous one.
	if a.minInterval > 0 {
		a.mu.Lock()
		elapsed := time.Since(a.lastCall)
		if !a.lastCall.IsZero() && elapsed < a.minInterval {
			a.mu.Unlock()
			return nil, fmt.Errorf("llm: rate limit — next call allowed in %s", a.minInterval-elapsed)
		}
		a.lastCall = time.Now()
		a.mu.Unlock()
	}

	// Apply functional options.
	dr := &debateRequest{}
	for _, opt := range opts {
		opt(dr)
	}

	systemPrompt := `You are a Professional Risk Manager overseeing an adversarial trading debate.
Evaluate each setup through a structured Bull vs Bear debate, then render a Judge verdict.
The Judge must weigh risk-reward asymmetry, position sizing implications, and worst-case scenarios before ruling.
Set risk_modifier to control position sizing: TIGHT (reduce size and tighten stop for uncertain setups), NORMAL (standard sizing), or WIDE (wider stop for high-conviction trending setups).
Respond ONLY with valid JSON — no markdown fences, no extra text.`

	userPrompt := buildPrompt(symbol, regime, indicators, dr)

	reqBody, err := json.Marshal(chatRequest{
		Model: a.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Provider: a.provider,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("llm: failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	// OpenRouter-recommended headers for app identification and routing priority.
	// These are harmless no-ops for non-OpenRouter endpoints.
	req.Header.Set("HTTP-Referer", "https://github.com/oh-my-opentrade")
	req.Header.Set("X-Title", "oh-my-opentrade")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: endpoint returned non-2xx status: %d", resp.StatusCode)
	}

	var completion chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		return nil, fmt.Errorf("llm: failed to parse completion response: %w", err)
	}
	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("llm: completion response contained no choices")
	}

	var result debateResult
	if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &result); err != nil {
		return nil, fmt.Errorf("llm: failed to parse debate JSON from assistant reply: %w", err)
	}

	// Options-specific validation — only enforced when an option chain was provided.
	if dr.optionChain != nil {
		if result.Direction == "SHORT" {
			return nil, fmt.Errorf("llm: option debate proposed short position — rejected by MVP constraint")
		}
		if result.ContractSymbol == "" {
			return nil, fmt.Errorf("llm: option debate response missing contract_symbol")
		}
		if result.MaxLossUSD <= 0 {
			return nil, fmt.Errorf("llm: option debate response missing max_loss_usd")
		}
		if result.ExitRules == "" {
			return nil, fmt.Errorf("llm: option debate response missing exit_rules")
		}
	}

	// NEUTRAL means the AI advises no trade — return nil decision (no error).
	// The enricher falls back to the original signal confidence.
	if strings.EqualFold(result.Direction, "NEUTRAL") {
		return nil, nil
	}

	direction, err := domain.NewDirection(result.Direction)
	if err != nil {
		return nil, fmt.Errorf("llm: invalid direction in AI response %q: %w", result.Direction, err)
	}

	return &domain.AdvisoryDecision{
		Direction:      direction,
		Confidence:     result.Confidence,
		Rationale:      result.Rationale,
		BullArgument:   result.BullArgument,
		BearArgument:   result.BearArgument,
		JudgeReasoning: result.JudgeReasoning,
		RiskModifier:   domain.NewRiskModifier(result.RiskModifier),
		ContractSymbol: result.ContractSymbol,
		MaxLossUSD:     result.MaxLossUSD,
		ExitRules:      result.ExitRules,
	}, nil
}

// buildPrompt constructs the structured adversarial debate prompt sent to the LLM.
//
// PRIVACY BOUNDARY — DO NOT CROSS:
// This function intentionally sends ONLY: symbol name, regime type/strength, and six
// standard technical indicator values (RSI, Stoch, EMA, VWAP).
// It MUST NOT send: strategy DNA TOML content, entry/exit rule logic, parameter values,
// proprietary filters, or any internal configuration. Sending strategy DNA to a third-party
// LLM endpoint (especially free-tier models that log prompts for training) would donate
// your trading edge to the model provider.
//
// When chain is non-nil, public option market data (delta, IV, bid/ask, OI, DTE, symbol)
// is appended as a separate section — these are standard market data fields, not proprietary.
//
// When sigCtx is non-nil, strategy signal metadata (type, side, strength, tags) is appended.
// Tags contain only public-facing metadata (setup name, ref price, regime) — no DNA parameters.
func vwapPosition(ema9, vwap float64) string {
	if vwap == 0 {
		return "N/A"
	}
	distPct := ((ema9 - vwap) / vwap) * 100
	switch {
	case distPct >= 2.0:
		return fmt.Sprintf("+%.2f%% above VWAP (overextended)", distPct)
	case distPct >= 0:
		return fmt.Sprintf("+%.2f%% above VWAP", distPct)
	case distPct > -2.0:
		return fmt.Sprintf("%.2f%% below VWAP", distPct)
	default:
		return fmt.Sprintf("%.2f%% below VWAP (overextended)", distPct)
	}
}

func emaTrend(ema9, ema21 float64) string {
	if ema21 == 0 {
		return "N/A"
	}
	spreadPct := ((ema9 - ema21) / ema21) * 100
	if spreadPct > 0 {
		return fmt.Sprintf("Bullish Cross (EMA9 > EMA21, spread: +%.2f%%)", spreadPct)
	}
	if spreadPct < 0 {
		return fmt.Sprintf("Bearish Divergence (EMA9 < EMA21, spread: %.2f%%)", spreadPct)
	}
	return "Flat (EMA9 = EMA21)"
}

func buildPrompt(symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, dr *debateRequest) string {
	chain := dr.optionChain
	sigCtx := dr.signalContext
	jsonTemplate := buildResponseTemplate(chain != nil)

	var sb strings.Builder
	fmt.Fprintf(&sb, `Analyze this trade setup and respond ONLY with valid JSON matching this schema (no markdown, no extra text).
Direction must be "LONG", "SHORT", or "NEUTRAL". Confidence must be 0.0 to 1.0.
%s

Symbol: %s
Market Regime: %s (strength: %.2f)
Technical Indicators:
  RSI: %.2f
  StochK: %.2f
  StochD: %.2f
  EMA9: %.2f
  EMA21: %.2f
  EMA Trend: %s
  VWAP: %.2f
  VWAP Position: %s`,
		jsonTemplate,
		symbol.String(),
		regime.Type.String(),
		regime.Strength,
		indicators.RSI,
		indicators.StochK,
		indicators.StochD,
		indicators.EMA9,
		indicators.EMA21,
		emaTrend(indicators.EMA9, indicators.EMA21),
		indicators.VWAP,
		vwapPosition(indicators.EMA9, indicators.VWAP))

	if len(indicators.AnchorRegimes) > 0 {
		primaryTF := domain.Timeframe("")
		for _, tf := range []domain.Timeframe{"1d", "1h", "15m", "5m"} {
			if _, ok := indicators.AnchorRegimes[tf]; ok {
				primaryTF = tf
				break
			}
		}

		sb.WriteString("\n\nMulti-Timeframe Regimes:")
		for _, tf := range []domain.Timeframe{"1m", "5m", "15m", "1h", "1d"} {
			r, ok := indicators.AnchorRegimes[tf]
			if !ok {
				continue
			}
			label := ""
			if tf == primaryTF {
				label = " (Primary Context)"
			}
			fmt.Fprintf(&sb, "\n  %s: %s (strength: %.2f)%s", tf, r.Type, r.Strength, label)
		}
	}

	if sigCtx != nil {
		fmt.Fprintf(&sb, "\n\nSignal Context:\n  Type: %s\n  Side: %s\n  Strength: %.2f",
			sigCtx.signalType, sigCtx.side, sigCtx.strength)
		if len(sigCtx.tags) > 0 {
			sb.WriteString("\n  Tags:")
			for k, v := range sigCtx.tags {
				fmt.Fprintf(&sb, "\n    %s: %s", k, v)
			}
		}
	}

	if ps := dr.perfSummary; ps != nil && ps.Overall.TradeCount > 0 {
		days := int(ps.Overall.Period.Hours() / 24)
		o := ps.Overall
		fmt.Fprintf(&sb, "\n\nStrategy Track Record (last %d days):", days)
		fmt.Fprintf(&sb, "\n  Overall: %d trades, Win Rate: %.0f%% (%d/%d), Expectancy: $%.2f/trade",
			o.TradeCount, o.WinRate*100, o.WinCount, o.TradeCount, o.Expectancy)
		if o.TotalPnL != 0 {
			fmt.Fprintf(&sb, ", Total P&L: $%.2f", o.TotalPnL)
		}
		if bs := ps.BySymbol; bs != nil && bs.TradeCount > 0 {
			fmt.Fprintf(&sb, "\n  %s only: %d trades, Win Rate: %.0f%%, Expectancy: $%.2f/trade",
				bs.Symbol, bs.TradeCount, bs.WinRate*100, bs.Expectancy)
		}
		for _, r := range ps.ByRegime {
			if r.TradeCount == 0 {
				continue
			}
			label := r.Regime.String()
			if r.Regime == regime.Type {
				label += " (CURRENT)"
			}
			fmt.Fprintf(&sb, "\n  %s: %d trades, Win Rate: %.0f%%, Expectancy: $%.2f/trade",
				label, r.TradeCount, r.WinRate*100, r.Expectancy)
		}
		if ps.HasNegativeExpectancy(regime.Type, 5) {
			sb.WriteString("\n  ⚠ Negative expectancy in current regime")
		}
	}

	if len(dr.newsItems) > 0 {
		sb.WriteString("\n\nRecent News Headlines (most recent first):")
		for i, item := range dr.newsItems {
			fmt.Fprintf(&sb, "\n  %d. [%s] %s (%s)",
				i+1,
				item.CreatedAt.Format("2006-01-02 15:04"),
				item.Headline,
				item.Source)
			if item.Summary != "" {
				summary := item.Summary
				if len(summary) > 200 {
					summary = summary[:200] + "..."
				}
				fmt.Fprintf(&sb, "\n     %s", summary)
			}
		}
		sb.WriteString("\n\nWeigh these headlines when evaluating the trade. News catalysts (earnings, FDA, macro events) can override technical signals.")
	}

	if chain != nil && len(chain.Candidates) > 0 {
		fmt.Fprintf(&sb, "\n\nOption Chain Candidates (top %d by delta proximity):\n", len(chain.Candidates))
		for i, c := range chain.Candidates {
			fmt.Fprintf(&sb, "  %d. %-25s delta=%.2f  IV=%.1f%%  bid=$%.2f  ask=$%.2f  OI=%-6d DTE=%d\n",
				i+1,
				c.ContractSymbol,
				c.Delta,
				c.IV,
				c.Bid,
				c.Ask,
				c.OpenInterest,
				c.DTE)
		}
		sb.WriteString("\nYou MUST select exactly one contract from the candidates above. You MUST NOT propose a short option position.")
	}

	return sb.String()
}

type anchorSelectionResult struct {
	SelectedAnchors []struct {
		CandidateID string  `json:"candidate_id"`
		Rank        int     `json:"rank"`
		Confidence  float64 `json:"confidence"`
		Reason      string  `json:"reason"`
	} `json:"selected_anchors"`
	Rationale string `json:"rationale"`
}

// SelectAnchors sends candidate anchor points to the LLM for ranking and
// selection. Returns the top-ranked anchors with confidence and rationale.
//
// PRIVACY BOUNDARY — same rules as buildPrompt:
// Only public market data (price, volume, time, regime) and candidate metadata
// are sent. No strategy DNA, parameters, or proprietary logic.
func (a *Advisor) SelectAnchors(ctx context.Context, req ports.AnchorSelectionRequest) (*strategy.AnchorSelection, error) {
	if a.minInterval > 0 {
		a.mu.Lock()
		elapsed := time.Since(a.lastCall)
		if !a.lastCall.IsZero() && elapsed < a.minInterval {
			a.mu.Unlock()
			return nil, fmt.Errorf("llm: rate limit — next call allowed in %s", a.minInterval-elapsed)
		}
		a.lastCall = time.Now()
		a.mu.Unlock()
	}

	systemPrompt := `You are an institutional VWAP trader selecting anchor points for Anchored VWAP computation.
Given a list of candidate anchor points (swing highs/lows, volume rotations, weekly opens), select the 5-7 most significant ones.
Rank by: (1) structural importance visible to all market participants, (2) how many times price has respected the level, (3) timeframe significance (daily > hourly > 5min).
Respond ONLY with valid JSON — no markdown fences, no extra text.`

	userPrompt := buildAnchorSelectionPrompt(req)

	reqBody, err := json.Marshal(chatRequest{
		Model: a.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Provider: a.provider,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: failed to marshal anchor selection request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("llm: failed to create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	httpReq.Header.Set("HTTP-Referer", "https://github.com/oh-my-opentrade")
	httpReq.Header.Set("X-Title", "oh-my-opentrade")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: anchor selection HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: anchor selection endpoint returned non-2xx status: %d", resp.StatusCode)
	}

	var completion chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		return nil, fmt.Errorf("llm: failed to parse anchor selection response: %w", err)
	}
	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("llm: anchor selection response contained no choices")
	}

	var result anchorSelectionResult
	if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &result); err != nil {
		return nil, fmt.Errorf("llm: failed to parse anchor selection JSON: %w", err)
	}

	if len(result.SelectedAnchors) == 0 {
		return nil, nil
	}

	selected := make([]strategy.SelectedAnchor, len(result.SelectedAnchors))
	for i, sa := range result.SelectedAnchors {
		selected[i] = strategy.SelectedAnchor{
			CandidateID: sa.CandidateID,
			AnchorName:  sa.CandidateID,
			Rank:        sa.Rank,
			Confidence:  sa.Confidence,
			Reason:      sa.Reason,
		}
	}

	sel, err := strategy.NewAnchorSelection(selected, result.Rationale)
	if err != nil {
		return nil, fmt.Errorf("llm: invalid anchor selection response: %w", err)
	}

	return &sel, nil
}

func buildAnchorSelectionPrompt(req ports.AnchorSelectionRequest) string {
	responseTemplate := `{
  "selected_anchors": [
    {"candidate_id": "swing_high_1h_1710000000", "rank": 1, "confidence": 0.92, "reason": "clear structural resistance tested 3x"},
    {"candidate_id": "volume_rotation_5m_1710050000", "rank": 2, "confidence": 0.85, "reason": "heavy accumulation zone"}
  ],
  "rationale": "Selected 2 key levels with strongest institutional significance"
}`

	var sb strings.Builder
	fmt.Fprintf(&sb, `Select the most significant anchor points for Anchored VWAP computation. Respond ONLY with valid JSON matching this schema:
%s

Symbol: %s
Current Price: $%.2f
Market Regime: %s (strength: %.2f)

Candidate Anchor Points (%d total):
`,
		responseTemplate,
		req.Symbol.String(),
		req.CurrentPrice,
		req.Regime.Type.String(),
		req.Regime.Strength,
		len(req.Candidates))

	for i, c := range req.Candidates {
		distPct := 0.0
		if req.CurrentPrice > 0 {
			distPct = ((c.Price - req.CurrentPrice) / req.CurrentPrice) * 100
		}
		fmt.Fprintf(&sb, "  %d. ID: %s\n     Type: %s | TF: %s | Price: $%.2f (%+.2f%% from current) | Strength: %.1f",
			i+1, c.ID, c.Type, c.Timeframe, c.Price, distPct, c.Strength)
		if c.TouchCount > 0 {
			fmt.Fprintf(&sb, " | Touches: %d", c.TouchCount)
		}
		if c.VolumeContext != nil {
			fmt.Fprintf(&sb, " | Rotation: %d bars, breakout vol: %.0f",
				c.VolumeContext.RotationBars, c.VolumeContext.BreakoutVolume)
		}
		fmt.Fprintf(&sb, " | Time: %s\n", c.Time.Format("2006-01-02 15:04"))
	}

	sb.WriteString("\nSelect 5-7 anchors. Rank 1 = most important. Use the candidate_id values exactly as shown.")

	return sb.String()
}
