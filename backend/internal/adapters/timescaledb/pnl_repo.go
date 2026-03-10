package timescaledb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

const (
	queryUpsertDailyPnL = `INSERT INTO daily_pnl (date, account_id, env_mode, realized_pnl, unrealized_pnl, trade_count, max_drawdown)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (account_id, env_mode, date) DO UPDATE SET
			realized_pnl = EXCLUDED.realized_pnl,
			unrealized_pnl = EXCLUDED.unrealized_pnl,
			trade_count = EXCLUDED.trade_count,
			max_drawdown = EXCLUDED.max_drawdown`

	querySelectDailyPnL = `SELECT date, realized_pnl, unrealized_pnl, trade_count, max_drawdown
		FROM daily_pnl
		WHERE account_id = $1 AND env_mode = $2 AND date >= $3 AND date <= $4
		ORDER BY date`

	querySelectDailyRealizedPnL = `SELECT COALESCE(realized_pnl, 0) FROM daily_pnl
		WHERE account_id = $1 AND env_mode = $2 AND date = $3`

	queryInsertEquityPoint = `INSERT INTO equity_curve (time, account_id, env_mode, equity, cash, drawdown)
		VALUES ($1, $2, $3, $4, $5, $6)`

	querySelectEquityCurve = `SELECT time, equity, cash, drawdown FROM equity_curve
		WHERE account_id = $1 AND env_mode = $2 AND time >= $3 AND time <= $4
		ORDER BY time`

	querySelectBucketedEquity = `SELECT
		time_bucket($1::interval, time) AS ts,
		last(equity, time) AS equity,
		last(cash, time) AS cash,
		last(drawdown, time) AS drawdown
		FROM equity_curve
		WHERE account_id = $2 AND env_mode = $3 AND time >= $4 AND time <= $5
		GROUP BY ts
		ORDER BY ts`

	querySelectMaxDrawdown = `SELECT COALESCE(MIN(drawdown), 0) FROM equity_curve
		WHERE account_id = $1 AND env_mode = $2 AND time >= $3 AND time <= $4`

	querySelectSharpe = `WITH eod AS (
		SELECT
			(time_bucket('1 day', time))::date AS trade_date,
			last(equity, time) AS equity
		FROM equity_curve
		WHERE account_id = $1 AND env_mode = $2 AND time >= $3 AND time <= $4
		GROUP BY 1
	),
	rets AS (
		SELECT
			trade_date,
			(equity / lag(equity) OVER (ORDER BY trade_date) - 1.0) AS r
		FROM eod
	)
	SELECT
		CASE
			WHEN COUNT(r) FILTER (WHERE r IS NOT NULL) >= 2
			 AND stddev_samp(r) FILTER (WHERE r IS NOT NULL) > 0
			THEN sqrt(252) * avg(r) FILTER (WHERE r IS NOT NULL) / stddev_samp(r) FILTER (WHERE r IS NOT NULL)
			ELSE NULL
		END AS sharpe
	FROM rets`

	querySelectSortino = `WITH eod AS (
		SELECT
			(time_bucket('1 day', time))::date AS trade_date,
			last(equity, time) AS equity
		FROM equity_curve
		WHERE account_id = $1 AND env_mode = $2 AND time >= $3 AND time <= $4
		GROUP BY 1
	),
	rets AS (
		SELECT
			(equity / lag(equity) OVER (ORDER BY trade_date) - 1.0) AS r
		FROM eod
	)
	SELECT
		CASE
			WHEN COUNT(r) FILTER (WHERE r IS NOT NULL) >= 2
			 AND sqrt(avg(r * r) FILTER (WHERE r IS NOT NULL AND r < 0)) > 0
			THEN sqrt(252) * avg(r) FILTER (WHERE r IS NOT NULL)
			     / sqrt(avg(r * r) FILTER (WHERE r IS NOT NULL AND r < 0))
			ELSE NULL
		END AS sortino
	FROM rets`
)

// PnLRepository implements ports.PnLPort using TimescaleDB.
type PnLRepository struct {
	db  DBTX
	log zerolog.Logger
}

// NewPnLRepository creates a new PnL repository.
func NewPnLRepository(db DBTX, log zerolog.Logger) *PnLRepository {
	return &PnLRepository{db: db, log: log}
}

// UpsertDailyPnL inserts or updates the daily P&L record for a given date.
func (r *PnLRepository) UpsertDailyPnL(ctx context.Context, pnl domain.DailyPnL) error {
	_, err := r.db.ExecContext(ctx, queryUpsertDailyPnL,
		pnl.Date, pnl.TenantID, string(pnl.EnvMode),
		pnl.RealizedPnL, pnl.UnrealizedPnL, pnl.TradeCount, pnl.MaxDrawdown,
	)
	if err != nil {
		r.log.Error().Err(err).
			Time("date", pnl.Date).
			Str("tenant_id", pnl.TenantID).
			Msg("failed to upsert daily P&L")
		return fmt.Errorf("timescaledb: upsert daily pnl: %w", err)
	}
	return nil
}

