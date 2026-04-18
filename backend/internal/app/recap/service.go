// Package recap builds the end-of-day trading recap by summarizing today's
// fills + P&L, sending the summary to an LLM, persisting the generated digest,
// and posting it to Discord. Runs inside omo-data; has no hot-path dependency
// on omo-core.
package recap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// PromptVersion is stamped into every recap row. Bump when the prompt
// contract changes so historical digests stay interpretable.
const PromptVersion = "v1"

// TradeFetcher returns all fills in [from, to].
type TradeFetcher interface {
	GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error)
}

// PnLFetcher returns the daily realized P&L for (tenant, env, date) and a
// window of prior days for baseline comparison.
type PnLFetcher interface {
	GetDailyRealizedPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, date time.Time) (float64, error)
	GetDailyPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.DailyPnL, error)
}

// DigestSink persists a generated recap row.
type DigestSink interface {
	Upsert(ctx context.Context, d timescaledb.RecapDigest) error
}

// ChatClient is the minimal shape of an OpenAI-compatible chat endpoint.
// The live implementation posts to /v1/chat/completions; tests use a fake.
type ChatClient interface {
	Chat(ctx context.Context, model, system, user string) (string, error)
}

// Config selects which tenant/env the service summarizes.
type Config struct {
	TenantID string
	EnvMode  domain.EnvMode
	Model    string
}

// Service assembles daily recap digests.
type Service struct {
	cfg      Config
	trades   TradeFetcher
	pnl      PnLFetcher
	sink     DigestSink
	chat     ChatClient
	notifier ports.NotifierPort
	log      zerolog.Logger
	now      func() time.Time
}

func NewService(
	cfg Config,
	trades TradeFetcher, pnl PnLFetcher, sink DigestSink,
	chat ChatClient, notifier ports.NotifierPort,
	log zerolog.Logger,
) *Service {
	if cfg.TenantID == "" {
		cfg.TenantID = "default"
	}
	if cfg.EnvMode == "" {
		cfg.EnvMode = domain.EnvModePaper
	}
	if cfg.Model == "" {
		cfg.Model = "anthropic/claude-sonnet-4"
	}
	return &Service{
		cfg:      cfg,
		trades:   trades,
		pnl:      pnl,
		sink:     sink,
		chat:     chat,
		notifier: notifier,
		log:      log.With().Str("component", "recap").Logger(),
		now:      time.Now,
	}
}

// SetClock is for tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// GenerateDigest builds and persists the digest for the given calendar day
// (interpreted as UTC midnight start). If there were no fills, it still
// writes a short "no-trade" digest and notifies.
func (s *Service) GenerateDigest(ctx context.Context, day time.Time) (timescaledb.RecapDigest, error) {
	dayUTC := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	from := dayUTC
	to := dayUTC.Add(24 * time.Hour)

	fills, err := s.trades.GetTrades(ctx, s.cfg.TenantID, s.cfg.EnvMode, from, to)
	if err != nil {
		return timescaledb.RecapDigest{}, fmt.Errorf("recap: load trades: %w", err)
	}

	netToday, err := s.pnl.GetDailyRealizedPnL(ctx, s.cfg.TenantID, s.cfg.EnvMode, dayUTC)
	if err != nil {
		return timescaledb.RecapDigest{}, fmt.Errorf("recap: load today pnl: %w", err)
	}

	// 5-day baseline ending yesterday (inclusive).
	baselineTo := dayUTC.Add(-24 * time.Hour)
	baselineFrom := baselineTo.AddDate(0, 0, -5)
	baseline, err := s.pnl.GetDailyPnL(ctx, s.cfg.TenantID, s.cfg.EnvMode, baselineFrom, baselineTo)
	if err != nil {
		return timescaledb.RecapDigest{}, fmt.Errorf("recap: load baseline pnl: %w", err)
	}
	baselineAvg := avgRealized(baseline)

	summary := summarizeFills(fills)
	prompt := buildPrompt(dayUTC, netToday, baselineAvg, summary, fills)

	body, err := s.chat.Chat(ctx, s.cfg.Model, systemPrompt, prompt)
	if err != nil {
		return timescaledb.RecapDigest{}, fmt.Errorf("recap: chat: %w", err)
	}
	body = strings.TrimSpace(body)

	d := timescaledb.RecapDigest{
		DigestDate:    dayUTC,
		TenantID:      s.cfg.TenantID,
		EnvMode:       string(s.cfg.EnvMode),
		Body:          body,
		TradesCovered: len(fills),
		NetPnLToday:   netToday,
		PromptVersion: PromptVersion,
		Model:         s.cfg.Model,
		GeneratedAt:   s.now().UTC(),
	}
	if err := s.sink.Upsert(ctx, d); err != nil {
		return d, fmt.Errorf("recap: persist: %w", err)
	}

	if s.notifier != nil {
		header := fmt.Sprintf("EOD Recap %s -- net $%.2f (%d fills)\n\n",
			dayUTC.Format("2006-01-02"), netToday, len(fills))
		if err := s.notifier.Notify(ctx, s.cfg.TenantID, header+body); err != nil {
			s.log.Warn().Err(err).Msg("recap: discord notify failed (non-fatal)")
		}
	}

	s.log.Info().
		Str("date", dayUTC.Format("2006-01-02")).
		Int("fills", len(fills)).
		Float64("net_pnl", netToday).
		Str("model", s.cfg.Model).
		Msg("recap generated")

	return d, nil
}

