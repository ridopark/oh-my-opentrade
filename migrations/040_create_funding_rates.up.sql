CREATE TABLE IF NOT EXISTS funding_rates (
    venue          TEXT NOT NULL,
    symbol         TEXT NOT NULL,
    timestamp      TIMESTAMPTZ NOT NULL,
    rate           DOUBLE PRECISION NOT NULL,
    interval_hours INTEGER NOT NULL DEFAULT 8,
    mark_price     DOUBLE PRECISION,
    PRIMARY KEY (venue, symbol, timestamp)
);
SELECT create_hypertable('funding_rates', 'timestamp', if_not_exists => TRUE);
CREATE INDEX IF NOT EXISTS idx_funding_rates_venue_symbol ON funding_rates (venue, symbol, timestamp DESC);
