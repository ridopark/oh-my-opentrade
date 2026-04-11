-- Persistent write-ahead journal for order intents.
-- Rows are written BEFORE broker.SubmitOrder so that a crash mid-submit
-- leaves a durable audit trail. Startup reconciliation cross-references
-- this table against broker open orders to decide whether to resume,
-- alert on unmanaged orders, or mark lost intents.
--
-- See tmp/others/SPRINT_2_PLAN.md, gap #2 and #4 in ROBUSTNESS.md.

CREATE TABLE IF NOT EXISTS order_intents (
    id                UUID PRIMARY KEY,
    idempotency_key   TEXT NOT NULL,
    tenant_id         TEXT NOT NULL,
    env_mode          TEXT NOT NULL,
    symbol            TEXT NOT NULL,
    direction         TEXT NOT NULL,
    asset_class       TEXT NOT NULL,
    order_type        TEXT NOT NULL,
    time_in_force     TEXT NOT NULL,
    quantity          DOUBLE PRECISION NOT NULL,
    limit_price       DOUBLE PRECISION,
    stop_loss         DOUBLE PRECISION,
    max_slippage_bps  INTEGER,
    strategy          TEXT,
    confidence        DOUBLE PRECISION,
    max_loss_usd      DOUBLE PRECISION,

    -- Options metadata (null for equity)
    instrument_kind   TEXT,
    instrument_json   JSONB,

    -- Lifecycle state: pending_submit | submitted | filled | canceled | rejected | expired | lost
    status            TEXT NOT NULL,
    broker_order_id   TEXT,
    submit_error      TEXT,
    filled_qty        DOUBLE PRECISION NOT NULL DEFAULT 0,
    filled_avg_price  DOUBLE PRECISION NOT NULL DEFAULT 0,

    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    submitted_at      TIMESTAMPTZ,
    terminal_at       TIMESTAMPTZ,

    -- Free-form metadata for rationale, gate decisions, etc.
    meta              JSONB
);

CREATE INDEX IF NOT EXISTS idx_order_intents_status_created
    ON order_intents(status, created_at);

CREATE INDEX IF NOT EXISTS idx_order_intents_broker_order_id
    ON order_intents(broker_order_id)
    WHERE broker_order_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_order_intents_idempotency
    ON order_intents(tenant_id, env_mode, idempotency_key);
