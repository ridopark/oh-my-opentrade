DROP INDEX IF EXISTS idx_trades_venue;
DROP INDEX IF EXISTS idx_order_intents_venue;

ALTER TABLE trades         DROP COLUMN IF EXISTS venue;
ALTER TABLE order_intents  DROP COLUMN IF EXISTS venue;
