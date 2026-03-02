-- 004_create_thought_logs.up.sql
-- TimescaleDB hypertable for AI reasoning logs (debate transcripts, bull/bear arguments).

CREATE TABLE IF NOT EXISTS thought_logs (
    time            TIMESTAMPTZ NOT NULL,
    account_id      TEXT NOT NULL,
    env_mode        TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    symbol          TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    direction       TEXT,
    confidence      DOUBLE PRECISION CHECK (confidence >= 0 AND confidence <= 1),
    bull_argument   TEXT,
    bear_argument   TEXT,
    judge_reasoning TEXT,
    rationale       TEXT,
    payload         JSONB
);

SELECT create_hypertable('thought_logs', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_thought_logs_account ON thought_logs (account_id, env_mode, time DESC);

ALTER TABLE thought_logs SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'account_id, env_mode'
);

SELECT add_compression_policy('thought_logs', INTERVAL '7 days', if_not_exists => TRUE);
