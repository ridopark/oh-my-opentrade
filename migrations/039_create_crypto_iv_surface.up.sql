-- Crypto options IV surface snapshots. Stores periodic IV surface metrics
-- from derivatives venues (Deribit) used by the skew-regime classifier to
-- gate carry-trade exposure. Separate from the equity iv_snapshots table
-- (migration 021) which tracks per-symbol ATM IV.
CREATE TABLE IF NOT EXISTS crypto_iv_surface (
    asset       TEXT NOT NULL,
    timestamp   TIMESTAMPTZ NOT NULL,
    atm_iv_7d   DOUBLE PRECISION,
    atm_iv_30d  DOUBLE PRECISION,
    rr_25d_7d   DOUBLE PRECISION,
    rr_25d_30d  DOUBLE PRECISION,
    bf_25d_7d   DOUBLE PRECISION,
    bf_25d_30d  DOUBLE PRECISION,
    term_slope  DOUBLE PRECISION,
    put_skew_7d DOUBLE PRECISION,
    PRIMARY KEY (asset, timestamp)
);

SELECT create_hypertable('crypto_iv_surface', 'timestamp', if_not_exists => TRUE);
