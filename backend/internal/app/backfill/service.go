package backfill

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// MarketDataFetcher is the interface for fetching historical bars.
type MarketDataFetcher interface {
	GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
}

// BarSaver is the interface for persisting bars.
type BarSaver interface {
	SaveMarketBars(ctx context.Context, bars []domain.MarketBar) (int, error)
	GetLatestMarketBarTime(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe) (*time.Time, error)
}

// Config holds all backfill parameters.
type Config struct {
	Symbols         []domain.Symbol
	Timeframe       domain.Timeframe
	From            time.Time
	To              time.Time
	Resume          bool
	Concurrency     int
	BatchSize       int
	DryRun          bool
	ContinueOnError bool
	MaxRetries      int
}

// Service orchestrates the backfill process.
type Service struct {
	fetcher MarketDataFetcher
	saver   BarSaver
	cfg     Config
	log     zerolog.Logger
}

// NewService creates a new backfill Service.
func NewService(fetcher MarketDataFetcher, saver BarSaver, cfg Config, log zerolog.Logger) *Service {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 2
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 500
	}
	if cfg.MaxRetries < 1 {
		cfg.MaxRetries = 5
	}
	return &Service{
		fetcher: fetcher,
		saver:   saver,
		cfg:     cfg,
		log:     log,
	}
}

// Run executes the full backfill job.
func (s *Service) Run(ctx context.Context) error {
	s.log.Info().
		Strs("symbols", symbolStrings(s.cfg.Symbols)).
		Str("timeframe", string(s.cfg.Timeframe)).
		Time("from", s.cfg.From).
		Time("to", s.cfg.To).
		Bool("resume", s.cfg.Resume).
		Int("concurrency", s.cfg.Concurrency).
		Int("batch_size", s.cfg.BatchSize).
		Bool("dry_run", s.cfg.DryRun).
		Msg("starting backfill")

	progress := NewProgress(len(s.cfg.Symbols), 10*time.Second, s.log)
	defer progress.Stop()

	// Worker pool over symbols
	sem := make(chan struct{}, s.cfg.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, sym := range s.cfg.Symbols {
		if ctx.Err() != nil {
			break
		}
		sym := sym
		wg.Add(1)
		sem <- struct{}{} // acquire
		go func() {
			defer wg.Done()
			defer func() { <-sem }() // release

			if err := s.backfillSymbol(ctx, sym, progress); err != nil {
				progress.AddErrors(1)
				s.log.Error().Err(err).Str("symbol", string(sym)).Msg("symbol backfill failed")
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				if !s.cfg.ContinueOnError {
					return
				}
			}
			progress.SymbolDone()
		}()
	}
	wg.Wait()

	if firstErr != nil && !s.cfg.ContinueOnError {
		return fmt.Errorf("backfill failed: %w", firstErr)
	}
	if progress.Errors() > 0 {
		s.log.Warn().Int64("errors", progress.Errors()).Msg("backfill completed with errors")
	}
	return nil
}

// backfillSymbol fetches and saves all chunks for a single symbol.
func (s *Service) backfillSymbol(ctx context.Context, sym domain.Symbol, progress *Progress) error {
	from := s.cfg.From

	// If resume mode, start from the last known bar time
	if s.cfg.Resume {
		latest, err := s.saver.GetLatestMarketBarTime(ctx, sym, s.cfg.Timeframe)
		if err != nil {
			return fmt.Errorf("get latest bar time for %s: %w", sym, err)
		}
		if latest != nil {
			// Start 1 second after the last bar to avoid re-fetching it
			from = latest.Add(time.Second)
			s.log.Info().Str("symbol", string(sym)).Time("resume_from", from).Msg("resuming from last known bar")
		}
	}

	if !from.Before(s.cfg.To) {
		s.log.Info().Str("symbol", string(sym)).Msg("already up to date, skipping")
		return nil
	}

	chunks := SplitTimeRange(from, s.cfg.To, s.cfg.Timeframe)
	s.log.Info().Str("symbol", string(sym)).Int("chunks", len(chunks)).Msg("starting symbol backfill")

	for i, chunk := range chunks {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		bars, err := s.fetchWithRetry(ctx, sym, chunk)
		if err != nil {
			return fmt.Errorf("chunk %d/%d for %s: %w", i+1, len(chunks), sym, err)
		}
		progress.AddChunks(1)

		if len(bars) == 0 {
			continue
		}

		if s.cfg.DryRun {
			s.log.Info().
				Str("symbol", string(sym)).
				Int("bars", len(bars)).
				Time("from", chunk.From).
				Time("to", chunk.To).
				Msg("[DRY RUN] would save bars")
			progress.AddBars(len(bars))
			continue
		}

		// Save in batches
		saved, err := s.saveBatched(ctx, bars)
		if err != nil {
			return fmt.Errorf("save bars for %s: %w", sym, err)
		}
		progress.AddBars(saved)
		skipped := len(bars) - saved
		if skipped > 0 {
			progress.AddSkipped(skipped)
		}
	}
	return nil
}

// fetchWithRetry fetches bars for a single chunk with exponential backoff retry.
func (s *Service) fetchWithRetry(ctx context.Context, sym domain.Symbol, chunk ChunkWindow) ([]domain.MarketBar, error) {
	var lastErr error
	for attempt := 0; attempt < s.cfg.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		bars, err := s.fetcher.GetHistoricalBars(ctx, sym, s.cfg.Timeframe, chunk.From, chunk.To)
		if err == nil {
			return bars, nil
		}
		lastErr = err
		backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
		s.log.Warn().
			Err(err).
			Str("symbol", string(sym)).
			Int("attempt", attempt+1).
			Dur("backoff", backoff).
			Msg("fetch failed, retrying")

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, fmt.Errorf("all %d retries exhausted: %w", s.cfg.MaxRetries, lastErr)
}

// saveBatched writes bars to the database in batch_size chunks.
func (s *Service) saveBatched(ctx context.Context, bars []domain.MarketBar) (int, error) {
	total := 0
	for i := 0; i < len(bars); i += s.cfg.BatchSize {
		end := i + s.cfg.BatchSize
		if end > len(bars) {
			end = len(bars)
		}
		n, err := s.saver.SaveMarketBars(ctx, bars[i:end])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func symbolStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = string(s)
	}
	return out
}