// GetDailyPnL retrieves daily P&L records for a tenant within a date range.
func (r *PnLRepository) GetDailyPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.DailyPnL, error) {
	rows, err := r.db.QueryContext(ctx, querySelectDailyPnL, tenantID, string(envMode), from, to)
	if err != nil {
		r.log.Error().Err(err).
			Str("tenant_id", tenantID).
			Msg("failed to query daily P&L")
		return nil, fmt.Errorf("timescaledb: get daily pnl: %w", err)
	}
	defer rows.Close()

	var results []domain.DailyPnL
	for rows.Next() {
		var pnl domain.DailyPnL
		if err := rows.Scan(&pnl.Date, &pnl.RealizedPnL, &pnl.UnrealizedPnL, &pnl.TradeCount, &pnl.MaxDrawdown); err != nil {
			return nil, fmt.Errorf("timescaledb: scan daily pnl: %w", err)
		}
		pnl.TenantID = tenantID
		pnl.EnvMode = envMode
		results = append(results, pnl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate daily pnl: %w", err)
	}
	return results, nil
}

// GetDailyRealizedPnL returns the cumulative realized P&L for the given date.
// Returns 0 if no record exists (no trades yet today).
func (r *PnLRepository) GetDailyRealizedPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, date time.Time) (float64, error) {
	row := r.db.QueryRowContext(ctx, querySelectDailyRealizedPnL, tenantID, string(envMode), date)
	var pnl float64
	if err := row.Scan(&pnl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("timescaledb: get daily realized pnl: %w", err)
	}
	return pnl, nil
}

// SaveEquityPoint appends a point to the equity curve.
func (r *PnLRepository) SaveEquityPoint(ctx context.Context, pt domain.EquityPoint) error {
	_, err := r.db.ExecContext(ctx, queryInsertEquityPoint,
		pt.Time, pt.TenantID, string(pt.EnvMode), pt.Equity, pt.Cash, pt.Drawdown,
	)
	if err != nil {
		r.log.Error().Err(err).
			Str("tenant_id", pt.TenantID).
			Msg("failed to save equity point")
		return fmt.Errorf("timescaledb: save equity point: %w", err)
	}
	return nil
}

// GetEquityCurve retrieves equity curve points for a tenant within a time range.
func (r *PnLRepository) GetEquityCurve(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.EquityPoint, error) {
	rows, err := r.db.QueryContext(ctx, querySelectEquityCurve, tenantID, string(envMode), from, to)
	if err != nil {
		r.log.Error().Err(err).
			Str("tenant_id", tenantID).
			Msg("failed to query equity curve")
		return nil, fmt.Errorf("timescaledb: get equity curve: %w", err)
	}
	defer rows.Close()

	var results []domain.EquityPoint
	for rows.Next() {
		var pt domain.EquityPoint
		if err := rows.Scan(&pt.Time, &pt.Equity, &pt.Cash, &pt.Drawdown); err != nil {
			return nil, fmt.Errorf("timescaledb: scan equity point: %w", err)
		}
		pt.TenantID = tenantID
		pt.EnvMode = envMode
		results = append(results, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate equity curve: %w", err)
	}
	return results, nil
}

// GetBucketedEquityCurve returns equity points downsampled by the given bucket interval.
func (r *PnLRepository) GetBucketedEquityCurve(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time, bucket string) ([]domain.EquityPoint, error) {
	rows, err := r.db.QueryContext(ctx, querySelectBucketedEquity, bucket, tenantID, string(envMode), from, to)
	if err != nil {
		r.log.Error().Err(err).
			Str("tenant_id", tenantID).
			Str("bucket", bucket).
			Msg("failed to query bucketed equity curve")
		return nil, fmt.Errorf("timescaledb: get bucketed equity curve: %w", err)
	}
	defer rows.Close()

	var results []domain.EquityPoint
	for rows.Next() {
		var pt domain.EquityPoint
		if err := rows.Scan(&pt.Time, &pt.Equity, &pt.Cash, &pt.Drawdown); err != nil {
			return nil, fmt.Errorf("timescaledb: scan bucketed equity point: %w", err)
		}
		pt.TenantID = tenantID
		pt.EnvMode = envMode
		results = append(results, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescaledb: iterate bucketed equity curve: %w", err)
	}
	return results, nil
}

// GetMaxDrawdown returns the worst (most negative) drawdown value in the time range.
func (r *PnLRepository) GetMaxDrawdown(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) (float64, error) {
	row := r.db.QueryRowContext(ctx, querySelectMaxDrawdown, tenantID, string(envMode), from, to)
	var dd float64
	if err := row.Scan(&dd); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("timescaledb: get max drawdown: %w", err)
	}
	return dd, nil
}

// GetSharpe computes the annualized Sharpe ratio from daily equity returns.
// Returns nil if insufficient data (fewer than 2 days or zero variance).
func (r *PnLRepository) GetSharpe(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) (*float64, error) {
	row := r.db.QueryRowContext(ctx, querySelectSharpe, tenantID, string(envMode), from, to)
	var sharpe *float64
	if err := row.Scan(&sharpe); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: get sharpe: %w", err)
	}
	return sharpe, nil
}

// GetSortino computes the annualized Sortino ratio from daily equity returns.
// Returns nil if insufficient data (fewer than 2 days or zero downside deviation).
func (r *PnLRepository) GetSortino(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) (*float64, error) {
	row := r.db.QueryRowContext(ctx, querySelectSortino, tenantID, string(envMode), from, to)
	var sortino *float64
	if err := row.Scan(&sortino); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("timescaledb: get sortino: %w", err)
	}
	return sortino, nil
}
