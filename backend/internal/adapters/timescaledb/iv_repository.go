package timescaledb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

const (
	queryInsertIVSnapshot = `INSERT INTO iv_snapshots (time, symbol, atm_iv, atm_strike, spot_price, call_iv, put_iv)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (symbol, time) DO UPDATE SET
			atm_iv = EXCLUDED.atm_iv,
			atm_strike = EXCLUDED.atm_strike,
			spot_price = EXCLUDED.spot_price,
			call_iv = EXCLUDED.call_iv,
			put_iv = EXCLUDED.put_iv`

	querySelectLatestIV = `SELECT time, symbol, atm_iv, atm_strike, spot_price, call_iv, put_iv
		FROM iv_snapshots
		WHERE symbol = $1
		ORDER BY time DESC
		LIMIT 1`

	// IV Rank = (current - 52w_low) / (52w_high - 52w_low)
	// IV Percentile = count(days where IV < current) / total_days
	querySelectIVStats = `WITH window AS (
		SELECT atm_iv
		FROM iv_snapshots
		WHERE symbol = $1 AND time >= $2 AND time <= $3
	),
	agg AS (
		SELECT
			MIN(atm_iv) AS low,
			MAX(atm_iv) AS high,
			COUNT(*) AS total
		FROM window
	),
	latest AS (
		SELECT atm_iv FROM iv_snapshots
		WHERE symbol = $1 AND time <= $3
		ORDER BY time DESC LIMIT 1
	),
	pct AS (
		SELECT COUNT(*) AS below
		FROM window, latest
		WHERE window.atm_iv < latest.atm_iv
	)
	SELECT
		latest.atm_iv,
		agg.low,
		agg.high,
		agg.total,
		CASE WHEN agg.high - agg.low > 0
			THEN (latest.atm_iv - agg.low) / (agg.high - agg.low)
			ELSE 0 END AS iv_rank,
		CASE WHEN agg.total > 0
			THEN pct.below::float / agg.total
			ELSE 0 END AS iv_percentile
	FROM latest, agg, pct`
)

type IVRepository struct {
	db  DBTX
	log zerolog.Logger
}

func NewIVRepository(db DBTX, log zerolog.Logger) *IVRepository {
	return &IVRepository{db: db, log: log}
}

func (r *IVRepository) SaveIVSnapshot(ctx context.Context, snap domain.IVSnapshot) error {
	_, err := r.db.ExecContext(ctx, queryInsertIVSnapshot,
		snap.Time, string(snap.Symbol), snap.ATMIV,
		snap.ATMStrike, snap.SpotPrice, snap.CallIV, snap.PutIV,
	)
	if err != nil {
		r.log.Error().Err(err).
			Str("symbol", string(snap.Symbol)).
			Time("time", snap.Time).
			Msg("failed to save IV snapshot")
	}
	return err
}

func (r *IVRepository) GetLatestIV(ctx context.Context, symbol domain.Symbol) (domain.IVSnapshot, error) {
	row := r.db.QueryRowContext(ctx, querySelectLatestIV, string(symbol))
	var snap domain.IVSnapshot
	var sym string
	err := row.Scan(&snap.Time, &sym, &snap.ATMIV, &snap.ATMStrike, &snap.SpotPrice, &snap.CallIV, &snap.PutIV)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.IVSnapshot{}, fmt.Errorf("no IV snapshots for %s", symbol)
		}
		return domain.IVSnapshot{}, err
	}
	snap.Symbol = domain.Symbol(sym)
	return snap, nil
}

func (r *IVRepository) GetIVStats(ctx context.Context, symbol domain.Symbol, asOf time.Time, lookbackDays int) (domain.IVStats, error) {
	from := asOf.AddDate(0, 0, -lookbackDays)
	row := r.db.QueryRowContext(ctx, querySelectIVStats, string(symbol), from, asOf)

	var stats domain.IVStats
	var currentIV, low, high, ivRank, ivPct float64
	var total int
	err := row.Scan(&currentIV, &low, &high, &total, &ivRank, &ivPct)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.IVStats{}, fmt.Errorf("no IV data for %s in lookback window", symbol)
		}
		return domain.IVStats{}, err
	}

	stats.Symbol = symbol
	stats.CurrentIV = currentIV
	stats.IVRank = ivRank
	stats.IVPercentile = ivPct
	stats.High52W = high
	stats.Low52W = low
	stats.LookbackDays = total
	return stats, nil
}
