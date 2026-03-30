package ingest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/backfill"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// GapFillConfig configures startup gap detection and backfill.
type GapFillConfig struct {
	Symbols     []domain.Symbol
	Timeframe   domain.Timeframe
	MaxLookback time.Duration // how far back to backfill if no data exists
	Concurrency int
	BatchSize   int
}

// GapFill detects and fills gaps in market bar data on startup.
// It also seeds the AdaptiveFilter with fetched bars for warmup.
func GapFill(
	ctx context.Context,
	cfg GapFillConfig,
	fetcher backfill.MarketDataFetcher,
	saver backfill.BarSaver,
	filter *ingestion.AdaptiveFilter,
	log zerolog.Logger,
) error {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 2
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 500
	}
	if cfg.MaxLookback == 0 {
		cfg.MaxLookback = 7 * 24 * time.Hour
	}

	log.Info().
		Strs("symbols", symbolStrings(cfg.Symbols)).
		Str("timeframe", string(cfg.Timeframe)).
		Dur("max_lookback", cfg.MaxLookback).
		Msg("starting gap-fill")

	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, sym := range cfg.Symbols {
		if ctx.Err() != nil {
			break
		}
		sym := sym
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if err := gapFillSymbol(ctx, sym, cfg, fetcher, saver, filter, log); err != nil {
				log.Error().Err(err).Str("symbol", string(sym)).Msg("gap-fill failed for symbol")
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return fmt.Errorf("gap-fill: %w", firstErr)
	}
	log.Info().Msg("gap-fill complete")
	return nil
}

func gapFillSymbol(
	ctx context.Context,
	sym domain.Symbol,
	cfg GapFillConfig,
	fetcher backfill.MarketDataFetcher,
	saver backfill.BarSaver,
	filter *ingestion.AdaptiveFilter,
	log zerolog.Logger,
) error {
	now := time.Now().UTC()

	latest, err := saver.GetLatestMarketBarTime(ctx, sym, cfg.Timeframe)
	if err != nil {
		return fmt.Errorf("get latest bar time for %s: %w", sym, err)
	}

	var from time.Time
	if latest != nil {
		from = latest.Add(time.Second)
	} else {
		from = now.Add(-cfg.MaxLookback)
	}

	if !from.Before(now) {
		log.Debug().Str("symbol", string(sym)).Msg("already up to date")
		return nil
	}

	chunks := backfill.SplitTimeRange(from, now, cfg.Timeframe)
	log.Info().
		Str("symbol", string(sym)).
		Time("from", from).
		Int("chunks", len(chunks)).
		Msg("backfilling gap")

	var allBars []domain.MarketBar
	for _, chunk := range chunks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		bars, fetchErr := fetcher.GetHistoricalBars(ctx, sym, cfg.Timeframe, chunk.From, chunk.To)
		if fetchErr != nil {
			log.Warn().Err(fetchErr).Str("symbol", string(sym)).Msg("fetch failed for chunk, skipping")
			continue
		}
		allBars = append(allBars, bars...)
	}

	if len(allBars) == 0 {
		log.Info().Str("symbol", string(sym)).Msg("no bars fetched during gap-fill")
		return nil
	}

	// Seed filter with fetched bars for warmup.
	filter.Seed(sym, allBars)

	// Persist in batches.
	total := 0
	for i := 0; i < len(allBars); i += cfg.BatchSize {
		end := i + cfg.BatchSize
		if end > len(allBars) {
			end = len(allBars)
		}
		n, saveErr := saver.SaveMarketBars(ctx, allBars[i:end])
		if saveErr != nil {
			return fmt.Errorf("save bars for %s: %w", sym, saveErr)
		}
		total += n
	}

	log.Info().
		Str("symbol", string(sym)).
		Int("fetched", len(allBars)).
		Int("saved", total).
		Msg("gap-fill done for symbol")

	return nil
}

func symbolStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = string(s)
	}
	return out
}
