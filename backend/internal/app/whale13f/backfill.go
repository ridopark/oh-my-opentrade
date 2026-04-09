// Package whale13f orchestrates 13F-HR whale filing backfill and accumulation scoring.
// It is reused by both the CLI tool (omo-backfill-13f) and the scheduled service in omo-core.
package whale13f

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/openfigi"
	"github.com/oh-my-opentrade/backend/internal/adapters/sec"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// QuarterID represents a fiscal quarter (e.g., 2025Q1).
type QuarterID struct {
	Year    int
	Quarter int // 1-4
}

// EndDate returns the quarter-end date (last day of the quarter month).
func (q QuarterID) EndDate() time.Time {
	month := time.Month(q.Quarter * 3)
	// Last day of the quarter month: first of next month minus one day.
	return time.Date(q.Year, month+1, 0, 0, 0, 0, 0, time.UTC)
}

func (q QuarterID) String() string {
	return fmt.Sprintf("%dQ%d", q.Year, q.Quarter)
}

// Prev returns the preceding quarter.
func (q QuarterID) Prev() QuarterID {
	if q.Quarter == 1 {
		return QuarterID{Year: q.Year - 1, Quarter: 4}
	}
	return QuarterID{Year: q.Year, Quarter: q.Quarter - 1}
}

// ParseQuarterID parses "2025Q1" format into QuarterID.
func ParseQuarterID(s string) (QuarterID, error) {
	var q QuarterID
	_, err := fmt.Sscanf(s, "%dQ%d", &q.Year, &q.Quarter)
	if err != nil || q.Quarter < 1 || q.Quarter > 4 {
		return q, fmt.Errorf("whale13f: invalid quarter: %q (expected format: 2025Q1)", s)
	}
	return q, nil
}

// CurrentQuarter returns the QuarterID for the most recently ended quarter.
func CurrentQuarter() QuarterID {
	now := time.Now()
	q := (int(now.Month()) - 1) / 3 // 0-based current quarter
	if q == 0 {
		return QuarterID{Year: now.Year() - 1, Quarter: 4}
	}
	return QuarterID{Year: now.Year(), Quarter: q}
}

// Config holds tuning knobs for the backfill service.
type Config struct {
	Concurrency int
	BatchSize   int
	UserAgent   string
}

// BackfillService orchestrates 13F filing download, CUSIP resolution, and accumulation scoring.
type BackfillService struct {
	edgar      *sec.EdgarClient
	figi       *openfigi.Client
	cusipCache *timescaledb.CUSIPCacheRepo
	whaleRepo  *timescaledb.WhaleRepo
	cfg        Config
	log        zerolog.Logger
}

// NewBackfillService creates a new BackfillService with validated defaults.
func NewBackfillService(
	edgar *sec.EdgarClient,
	figi *openfigi.Client,
	cusipCache *timescaledb.CUSIPCacheRepo,
	whaleRepo *timescaledb.WhaleRepo,
	cfg Config,
	log zerolog.Logger,
) *BackfillService {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 3
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 500
	}
	return &BackfillService{
		edgar:      edgar,
		figi:       figi,
		cusipCache: cusipCache,
		whaleRepo:  whaleRepo,
		cfg:        cfg,
		log:        log.With().Str("component", "whale13f").Logger(),
	}
}

// RunQuarters processes all filers for each quarter in [from, to] sequentially,
// then computes and saves accumulation scores per quarter.
func (s *BackfillService) RunQuarters(ctx context.Context, from, to QuarterID, filers []sec.FilerConfig) error {
	quarters := generateQuarters(from, to)
	if len(quarters) == 0 {
		return fmt.Errorf("whale13f: no quarters in range %s to %s", from, to)
	}

	s.log.Info().Str("from", from.String()).Str("to", to.String()).Int("quarters", len(quarters)).Int("filers", len(filers)).Msg("starting backfill")

	for _, q := range quarters {
		quarterEnd := q.EndDate()
		s.log.Info().Str("quarter", q.String()).Time("quarter_end", quarterEnd).Msg("processing quarter")

		if err := s.processQuarter(ctx, q, filers); err != nil {
			return fmt.Errorf("whale13f: process quarter %s: %w", q, err)
		}

		if err := s.computeAndSaveScores(ctx, q); err != nil {
			return fmt.Errorf("whale13f: compute scores for %s: %w", q, err)
		}

		s.log.Info().Str("quarter", q.String()).Msg("quarter complete")
	}

	return nil
}

