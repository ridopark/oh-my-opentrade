DROP INDEX IF EXISTS idx_trades_execution_id;
ALTER TABLE trades DROP COLUMN IF EXISTS execution_id;
