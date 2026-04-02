package backfill

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// GapFillConfig configures a gap-fill run.
type GapFillConfig struct {
	Symbols     []domain.Symbol
	Timeframe   domain.Timeframe
	From        time.Time
	To          time.Time
	Concurrency int
	BatchSize   int
	MaxRetries  int
	RTHOnly     bool // When true, only fill gaps during Regular Trading Hours (backtest mode).
	                 // When false, fill all detected gaps including pre/post-market (default for CLI).
}

// GapFillService detects and fills RTH data gaps using the same logic
// as the backtest runner, but with robust chunking, retry, and concurrency.
type GapFillService struct {
	fetcher  MarketDataFetcher
	saver    BarSaver
	detector GapDetector
	cfg      GapFillConfig
	log      zerolog.Logger
}

// NewGapFillService creates a gap-fill service.
func NewGapFillService(fetcher MarketDataFetcher, saver BarSaver, detector GapDetector, cfg GapFillConfig, log zerolog.Logger) *GapFillService {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 4
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 500
	}
	if cfg.MaxRetries < 1 {
		cfg.MaxRetries = 5
	}
	return &GapFillService{
		fetcher:  fetcher,
		saver:    saver,
		detector: detector,
		cfg:      cfg,
		log:      log,
	}
}

// Run detects and fills all RTH gaps for the configured symbols and date range.
// It returns the total number of bars fetched and saved.
func (s *GapFillService) Run(ctx context.Context) (totalFetched int, totalSaved int, err error) {
	loc, _ := time.LoadLocation("America/New_York")

	progress := NewProgress(len(s.cfg.Symbols), 10*time.Second, s.log)
	defer progress.Stop()

	sem := make(chan struct{}, s.cfg.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	var fetched, saved int

	for _, sym := range s.cfg.Symbols {
		if ctx.Err() != nil {
			break
		}
		sym := sym
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			f, sv, symErr := s.gapFillSymbol(ctx, sym, loc)
			mu.Lock()
			fetched += f
			saved += sv
			if symErr != nil && firstErr == nil {
				firstErr = symErr
			}
			mu.Unlock()

			if symErr != nil {
				s.log.Error().Err(symErr).Str("symbol", string(sym)).Msg("gap-fill failed for symbol")
			}
			progress.SymbolDone()
		}()
	}
	wg.Wait()

	s.log.Info().
		Int("total_fetched", fetched).
		Int("total_saved", saved).
		Int("symbols", len(s.cfg.Symbols)).
		Msg("gap-fill complete")

	return fetched, saved, firstErr
}

// gapFillSymbol detects and fills gaps for a single symbol.
// Returns (fetched, saved, error).
func (s *GapFillService) gapFillSymbol(ctx context.Context, sym domain.Symbol, loc *time.Location) (int, int, error) {
	totalFetched := 0
	totalSaved := 0

	// Check existing data range for this symbol.
	first, last, barCount, rangeErr := s.detector.GetMarketBarRange(ctx, sym, s.cfg.Timeframe, s.cfg.From, s.cfg.To)
	if rangeErr != nil {
		return 0, 0, fmt.Errorf("get bar range for %s: %w", sym, rangeErr)
	}

	// Zero bars: full backfill needed.
	if barCount == 0 || first == nil || last == nil {
		s.log.Info().Str("symbol", string(sym)).Msg("no bars found — full backfill")
		f, sv, err := s.fetchAndSaveRange(ctx, sym, s.cfg.From, clampToNow(s.cfg.To))
		return f, sv, err
	}

	// 1. Internal gaps via FindDataGaps.
	gaps, gapErr := s.detector.FindDataGaps(ctx, sym, s.cfg.Timeframe, s.cfg.From, s.cfg.To, GapThreshold)
	if gapErr != nil {
		s.log.Warn().Err(gapErr).Str("symbol", string(sym)).Msg("gap detection failed")
	} else {
		for _, g := range gaps {
			if s.cfg.RTHOnly && !IsRTHGap(g.Start, g.End, loc) {
				continue
			}
			s.log.Info().
				Str("symbol", string(sym)).
				Time("start", g.Start).
				Time("end", g.End).
				Dur("duration", g.Duration).
				Msg("detected data gap")

			f, sv, err := s.fetchAndSaveRange(ctx, sym, g.Start.Add(time.Minute), g.End)
			totalFetched += f
			totalSaved += sv
			if err != nil {
				s.log.Warn().Err(err).Str("symbol", string(sym)).Msg("failed to fill internal gap")
			}
		}
	}

	// 2. Leading edge gap: first bar is well after requested start.
	if first.Sub(s.cfg.From) > GapThreshold && (!s.cfg.RTHOnly || IsRTHGap(s.cfg.From, *first, loc)) {
		s.log.Info().Str("symbol", string(sym)).Time("from", s.cfg.From).Time("first_bar", *first).Msg("detected leading data gap")
		f, sv, err := s.fetchAndSaveRange(ctx, sym, s.cfg.From, *first)
		totalFetched += f
		totalSaved += sv
		if err != nil {
			s.log.Warn().Err(err).Str("symbol", string(sym)).Msg("failed to fill leading gap")
		}
	}

	// 3. Trailing edge gap: last bar is well before requested end.
	if s.cfg.To.Sub(*last) > GapThreshold && (!s.cfg.RTHOnly || IsRTHGap(*last, s.cfg.To, loc)) {
		fetchTo := clampToNow(s.cfg.To)
		s.log.Info().Str("symbol", string(sym)).Time("last_bar", *last).Time("to", fetchTo).Msg("detected trailing data gap")
		f, sv, err := s.fetchAndSaveRange(ctx, sym, last.Add(time.Minute), fetchTo)
		totalFetched += f
		totalSaved += sv
		if err != nil {
			s.log.Warn().Err(err).Str("symbol", string(sym)).Msg("failed to fill trailing gap")
		}
	}

	return totalFetched, totalSaved, nil
}

// fetchAndSaveRange fetches bars for a time range using chunking and retry,
// then saves them in batches. Returns (fetched, saved, error).
func (s *GapFillService) fetchAndSaveRange(ctx context.Context, sym domain.Symbol, from, to time.Time) (int, int, error) {
	chunks := SplitTimeRange(from, to, s.cfg.Timeframe)
	totalFetched := 0
	totalSaved := 0

	svc := &Service{
		fetcher: s.fetcher,
		saver:   s.saver,
		cfg: Config{
			Timeframe:  s.cfg.Timeframe,
			BatchSize:  s.cfg.BatchSize,
			MaxRetries: s.cfg.MaxRetries,
		},
		log: s.log,
	}

	for _, chunk := range chunks {
		if ctx.Err() != nil {
			return totalFetched, totalSaved, ctx.Err()
		}

		bars, err := svc.fetchWithRetry(ctx, sym, chunk)
		if err != nil {
			return totalFetched, totalSaved, fmt.Errorf("fetch %s [%s → %s]: %w", sym, chunk.From.Format(time.DateOnly), chunk.To.Format(time.DateOnly), err)
		}
		totalFetched += len(bars)

		if len(bars) == 0 {
			continue
		}

		saved, err := svc.saveBatched(ctx, bars)
		if err != nil {
			return totalFetched, totalSaved, fmt.Errorf("save %s: %w", sym, err)
		}
		totalSaved += saved
	}

	return totalFetched, totalSaved, nil
}

func clampToNow(t time.Time) time.Time {
	if t.After(time.Now()) {
		return time.Now()
	}
	return t
}
