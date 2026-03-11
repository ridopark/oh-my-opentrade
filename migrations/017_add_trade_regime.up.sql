ALTER TABLE trades ADD COLUMN IF NOT EXISTS regime TEXT DEFAULT NULL;
CREATE INDEX IF NOT EXISTS idx_trades_regime ON trades (strategy, regime) WHERE regime IS NOT NULL;