const systemPrompt = `You are a trading analyst writing a tight end-of-day recap for a paper-trading account. Be direct and specific. No filler, no bullet-point cheerleading, no disclaimers. Under 220 words.`

type strategySummary struct {
	Strategy string  `json:"strategy"`
	Fills    int     `json:"fills"`
	Notional float64 `json:"notional"`
}

type symbolSummary struct {
	Symbol   string  `json:"symbol"`
	Buys     int     `json:"buys"`
	Sells    int     `json:"sells"`
	BuyNotl  float64 `json:"buy_notional"`
	SellNotl float64 `json:"sell_notional"`
	Strategy string  `json:"strategy"`
}

type fillsRollup struct {
	ByStrategy []strategySummary `json:"by_strategy"`
	BySymbol   []symbolSummary   `json:"by_symbol"`
}

func summarizeFills(trades []domain.Trade) fillsRollup {
	byStrategy := map[string]*strategySummary{}
	bySymbol := map[string]*symbolSummary{}
	for _, t := range trades {
		notional := t.Quantity * t.Price
		if t.InstrumentType == domain.InstrumentTypeOption && t.Premium > 0 {
			notional = t.Quantity * t.Premium * 100
		}
		sKey := t.Strategy
		if sKey == "" {
			sKey = "(unlabeled)"
		}
		ss := byStrategy[sKey]
		if ss == nil {
			ss = &strategySummary{Strategy: sKey}
			byStrategy[sKey] = ss
		}
		ss.Fills++
		ss.Notional += notional

		symKey := string(t.Symbol)
		cs := bySymbol[symKey]
		if cs == nil {
			cs = &symbolSummary{Symbol: symKey, Strategy: sKey}
			bySymbol[symKey] = cs
		}
		if strings.EqualFold(t.Side, "BUY") {
			cs.Buys++
			cs.BuyNotl += notional
		} else {
			cs.Sells++
			cs.SellNotl += notional
		}
	}
	stratOut := make([]strategySummary, 0, len(byStrategy))
	for _, v := range byStrategy {
		stratOut = append(stratOut, *v)
	}
	sort.Slice(stratOut, func(i, j int) bool { return stratOut[i].Notional > stratOut[j].Notional })

	symOut := make([]symbolSummary, 0, len(bySymbol))
	for _, v := range bySymbol {
		symOut = append(symOut, *v)
	}
	sort.Slice(symOut, func(i, j int) bool {
		return (symOut[i].BuyNotl + symOut[i].SellNotl) > (symOut[j].BuyNotl + symOut[j].SellNotl)
	})
	if len(symOut) > 25 {
		symOut = symOut[:25]
	}
	return fillsRollup{ByStrategy: stratOut, BySymbol: symOut}
}

func avgRealized(series []domain.DailyPnL) float64 {
	if len(series) == 0 {
		return 0
	}
	var sum float64
	for _, r := range series {
		sum += r.RealizedPnL
	}
	return sum / float64(len(series))
}

