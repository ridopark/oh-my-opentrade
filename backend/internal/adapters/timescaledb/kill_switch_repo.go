package timescaledb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/rs/zerolog"
)

// KillSwitchRepo persists the Sprint 4 Phase 4 kill switch state and the
// append-only audit log of transitions. Satisfies risk.KillSwitchSink.
type KillSwitchRepo struct {
	db  DBTX
	log zerolog.Logger
}

func NewKillSwitchRepo(db DBTX, log zerolog.Logger) *KillSwitchRepo {
	return &KillSwitchRepo{db: db, log: log}
}

const (
	queryInsertKillSwitchEvent = `INSERT INTO kill_switch_events (at, old_state, new_state, reason, actor)
		VALUES ($1, $2, $3, $4, $5)`

	queryUpsertKillSwitchState = `INSERT INTO kill_switch_state (singleton_key, state, reason, actor, updated_at)
		VALUES (1, $1, $2, $3, $4)
		ON CONFLICT (singleton_key) DO UPDATE SET
			state = EXCLUDED.state,
			reason = EXCLUDED.reason,
			actor = EXCLUDED.actor,
			updated_at = EXCLUDED.updated_at`

	querySelectKillSwitchState = `SELECT state, reason, actor, updated_at FROM kill_switch_state WHERE singleton_key = 1`

	querySelectLastKillSwitchEvent = `SELECT at, old_state, new_state, reason, actor FROM kill_switch_events ORDER BY at DESC, id DESC LIMIT 1`
)

// RecordTransition implements risk.KillSwitchSink. Writes both the event row
// (audit log) and the upsert into kill_switch_state (for process restarts).
func (r *KillSwitchRepo) RecordTransition(ctx context.Context, t risk.KillSwitchTransition) error {
	at := t.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if _, err := r.db.ExecContext(ctx, queryInsertKillSwitchEvent,
		at, t.OldState.String(), t.NewState.String(), t.Reason, t.Actor,
	); err != nil {
		return fmt.Errorf("kill_switch_repo: insert event: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, queryUpsertKillSwitchState,
		t.NewState.String(), t.Reason, t.Actor, at,
	); err != nil {
		return fmt.Errorf("kill_switch_repo: upsert state: %w", err)
	}
	return nil
}

// KillSwitchStateRow is returned by LoadState.
type KillSwitchStateRow struct {
	State     risk.KillSwitchState
	Reason    string
	Actor     string
	UpdatedAt time.Time
}

// LoadState reads the persisted singleton. Returns (nil, nil) when no row
// exists so callers can default to ACTIVE.
func (r *KillSwitchRepo) LoadState(ctx context.Context) (*KillSwitchStateRow, error) {
	row := r.db.QueryRowContext(ctx, querySelectKillSwitchState)
	var stateStr, reason, actor string
	var updatedAt time.Time
	if err := row.Scan(&stateStr, &reason, &actor, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("kill_switch_repo: load state: %w", err)
	}
	st, err := risk.ParseKillSwitchState(stateStr)
	if err != nil {
		return nil, fmt.Errorf("kill_switch_repo: parse state %q: %w", stateStr, err)
	}
	return &KillSwitchStateRow{State: st, Reason: reason, Actor: actor, UpdatedAt: updatedAt}, nil
}

// KillSwitchEventRow is returned by LastEvent.
type KillSwitchEventRow struct {
	At       time.Time
	OldState risk.KillSwitchState
	NewState risk.KillSwitchState
	Reason   string
	Actor    string
}

// LastEvent returns the most recent transition, or (nil, nil) when the
// audit log is empty.
func (r *KillSwitchRepo) LastEvent(ctx context.Context) (*KillSwitchEventRow, error) {
	row := r.db.QueryRowContext(ctx, querySelectLastKillSwitchEvent)
	var at time.Time
	var oldStr, newStr, reason, actor string
	if err := row.Scan(&at, &oldStr, &newStr, &reason, &actor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("kill_switch_repo: last event: %w", err)
	}
	oldSt, err := risk.ParseKillSwitchState(oldStr)
	if err != nil {
		return nil, err
	}
	newSt, err := risk.ParseKillSwitchState(newStr)
	if err != nil {
		return nil, err
	}
	return &KillSwitchEventRow{At: at, OldState: oldSt, NewState: newSt, Reason: reason, Actor: actor}, nil
}
