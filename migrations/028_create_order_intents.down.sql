DROP INDEX IF EXISTS uq_order_intents_idempotency;
DROP INDEX IF EXISTS idx_order_intents_broker_order_id;
DROP INDEX IF EXISTS idx_order_intents_status_created;
DROP TABLE IF EXISTS order_intents;
