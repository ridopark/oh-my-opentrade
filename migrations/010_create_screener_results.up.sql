CREATE TABLE IF NOT EXISTS screener_results (
  tenant_id            TEXT        NOT NULL,
  env_mode             TEXT        NOT NULL,
  run_id               TEXT        NOT NULL,
  as_of                TIMESTAMPTZ NOT NULL,
  symbol               TEXT        NOT NULL,
  prev_close           DOUBLE PRECISION NULL,
  premarket_price      DOUBLE PRECISION NULL,
  premarket_volume     BIGINT NULL,
  avg_hist_volume      BIGINT NULL,
  gap_pct              DOUBLE PRECISION NULL,
  rvol                 DOUBLE PRECISION NULL,
  gap_score            DOUBLE PRECISION NOT NULL,
  rvol_score           DOUBLE PRECISION NOT NULL,
  news_score           DOUBLE PRECISION NULL,
  total_score          DOUBLE PRECISION NOT NULL,
  status               TEXT        NOT NULL,
  price_source         TEXT        NULL,
  error_msg            TEXT        NULL,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, env_mode, run_id, symbol)
);
SELECT create_hypertable('screener_results', 'as_of', if_not_exists => TRUE);
CREATE INDEX IF NOT EXISTS idx_screener_results_as_of_desc ON screener_results (as_of DESC);
CREATE INDEX IF NOT EXISTS idx_screener_results_symbol_as_of ON screener_results (symbol, as_of DESC);
CREATE INDEX IF NOT EXISTS idx_screener_results_score ON screener_results (as_of DESC, total_score DESC);
