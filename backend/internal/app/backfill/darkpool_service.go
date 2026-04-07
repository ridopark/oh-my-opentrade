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

// TradeFetcher is the interface for fetching historical trades.
type TradeFetcher interface {
	GetHistoricalTrades(ctx context.Context, symbol domain.Symbol, from, to time.Time, handler func(trade TradeTick)) error
}

// TradeTick represents a single trade tick from the broker.
type TradeTick struct {
	T time.Time
	X string  // exchange
	P float64 // price
	S float64 // size
}

// DPBarSaver is the interface for persisting dark pool bars.
type DPBarSaver interface {
	SaveDarkPoolBars(ctx context.Context, bars []domain.DarkPoolBar) (int, error)
	GetLatestDarkPoolBarTime(ctx context.Context, sym domain.Symbol, tf domain.Timeframe) (*time.Time, error)
}

// DarkPoolConfig holds all dark pool backfill parameters.
type DarkPoolConfig struct {
	Symbols         []domain.Symbol
	From            time.Time
	To              time.Time
	Resume          bool
	Concurrency     int
	BatchSize       int
	ContinueOnError bool
	MaxRetries      int
}

// DarkPoolService orchestrates the dark pool backfill process.
type DarkPoolService struct {
	fetcher TradeFetcher
	saver   DPBarSaver
	cfg     DarkPoolConfig
	log     zerolog.Logger
}

// NewDarkPoolService creates a new dark pool backfill service.
func NewDarkPoolService(fetcher TradeFetcher, saver DPBarSaver, cfg DarkPoolConfig, log zerolog.Logger) *DarkPoolService {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 2
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 500
	}
	if cfg.MaxRetries < 1 {
		cfg.MaxRetries = 5
	}
	return &DarkPoolService{
		fetcher: fetcher,
		saver:   saver,
		cfg:     cfg,
		log:     log,
	}
}

// Run executes the full dark pool backfill job.
func (s *DarkPoolService) Run(ctx context.Context) error {
	s.log.Info().
		Strs("symbols", symbolStrings(s.cfg.Symbols)).
		Time("from", s.cfg.From).
		Time("to", s.cfg.To).
		Bool("resume", s.cfg.Resume).
		Int("concurrency", s.cfg.Concurrency).
		Int("batch_size", s.cfg.BatchSize).
		Msg("starting dark pool backfill")

	progress := NewProgress(len(s.cfg.Symbols), 10*time.Second, s.log)
	defer progress.Stop()

	// Worker pool over symbols.
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
				s.log.Error().Err(err).Str("symbol", string(sym)).Msg("dark pool symbol backfill failed")
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
		return fmt.Errorf("dark pool backfill failed: %w", firstErr)
	}
	if progress.Errors() > 0 {
		s.log.Warn().Int64("errors", progress.Errors()).Msg("dark pool backfill completed with errors")
	}
	return nil
}

// backfillSymbol fetches and saves dark pool bars for a single symbol.
func (s *DarkPoolService) backfillSymbol(ctx context.Context, sym domain.Symbol, progress *Progress) error {
	from := s.cfg.From

	// If resume mode, start from the last known bar time.
	if s.cfg.Resume {
		latest, err := s.saver.GetLatestDarkPoolBarTime(ctx, sym, dpBarTimeframe)
		if err != nil {
			return fmt.Errorf("get latest dark pool bar time for %s: %w", sym, err)
		}
		if latest != nil {
			from = latest.Add(time.Second)
			s.log.Info().Str("symbol", string(sym)).Time("resume_from", from).Msg("resuming dark pool from last known bar")
		}
	}

	if !from.Before(s.cfg.To) {
		s.log.Info().Str("symbol", string(sym)).Msg("dark pool already up to date, skipping")
		return nil
	}

	// Chunk by 1 day.
	chunks := splitDayRange(from, s.cfg.To)
	s.log.Info().Str("symbol", string(sym)).Int("chunks", len(chunks)).Msg("starting dark pool symbol backfill")

	for i, chunk := range chunks {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		bars, err := s.fetchDayWithRetry(ctx, sym, chunk.from, chunk.to)
		if err != nil {
			return fmt.Errorf("dark pool chunk %d/%d for %s: %w", i+1, len(chunks), sym, err)
		}
		progress.AddChunks(1)

		if len(bars) == 0 {
			continue
		}

		// Save in batches.
		saved, err := s.saveBatched(ctx, bars)
		if err != nil {
			return fmt.Errorf("save dark pool bars for %s: %w", sym, err)
		}
		progress.AddBars(saved)
	}
	return nil
}

// dayChunk is a single day time range.
type dayChunk struct {
	from, to time.Time
}

// splitDayRange splits a time range into per-day chunks.
func splitDayRange(from, to time.Time) []dayChunk {
	var chunks []dayChunk
	current := from
	for current.Before(to) {
		dayEnd := time.Date(current.Year(), current.Month(), current.Day()+1, 0, 0, 0, 0, current.Location())
		if dayEnd.After(to) {
			dayEnd = to
		}
		chunks = append(chunks, dayChunk{from: current, to: dayEnd})
		current = dayEnd
	}
	return chunks
}

// fetchDayWithRetry fetches trades for a single day and aggregates into bars, with exponential backoff retry.
func (s *DarkPoolService) fetchDayWithRetry(ctx context.Context, sym domain.Symbol, from, to time.Time) ([]domain.DarkPoolBar, error) {
	var lastErr error
	for attempt := 0; attempt < s.cfg.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		agg := NewDPAggregator(sym)
		err := s.fetcher.GetHistoricalTrades(ctx, sym, from, to, func(t TradeTick) {
			agg.AddTrade(t.T, t.X, t.P, t.S)
		})
		if err == nil {
			return agg.Flush(), nil
		}

		lastErr = err
		backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
		s.log.Warn().
			Err(err).
			Str("symbol", string(sym)).
			Int("attempt", attempt+1).
			Dur("backoff", backoff).
			Msg("dark pool fetch failed, retrying")

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, fmt.Errorf("all %d retries exhausted: %w", s.cfg.MaxRetries, lastErr)
}

// saveBatched writes dark pool bars to the database in batch_size chunks.
func (s *DarkPoolService) saveBatched(ctx context.Context, bars []domain.DarkPoolBar) (int, error) {
	total := 0
	for i := 0; i < len(bars); i += s.cfg.BatchSize {
		end := i + s.cfg.BatchSize
		if end > len(bars) {
			end = len(bars)
		}
		n, err := s.saver.SaveDarkPoolBars(ctx, bars[i:end])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}
