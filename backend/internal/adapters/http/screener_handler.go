package http

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// ScreenerResult is the JSON shape returned by GET /screener.
type ScreenerResult struct {
	Symbol      string  `json:"symbol"`
	Price       float64 `json:"price"`
	ATR         float64 `json:"atr"`
	ATRPct      float64 `json:"atr_pct"`
	NR7         bool    `json:"nr7"`
	Bias        string  `json:"bias"`
	EMA200      float64 `json:"ema200"`
	RealizedVol float64 `json:"realized_vol"`
	Score       float64 `json:"score"`
	// Snapshot fields (only populated in universe mode)
	GapPct  float64 `json:"gap_pct,omitempty"`
	PMVol   int64   `json:"pm_vol,omitempty"`
	RVOL    float64 `json:"rvol,omitempty"`
	PassATR bool    `json:"pass_atr"`
}

// ScreenerHandler serves GET /screener for the dashboard screener page.
// Supports two modes:
//   - Custom symbols: ?symbols=AAPL,MSFT (or default list)
//   - Universe scan: ?mode=universe (uses Alpaca universe + Pass0 filter)
type ScreenerHandler struct {
	fetcher        ports.MarketDataPort
	snapshots      ports.SnapshotPort
	universe       ports.UniverseProviderPort
	defaultSymbols []string
	log            zerolog.Logger
}

// NewScreenerHandler creates a ScreenerHandler.
func NewScreenerHandler(
	fetcher ports.MarketDataPort,
	snapshots ports.SnapshotPort,
	universe ports.UniverseProviderPort,
	defaultSymbols []string,
	log zerolog.Logger,
) *ScreenerHandler {
	return &ScreenerHandler{
		fetcher:        fetcher,
		snapshots:      snapshots,
		universe:       universe,
		defaultSymbols: defaultSymbols,
		log:            log,
	}
}

// ServeHTTP handles GET /screener?symbols=AAPL,MSFT&mode=universe&date=2026-03-10
func (h *ScreenerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	// Resolve date
	asOf := time.Now()
	if dateStr := r.URL.Query().Get("date"); dateStr != "" {
		loc, _ := time.LoadLocation("America/New_York")
		if parsed, err := time.ParseInLocation("2006-01-02", dateStr, loc); err == nil {
			asOf = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 16, 0, 0, 0, loc)
		}
	}

	mode := r.URL.Query().Get("mode")

	var symbols []string

	if mode == "universe" {
		// Universe mode: fetch all tradeable equities, get snapshots, filter by Pass0-like criteria
		symbols = h.resolveUniverse(ctx, asOf)
		h.log.Info().Int("universe_symbols", len(symbols)).Msg("screener: universe resolved")
	} else if q := r.URL.Query().Get("symbols"); q != "" {
		for _, s := range strings.Split(q, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				symbols = append(symbols, s)
			}
		}
	} else {
		symbols = h.defaultSymbols
	}

	if len(symbols) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"date": asOf.Format("2006-01-02"), "results": []ScreenerResult{}})
		return
	}

	// Compute indicators for all symbols
	results := h.computeAll(ctx, symbols, asOf)

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	// In universe mode, limit to top 50 results
	if mode == "universe" && len(results) > 50 {
		results = results[:50]
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"date":    asOf.Format("2006-01-02"),
		"mode":    mode,
		"total":   len(symbols),
		"results": results,
	})
}

