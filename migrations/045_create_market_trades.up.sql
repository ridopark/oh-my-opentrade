-- 045_create_market_trades.up.sql
-- TimescaleDB hypertable for raw trade ticks consumed off the live event bus.
--
-- Phase 0 of the backtest/live parity plan. Two downstream consumers depend
-- on it:
--   - Live DP aggregation (Phase 4): replay-on-boot for the partial 5m
--     bucket so the in-memory aggregator survives mid-session restarts.
--   - omo-data audit (Phase 5): SQL diff against darkpool_bars instead of
--     refetching trades over REST.
--
-- Volume budget: 34 active equity symbols x ~6.5h RTH x ~1.7k trades/min/sym
-- ~= 3-5M rows/day, ~250 MB/day uncompressed, ~25-50 MB/day after Timescale
-- compression. 30-day retention caps the rolling window at ~1-2 GB.

CREATE TABLE IF NOT EXISTS market_trades (
    time        TIMESTAMPTZ NOT NULL,
    account_id  TEXT NOT NULL DEFAULT '',
    env_mode    TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    symbol      TEXT NOT NULL,
    price       DOUBLE PRECISION NOT NULL,
    size        DOUBLE PRECISION NOT NULL,
    exchange    TEXT NOT NULL DEFAULT '',
    conditions  TEXT[] NOT NULL DEFAULT '{}',
    tape        TEXT NOT NULL DEFAULT '',
    taker_side  TEXT NOT NULL DEFAULT '',
    venue       TEXT NOT NULL DEFAULT ''
);

-- 1-day chunks match market_bars; one chunk per trading day.
SELECT create_hypertable('market_trades', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

-- DP aggregator path: WHERE symbol = $1 AND exchange = 'D' AND time BETWEEN ...
-- The compound index covers that filter; the symbol-only index covers replay.
CREATE INDEX IF NOT EXISTS idx_market_trades_symbol_time ON market_trades (symbol, time DESC);
CREATE INDEX IF NOT EXISTS idx_market_trades_symbol_exchange_time ON market_trades (symbol, exchange, time DESC);

ALTER TABLE market_trades SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'symbol',
    timescaledb.compress_orderby = 'time DESC'
);

SELECT add_compression_policy('market_trades', INTERVAL '1 day', if_not_exists => TRUE);
SELECT add_retention_policy('market_trades', INTERVAL '30 days', if_not_exists => TRUE);
