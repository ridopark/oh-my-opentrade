ALTER TABLE trades ADD COLUMN IF NOT EXISTS execution_id TEXT DEFAULT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_trades_execution_id ON trades (execution_id, time) WHERE execution_id IS NOT NULL;
