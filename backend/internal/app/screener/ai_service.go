package screener

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	screenerdomain "github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/oh-my-opentrade/backend/internal/ports"
	strategyports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

type aiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiChatRequest struct {
	Model    string          `json:"model"`
	Messages []aiChatMessage `json:"messages"`
}

type aiChatChoice struct {
	Message aiChatMessage `json:"message"`
}

type aiChatCompletionResponse struct {
	Choices []aiChatChoice `json:"choices"`
}

type aiTopPick struct {
	Symbol    string
	Score     int
	Rationale string
}

type aiStrategyResult struct {
	StrategyKey string
	Model       string
	Candidates  int
	Scored      int
	LatencyMS   int64
	TopPicks    []aiTopPick
	Err         error
}

type AIService struct {
	log       zerolog.Logger
	cfg       config.AIScreenerConfig
	aiCfg     config.AIConfig
	tenantID  string
	envMode   string
	bus       Bus
	snapshots ports.SnapshotPort
	market    MarketDataProvider
	universe  ports.UniverseProviderPort
	repo      ports.AIScreenerRepoPort
	specStore strategyports.SpecStore
	notifier  ports.NotifierPort
	now       func() time.Time
}

func NewAIService(
	log zerolog.Logger,
	cfg config.AIScreenerConfig,
	aiCfg config.AIConfig,
	tenantID string,
	envMode string,
	bus Bus,
	snapshots ports.SnapshotPort,
	market MarketDataProvider,
	universe ports.UniverseProviderPort,
	repo ports.AIScreenerRepoPort,
	specStore strategyports.SpecStore,
	notifier ports.NotifierPort,
) (*AIService, error) {
	if tenantID == "" {
		return nil, errors.New("tenantID is required")
	}
	if envMode == "" {
		return nil, errors.New("envMode is required")
	}
	if bus == nil {
		return nil, errors.New("bus is required")
	}
	if snapshots == nil {
		return nil, errors.New("snapshots port is required")
	}
	if market == nil {
		return nil, errors.New("market data provider is required")
	}
	if repo == nil {
		return nil, errors.New("repo is required")
	}
	if specStore == nil {
		return nil, errors.New("specStore is required")
	}

	return &AIService{
		log:       log,
		cfg:       cfg,
		aiCfg:     aiCfg,
		tenantID:  tenantID,
		envMode:   envMode,
		bus:       bus,
		snapshots: snapshots,
		market:    market,
		universe:  universe,
		repo:      repo,
		specStore: specStore,
		notifier:  notifier,
		now:       time.Now,
	}, nil
}

func (s *AIService) Start(ctx context.Context) error {
	if !s.cfg.Enabled {
		s.log.Info().Msg("ai screener disabled")
		return nil
	}
	s.bootstrapFromDB(ctx)
	go s.schedulerLoop(ctx)
	return nil
}

