-- Sprint 7 add-on: survivorship-bias filter.
--
-- universe_history records when each symbol was tradable. Windows are
-- [from_date, to_date); a NULL to_date means "still tradable today". A
-- symbol with multiple non-overlapping rows models delist-then-relist
-- scenarios (e.g., pre-IPO → IPO → delist → relisted under same ticker).
--
-- Populated from IBKR/Polygon listing metadata by omo-data (future work)
-- or manually via scripts. This migration embeds a conservative seed for
-- the 34 symbols in the active universe, assuming each has been tradable
-- since 2020-01-01. Most actually predate that date; a few (AFRM, COIN,
-- HIMS, HOOD, RBLX, RIVN) IPO'd later but we use the conservative
-- "tradable since 2020" floor — the backtest date range will naturally
-- clip to the symbol's actual data availability in market_bars.

CREATE TABLE IF NOT EXISTS universe_history (
    symbol     TEXT        NOT NULL,
    from_date  DATE        NOT NULL,
    to_date    DATE,                              -- NULL = still tradable
    source     TEXT        NOT NULL,              -- 'ibkr' | 'polygon' | 'manual' | 'seed'
    note       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (symbol, from_date)
);

CREATE INDEX IF NOT EXISTS idx_universe_history_symbol
    ON universe_history (symbol);

CREATE INDEX IF NOT EXISTS idx_universe_history_active
    ON universe_history (symbol) WHERE to_date IS NULL;

-- Seed: 34 active universe symbols, conservative from_date 2020-01-01.
INSERT INTO universe_history (symbol, from_date, to_date, source, note) VALUES
    ('AAPL',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('AFRM',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('AMD',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('AMZN',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('AVGO',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('BA',    DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('COIN',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('CRM',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('GOOGL', DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('HIMS',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('HOOD',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('IWM',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('JPM',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('LLY',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('META',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('MRNA',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('MRVL',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('MSFT',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('MU',    DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('NET',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('NFLX',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('NVDA',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('OXY',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('PLTR',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('QQQ',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('RBLX',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('RIVN',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('SMCI',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('SNOW',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('SOFI',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('SOXL',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('SPY',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('TSLA',  DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed'),
    ('XOM',   DATE '2020-01-01', NULL, 'seed', 'initial active-universe seed')
ON CONFLICT (symbol, from_date) DO NOTHING;
