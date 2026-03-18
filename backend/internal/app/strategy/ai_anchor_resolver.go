package strategy

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

const (
	maxCandidatesPerSymbol = 50
	maxActiveAnchors       = 20
	defaultSelectCount     = 7
)

var tfWeights = map[string]float64{
	"1m": 0.5, "5m": 1.0, "15m": 1.5, "1h": 2.0, "4h": 2.5, "1d": 3.0,
}

type symbolDetectors struct {
	swings map[string]*start.SwingDetector
	volume map[string]*start.VolumeProfiler
	weekly *start.WeeklyAnchorDetector
}

// SessionAnchorFn is the legacy anchor resolver signature used by
// SessionResolver.ResolveAnchors. When set, its results are included
// as baseline session-derived candidates so AVWAP has anchors from bar 1
// before the algo detectors have warmed up.
type SessionAnchorFn func(symbol string, barTime time.Time, anchors []string) map[string]time.Time

// AIAnchorResolver orchestrates candidate anchor detection (algo) and AI
// selection to provide AVWAP anchor points. It feeds bars through
// deterministic detectors, accumulates candidates, and on ResolveAnchors
// queries the LLM (or falls back to deterministic ranking).
type AIAnchorResolver struct {
	advisor   ports.AIAdvisorPort
	store     ports.AnchorStorePort
	scorer    *start.MultiTimeframeScorer
	logger    *slog.Logger
	sessionFn SessionAnchorFn

	mu         sync.RWMutex
	detectors  map[string]*symbolDetectors
	candidates map[string][]start.CandidateAnchor
}

func NewAIAnchorResolver(advisor ports.AIAdvisorPort, store ports.AnchorStorePort, logger *slog.Logger) *AIAnchorResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &AIAnchorResolver{
		advisor:    advisor,
		store:      store,
		scorer:     start.NewMultiTimeframeScorer(),
		logger:     logger.With("component", "ai_anchor_resolver"),
		detectors:  make(map[string]*symbolDetectors),
		candidates: make(map[string][]start.CandidateAnchor),
	}
}

func (r *AIAnchorResolver) SetSessionResolver(fn SessionAnchorFn) {
	r.sessionFn = fn
}

func (r *AIAnchorResolver) RegisterSymbol(symbol string, isCrypto bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.detectors[symbol] = &symbolDetectors{
		swings: map[string]*start.SwingDetector{
			"5m": start.NewSwingDetector(5, "5m"),
			"1h": start.NewSwingDetector(3, "1h"),
			"1d": start.NewSwingDetector(2, "1d"),
		},
		volume: map[string]*start.VolumeProfiler{
			"5m": start.NewVolumeProfiler(0.25, 20, "5m"),
		},
		weekly: start.NewWeeklyAnchorDetector(isCrypto, "5m"),
	}
	r.candidates[symbol] = nil
}

func (r *AIAnchorResolver) OnBar(symbol string, bar start.Bar, timeframe string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	det, ok := r.detectors[symbol]
	if !ok {
		return
	}

	if sd, ok := det.swings[timeframe]; ok {
		for _, ca := range sd.Push(bar) {
			r.addCandidate(symbol, ca)
		}
	}

	if vp, ok := det.volume[timeframe]; ok {
		if ca := vp.Push(bar); ca != nil {
			r.addCandidate(symbol, *ca)
		}
	}

	if timeframe == "5m" && det.weekly != nil {
		if ca := det.weekly.Push(bar); ca != nil {
			r.addCandidate(symbol, *ca)
		}
	}
}

func (r *AIAnchorResolver) addCandidate(symbol string, ca start.CandidateAnchor) {
	cands := r.candidates[symbol]
	for _, existing := range cands {
		if existing.ID == ca.ID {
			return
		}
	}
	cands = append(cands, ca)
	if len(cands) > maxCandidatesPerSymbol {
		cands = cands[len(cands)-maxCandidatesPerSymbol:]
	}
	r.candidates[symbol] = cands
}

