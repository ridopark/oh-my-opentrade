package timescaledb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// EarningsRepo implements ports.EarningsCalendarPort using TimescaleDB.
// Uses raw *sql.DB (not SqlDB) because UpsertBatch needs transactions.
type EarningsRepo struct {
	db  *sql.DB
	log zerolog.Logger
}

// NewEarningsRepo creates a new earnings calendar repository.
func NewEarningsRepo(db *sql.DB, log zerolog.Logger) *EarningsRepo {
	return &EarningsRepo{db: db, log: log}
}

var _ ports.EarningsCalendarPort = (*EarningsRepo)(nil)

// GetNextEarnings returns the next earnings date for a symbol.
func (r *EarningsRepo) GetNextEarnings(ctx context.Context, symbol string) (*ports.EarningsEntry, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT symbol, earnings_date, hour, quarter, year
		 FROM earnings_calendar
		 WHERE symbol = $1 AND earnings_date >= $2
		 LIMIT 1`,
		symbol, time.Now().AddDate(0, 0, -1).Format("2006-01-02"))

	var e ports.EarningsEntry
	var hour sql.NullString
	var quarter, year sql.NullInt32
	err := row.Scan(&e.Symbol, &e.EarningsDate, &hour, &quarter, &year)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("earnings_repo: get next: %w", err)
	}
	if hour.Valid {
		e.Hour = hour.String
	}
	if quarter.Valid {
		e.Quarter = int(quarter.Int32)
	}
	if year.Valid {
		e.Year = int(year.Int32)
	}
	return &e, nil
}

// UpsertEarnings stores or updates an earnings entry.
func (r *EarningsRepo) UpsertEarnings(ctx context.Context, entry ports.EarningsEntry) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO earnings_calendar (symbol, earnings_date, hour, quarter, year, fetched_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (symbol) DO UPDATE SET
		     earnings_date = EXCLUDED.earnings_date,
		     hour = EXCLUDED.hour,
		     quarter = EXCLUDED.quarter,
		     year = EXCLUDED.year,
		     fetched_at = now()`,
		entry.Symbol, entry.EarningsDate, nullString(entry.Hour),
		nullInt(entry.Quarter), nullInt(entry.Year))
	return err
}

// UpsertBatch stores or updates multiple earnings entries in a single transaction.
func (r *EarningsRepo) UpsertBatch(ctx context.Context, entries []ports.EarningsEntry) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("earnings_repo: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO earnings_calendar (symbol, earnings_date, hour, quarter, year, fetched_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (symbol) DO UPDATE SET
		     earnings_date = EXCLUDED.earnings_date,
		     hour = EXCLUDED.hour,
		     quarter = EXCLUDED.quarter,
		     year = EXCLUDED.year,
		     fetched_at = now()`)
	if err != nil {
		return fmt.Errorf("earnings_repo: prepare: %w", err)
	}
	defer stmt.Close()

	for _, e := range entries {
		_, err := stmt.ExecContext(ctx, e.Symbol, e.EarningsDate,
			nullString(e.Hour), nullInt(e.Quarter), nullInt(e.Year))
		if err != nil {
			return fmt.Errorf("earnings_repo: upsert %s: %w", e.Symbol, err)
		}
	}

	committed = true
	return tx.Commit()
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt(i int) sql.NullInt32 {
	if i == 0 {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(i), Valid: true}
}
