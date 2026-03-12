CREATE TABLE IF NOT EXISTS iv_snapshots (
    time        TIMESTAMPTZ NOT NULL,
    symbol      TEXT        NOT NULL,
    atm_iv      DOUBLE PRECISION NOT NULL,
    atm_strike  DOUBLE PRECISION,
    spot_price  DOUBLE PRECISION,
    call_iv     DOUBLE PRECISION,
    put_iv      DOUBLE PRECISION
);

SELECT create_hypertable('iv_snapshots', 'time', if_not_exists => TRUE);

CREATE UNIQUE INDEX IF NOT EXISTS idx_iv_snapshots_symbol_time
    ON iv_snapshots (symbol, time DESC);
