CREATE TABLE IF NOT EXISTS darkpool_bars (
    time               TIMESTAMPTZ      NOT NULL,
    symbol             TEXT             NOT NULL,
    timeframe          TEXT             NOT NULL DEFAULT '5m',
    dp_volume          DOUBLE PRECISION NOT NULL DEFAULT 0,
    dp_trades          INTEGER          NOT NULL DEFAULT 0,
    dp_vwap            DOUBLE PRECISION NOT NULL DEFAULT 0,
    lit_volume         DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_volume       DOUBLE PRECISION NOT NULL DEFAULT 0,
    dp_ratio           DOUBLE PRECISION NOT NULL DEFAULT 0,
    buy_volume         DOUBLE PRECISION NOT NULL DEFAULT 0,
    sell_volume        DOUBLE PRECISION NOT NULL DEFAULT 0,
    large_print_volume DOUBLE PRECISION NOT NULL DEFAULT 0,
    large_print_count  INTEGER          NOT NULL DEFAULT 0,
    max_print_size     DOUBLE PRECISION NOT NULL DEFAULT 0
);

SELECT create_hypertable('darkpool_bars', 'time',
    chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);

CREATE UNIQUE INDEX IF NOT EXISTS idx_darkpool_bars_unique
    ON darkpool_bars (symbol, timeframe, time);

CREATE INDEX IF NOT EXISTS idx_darkpool_bars_lookup
    ON darkpool_bars (symbol, timeframe, time DESC);

ALTER TABLE darkpool_bars SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'symbol, timeframe'
);
SELECT add_compression_policy('darkpool_bars', INTERVAL '7 days', if_not_exists => TRUE);
