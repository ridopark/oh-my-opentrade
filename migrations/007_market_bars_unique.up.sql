-- Add unique constraint on (symbol, timeframe, time) so that upserts are
-- idempotent. The existing idx_market_bars_symbol_time index is non-unique
-- and kept for query performance; this new unique index enforces deduplication.
CREATE UNIQUE INDEX IF NOT EXISTS idx_market_bars_unique
    ON market_bars (symbol, timeframe, time);