// processQuarter fans out across filers with bounded concurrency.
func (s *BackfillService) processQuarter(ctx context.Context, q QuarterID, filers []sec.FilerConfig) error {
	sem := make(chan struct{}, s.cfg.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, filer := range filers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(f sec.FilerConfig) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := s.processFiler(ctx, f, q.EndDate()); err != nil {
				s.log.Error().Err(err).Str("filer", f.Name).Str("cik", f.CIK).Msg("filer processing failed")
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s (CIK %s): %w", f.Name, f.CIK, err))
				mu.Unlock()
			}
		}(filer)
	}

	wg.Wait()

	if len(errs) > 0 {
		s.log.Warn().Int("failed", len(errs)).Int("total", len(filers)).Msg("some filers had errors")
		// Non-fatal: log but continue so partial data is still scored.
	}
	return nil
}

// processFiler fetches a single filer's 13F-HR for the given quarter, resolves
// CUSIPs to tickers, and persists the holdings.
func (s *BackfillService) processFiler(ctx context.Context, filer sec.FilerConfig, quarterEnd time.Time) error {
	// SEC filing deadline is 45 days after quarter end. Search up to 90 days
	// to catch late filings and amendments.
	from := quarterEnd
	to := quarterEnd.AddDate(0, 0, 90)

	entries, err := s.edgar.FetchFilingIndex(ctx, filer.CIK, from, to)
	if err != nil {
		return fmt.Errorf("whale13f: fetch index: %w", err)
	}
	if len(entries) == 0 {
		s.log.Debug().Str("filer", filer.Name).Str("cik", filer.CIK).Msg("no filings found for quarter")
		return nil
	}

	// Pick the best filing: prefer 13F-HR/A (amendment) over 13F-HR, then latest date.
	best := pickBestFiling(entries)

	holdings, err := s.edgar.FetchInformationTable(ctx, filer.CIK, best.AccessionNumber)
	if err != nil {
		return fmt.Errorf("whale13f: fetch infotable: %w", err)
	}
	if len(holdings) == 0 {
		s.log.Warn().Str("filer", filer.Name).Str("accession", best.AccessionNumber).Msg("empty information table")
		return nil
	}

	// Collect unique CUSIPs.
	cusipSet := make(map[string]struct{}, len(holdings))
	for _, h := range holdings {
		cusipSet[h.CUSIP] = struct{}{}
	}
	cusips := make([]string, 0, len(cusipSet))
	for cu := range cusipSet {
		cusips = append(cusips, cu)
	}

	// Resolve CUSIPs: cache first, then OpenFIGI for uncached.
	tickerMap, err := s.resolveCUSIPs(ctx, cusips)
	if err != nil {
		return fmt.Errorf("whale13f: resolve cusips: %w", err)
	}

	// Build domain filings.
	filings := make([]domain.WhaleFiling, 0, len(holdings))
	for _, h := range holdings {
		ticker := ""
		if m, ok := tickerMap[h.CUSIP]; ok {
			ticker = m.Ticker
		}
		filings = append(filings, domain.WhaleFiling{
			FilingDate:      quarterEnd,
			FilerCIK:        filer.CIK,
			FilerName:       filer.Name,
			CUSIP:           h.CUSIP,
			Ticker:          ticker,
			IssuerName:      h.NameOfIssuer,
			ShareCount:      h.ShareCount,
			MarketValue1000: h.Value,
			PutCall:         h.PutCall,
			FilerTier:       filer.Tier,
		})
	}

	n, err := s.whaleRepo.SaveWhaleFilingsBatch(ctx, filings)
	if err != nil {
		return fmt.Errorf("whale13f: save filings: %w", err)
	}

	s.log.Info().
		Str("filer", filer.Name).
		Str("accession", best.AccessionNumber).
		Int64("rows", n).
		Int("holdings", len(holdings)).
		Msg("saved filings")

	return nil
}

