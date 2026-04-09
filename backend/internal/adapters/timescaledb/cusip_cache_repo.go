package timescaledb

import (
	"context"
	"fmt"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// CUSIPCacheRepo handles persistence of CUSIP-to-ticker resolution cache.
type CUSIPCacheRepo struct {
	db  DBTX
	log zerolog.Logger
}

// NewCUSIPCacheRepo creates a new CUSIPCacheRepo with a structured logger.
func NewCUSIPCacheRepo(db DBTX, log zerolog.Logger) *CUSIPCacheRepo {
	return &CUSIPCacheRepo{db: db, log: log}
}

// GetCached returns cached CUSIP-to-ticker mappings for the given CUSIPs.
func (r *CUSIPCacheRepo) GetCached(ctx context.Context, cusips []string) (map[string]domain.CUSIPMapping, error) {
	if len(cusips) == 0 {
		return nil, nil
	}

	const query = `SELECT cusip, ticker, figi, resolved_at FROM cusip_ticker_cache WHERE cusip = ANY($1)`

	rows, err := r.db.QueryContext(ctx, query, cusips)
	if err != nil {
		return nil, fmt.Errorf("cusip_cache_repo: query: %w", err)
	}
	defer rows.Close()

	result := make(map[string]domain.CUSIPMapping, len(cusips))
	for rows.Next() {
		var m domain.CUSIPMapping
		if err := rows.Scan(&m.CUSIP, &m.Ticker, &m.FIGI, &m.ResolvedAt); err != nil {
			return nil, fmt.Errorf("cusip_cache_repo: scan: %w", err)
		}
		result[m.CUSIP] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cusip_cache_repo: rows: %w", err)
	}
	return result, nil
}

// Save upserts a batch of CUSIP-to-ticker mappings.
func (r *CUSIPCacheRepo) Save(ctx context.Context, mappings []domain.CUSIPMapping) error {
	if len(mappings) == 0 {
		return nil
	}

	const maxBatchSize = 5000
	if len(mappings) > maxBatchSize {
		for i := 0; i < len(mappings); i += maxBatchSize {
			end := i + maxBatchSize
			if end > len(mappings) {
				end = len(mappings)
			}
			if err := r.Save(ctx, mappings[i:end]); err != nil {
				return err
			}
		}
		return nil
	}

	const cols = 4
	var b strings.Builder
	b.WriteString("INSERT INTO cusip_ticker_cache (cusip, ticker, figi, resolved_at) VALUES ")

	args := make([]any, 0, len(mappings)*cols)
	for i, m := range mappings {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i*cols + 1
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d)", base, base+1, base+2, base+3)
		args = append(args, m.CUSIP, m.Ticker, m.FIGI, m.ResolvedAt)
	}

	b.WriteString(" ON CONFLICT (cusip) DO UPDATE SET ticker=EXCLUDED.ticker, figi=EXCLUDED.figi, resolved_at=EXCLUDED.resolved_at")

	_, err := r.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		r.log.Error().Err(err).Int("batch_size", len(mappings)).Msg("cusip_cache_repo: failed to save mappings")
		return fmt.Errorf("cusip_cache_repo: save: %w", err)
	}
	return nil
}
