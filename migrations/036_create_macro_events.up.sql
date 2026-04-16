-- Sprint 4.6: macro economic event calendar.
--
-- Populated by omo-data on a daily refresh from Finnhub economic-calendar
-- (augmented with FRED rate-release history). Consumed by macro_event_gate
-- which rejects new entries inside a configurable window around
-- high-impact events (FOMC, CPI, NFP, PCE, PPI, FOMC Minutes).

CREATE TABLE IF NOT EXISTS macro_events (
    id           TEXT        PRIMARY KEY,
    name         TEXT        NOT NULL,
    scheduled_at TIMESTAMPTZ NOT NULL,
    impact       TEXT        NOT NULL DEFAULT 'medium',
    actual       NUMERIC,
    consensus    NUMERIC,
    previous     NUMERIC,
    released     BOOLEAN     NOT NULL DEFAULT FALSE,
    fetched_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_macro_events_scheduled_at
    ON macro_events (scheduled_at);

CREATE INDEX IF NOT EXISTS idx_macro_events_impact_scheduled
    ON macro_events (impact, scheduled_at);
