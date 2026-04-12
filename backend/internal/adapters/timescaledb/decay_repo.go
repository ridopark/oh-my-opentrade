package timescaledb

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

const (
	queryInsertTradeStats = `INSERT INTO strategy_trade_stats
		(trade_id, strategy, symbol, pnl, regime, vix_bucket)
		VALUES ($1, $2, $3, $4, $5, $6)`

	querySelectRollingDecay = `WITH ordered AS (
		SELECT id, strategy, symbol, pnl, inserted_at,
			ROW_NUMBER() OVER (PARTITION BY strategy ORDER BY inserted_at) AS trade_seq
		FROM strategy_trade_stats
		WHERE strategy = $1
		ORDER BY inserted_at
	)
	SELECT trade_seq, pnl,
		SUM(CASE WHEN pnl > 0 THEN pnl ELSE 0 END) OVER w20 /
			NULLIF(ABS(SUM(CASE WHEN pnl < 0 THEN pnl ELSE 0 END) OVER w20), 0) AS rolling_pf_20,
		SUM(CASE WHEN pnl > 0 THEN pnl ELSE 0 END) OVER w60 /
			NULLIF(ABS(SUM(CASE WHEN pnl < 0 THEN pnl ELSE 0 END) OVER w60), 0) AS rolling_pf_60,
		SUM(CASE WHEN pnl > 0 THEN pnl ELSE 0 END) OVER w120 /
			NULLIF(ABS(SUM(CASE WHEN pnl < 0 THEN pnl ELSE 0 END) OVER w120), 0) AS rolling_pf_120,
		AVG(CASE WHEN pnl > 0 THEN 1.0 ELSE 0.0 END) OVER w60 AS rolling_wr_60
	FROM ordered
	WINDOW
		w20 AS (ORDER BY trade_seq ROWS BETWEEN 19 PRECEDING AND CURRENT ROW),
		w60 AS (ORDER BY trade_seq ROWS BETWEEN 59 PRECEDING AND CURRENT ROW),
		w120 AS (ORDER BY trade_seq ROWS BETWEEN 119 PRECEDING AND CURRENT ROW)
	ORDER BY trade_seq`

	querySelectComponentAttribution = `SELECT
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
		AND t.thesis->>'v' = '1'
	GROUP BY comp->>'name', comp->>'group'`
)

// DecayRepository implements ports.DecayTelemetryPort using TimescaleDB.
type DecayRepository struct {
	db  DBTX
	log zerolog.Logger
}

// NewDecayRepository creates a new decay telemetry repository.
func NewDecayRepository(db DBTX, log zerolog.Logger) *DecayRepository {
	return &DecayRepository{db: db, log: log}
}

// InsertTradeStats records a single trade's stats for decay analysis.
func (r *DecayRepository) InsertTradeStats(ctx context.Context, tradeID uuid.UUID, strategy, symbol string, pnl float64, regime, vixBucket string) error {
	_, err := r.db.ExecContext(ctx, queryInsertTradeStats,
		tradeID, strategy, symbol, pnl, regime, vixBucket,
	)
	if err != nil {
		r.log.Error().Err(err).
			Str("trade_id", tradeID.String()).
			Str("strategy", strategy).
			Msg("failed to insert trade stats")
		return fmt.Errorf("timescaledb: insert trade stats: %w", err)
	}
	return nil
}

// GetRollingDecayStats computes rolling PF/WR over trailing windows for a strategy.
func (r *DecayRepository) GetRollingDecayStats(ctx context.Context, strategy string) ([]domain.RollingDecayPoint, error) {
	rows, err := r.db.QueryContext(ctx, querySelectRollingDecay, strategy)
	if err != nil {
		r.log.Error().Err(err).
			Str("strategy", strategy).
			Msg("failed to query rolling decay stats")
		return nil, fmt.Errorf("timescaledb: get rolling decay stats: %w", err)
	}
	defer rows.Close()

	var results []domain.RollingDecayPoint
	for rows.Next() {
		var pt domain.RollingDecayPoint
		if err := rows.Scan(
			&pt.TradeSeq, &pt.PnL,
			&pt.RollingPF20, &pt.RollingPF60, &pt.RollingPF120,
			&pt.RollingWR60,
		); err != nil {
			return nil, fmt.Errorf("timescaledb: scan rolling decay point: %w", err)
		}
		results = append(results, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate rolling decay stats: %w", err)
	}
	return results, nil
}

// GetComponentAttribution computes PF-with vs PF-without for each confluence component.
func (r *DecayRepository) GetComponentAttribution(ctx context.Context, strategy string) ([]domain.ComponentAttribution, error) {
	rows, err := r.db.QueryContext(ctx, querySelectComponentAttribution, strategy)
	if err != nil {
		r.log.Error().Err(err).
			Str("strategy", strategy).
			Msg("failed to query component attribution")
		return nil, fmt.Errorf("timescaledb: get component attribution: %w", err)
	}
	defer rows.Close()

	var results []domain.ComponentAttribution
	for rows.Next() {
		var attr domain.ComponentAttribution
		if err := rows.Scan(
			&attr.Component, &attr.Group,
			&attr.NFired, &attr.NNotFired,
			&attr.PFFired, &attr.PFNotFired,
		); err != nil {
			return nil, fmt.Errorf("timescaledb: scan component attribution: %w", err)
		}
		// Compute marginal: PF-fired minus PF-not-fired.
		if attr.PFFired != nil && attr.PFNotFired != nil {
			m := *attr.PFFired - *attr.PFNotFired
			attr.Marginal = &m
		}
		results = append(results, attr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate component attribution: %w", err)
	}
	return results, nil
}
