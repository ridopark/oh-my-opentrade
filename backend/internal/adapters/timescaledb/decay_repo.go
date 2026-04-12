package timescaledb

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// DecayRepository implements decay telemetry persistence.
type DecayRepository struct {
	db  *sql.DB
	log zerolog.Logger
}

// NewDecayRepository creates a new DecayRepository.
func NewDecayRepository(db *sql.DB, log zerolog.Logger) *DecayRepository {
	return &DecayRepository{db: db, log: log}
}

// InsertTradeStats records a completed trade for rolling PF/WR computation.
func (r *DecayRepository) InsertTradeStats(ctx context.Context, tradeID uuid.UUID, strategy, symbol string, pnl float64, regime, vixBucket string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO strategy_trade_stats (trade_id, strategy, symbol, pnl, regime, vix_bucket) VALUES ($1, $2, $3, $4, $5, $6)`,
		tradeID, strategy, symbol, pnl, regime, vixBucket,
	)
	return err
}

const queryRollingDecay = `
WITH ordered AS (
  SELECT pnl,
    ROW_NUMBER() OVER (ORDER BY inserted_at) AS trade_seq
  FROM strategy_trade_stats
  WHERE strategy = $1
)
SELECT trade_seq::int, pnl,
  SUM(CASE WHEN pnl > 0 THEN pnl ELSE 0 END) OVER w20 /
    NULLIF(ABS(SUM(CASE WHEN pnl < 0 THEN pnl ELSE 0 END) OVER w20), 0) AS rolling_pf_20,
  SUM(CASE WHEN pnl > 0 THEN pnl ELSE 0 END) OVER w60 /
    NULLIF(ABS(SUM(CASE WHEN pnl < 0 THEN pnl ELSE 0 END) OVER w60), 0) AS rolling_pf_60,
  SUM(CASE WHEN pnl > 0 THEN pnl ELSE 0 END) OVER w120 /
    NULLIF(ABS(SUM(CASE WHEN pnl < 0 THEN pnl ELSE 0 END) OVER w120), 0) AS rolling_pf_120,
  AVG(CASE WHEN pnl > 0 THEN 1.0 ELSE 0.0 END) OVER w60 AS rolling_wr_60
FROM ordered
WINDOW
  w20  AS (ORDER BY trade_seq ROWS BETWEEN 19 PRECEDING AND CURRENT ROW),
  w60  AS (ORDER BY trade_seq ROWS BETWEEN 59 PRECEDING AND CURRENT ROW),
  w120 AS (ORDER BY trade_seq ROWS BETWEEN 119 PRECEDING AND CURRENT ROW)
ORDER BY trade_seq
`

// GetRollingDecayStats returns rolling PF/WR points for a strategy.
func (r *DecayRepository) GetRollingDecayStats(ctx context.Context, strategy string) ([]domain.RollingDecayPoint, error) {
	rows, err := r.db.QueryContext(ctx, queryRollingDecay, strategy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []domain.RollingDecayPoint
	for rows.Next() {
		var p domain.RollingDecayPoint
		if err := rows.Scan(&p.TradeSeq, &p.PnL, &p.RollingPF20, &p.RollingPF60, &p.RollingPF120, &p.RollingWR60); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

const queryComponentAttribution = `
SELECT
  comp->>'name' AS component_name,
  comp->>'group' AS component_group,
  COUNT(*) FILTER (WHERE (comp->>'fired')::boolean) AS n_fired,
  COUNT(*) FILTER (WHERE NOT (comp->>'fired')::boolean) AS n_not_fired,
  COALESCE(SUM(s.pnl) FILTER (WHERE s.pnl > 0 AND (comp->>'fired')::boolean), 0) /
    NULLIF(ABS(COALESCE(SUM(s.pnl) FILTER (WHERE s.pnl < 0 AND (comp->>'fired')::boolean), 0)), 0) AS pf_fired,
  COALESCE(SUM(s.pnl) FILTER (WHERE s.pnl > 0 AND NOT (comp->>'fired')::boolean), 0) /
    NULLIF(ABS(COALESCE(SUM(s.pnl) FILTER (WHERE s.pnl < 0 AND NOT (comp->>'fired')::boolean), 0)), 0) AS pf_not_fired
FROM trades t
CROSS JOIN LATERAL jsonb_array_elements(t.thesis->'confluence'->'components') AS comp
JOIN strategy_trade_stats s ON s.trade_id = t.trade_id
WHERE s.strategy = $1
  AND t.thesis ? 'v'
GROUP BY comp->>'name', comp->>'group'
`

// GetComponentAttribution returns per-component PF attribution for ablation.
func (r *DecayRepository) GetComponentAttribution(ctx context.Context, strategy string) ([]domain.ComponentAttribution, error) {
	rows, err := r.db.QueryContext(ctx, queryComponentAttribution, strategy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.ComponentAttribution
	for rows.Next() {
		var ca domain.ComponentAttribution
		if err := rows.Scan(&ca.Component, &ca.Group, &ca.NFired, &ca.NNotFired, &ca.PFFired, &ca.PFNotFired); err != nil {
			return nil, err
		}
		if ca.PFFired != nil && ca.PFNotFired != nil {
			m := *ca.PFFired - *ca.PFNotFired
			ca.Marginal = &m
		}
		results = append(results, ca)
	}
	return results, rows.Err()
}
