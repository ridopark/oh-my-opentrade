-- 002_create_market_bars.up.sql
-- TimescaleDB hypertable for OHLCV candle data.

CREATE TABLE IF NOT EXISTS market_bars (
    time        TIMESTAMPTZ NOT NULL,
    account_id  TEXT NOT NULL,
    env_mode    TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    symbol      TEXT NOT NULL,
    timeframe   TEXT NOT NULL CHECK (timeframe IN ('1m', '5m', '15m', '1h', '1d')),
    open        DOUBLE PRECISION NOT NULL,
    high        DOUBLE PRECISION NOT NULL,
    low         DOUBLE PRECISION NOT NULL,
    close       DOUBLE PRECISION NOT NULL,
    volume      DOUBLE PRECISION NOT NULL CHECK (volume > 0),
    suspect     BOOLEAN NOT NULL DEFAULT false,

    CONSTRAINT market_bars_high_gte_low CHECK (high >= low)
);

-- Convert to hypertable with 1-day chunk interval.
SELECT create_hypertable('market_bars', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

-- Indexes for common query patterns.
CREATE INDEX IF NOT EXISTS idx_market_bars_symbol_time ON market_bars (account_id, env_mode, symbol, timeframe, time DESC);

-- Compression policy: compress data older than 7 days.
-- segmentby ensures efficient per-tenant queries after compression.
ALTER TABLE market_bars SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'account_id, env_mode, symbol, timeframe'
);

SELECT add_compression_policy('market_bars', INTERVAL '7 days', if_not_exists => TRUE);