// ResolveAnchors runs the full pipeline: load persisted → merge in-memory →
// score → AI select (or fallback) → persist → return anchor times.
func (r *AIAnchorResolver) ResolveAnchors(
	ctx context.Context,
	symbol string,
	barTime time.Time,
	currentPrice float64,
	regime domain.MarketRegime,
	indicators domain.IndicatorSnapshot,
	anchorNames []string,
) (map[string]time.Time, error) {
	r.mu.RLock()
	inMemory := make([]start.CandidateAnchor, len(r.candidates[symbol]))
	copy(inMemory, r.candidates[symbol])
	r.mu.RUnlock()

	var persisted []start.CandidateAnchor
	if r.store != nil {
		var err error
		persisted, err = r.store.LoadActive(ctx, symbol)
		if err != nil {
			r.logger.Warn("failed to load persisted anchors", "symbol", symbol, "error", err)
		}
	}

	var sessionCandidates []start.CandidateAnchor
	if r.sessionFn != nil {
		sessionTimes := r.sessionFn(symbol, barTime, anchorNames)
		for name, t := range sessionTimes {
			if t.IsZero() {
				continue
			}
			ca, err := start.NewCandidateAnchor(t, currentPrice, start.AnchorSessionDerived, "1m", 1.0)
			if err == nil {
				ca.ID = name
				ca.Source = "session"
				sessionCandidates = append(sessionCandidates, ca)
			}
		}
	}

	merged := r.mergeCandidates(persisted, inMemory)
	merged = r.mergeCandidates(merged, sessionCandidates)
	scored := r.scorer.Score(merged)

	if len(scored) == 0 {
		return nil, nil
	}

	if r.store != nil {
		if err := r.store.Save(ctx, inMemory); err != nil {
			r.logger.Warn("failed to persist candidates", "symbol", symbol, "error", err)
		}
	}

	var selection *start.AnchorSelection

	if r.advisor != nil {
		sel, err := r.advisor.SelectAnchors(ctx, ports.AnchorSelectionRequest{
			Symbol:       domain.Symbol(symbol),
			Candidates:   scored,
			CurrentPrice: currentPrice,
			Regime:       regime,
			Indicators:   indicators,
		})
		if err != nil {
			r.logger.Warn("AI anchor selection failed, using fallback", "symbol", symbol, "error", err)
		} else {
			selection = sel
		}
	}

	if selection == nil {
		selection = r.fallbackRank(scored, defaultSelectCount)
	}

	if selection == nil {
		return nil, nil
	}

	if r.store != nil {
		if err := r.store.SaveSelection(ctx, symbol, *selection); err != nil {
			r.logger.Warn("failed to persist selection", "symbol", symbol, "error", err)
		}
	}

	return r.selectionToAnchorTimes(selection, scored), nil
}

func (r *AIAnchorResolver) mergeCandidates(persisted, inMemory []start.CandidateAnchor) []start.CandidateAnchor {
	seen := make(map[string]bool, len(persisted)+len(inMemory))
	var merged []start.CandidateAnchor

	for _, c := range persisted {
		if !seen[c.ID] {
			seen[c.ID] = true
			merged = append(merged, c)
		}
	}
	for _, c := range inMemory {
		if !seen[c.ID] {
			seen[c.ID] = true
			merged = append(merged, c)
		}
	}
	return merged
}

func (r *AIAnchorResolver) fallbackRank(candidates []start.CandidateAnchor, maxAnchors int) *start.AnchorSelection {
	if len(candidates) == 0 {
		return nil
	}

	type scored struct {
		ca    start.CandidateAnchor
		score float64
	}

	items := make([]scored, len(candidates))
	for i, c := range candidates {
		w := tfWeights[c.Timeframe]
		if w == 0 {
			w = 1.0
		}
		items[i] = scored{ca: c, score: w*10 + c.Strength}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].ca.Time.After(items[j].ca.Time)
	})

	if len(items) > maxAnchors {
		items = items[:maxAnchors]
	}

	var maxScore float64
	for _, it := range items {
		if it.score > maxScore {
			maxScore = it.score
		}
	}

	selected := make([]start.SelectedAnchor, len(items))
	for i, it := range items {
		conf := 0.5
		if maxScore > 0 {
			conf = it.score / maxScore
		}
		selected[i] = start.SelectedAnchor{
			CandidateID: it.ca.ID,
			AnchorName:  it.ca.ID,
			Rank:        i + 1,
			Confidence:  conf,
			Reason:      "fallback: deterministic ranking",
		}
	}

	sel, err := start.NewAnchorSelection(selected, "deterministic fallback ranking")
	if err != nil {
		r.logger.Error("fallback ranking produced invalid selection", "error", err)
		return nil
	}
	return &sel
}

func (r *AIAnchorResolver) selectionToAnchorTimes(sel *start.AnchorSelection, candidates []start.CandidateAnchor) map[string]time.Time {
	candidateMap := make(map[string]start.CandidateAnchor, len(candidates))
	for _, c := range candidates {
		candidateMap[c.ID] = c
	}

	result := make(map[string]time.Time)
	count := 0
	for _, sa := range sel.SelectedAnchors {
		if count >= maxActiveAnchors {
			break
		}
		if c, ok := candidateMap[sa.CandidateID]; ok {
			result[sa.AnchorName] = c.Time
			count++
		}
	}
	return result
}

// Candidates returns the current in-memory candidates for a symbol (for testing).
func (r *AIAnchorResolver) Candidates(symbol string) []start.CandidateAnchor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cands := r.candidates[symbol]
	out := make([]start.CandidateAnchor, len(cands))
	copy(out, cands)
	return out
}
