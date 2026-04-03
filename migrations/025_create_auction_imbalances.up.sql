CREATE TABLE IF NOT EXISTS auction_imbalances (
    time        TIMESTAMPTZ      NOT NULL,
    symbol      TEXT             NOT NULL,
    volume      DOUBLE PRECISION NOT NULL DEFAULT 0,
    price       DOUBLE PRECISION NOT NULL DEFAULT 0,
    imbalance   DOUBLE PRECISION NOT NULL DEFAULT 0
);

SELECT create_hypertable('auction_imbalances', 'time', if_not_exists => TRUE);

-- Index for symbol+time queries (backtesting)
CREATE INDEX IF NOT EXISTS idx_auction_imbalances_symbol_time
    ON auction_imbalances (symbol, time DESC);
