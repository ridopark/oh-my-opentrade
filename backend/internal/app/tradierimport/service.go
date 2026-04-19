// Package tradierimport coordinates daily snapshots of option chain data
// from the Tradier sandbox API into the historical_option_chain table.
//
// Mirrors the shape of optionsimport/ (DoltHub) but for symbols DoltHub
// doesn't cover. The two services can run side-by-side: DoltHub fills
// covered symbols, Tradier fills the gap set (AFRM, IWM, MRVL, NET, QQQ,
// RBLX, RIVN, SNOW, SOFI, SOXL). Both write into the same table, so the
// simbroker's HistoricalOptionsPort consumes them uniformly.
package tradierimport

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/tradier"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Service drives a single snapshot pass for a set of symbols.
type Service struct {
	client *tradier.Client
	repo   ports.HistoricalOptionsPort
	log    zerolog.Logger
}

// NewService constructs a Service. When client is nil (no Tradier token
// configured), every Snapshot call returns immediately with a zero result.
func NewService(client *tradier.Client, repo ports.HistoricalOptionsPort, log zerolog.Logger) *Service {
	return &Service{
		client: client,
		repo:   repo,
		log:    log.With().Str("component", "tradier_import").Logger(),
	}
}

// Stats is the per-run result reported back to the scheduler.
type Stats struct {
	Symbols      int
	Expirations  int
	ContractRows int
	Errors       int
}

// Snapshot captures the current Tradier chain (all expirations) for each
// symbol and writes rows into the historical options repo. The snapshot
// date column is today (UTC).
//
// The method rate-limits itself gently via a per-call sleep so a default
// sandbox token (~120 req/min cap) cannot be exhausted by a single run.
func (s *Service) Snapshot(ctx context.Context, symbols []string) (Stats, error) {
	var stats Stats
	if s.client == nil {
		return stats, fmt.Errorf("tradierimport: nil client — TRADIER_TOKEN not configured")
	}

	for _, sym := range symbols {
		stats.Symbols++
		slog := s.log.With().Str("symbol", sym).Logger()

		exps, err := s.client.Expirations(ctx, sym)
		if err != nil {
			slog.Warn().Err(err).Msg("expirations fetch failed")
			stats.Errors++
			continue
		}
		slog.Debug().Int("n", len(exps)).Msg("expirations")

		for _, exp := range exps {
			stats.Expirations++
			rows, err := s.client.ChainSnapshot(ctx, sym, exp)
			if err != nil {
				slog.Warn().Err(err).Time("expiration", exp).Msg("chain fetch failed")
				stats.Errors++
				continue
			}
			if len(rows) == 0 {
				continue
			}
			if err := s.repo.SaveBatch(ctx, rows); err != nil {
				slog.Error().Err(err).Time("expiration", exp).Msg("save failed")
				stats.Errors++
				continue
			}
			stats.ContractRows += len(rows)

			// Gentle rate limit — sandbox allows ~120 req/min. 500ms/call
			// caps us at 120/min, leaving headroom for the expirations call.
			select {
			case <-ctx.Done():
				return stats, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		slog.Info().Int("expirations", len(exps)).Msg("symbol complete")
	}
	return stats, nil
}