func (s *AIService) bootstrapFromDB(ctx context.Context) {
	specs, err := s.specStore.List(ctx, nil)
	if err != nil {
		s.log.Warn().Err(err).Msg("ai screener bootstrap: failed to list specs")
		return
	}

	restored := 0
	for _, spec := range specs {
		if spec.Screening.Description == "" {
			continue
		}
		strategyKey := string(spec.ID)
		results, err := s.repo.GetLatestAIResults(ctx, s.tenantID, s.envMode, strategyKey)
		if err != nil {
			s.log.Warn().Err(err).Str("strategy", strategyKey).Msg("ai screener bootstrap: failed to load results")
			continue
		}
		if len(results) == 0 {
			continue
		}

		ranked := make([]screenerdomain.AIRankedSymbol, 0, len(results))
		for _, r := range results {
			ranked = append(ranked, screenerdomain.AIRankedSymbol{
				Symbol:    r.Symbol,
				Score:     r.Score,
				Rationale: r.Rationale,
			})
		}

		payload := screenerdomain.AIScreenerCompletedPayload{
			RunID:       results[0].RunID,
			AsOf:        results[0].AsOf,
			StrategyKey: strategyKey,
			Model:       results[0].Model,
			Candidates:  len(results),
			Ranked:      ranked,
			LatencyMS:   0,
		}
		ev, err := domain.NewEvent(
			domain.EventAIScreenerCompleted,
			s.tenantID,
			domain.EnvMode(s.envMode),
			results[0].RunID+"-bootstrap-"+strategyKey,
			payload,
		)
		if err != nil {
			s.log.Warn().Err(err).Str("strategy", strategyKey).Msg("ai screener bootstrap: failed to create event")
			continue
		}
		if err := s.bus.Publish(ctx, *ev); err != nil {
			s.log.Warn().Err(err).Str("strategy", strategyKey).Msg("ai screener bootstrap: failed to publish event")
			continue
		}
		restored++
		s.log.Info().
			Str("strategy", strategyKey).
			Str("run_id", results[0].RunID).
			Time("as_of", results[0].AsOf).
			Int("symbols", len(ranked)).
			Msg("ai screener bootstrap: restored from DB")
	}

	if restored > 0 {
		s.log.Info().Int("strategies_restored", restored).Msg("ai screener bootstrap: complete")
	}
}

func (s *AIService) schedulerLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		next := nextRunTimeET(s.now(), s.cfg.AIRunAtHourET, s.cfg.AIRunAtMinuteET)
		if isNonTradingDay(next) {
			next = nextRunTimeET(next.Add(24*time.Hour), s.cfg.AIRunAtHourET, s.cfg.AIRunAtMinuteET)
		}
		sleep := time.Until(next)
		if sleep > 0 {
			timer := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		if isNonTradingDay(next) {
			continue
		}

		if err := s.RunAIScreen(ctx, next); err != nil {
			s.log.Error().Err(err).Msg("ai screener run failed")
		}
	}
}

func filterByAssetClass(symbols []string, assetClasses []string) []string {
	if len(assetClasses) == 0 {
		return symbols
	}
	wantCrypto, wantEquity := false, false
	for _, ac := range assetClasses {
		switch strings.ToUpper(ac) {
		case "CRYPTO":
			wantCrypto = true
		case "EQUITY":
			wantEquity = true
		}
	}
	if wantCrypto && wantEquity {
		return symbols
	}
	var out []string
	for _, sym := range symbols {
		isCrypto := strings.Contains(sym, "/")
		if isCrypto && wantCrypto {
			out = append(out, sym)
		} else if !isCrypto && wantEquity {
			out = append(out, sym)
		}
	}
	return out
}

const snapshotBatchSize = 500

