package timescaledb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

// RecapDigest is one row of the recap_digests table.
type RecapDigest struct {
	ID            int64
	DigestDate    time.Time
	TenantID      string
	EnvMode       string
	Body          string
	TradesCovered int
	NetPnLToday   float64
	PromptVersion string
	Model         string
	GeneratedAt   time.Time
}

// RecapRepo persists EOD recap digests produced by the recap service.
type RecapRepo struct {
	db  *sql.DB
	log zerolog.Logger
}

func NewRecapRepo(db *sql.DB, log zerolog.Logger) *RecapRepo {
	return &RecapRepo{db: db, log: log}
}

// Upsert writes a digest for (tenant, env, date). Second write on the same
// day overwrites the previous one, so re-runs during the same afternoon
// replace rather than duplicate.
func (r *RecapRepo) Upsert(ctx context.Context, d RecapDigest) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO recap_digests
		 (digest_date, tenant_id, env_mode, body, trades_covered, net_pnl_today, prompt_version, model, generated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (tenant_id, env_mode, digest_date) DO UPDATE SET
		   body = EXCLUDED.body,
		   trades_covered = EXCLUDED.trades_covered,
		   net_pnl_today = EXCLUDED.net_pnl_today,
		   prompt_version = EXCLUDED.prompt_version,
		   model = EXCLUDED.model,
		   generated_at = EXCLUDED.generated_at`,
		d.DigestDate, d.TenantID, d.EnvMode, d.Body,
		d.TradesCovered, d.NetPnLToday, d.PromptVersion, d.Model, d.GeneratedAt,
	)
	if err != nil {
		return fmt.Errorf("timescaledb: upsert recap digest: %w", err)
	}
	return nil
}

// GetByDate returns the digest for (tenant, env, date) or nil if none exists.
func (r *RecapRepo) GetByDate(ctx context.Context, tenantID, envMode string, date time.Time) (*RecapDigest, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, digest_date, tenant_id, env_mode, body, trades_covered, net_pnl_today, prompt_version, model, generated_at
		 FROM recap_digests
		 WHERE tenant_id = $1 AND env_mode = $2 AND digest_date = $3`,
		tenantID, envMode, date)
	var d RecapDigest
	if err := row.Scan(&d.ID, &d.DigestDate, &d.TenantID, &d.EnvMode, &d.Body,
		&d.TradesCovered, &d.NetPnLToday, &d.PromptVersion, &d.Model, &d.GeneratedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: get recap digest: %w", err)
	}
	return &d, nil
}
