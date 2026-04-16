-- Sprint 4.5: wash-sale journal (IRS §1091 observational record).
--
-- When a realized loss closes a position, the WashSaleJournal scans ±30
-- days of buys on the same symbol and writes one row per match. The
-- journal is purely observational — it is NOT used to block orders.
-- Operators and accountants consume it for year-end reconciliation.

CREATE TABLE IF NOT EXISTS wash_sales (
    id                 SERIAL PRIMARY KEY,
    symbol             TEXT NOT NULL,
    loss_trade_id      TEXT NOT NULL,
    loss_realized_at   TIMESTAMPTZ NOT NULL,
    loss_amount        NUMERIC NOT NULL,
    disallowed_amount  NUMERIC NOT NULL,
    triggering_buy_id  TEXT NOT NULL,
    triggering_buy_at  TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_wash_sales_symbol ON wash_sales (symbol, loss_realized_at DESC);
CREATE INDEX IF NOT EXISTS idx_wash_sales_loss_trade ON wash_sales (loss_trade_id);
