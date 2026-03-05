-- 011_add_order_strategy.down.sql
ALTER TABLE orders DROP COLUMN IF EXISTS strategy;
ALTER TABLE orders DROP COLUMN IF EXISTS rationale;
ALTER TABLE orders DROP COLUMN IF EXISTS confidence;
