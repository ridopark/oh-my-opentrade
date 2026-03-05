-- 012_create_strategy_perf.up.sql
-- Per-strategy performance tables: daily P&L, equity curve, and signal events.
-- Backward compatible: existing daily_pnl and equity_curve tables remain untouched.

-- Per-strategy daily P&L: one row per strategy per day.
CREATE TABLE IF NOT EXISTS strategy_daily_pnl (
    date           DATE NOT NULL,
    account_id     TEXT NOT NULL,
    env_mode       TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    strategy       TEXT NOT NULL,
    realized_pnl   DOUBLE PRECISION NOT NULL DEFAULT 0,
    fees           DOUBLE PRECISION NOT NULL DEFAULT 0,
    trade_count    INTEGER NOT NULL DEFAULT 0,
    win_count      INTEGER NOT NULL DEFAULT 0,
    loss_count     INTEGER NOT NULL DEFAULT 0,
    gross_profit   DOUBLE PRECISION NOT NULL DEFAULT 0,
    gross_loss     DOUBLE PRECISION NOT NULL DEFAULT 0,

    CONSTRAINT strategy_daily_pnl_pk PRIMARY KEY (account_id, env_mode, strategy, date)
);

CREATE INDEX IF NOT EXISTS idx_strategy_daily_pnl_date ON strategy_daily_pnl (date DESC);
CREATE INDEX IF NOT EXISTS idx_strategy_daily_pnl_strategy ON strategy_daily_pnl (strategy, date DESC);

-- Per-strategy equity curve: time-series snapshots updated on each fill.
CREATE TABLE IF NOT EXISTS strategy_equity_points (
    time                  TIMESTAMPTZ NOT NULL,
    account_id            TEXT NOT NULL,
    env_mode              TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    strategy              TEXT NOT NULL,
    equity                DOUBLE PRECISION NOT NULL,
    realized_pnl_to_date  DOUBLE PRECISION NOT NULL DEFAULT 0,
    fees_to_date          DOUBLE PRECISION NOT NULL DEFAULT 0,
    trade_count_to_date   INTEGER NOT NULL DEFAULT 0
);

SELECT create_hypertable('strategy_equity_points', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_strategy_equity_points_lookup
    ON strategy_equity_points (account_id, env_mode, strategy, time DESC);

ALTER TABLE strategy_equity_points SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'account_id, env_mode, strategy'
);

SELECT add_compression_policy('strategy_equity_points', INTERVAL '7 days', if_not_exists => TRUE);

-- Strategy signal events: append-only time-series for signal lifecycle tracking.
CREATE TABLE IF NOT EXISTS strategy_signal_events (
    ts          TIMESTAMPTZ NOT NULL,
    account_id  TEXT NOT NULL,
    env_mode    TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    strategy    TEXT NOT NULL,
    signal_id   TEXT NOT NULL,
    symbol      TEXT NOT NULL,
    kind        TEXT NOT NULL, -- entry, exit, scale_in, scale_out
    side        TEXT NOT NULL, -- BUY, SELL
    status      TEXT NOT NULL, -- generated, validated, executed, suppressed, rejected, debate_override
    reason      TEXT NOT NULL DEFAULT '',
    confidence  DOUBLE PRECISION NOT NULL DEFAULT 0,
    payload     JSONB
);

SELECT create_hypertable('strategy_signal_events', 'ts', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_strategy_signal_events_lookup
    ON strategy_signal_events (account_id, env_mode, strategy, ts DESC);
CREATE INDEX IF NOT EXISTS idx_strategy_signal_events_symbol
    ON strategy_signal_events (symbol, ts DESC);
CREATE INDEX IF NOT EXISTS idx_strategy_signal_events_signal_id
    ON strategy_signal_events (signal_id, ts DESC);

ALTER TABLE strategy_signal_events SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'account_id, env_mode, strategy'
);

SELECT add_compression_policy('strategy_signal_events', INTERVAL '7 days', if_not_exists => TRUE);