func (s *AIService) RunAIScreen(ctx context.Context, asOfET time.Time) error {
	specs, err := s.specStore.List(ctx, nil)
	if err != nil {
		return fmt.Errorf("ai_screener: list specs: %w", err)
	}

	var activeSpecs []strategyports.Spec
	for _, spec := range specs {
		if spec.Screening.Description != "" {
			activeSpecs = append(activeSpecs, spec)
		}
	}
	if len(activeSpecs) == 0 {
		s.log.Info().Msg("ai screener: no strategies with screening descriptions")
		return nil
	}

	symList, err := s.buildCandidateUniverse(ctx, activeSpecs)
	if err != nil {
		return fmt.Errorf("ai_screener: build universe: %w", err)
	}
	if len(symList) == 0 {
		s.log.Warn().Msg("ai screener: no symbols after universe expansion")
		return nil
	}

	snaps, err := s.getSnapshotsBatched(ctx, symList, asOfET)
	if err != nil {
		return fmt.Errorf("ai_screener: get snapshots: %w", err)
	}

	passed := Pass0Filter(snaps, s.cfg)
	s.log.Info().
		Int("universe", len(symList)).
		Int("snapshots", len(snaps)).
		Int("pass0_survivors", len(passed)).
		Msg("ai screener: pass0 complete")

	if len(passed) == 0 {
		s.log.Warn().Msg("ai screener: no symbols survived pass0")
		return nil
	}

	passedSnaps := make(map[string]ports.Snapshot, len(passed))
	for _, sym := range passed {
		if snap, ok := snaps[sym]; ok {
			passedSnaps[sym] = snap
		}
	}

	runID := uuid.NewString()
	runStart := time.Now()

	results := make([]aiStrategyResult, 0, len(activeSpecs))
	for _, spec := range activeSpecs {
		filtered := filterByAssetClass(passed, spec.Routing.AssetClasses)
		filteredSnaps := make(map[string]ports.Snapshot, len(filtered))
		for _, sym := range filtered {
			if snap, ok := passedSnaps[sym]; ok {
				filteredSnaps[sym] = snap
			}
		}
		r := aiStrategyResult{StrategyKey: string(spec.ID), Candidates: len(filtered)}
		if len(filtered) == 0 {
			s.log.Info().Str("strategy", string(spec.ID)).Strs("asset_classes", spec.Routing.AssetClasses).Msg("ai screener: no candidates for asset class")
			results = append(results, r)
			continue
		}
		if err := s.runForStrategy(ctx, spec, filteredSnaps, filtered, runID, asOfET, &r); err != nil {
			r.Err = err
			s.log.Error().Err(err).Str("strategy", string(spec.ID)).Msg("ai screener: strategy run failed")
		}
		results = append(results, r)
	}

	totalDuration := time.Since(runStart)
	s.sendRunSummary(ctx, results, len(symList), len(snaps), len(passed), totalDuration, asOfET)

	return nil
}

