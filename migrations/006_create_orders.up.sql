-- 006_create_orders.up.sql
-- Tracks submitted broker orders and their fill status.
-- This is a regular (non-hypertable) table since order volume is low.

CREATE TABLE IF NOT EXISTS orders (
    time            TIMESTAMPTZ     NOT NULL DEFAULT now(),
    account_id      TEXT            NOT NULL,
    env_mode        TEXT            NOT NULL,
    intent_id       UUID            NOT NULL,
    broker_order_id TEXT            NOT NULL,
    symbol          TEXT            NOT NULL,
    side            TEXT            NOT NULL,
    quantity        DOUBLE PRECISION NOT NULL,
    limit_price     DOUBLE PRECISION NOT NULL,
    stop_loss       DOUBLE PRECISION NOT NULL,
    status          TEXT            NOT NULL DEFAULT 'submitted',
    filled_at       TIMESTAMPTZ,
    filled_price    DOUBLE PRECISION,
    filled_qty      DOUBLE PRECISION,
    PRIMARY KEY (broker_order_id)
);

CREATE INDEX IF NOT EXISTS orders_account_env_time
    ON orders (account_id, env_mode, time DESC);
