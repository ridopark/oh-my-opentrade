-- 011_add_order_strategy.up.sql
-- Add strategy, rationale, and confidence columns to orders table
-- so historical order intents carry their originating strategy and AI reasoning.

ALTER TABLE orders ADD COLUMN IF NOT EXISTS strategy   TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS rationale  TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS confidence DOUBLE PRECISION;
