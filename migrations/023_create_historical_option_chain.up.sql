CREATE TABLE IF NOT EXISTS historical_option_chain (
    date        DATE             NOT NULL,
    symbol      TEXT             NOT NULL,
    expiration  DATE             NOT NULL,
    strike      DOUBLE PRECISION NOT NULL,
    call_put    TEXT             NOT NULL,
    bid         DOUBLE PRECISION,
    ask         DOUBLE PRECISION,
    iv          DOUBLE PRECISION,
    delta       DOUBLE PRECISION,
    gamma       DOUBLE PRECISION,
    theta       DOUBLE PRECISION,
    vega        DOUBLE PRECISION,
    rho         DOUBLE PRECISION
);

SELECT create_hypertable('historical_option_chain', 'date', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_hist_opt_chain_symbol_date
    ON historical_option_chain (symbol, date DESC);

CREATE INDEX IF NOT EXISTS idx_hist_opt_chain_lookup
    ON historical_option_chain (symbol, date, call_put, expiration);

CREATE UNIQUE INDEX IF NOT EXISTS idx_hist_opt_chain_unique
    ON historical_option_chain (date, symbol, expiration, strike, call_put);