func (s *AIService) buildCandidateUniverse(ctx context.Context, specs []strategyports.Spec) ([]string, error) {
	seen := make(map[string]struct{})

	if s.universe != nil {
		for _, ac := range []domain.AssetClass{domain.AssetClassEquity, domain.AssetClassCrypto} {
			assets, err := s.universe.ListTradeable(ctx, ac)
			if err != nil {
				s.log.Warn().Err(err).Str("asset_class", string(ac)).Msg("ai screener: universe fetch failed, falling back to static symbols")
				continue
			}
			for _, a := range assets {
				seen[a.Symbol] = struct{}{}
			}
		}
	}

	if len(seen) == 0 {
		for _, spec := range specs {
			for _, sym := range spec.Routing.Symbols {
				seen[sym] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(seen))
	for sym := range seen {
		out = append(out, sym)
	}
	return out, nil
}

func (s *AIService) getSnapshotsBatched(ctx context.Context, symbols []string, asOf time.Time) (map[string]ports.Snapshot, error) {
	if len(symbols) <= snapshotBatchSize {
		return s.snapshots.GetSnapshots(ctx, symbols, asOf)
	}

	all := make(map[string]ports.Snapshot, len(symbols))
	for i := 0; i < len(symbols); i += snapshotBatchSize {
		end := i + snapshotBatchSize
		if end > len(symbols) {
			end = len(symbols)
		}
		batch, err := s.snapshots.GetSnapshots(ctx, symbols[i:end], asOf)
		if err != nil {
			s.log.Warn().Err(err).Int("batch_start", i).Int("batch_end", end).Msg("ai screener: snapshot batch failed")
			continue
		}
		for sym, snap := range batch {
			all[sym] = snap
		}
	}
	return all, nil
}

func (s *AIService) runForStrategy(
	ctx context.Context,
	spec strategyports.Spec,
	snaps map[string]ports.Snapshot,
	candidateSymbols []string,
	runID string,
	asOfET time.Time,
	result *aiStrategyResult,
) error {
	symbols := candidateSymbols
	if len(symbols) == 0 {
		return nil
	}

	maxCandidates := s.cfg.MaxCandidatesPerCall
	if maxCandidates > 0 && len(symbols) > maxCandidates {
		symbols = symbols[:maxCandidates]
	}

	anonSymbols, mapping := Anonymize(symbols)

	candidates := make([]CandidateData, 0, len(symbols))
	for i, sym := range symbols {
		cd := CandidateData{AnonID: anonSymbols[i]}
		if snap, ok := snaps[sym]; ok {
			if snap.PrevClose != nil {
				cd.PrevClose = *snap.PrevClose
			}
			if snap.PreMarketPrice != nil && *snap.PreMarketPrice > 0 {
				cd.Price = *snap.PreMarketPrice
			} else if snap.LastTradePrice != nil {
				cd.Price = *snap.LastTradePrice
			}
			if cd.PrevClose > 0 && cd.Price > 0 {
				cd.GapPct = (cd.Price - cd.PrevClose) / cd.PrevClose * 100.0
			}
			if snap.PreMarketVolume != nil {
				cd.PMVol = *snap.PreMarketVolume
			}
			if snap.PrevDailyVolume != nil {
				cd.AvgVol = *snap.PrevDailyVolume
			}
			if cd.AvgVol > 0 && cd.PMVol > 0 {
				cd.RVOL = float64(cd.PMVol) / float64(cd.AvgVol)
			}
		}
		candidates = append(candidates, cd)
	}

	prompt := BuildAIScreenerPrompt(spec.Screening.Description, candidates, asOfET)
	promptHash := fmt.Sprintf("%x", sha256.Sum256([]byte(prompt)))

	strategyKey := string(spec.ID)
	start := time.Now()

	content, model, err := s.callLLM(ctx, prompt)
	latencyMS := time.Since(start).Milliseconds()
	result.Model = model
	result.LatencyMS = latencyMS
	if err != nil {
		return fmt.Errorf("ai_screener: llm call for %s: %w", strategyKey, err)
	}

	scores, err := ParseAIScreenerResponse(content)
	if err != nil {
		return fmt.Errorf("ai_screener: parse response for %s: %w", strategyKey, err)
	}
	result.Scored = len(scores)

	nowUTC := s.now().UTC()
	results := make([]screenerdomain.AIScreenerResult, 0, len(scores))
	ranked := make([]screenerdomain.AIRankedSymbol, 0, len(scores))

	for _, sc := range scores {
		realSymbol, ok := Deanonymize(mapping, sc.AnonID)
		if !ok {
			continue
		}
		results = append(results, screenerdomain.AIScreenerResult{
			TenantID:    s.tenantID,
			EnvMode:     s.envMode,
			RunID:       runID,
			AsOf:        asOfET.UTC(),
			StrategyKey: strategyKey,
			Symbol:      realSymbol,
			AnonID:      sc.AnonID,
			Score:       sc.Score,
			Rationale:   sc.Rationale,
			Model:       model,
			LatencyMS:   latencyMS,
			PromptHash:  promptHash,
			CreatedAt:   nowUTC,
		})
		ranked = append(ranked, screenerdomain.AIRankedSymbol{
			Symbol:    realSymbol,
			Score:     sc.Score,
			Rationale: sc.Rationale,
		})
	}

	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	for _, r := range ranked {
		if r.Score >= 3 {
			result.TopPicks = append(result.TopPicks, aiTopPick{
				Symbol:    r.Symbol,
				Score:     r.Score,
				Rationale: r.Rationale,
			})
		}
	}

	if len(results) > 0 {
		if err := s.repo.SaveAIResults(ctx, results); err != nil {
			return fmt.Errorf("ai_screener: save results for %s: %w", strategyKey, err)
		}
	}

	payload := screenerdomain.AIScreenerCompletedPayload{
		RunID:       runID,
		AsOf:        asOfET.UTC(),
		StrategyKey: strategyKey,
		Model:       model,
		Candidates:  len(candidates),
		Ranked:      ranked,
		LatencyMS:   latencyMS,
	}
	ev, err := domain.NewEvent(
		domain.EventAIScreenerCompleted,
		s.tenantID,
		domain.EnvMode(s.envMode),
		runID+"-ai-screener-"+strategyKey,
		payload,
	)
	if err != nil {
		return fmt.Errorf("ai_screener: new event for %s: %w", strategyKey, err)
	}
	if err := s.bus.Publish(ctx, *ev); err != nil {
		return fmt.Errorf("ai_screener: publish completed for %s: %w", strategyKey, err)
	}

	s.log.Info().
		Str("strategy", strategyKey).
		Str("model", model).
		Int("candidates", len(candidates)).
		Int("scored", len(scores)).
		Int64("latency_ms", latencyMS).
		Msg("ai screener completed")

	return nil
}

func (s *AIService) sendRunSummary(ctx context.Context, results []aiStrategyResult, universe, snapshots, pass0 int, duration time.Duration, asOfET time.Time) {
	if s.notifier == nil {
		return
	}

	notifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	succeeded, failed := 0, 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		} else {
			succeeded++
		}
	}

	var header strings.Builder
	header.WriteString("🔬 **AI Pre-Market Screener**\n")
	fmt.Fprintf(&header, "⏰ %s ET | Duration: **%s**\n", asOfET.Format("15:04"), fmtDurationShort(duration))
	fmt.Fprintf(&header, "🌐 Universe: %d → Snapshots: %d → Pass0: **%d**\n", universe, snapshots, pass0)
	fmt.Fprintf(&header, "📊 **%d/%d strategies succeeded**", succeeded, len(results))
	if failed > 0 {
		header.WriteString("\n")
		for _, r := range results {
			if r.Err != nil {
				fmt.Fprintf(&header, "❌ **%s** — %s\n", r.StrategyKey, shortenError(r.Err))
			}
		}
	}

	if err := s.notifier.Notify(notifyCtx, s.tenantID, header.String()); err != nil {
		s.log.Warn().Err(err).Msg("ai screener: failed to send header notification")
	}

	for _, r := range results {
		if r.Err != nil || len(r.TopPicks) == 0 {
			continue
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "🎯 **%s** (%dms)\n", r.StrategyKey, r.LatencyMS)
		for _, p := range r.TopPicks {
			fmt.Fprintf(&sb, "• **%s** [%d/5] — %s\n", p.Symbol, p.Score, p.Rationale)
		}

		if err := s.notifier.Notify(notifyCtx, s.tenantID, sb.String()); err != nil {
			s.log.Warn().Err(err).Str("strategy", r.StrategyKey).Msg("ai screener: failed to send strategy notification")
		}
	}
}

func fmtDurationShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	m := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm %ds", m, secs)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func shortenError(err error) string {
	msg := err.Error()
	if idx := strings.LastIndex(msg, ": "); idx > 0 && idx < len(msg)-2 {
		msg = msg[idx+2:]
	}
	if len(msg) > 80 {
		msg = msg[:77] + "..."
	}
	return msg
}

func (s *AIService) callLLM(ctx context.Context, prompt string) (content string, model string, err error) {
	for _, m := range s.cfg.Models {
		content, err = s.callModel(ctx, m, prompt)
		if err == nil {
			return content, m, nil
		}
		s.log.Warn().Err(err).Str("model", m).Msg("ai screener: model failed, trying next")
	}
	return "", "", fmt.Errorf("ai_screener: all models failed, last error: %w", err)
}

func (s *AIService) callModel(ctx context.Context, model, prompt string) (string, error) {
	reqBody, err := json.Marshal(aiChatRequest{
		Model: model,
		Messages: []aiChatMessage{
			{Role: "system", Content: "You are a Senior Quantitative Portfolio Manager. Score each candidate 0-5 for fit with the described strategy."},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.aiCfg.BaseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.aiCfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.aiCfg.APIKey)
	}
	req.Header.Set("HTTP-Referer", "https://github.com/oh-my-opentrade")
	req.Header.Set("X-Title", "oh-my-opentrade")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("endpoint returned status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var completion aiChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		return "", fmt.Errorf("parse completion: %w", err)
	}
	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("completion contained no choices")
	}

	return completion.Choices[0].Message.Content, nil
}
