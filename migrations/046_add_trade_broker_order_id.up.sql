-- Add broker_order_id to trades so the boot fill reconciler can dedup
-- against live writes whose execution_id is empty (fastPollPosition path).
-- Without this column, reconcileFillsOnBoot reinserts every live fill as
-- a duplicate Path A row at the next process restart.

ALTER TABLE trades ADD COLUMN IF NOT EXISTS broker_order_id TEXT DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_trades_broker_order_id
    ON trades (broker_order_id, time DESC)
    WHERE broker_order_id IS NOT NULL;
