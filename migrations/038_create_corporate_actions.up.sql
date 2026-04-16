-- Sprint 6.3: corporate actions table.
--
-- Captures stock splits, reverse splits, dividends, delistings, and mergers
-- so historical option chain loaders can retroactively adjust strikes and
-- filter post-delisting rows. Adjustments follow OCC Rule 809 for standard
-- forward/reverse splits: strike is divided by the split ratio, quantity is
-- multiplied, and the contract multiplier stays at 100 (pre-split chains
-- viewed from after a 4-for-1 split see strikes/4).
--
-- Populated by Alpaca's /v2/corporate_actions endpoint (equities) and IBKR's
-- corporate-action subscription (not yet implemented — see ibkr adapter
-- stub). Manual entries are allowed via source='manual'.

CREATE TABLE IF NOT EXISTS corporate_actions (
    id               SERIAL PRIMARY KEY,
    symbol           TEXT NOT NULL,
    action_type      TEXT NOT NULL, -- 'split' | 'reverse_split' | 'dividend' | 'delisting' | 'merger'
    effective_date   DATE NOT NULL,
    ratio_numerator  NUMERIC NOT NULL DEFAULT 1,
    ratio_denominator NUMERIC NOT NULL DEFAULT 1,
    cash_component   NUMERIC DEFAULT 0,
    source           TEXT NOT NULL, -- 'ibkr' | 'alpaca' | 'manual'
    note             TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (symbol, effective_date, action_type)
);

CREATE INDEX IF NOT EXISTS idx_corp_actions_symbol_date
    ON corporate_actions (symbol, effective_date);