// resolveCUSIPs checks the cache first, then calls OpenFIGI for uncached CUSIPs,
// and saves newly resolved mappings back to the cache.
func (s *BackfillService) resolveCUSIPs(ctx context.Context, cusips []string) (map[string]domain.CUSIPMapping, error) {
	cached, err := s.cusipCache.GetCached(ctx, cusips)
	if err != nil {
		return nil, fmt.Errorf("whale13f: get cached cusips: %w", err)
	}

	// Find uncached CUSIPs.
	var uncached []string
	for _, cu := range cusips {
		if _, ok := cached[cu]; !ok {
			uncached = append(uncached, cu)
		}
	}

	if len(uncached) == 0 {
		return cached, nil
	}

	s.log.Debug().Int("cached", len(cached)).Int("uncached", len(uncached)).Msg("resolving uncached CUSIPs via OpenFIGI")

	resolved, err := s.figi.ResolveCUSIPs(ctx, uncached)
	if err != nil {
		return nil, fmt.Errorf("whale13f: openfigi resolve: %w", err)
	}

	// Save newly resolved to cache.
	if len(resolved) > 0 {
		newMappings := make([]domain.CUSIPMapping, 0, len(resolved))
		for _, m := range resolved {
			newMappings = append(newMappings, m)
		}
		if err := s.cusipCache.Save(ctx, newMappings); err != nil {
			s.log.Error().Err(err).Msg("failed to save CUSIP cache (non-fatal)")
		}
	}

	// Merge cached + resolved.
	result := make(map[string]domain.CUSIPMapping, len(cached)+len(resolved))
	for k, v := range cached {
		result[k] = v
	}
	for k, v := range resolved {
		result[k] = v
	}

	return result, nil
}

// computeAndSaveScores loads current and previous quarter filings, computes
// accumulation scores, and persists them.
func (s *BackfillService) computeAndSaveScores(ctx context.Context, q QuarterID) error {
	quarterEnd := q.EndDate()
	prevEnd := q.Prev().EndDate()

	current, err := s.whaleRepo.GetWhaleFilingsByQuarter(ctx, quarterEnd)
	if err != nil {
		return fmt.Errorf("whale13f: load current quarter filings: %w", err)
	}

	previous, err := s.whaleRepo.GetWhaleFilingsByQuarter(ctx, prevEnd)
	if err != nil {
		return fmt.Errorf("whale13f: load previous quarter filings: %w", err)
	}

	scores := ComputeAccumulation(current, previous, quarterEnd)
	if len(scores) == 0 {
		s.log.Info().Str("quarter", q.String()).Msg("no accumulation scores to save")
		return nil
	}

	n, err := s.whaleRepo.SaveWhaleAccumulationBatch(ctx, scores)
	if err != nil {
		return fmt.Errorf("whale13f: save accumulation scores: %w", err)
	}

	s.log.Info().Str("quarter", q.String()).Int64("scores_saved", n).Int("tickers", len(scores)).Msg("accumulation scores computed and saved")
	return nil
}

// generateQuarters returns an ordered slice of quarters from 'from' to 'to' (inclusive).
func generateQuarters(from, to QuarterID) []QuarterID {
	var quarters []QuarterID
	q := from
	for {
		quarters = append(quarters, q)
		if q.Year == to.Year && q.Quarter == to.Quarter {
			break
		}
		// Advance to next quarter.
		if q.Quarter == 4 {
			q = QuarterID{Year: q.Year + 1, Quarter: 1}
		} else {
			q = QuarterID{Year: q.Year, Quarter: q.Quarter + 1}
		}
		// Safety: prevent infinite loop if from > to.
		if q.Year > to.Year+1 {
			break
		}
	}
	return quarters
}

// pickBestFiling selects the best filing from a list: prefers 13F-HR/A (amendment)
// over 13F-HR, then the latest filing date.
func pickBestFiling(entries []sec.FilingEntry) sec.FilingEntry {
	best := entries[0]
	for _, e := range entries[1:] {
		eBetter := false
		if e.FormType == "13F-HR/A" && best.FormType != "13F-HR/A" {
			eBetter = true
		} else if e.FormType == best.FormType && e.FilingDate.After(best.FilingDate) {
			eBetter = true
		}
		if eBetter {
			best = e
		}
	}
	return best
}
