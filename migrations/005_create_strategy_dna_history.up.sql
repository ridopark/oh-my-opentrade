-- 005_create_strategy_dna_history.up.sql
-- TimescaleDB hypertable for strategy parameter evolution tracking.

CREATE TABLE IF NOT EXISTS strategy_dna_history (
    time            TIMESTAMPTZ NOT NULL,
    account_id      TEXT NOT NULL,
    env_mode        TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    strategy_id     UUID NOT NULL,
    version         INTEGER NOT NULL CHECK (version > 0),
    parameters      JSONB NOT NULL DEFAULT '{}',
    performance     JSONB NOT NULL DEFAULT '{}',

    CONSTRAINT strategy_dna_unique_version UNIQUE (strategy_id, version, time)
);

SELECT create_hypertable('strategy_dna_history', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_strategy_dna_account ON strategy_dna_history (account_id, env_mode, strategy_id, time DESC);

ALTER TABLE strategy_dna_history SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'account_id, env_mode, strategy_id'
);

SELECT add_compression_policy('strategy_dna_history', INTERVAL '7 days', if_not_exists => TRUE);
