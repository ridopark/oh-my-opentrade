-- Decay telemetry: per-trade stats for rolling PF/WR computation,
-- and GIN index on trades.thesis for confluence component attribution queries.

-- Per-trade record keyed by trade_id for rolling decay metrics.
-- Rolling PF/WR are computed at query time via window functions over this
-- table, not pre-stored — avoids drift if trades are corrected or deleted.
CREATE TABLE IF NOT EXISTS strategy_trade_stats (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    trade_id        UUID NOT NULL,
    strategy        TEXT NOT NULL,
    symbol          TEXT NOT NULL,
    pnl             DOUBLE PRECISION NOT NULL,
    regime          TEXT,
    vix_bucket      TEXT,
    inserted_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_strategy_trade_stats_strategy_inserted
    ON strategy_trade_stats(strategy, inserted_at);

CREATE INDEX IF NOT EXISTS idx_strategy_trade_stats_trade_id
    ON strategy_trade_stats(trade_id);

-- GIN index on trades.thesis JSONB for confluence component attribution queries.
-- Enables efficient queries like:
--   WHERE thesis @> '{"confluence":{"components":[{"name":"dp_buy","fired":true}]}}'
CREATE INDEX IF NOT EXISTS idx_trades_thesis_gin
    ON trades USING gin (thesis jsonb_path_ops);
