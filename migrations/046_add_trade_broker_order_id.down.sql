DROP INDEX IF EXISTS idx_trades_broker_order_id;
ALTER TABLE trades DROP COLUMN IF EXISTS broker_order_id;
