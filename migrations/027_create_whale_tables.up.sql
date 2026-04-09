-- Raw 13F holdings per filer per quarter
CREATE TABLE IF NOT EXISTS whale_filings (
    filing_date   TIMESTAMPTZ      NOT NULL,
    filer_cik     TEXT             NOT NULL,
    filer_name    TEXT             NOT NULL DEFAULT '',
    cusip         TEXT             NOT NULL,
    ticker        TEXT             NOT NULL DEFAULT '',
    issuer_name   TEXT             NOT NULL DEFAULT '',
    share_count   BIGINT           NOT NULL DEFAULT 0,
    market_value  BIGINT           NOT NULL DEFAULT 0,
    put_call      TEXT             NOT NULL DEFAULT '',
    filer_tier    SMALLINT         NOT NULL DEFAULT 2
);

SELECT create_hypertable('whale_filings', 'filing_date',
    chunk_time_interval => INTERVAL '90 days', if_not_exists => TRUE);

CREATE UNIQUE INDEX IF NOT EXISTS idx_whale_filings_pk
    ON whale_filings (filer_cik, filing_date, cusip, put_call);

CREATE INDEX IF NOT EXISTS idx_whale_filings_ticker
    ON whale_filings (ticker, filing_date DESC);

ALTER TABLE whale_filings SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'filer_cik'
);
SELECT add_compression_policy('whale_filings', INTERVAL '180 days', if_not_exists => TRUE);

-- Pre-computed per-ticker accumulation scores
CREATE TABLE IF NOT EXISTS whale_accumulation (
    quarter_end    TIMESTAMPTZ      NOT NULL,
    ticker         TEXT             NOT NULL,
    score          INTEGER          NOT NULL DEFAULT 0,
    new_positions  INTEGER          NOT NULL DEFAULT 0,
    additions_50   INTEGER          NOT NULL DEFAULT 0,
    additions_25   INTEGER          NOT NULL DEFAULT 0,
    reductions     INTEGER          NOT NULL DEFAULT 0,
    total_filers   INTEGER          NOT NULL DEFAULT 0,
    top_filer_json JSONB
);

SELECT create_hypertable('whale_accumulation', 'quarter_end',
    chunk_time_interval => INTERVAL '90 days', if_not_exists => TRUE);

CREATE UNIQUE INDEX IF NOT EXISTS idx_whale_accumulation_pk
    ON whale_accumulation (ticker, quarter_end);

CREATE INDEX IF NOT EXISTS idx_whale_accumulation_ticker
    ON whale_accumulation (ticker, quarter_end DESC);

-- CUSIP-to-ticker resolution cache
CREATE TABLE IF NOT EXISTS cusip_ticker_cache (
    cusip       TEXT PRIMARY KEY,
    ticker      TEXT NOT NULL DEFAULT '',
    figi        TEXT NOT NULL DEFAULT '',
    resolved_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
