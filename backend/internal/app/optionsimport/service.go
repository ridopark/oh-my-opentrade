// Package optionsimport fetches historical option chain data from DoltHub
// and stores it in the local database for backtest consumption.
package optionsimport

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/dolthub"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Service coordinates importing historical option chain data from DoltHub.
type Service struct {
	client *dolthub.Client
	repo   ports.HistoricalOptionsPort
	log    zerolog.Logger
}

// NewService creates a new options import service.
func NewService(client *dolthub.Client, repo ports.HistoricalOptionsPort, log zerolog.Logger) *Service {
	return &Service{
		client: client,
		repo:   repo,
		log:    log.With().Str("component", "options_import").Logger(),
	}
}

// EnsureData checks for missing dates in the local DB and imports them from DoltHub.
// Skips weekends and dates that already have data. Rate-limits to ~2 req/sec.
func (s *Service) EnsureData(ctx context.Context, symbol string, from, to time.Time) error {
	sym := domain.Symbol(symbol)

	// Iterate over trading days in the date range.
	imported := 0
	skipped := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check if we already have data for this date.
		has, err := s.repo.HasData(ctx, sym, d)
		if err != nil {
			return fmt.Errorf("HasData check for %s %s: %w", symbol, d.Format("2006-01-02"), err)
		}
		if has {
			skipped++
			continue
		}

		// Fetch from DoltHub.
		rows, err := s.client.FetchChain(ctx, symbol, d)
		if err != nil {
			s.log.Warn().Err(err).Str("symbol", symbol).Str("date", d.Format("2006-01-02")).
				Msg("failed to fetch from DoltHub — skipping date")
			continue
		}

		if len(rows) == 0 {
			continue // no data for this date (holiday, or symbol not covered)
		}

		// Store locally.
		if err := s.repo.SaveBatch(ctx, rows); err != nil {
			return fmt.Errorf("SaveBatch for %s %s: %w", symbol, d.Format("2006-01-02"), err)
		}

		imported++
		if imported%10 == 0 {
			s.log.Info().Str("symbol", symbol).Int("imported_days", imported).Int("skipped", skipped).
				Str("current_date", d.Format("2006-01-02")).Msg("import progress")
		}

		// Rate limit: ~2 requests/sec.
		time.Sleep(500 * time.Millisecond)
	}

	s.log.Info().Str("symbol", symbol).Int("imported_days", imported).Int("skipped_existing", skipped).
		Msg("DoltHub import complete")
	return nil
}
