-- Earnings calendar: stores next earnings date per symbol, refreshed daily
-- by omo-data via Finnhub API. Consumed by position monitor to compute
-- days_to_earnings for the IV ramp model.

CREATE TABLE IF NOT EXISTS earnings_calendar (
    symbol          TEXT NOT NULL,
    earnings_date   DATE NOT NULL,
    hour            TEXT,           -- 'bmo' (before market open), 'amc' (after market close), 'dmh' (during)
    quarter         INT,
    year            INT,
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (symbol)
);

CREATE INDEX IF NOT EXISTS idx_earnings_calendar_date
    ON earnings_calendar(earnings_date);
