-- 008_create_pnl.up.sql
-- Daily P&L summary table and equity curve hypertable for trade ledger tracking.

-- Daily P&L summary: one row per tenant per day.
CREATE TABLE IF NOT EXISTS daily_pnl (
    date           DATE NOT NULL,
    account_id     TEXT NOT NULL,
    env_mode       TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    realized_pnl   DOUBLE PRECISION NOT NULL DEFAULT 0,
    unrealized_pnl DOUBLE PRECISION NOT NULL DEFAULT 0,
    trade_count    INTEGER NOT NULL DEFAULT 0,
    max_drawdown   DOUBLE PRECISION NOT NULL DEFAULT 0,

    CONSTRAINT daily_pnl_pk PRIMARY KEY (account_id, env_mode, date)
);

CREATE INDEX IF NOT EXISTS idx_daily_pnl_date ON daily_pnl (date DESC);

-- Equity curve: high-frequency snapshots updated on each fill.
CREATE TABLE IF NOT EXISTS equity_curve (
    time       TIMESTAMPTZ NOT NULL,
    account_id TEXT NOT NULL,
    env_mode   TEXT NOT NULL CHECK (env_mode IN ('Paper', 'Live')),
    equity     DOUBLE PRECISION NOT NULL,
    cash       DOUBLE PRECISION NOT NULL DEFAULT 0,
    drawdown   DOUBLE PRECISION NOT NULL DEFAULT 0
);

SELECT create_hypertable('equity_curve', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_equity_curve_account ON equity_curve (account_id, env_mode, time DESC);

ALTER TABLE equity_curve SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'account_id, env_mode'
);

SELECT add_compression_policy('equity_curve', INTERVAL '7 days', if_not_exists => TRUE);
