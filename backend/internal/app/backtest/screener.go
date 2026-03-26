package backtest

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// ScoredSymbol is a symbol with its screener scores for a given date.
type ScoredSymbol struct {
	Symbol string
	Score  float64
	ATRPct float64
	NR7    bool
	Bias   string
}

// DailyScreener pre-computes the top-N symbols for each trading day.
type DailyScreener struct {
	repo   ports.RepositoryPort
	market ports.MarketDataPort
	log    zerolog.Logger
}

// NewDailyScreener creates a DailyScreener. repo is optional (nil = API only).
func NewDailyScreener(repo ports.RepositoryPort, market ports.MarketDataPort, log zerolog.Logger) *DailyScreener {
	return &DailyScreener{repo: repo, market: market, log: log}
}

// ComputeForDate scores all candidate symbols as of the given date and returns
// the top N sorted by composite score descending.
func (ds *DailyScreener) ComputeForDate(ctx context.Context, asOf time.Time, candidates []domain.Symbol, topN int) []ScoredSymbol {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []ScoredSymbol
		sem     = make(chan struct{}, 20)
	)

	from := asOf.Add(-400 * 24 * time.Hour)

	for _, sym := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(sym domain.Symbol) {
			defer wg.Done()
			defer func() { <-sem }()

			// Try DB first, fall back to API
			var bars []domain.MarketBar
			if ds.repo != nil {
				bars, _ = ds.repo.GetMarketBars(ctx, sym, "1d", from, asOf)
			}
			if len(bars) < 21 && ds.market != nil {
				apiBars, apiErr := ds.market.GetHistoricalBars(ctx, sym, "1d", from, asOf)
				if apiErr == nil && len(apiBars) > len(bars) {
					bars = apiBars
				}
			}
			if len(bars) < 21 {
				return
			}

			lastClose := bars[len(bars)-1].Close
			if lastClose <= 0 {
				return
			}

			atr := monitor.ComputeDailyATR(bars, 14)
			atrPct := atr / lastClose * 100

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

			score := atrPct * 10
			if nr7 {
				score += 20
			}
			if bias == "BULLISH" || bias == "BEARISH" {
				score += 5
			}

			// Only include symbols with meaningful ATR
			if atrPct < 0.5 {
				return
			}

			mu.Lock()
			results = append(results, ScoredSymbol{
				Symbol: string(sym),
				Score:  math.Round(score*10) / 10,
				ATRPct: math.Round(atrPct*10) / 10,
				NR7:    nr7,
				Bias:   bias,
			})
			mu.Unlock()
		}(sym)
	}

	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	if topN > 0 && len(results) > topN {
		results = results[:topN]
	}

	return results
}

// PrecomputeAll computes the screener for every trading day in the given range.
// Returns a map of date string ("2006-01-02") → set of active symbols for that day.
func (ds *DailyScreener) PrecomputeAll(
	ctx context.Context,
	tradingDays []time.Time,
	candidates []domain.Symbol,
	topN int,
	onProgress func(day int, total int, date string),
) map[string]map[string]bool {
	result := make(map[string]map[string]bool, len(tradingDays))

	for i, day := range tradingDays {
		if ctx.Err() != nil {
			break
		}
		if onProgress != nil {
			onProgress(i, len(tradingDays), day.Format("2006-01-02"))
		}

		scored := ds.ComputeForDate(ctx, day, candidates, topN)

		symSet := make(map[string]bool, len(scored))
		for _, s := range scored {
			symSet[s.Symbol] = true
		}
		result[day.Format("2006-01-02")] = symSet

		ds.log.Debug().
			Str("date", day.Format("2006-01-02")).
			Int("top_n", len(scored)).
			Msg("daily screener computed")
	}

	return result
}
