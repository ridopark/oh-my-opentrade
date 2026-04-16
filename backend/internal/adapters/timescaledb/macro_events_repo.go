package timescaledb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// MacroEventsRepo persists scheduled macro releases and exposes the
// read paths used by the macro_event_gate. Backs
// ports.MacroCalendarPort.
type MacroEventsRepo struct {
	db  *sql.DB
	log zerolog.Logger
}

// NewMacroEventsRepo wires the repo to a raw *sql.DB so UpsertBatch can
// run in a transaction (matching EarningsRepo).
func NewMacroEventsRepo(db *sql.DB, log zerolog.Logger) *MacroEventsRepo {
	return &MacroEventsRepo{db: db, log: log}
}

var _ ports.MacroCalendarPort = (*MacroEventsRepo)(nil)

const (
	queryInsertMacroEvent = `INSERT INTO macro_events
		(id, name, scheduled_at, impact, actual, consensus, previous, released, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			scheduled_at = EXCLUDED.scheduled_at,
			impact = EXCLUDED.impact,
			actual = EXCLUDED.actual,
			consensus = EXCLUDED.consensus,
			previous = EXCLUDED.previous,
			released = EXCLUDED.released,
			fetched_at = now()`

	querySelectMacroEventsRange = `SELECT id, name, scheduled_at, impact, actual, consensus, previous, released
		FROM macro_events
		WHERE scheduled_at >= $1 AND scheduled_at < $2
		ORDER BY scheduled_at ASC`
)

// UpsertBatch writes the given events in a single transaction.
func (r *MacroEventsRepo) UpsertBatch(ctx context.Context, events []ports.MacroEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("macro_events_repo: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.PrepareContext(ctx, queryInsertMacroEvent)
	if err != nil {
		return fmt.Errorf("macro_events_repo: prepare: %w", err)
	}
	defer stmt.Close()
	for _, e := range events {
		if _, err := stmt.ExecContext(ctx,
			e.ID, e.Name, e.ScheduledAt, nullStringLower(e.Impact),
			nullFloat(e.Actual), nullFloat(e.Consensus), nullFloat(e.Previous), e.Released,
		); err != nil {
			return fmt.Errorf("macro_events_repo: upsert %s: %w", e.ID, err)
		}
	}
	committed = true
	return tx.Commit()
}

// UpcomingEvents returns events scheduled in [from, from+windowHours).
func (r *MacroEventsRepo) UpcomingEvents(ctx context.Context, from time.Time, windowHours int) ([]ports.MacroEvent, error) {
	if windowHours <= 0 {
		return nil, nil
	}
	to := from.Add(time.Duration(windowHours) * time.Hour)
	return r.queryRange(ctx, from, to)
}

// EventsInWindow returns events scheduled inside a symmetric ± window
// around `around`, in minutes.
func (r *MacroEventsRepo) EventsInWindow(ctx context.Context, around time.Time, windowMinutes int) ([]ports.MacroEvent, error) {
	if windowMinutes <= 0 {
		return nil, nil
	}
	half := time.Duration(windowMinutes) * time.Minute
	return r.queryRange(ctx, around.Add(-half), around.Add(half))
}

func (r *MacroEventsRepo) queryRange(ctx context.Context, from, to time.Time) ([]ports.MacroEvent, error) {
	rows, err := r.db.QueryContext(ctx, querySelectMacroEventsRange, from, to)
	if err != nil {
		return nil, fmt.Errorf("macro_events_repo: query: %w", err)
	}
	defer rows.Close()
	var out []ports.MacroEvent
	for rows.Next() {
		var e ports.MacroEvent
		var impact sql.NullString
		var actual, consensus, previous sql.NullFloat64
		if err := rows.Scan(&e.ID, &e.Name, &e.ScheduledAt, &impact, &actual, &consensus, &previous, &e.Released); err != nil {
			return nil, fmt.Errorf("macro_events_repo: scan: %w", err)
		}
		if impact.Valid {
			e.Impact = impact.String
		}
		if actual.Valid {
			v := actual.Float64
			e.Actual = &v
		}
		if consensus.Valid {
			v := consensus.Float64
			e.Consensus = &v
		}
		if previous.Valid {
			v := previous.Float64
			e.Previous = &v
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("macro_events_repo: rows: %w", err)
	}
	return out, nil
}

func nullFloat(v *float64) sql.NullFloat64 {
	if v == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *v, Valid: true}
}

func nullStringLower(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
