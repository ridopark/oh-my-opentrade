package timescaledb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// UniverseHistoryRepo persists per-symbol tradable windows. Backs the
// Sprint-7 survivorship-bias filter.
type UniverseHistoryRepo struct {
	db  DBTX
	log zerolog.Logger
}

// NewUniverseHistoryRepo wires the repo to a DBTX so it can be exercised
// with an in-memory mock in unit tests.
func NewUniverseHistoryRepo(db DBTX, log zerolog.Logger) *UniverseHistoryRepo {
	return &UniverseHistoryRepo{
		db:  db,
		log: log.With().Str("component", "universe_history_repo").Logger(),
	}
}

var _ ports.UniverseHistoryPort = (*UniverseHistoryRepo)(nil)

const (
	queryUpsertUniverseHistory = `INSERT INTO universe_history
		(symbol, from_date, to_date, source, note, created_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (symbol, from_date) DO UPDATE SET
			to_date = EXCLUDED.to_date,
			source  = EXCLUDED.source,
			note    = EXCLUDED.note`

	querySelectUniverseWindows = `SELECT symbol, from_date, to_date, source, COALESCE(note, '')
		FROM universe_history
		WHERE symbol = $1
		ORDER BY from_date ASC`

	queryUniverseWasTradable = `SELECT EXISTS (
		SELECT 1 FROM universe_history
		WHERE symbol = $1
		  AND from_date <= $2::date
		  AND (to_date IS NULL OR to_date > $2::date)
	)`

	queryUniverseActiveSymbols = `SELECT DISTINCT symbol FROM universe_history
		WHERE from_date <= $1::date
		  AND (to_date IS NULL OR to_date > $1::date)
		ORDER BY symbol`
)

// WasTradable returns true iff `at` falls inside a tradable window.
func (r *UniverseHistoryRepo) WasTradable(ctx context.Context, sym domain.Symbol, at time.Time) (bool, error) {
	var exists bool
	if err := r.db.QueryRowContext(ctx, queryUniverseWasTradable, string(sym), at.UTC()).Scan(&exists); err != nil {
		return false, fmt.Errorf("universe_history_repo: was_tradable: %w", err)
	}
	return exists, nil
}

// WindowsFor returns all windows for a symbol, ordered by FromDate asc.
func (r *UniverseHistoryRepo) WindowsFor(ctx context.Context, sym domain.Symbol) ([]ports.UniverseWindow, error) {
	rows, err := r.db.QueryContext(ctx, querySelectUniverseWindows, string(sym))
	if err != nil {
		return nil, fmt.Errorf("universe_history_repo: windows_for: %w", err)
	}
	defer rows.Close()
	var out []ports.UniverseWindow
	for rows.Next() {
		var (
			w      ports.UniverseWindow
			symbol string
			to     sql.NullTime
		)
		if err := rows.Scan(&symbol, &w.FromDate, &to, &w.Source, &w.Note); err != nil {
			return nil, fmt.Errorf("universe_history_repo: scan: %w", err)
		}
		w.Symbol = domain.Symbol(symbol)
		if to.Valid {
			t := to.Time
			w.ToDate = &t
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("universe_history_repo: rows: %w", err)
	}
	return out, nil
}

// Upsert inserts or refreshes a window keyed by (symbol, from_date).
func (r *UniverseHistoryRepo) Upsert(ctx context.Context, w ports.UniverseWindow) error {
	if w.Symbol == "" {
		return fmt.Errorf("universe_history_repo: upsert: empty symbol")
	}
	if w.Source == "" {
		return fmt.Errorf("universe_history_repo: upsert: empty source")
	}
	var toDate sql.NullTime
	if w.ToDate != nil {
		toDate = sql.NullTime{Time: *w.ToDate, Valid: true}
	}
	var note sql.NullString
	if w.Note != "" {
		note = sql.NullString{String: w.Note, Valid: true}
	}
	if _, err := r.db.ExecContext(ctx, queryUpsertUniverseHistory,
		string(w.Symbol), w.FromDate.UTC(), toDate, w.Source, note,
	); err != nil {
		return fmt.Errorf("universe_history_repo: upsert %s: %w", w.Symbol, err)
	}
	return nil
}

// ActiveSymbols returns the set of symbols tradable at `at`.
func (r *UniverseHistoryRepo) ActiveSymbols(ctx context.Context, at time.Time) ([]domain.Symbol, error) {
	rows, err := r.db.QueryContext(ctx, queryUniverseActiveSymbols, at.UTC())
	if err != nil {
		return nil, fmt.Errorf("universe_history_repo: active_symbols: %w", err)
	}
	defer rows.Close()
	var out []domain.Symbol
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("universe_history_repo: scan: %w", err)
		}
		out = append(out, domain.Symbol(s))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("universe_history_repo: rows: %w", err)
	}
	return out, nil
}