// resolveUniverse fetches tradeable equities from the universe provider,
// gets snapshots, and applies basic Pass0-like filters (price > $5, ADV > 500K).
func (h *ScreenerHandler) resolveUniverse(ctx context.Context, asOf time.Time) []string {
	if h.universe == nil || h.snapshots == nil {
		h.log.Warn().Msg("screener: universe or snapshot provider not available, using defaults")
		return h.defaultSymbols
	}

	// Get all tradeable equities
	assets, err := h.universe.ListTradeable(ctx, domain.AssetClassEquity)
	if err != nil || len(assets) == 0 {
		h.log.Warn().Err(err).Msg("screener: universe fetch failed, using defaults")
		return h.defaultSymbols
	}

	allSymbols := make([]string, 0, len(assets))
	for _, a := range assets {
		allSymbols = append(allSymbols, a.Symbol)
	}
	h.log.Info().Int("tradeable", len(allSymbols)).Msg("screener: fetched tradeable universe")

	// Get snapshots in batches
	snaps, err := h.getSnapshotsBatched(ctx, allSymbols, asOf)
	if err != nil {
		h.log.Warn().Err(err).Msg("screener: snapshot fetch failed, using defaults")
		return h.defaultSymbols
	}

	// Pass0-like filter: price > $5, pre-market volume > 50K or ADV > 500K
	var passed []string
	for _, sym := range allSymbols {
		snap, ok := snaps[sym]
		if !ok {
			continue
		}
		price := 0.0
		if snap.PreMarketPrice != nil && *snap.PreMarketPrice > 0 {
			price = *snap.PreMarketPrice
		} else if snap.LastTradePrice != nil {
			price = *snap.LastTradePrice
		}
		if price < 5.0 {
			continue
		}
		// Require some volume activity
		if snap.PrevDailyVolume != nil && *snap.PrevDailyVolume < 500000 {
			continue
		}
		passed = append(passed, sym)
	}

	h.log.Info().Int("pass0_survivors", len(passed)).Msg("screener: pass0 filter applied")
	return passed
}

func (h *ScreenerHandler) getSnapshotsBatched(ctx context.Context, symbols []string, asOf time.Time) (map[string]ports.Snapshot, error) {
	const batchSize = 500
	result := make(map[string]ports.Snapshot, len(symbols))

	for i := 0; i < len(symbols); i += batchSize {
		end := i + batchSize
		if end > len(symbols) {
			end = len(symbols)
		}
		batch := symbols[i:end]
		snaps, err := h.snapshots.GetSnapshots(ctx, batch, asOf)
		if err != nil {
			return nil, err
		}
		for k, v := range snaps {
			result[k] = v
		}
	}
	return result, nil
}

// computeAll fetches daily bars and computes indicators for all symbols concurrently.
func (h *ScreenerHandler) computeAll(ctx context.Context, symbols []string, asOf time.Time) []ScreenerResult {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []ScreenerResult
		sem     = make(chan struct{}, 8) // bounded concurrency
	)

	from := asOf.Add(-400 * 24 * time.Hour)

	for _, sym := range symbols {
		wg.Add(1)
		sem <- struct{}{}
		go func(sym string) {
			defer wg.Done()
			defer func() { <-sem }()

			s, _ := domain.NewSymbol(sym)
			bars, err := h.fetcher.GetHistoricalBars(ctx, s, "1d", from, asOf)
			if err != nil || len(bars) < 21 {
				return
			}

			lastClose := bars[len(bars)-1].Close
			if lastClose <= 0 {
				return
			}

			atr := monitor.ComputeDailyATR(bars, 14)
			atrPct := 0.0
			if lastClose > 0 {
				atrPct = atr / lastClose * 100
			}

			nr7 := monitor.ComputeNR7(bars)

			closes := make([]float64, len(bars))
			for i, b := range bars {
				closes[i] = b.Close
			}
			ema200 := monitor.ComputeStaticEMA(closes, 200)
			bias := "NEUTRAL"
			if ema200 > 0 {
				if lastClose > ema200*1.005 {
					bias = "BULLISH"
				} else if lastClose < ema200*0.995 {
					bias = "BEARISH"
				}
			}

			realVol := monitor.ComputeRealizedVol(bars, 20)

			score := atrPct * 10
			if nr7 {
				score += 20
			}
			if bias == "BULLISH" || bias == "BEARISH" {
				score += 5
			}

			mu.Lock()
			results = append(results, ScreenerResult{
				Symbol:      sym,
				Price:       math.Round(lastClose*100) / 100,
				ATR:         math.Round(atr*100) / 100,
				ATRPct:      math.Round(atrPct*10) / 10,
				NR7:         nr7,
				Bias:        bias,
				EMA200:      math.Round(ema200*100) / 100,
				RealizedVol: math.Round(realVol*10) / 10,
				Score:       math.Round(score*10) / 10,
				PassATR:     atrPct >= 0.8,
			})
			mu.Unlock()
		}(sym)
	}

	wg.Wait()
	return results
}
