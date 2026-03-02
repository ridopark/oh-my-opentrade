-- 003_create_trades.up.sql
-- TimescaleDB hypertable for executed trades.

CREATE TABLE IF NOT EXISTS trades (
    time        TIMESTAMPTZ NOT NULL,
    account_id  TEXT NOT NULL,
    env_mode    TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    trade_id    UUID NOT NULL,
    symbol      TEXT NOT NULL,
    side        TEXT NOT NULL CHECK (side IN ('BUY', 'SELL')),
    quantity    DOUBLE PRECISION NOT NULL CHECK (quantity >= 0),
    price       DOUBLE PRECISION NOT NULL,
    commission  DOUBLE PRECISION NOT NULL DEFAULT 0,
    status      TEXT NOT NULL CHECK (status IN ('PENDING', 'FILLED', 'PARTIALLY_FILLED', 'CANCELLED', 'REJECTED')),
    strategy    TEXT,
    rationale   TEXT,

    CONSTRAINT trades_unique_id UNIQUE (trade_id, time)
);

SELECT create_hypertable('trades', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_trades_account_symbol ON trades (account_id, env_mode, symbol, time DESC);

ALTER TABLE trades SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'account_id, env_mode, symbol'
);

SELECT add_compression_policy('trades', INTERVAL '7 days', if_not_exists => TRUE);
