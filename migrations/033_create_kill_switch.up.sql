-- Sprint 4 Phase 4: 3-state kill switch audit + persisted current state.
--
-- kill_switch_events is an append-only audit log of every state transition
-- (ACTIVE/REDUCING/HALTED). kill_switch_state holds the singleton current
-- state so process restarts can restore the operator's last intent instead
-- of defaulting back to ACTIVE, which would silently reopen entries after
-- a crash mid-halt.

CREATE TABLE IF NOT EXISTS kill_switch_events (
    id BIGSERIAL PRIMARY KEY,
    at TIMESTAMPTZ NOT NULL DEFAULT now(),
    old_state TEXT NOT NULL,
    new_state TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    actor TEXT NOT NULL DEFAULT 'system'
);

CREATE INDEX IF NOT EXISTS idx_kill_switch_events_at ON kill_switch_events (at DESC);

CREATE TABLE IF NOT EXISTS kill_switch_state (
    singleton_key INTEGER PRIMARY KEY DEFAULT 1 CHECK (singleton_key = 1),
    state TEXT NOT NULL DEFAULT 'ACTIVE',
    reason TEXT NOT NULL DEFAULT '',
    actor TEXT NOT NULL DEFAULT 'system',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO kill_switch_state (singleton_key, state) VALUES (1, 'ACTIVE')
ON CONFLICT (singleton_key) DO NOTHING;
