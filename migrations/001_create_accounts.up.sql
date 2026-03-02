-- 001_create_accounts.up.sql
-- Regular PostgreSQL table (not a hypertable) for account configuration.

CREATE TABLE IF NOT EXISTS accounts (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    env_mode    TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    broker      TEXT NOT NULL DEFAULT 'alpaca',
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_accounts_env_mode ON accounts (env_mode);