func buildPrompt(day time.Time, netToday, baseline float64, rollup fillsRollup, fills []domain.Trade) string {
	rollupJSON, _ := json.MarshalIndent(rollup, "", "  ")
	var fillPreview []map[string]any
	for i, t := range fills {
		if i >= 30 {
			break
		}
		row := map[string]any{
			"time":     t.Time.UTC().Format("15:04"),
			"symbol":   string(t.Symbol),
			"side":     t.Side,
			"qty":      t.Quantity,
			"price":    t.Price,
			"strategy": t.Strategy,
		}
		if t.InstrumentType == domain.InstrumentTypeOption {
			row["option_symbol"] = t.OptionSymbol
			row["premium"] = t.Premium
		}
		if t.Rationale != "" {
			row["rationale"] = t.Rationale
		}
		fillPreview = append(fillPreview, row)
	}
	preview, _ := json.MarshalIndent(fillPreview, "", "  ")

	return fmt.Sprintf(`Date: %s (UTC)
Net realized P&L today: $%.2f
5-day baseline realized P&L avg: $%.2f
Total fills today: %d

Rollup:
%s

First 30 fills:
%s

Write:
1. One line on today vs baseline (direction + rough magnitude).
2. Best + worst trades you can identify from the fills (one sentence each on WHY, not what).
3. Any strategy materially over/underperforming today.
4. One concrete flag for tomorrow (symbol, strategy, or risk).

No opening filler. No bullet points on line 1.`,
		day.Format("2006-01-02"),
		netToday, baseline, len(fills),
		string(rollupJSON), string(preview))
}

// ----------------------------------------------------------------------
// HTTPChatClient -- speaks either the OpenAI-compatible /v1/chat/completions
// protocol or Anthropic's native /v1/messages protocol, selected by
// `provider`. Same Chat(...) interface so the service layer is agnostic.
// ----------------------------------------------------------------------

const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	anthropicVersion  = "2023-06-01"
	anthropicMaxTok   = 1024
)

type HTTPChatClient struct {
	baseURL  string
	apiKey   string
	provider string
	http     *http.Client
}

// NewHTTPChatClient returns a client that speaks the OpenAI-compatible
// chat-completions protocol. Kept for callers that don't care about provider.
func NewHTTPChatClient(baseURL, apiKey string, httpClient *http.Client) *HTTPChatClient {
	return NewHTTPChatClientWithProvider(ProviderOpenAI, baseURL, apiKey, httpClient)
}

// NewHTTPChatClientWithProvider picks the wire protocol via `provider`
// ("openai" or "anthropic"). Empty string == "openai".
func NewHTTPChatClientWithProvider(provider, baseURL, apiKey string, httpClient *http.Client) *HTTPChatClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	if provider == "" {
		provider = ProviderOpenAI
	}
	return &HTTPChatClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		provider: provider,
		http:     httpClient,
	}
}

// -- OpenAI-compatible shapes -----------------------------------------

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatReq struct {
	Model    string    `json:"model"`
	Messages []chatMsg `json:"messages"`
}
type chatChoice struct {
	Message chatMsg `json:"message"`
}
type chatResp struct {
	Choices []chatChoice `json:"choices"`
}

// -- Anthropic native shapes ------------------------------------------

type anthMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type anthReq struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []anthMsg `json:"messages"`
}
type anthContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type anthResp struct {
	Content []anthContent `json:"content"`
}

func (c *HTTPChatClient) Chat(ctx context.Context, model, system, user string) (string, error) {
	if c.provider == ProviderAnthropic {
		return c.chatAnthropic(ctx, model, system, user)
	}
	return c.chatOpenAI(ctx, model, system, user)
}

func (c *HTTPChatClient) chatOpenAI(ctx context.Context, model, system, user string) (string, error) {
	reqBody, err := json.Marshal(chatReq{
		Model: model,
		Messages: []chatMsg{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("HTTP-Referer", "https://github.com/oh-my-opentrade")
	req.Header.Set("X-Title", "oh-my-opentrade")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, snippet)
	}
	var out chatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return out.Choices[0].Message.Content, nil
}

func (c *HTTPChatClient) chatAnthropic(ctx context.Context, model, system, user string) (string, error) {
	reqBody, err := json.Marshal(anthReq{
		Model:     model,
		MaxTokens: anthropicMaxTok,
		System:    system,
		Messages:  []anthMsg{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		snippet := string(body)
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		return "", fmt.Errorf("anthropic status %d: %s", resp.StatusCode, snippet)
	}
	var out anthResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	for _, blk := range out.Content {
		if blk.Type == "text" && blk.Text != "" {
			return blk.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in anthropic response")
}
