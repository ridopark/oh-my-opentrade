package http

import (
	"context"
	"encoding/json"
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
	Symbol       string  `json:"symbol"`
	Price        float64 `json:"price"`
	ATR          float64 `json:"atr"`
	ATRPct       float64 `json:"atr_pct"`
	NR7          bool    `json:"nr7"`
	Bias         string  `json:"bias"`
	EMA200       float64 `json:"ema200"`
	RealizedVol  float64 `json:"realized_vol"`
	Score        float64 `json:"score"`
}

// ScreenerHandler serves GET /screener for the dashboard screener page.
type ScreenerHandler struct {
	fetcher        ports.MarketDataPort
	defaultSymbols []string
	log            zerolog.Logger
}

// NewScreenerHandler creates a ScreenerHandler.
func NewScreenerHandler(fetcher ports.MarketDataPort, defaultSymbols []string, log zerolog.Logger) *ScreenerHandler {
	return &ScreenerHandler{fetcher: fetcher, defaultSymbols: defaultSymbols, log: log}
}

// ServeHTTP handles GET /screener?symbols=AAPL,MSFT,...
func (h *ScreenerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// Resolve symbols
	symbols := h.defaultSymbols
	if q := r.URL.Query().Get("symbols"); q != "" {
		symbols = nil
		for _, s := range strings.Split(q, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				symbols = append(symbols, s)
			}
		}
	}

	if len(symbols) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]ScreenerResult{})
		return
	}

	// Fetch and compute concurrently with bounded concurrency
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []ScreenerResult
		sem     = make(chan struct{}, 5)
	)

	now := time.Now()
	from := now.Add(-400 * 24 * time.Hour)

	for _, sym := range symbols {
		wg.Add(1)
		sem <- struct{}{} // acquire
		go func(sym string) {
			defer wg.Done()
			defer func() { <-sem }() // release

			s, _ := domain.NewSymbol(sym)
			bars, err := h.fetcher.GetHistoricalBars(ctx, s, "1d", from, now)
			if err != nil || len(bars) < 21 {
				h.log.Debug().Str("symbol", sym).Err(err).Msg("screener: skipping symbol")
				return
			}

			lastClose := bars[len(bars)-1].Close
			if lastClose <= 0 {
				return
			}

			// ATR
			atr := monitor.ComputeDailyATR(bars, 14)
			atrPct := 0.0
			if lastClose > 0 {
				atrPct = atr / lastClose * 100
			}

			// NR7
			nr7 := monitor.ComputeNR7(bars)

			// EMA200 + Bias
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

			// Realized vol
			realVol := monitor.ComputeRealizedVol(bars, 20)

			// Composite score
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
				Price:       lastClose,
				ATR:         atr,
				ATRPct:      atrPct,
				NR7:         nr7,
				Bias:        bias,
				EMA200:      ema200,
				RealizedVol: realVol,
				Score:       score,
			})
			mu.Unlock()
		}(sym)
	}

	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}
