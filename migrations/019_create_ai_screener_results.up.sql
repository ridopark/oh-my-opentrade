CREATE TABLE IF NOT EXISTS ai_screener_results (
  tenant_id            TEXT        NOT NULL,
  env_mode             TEXT        NOT NULL,
  run_id               TEXT        NOT NULL,
  as_of                TIMESTAMPTZ NOT NULL,
  strategy_key         TEXT        NOT NULL,
  symbol               TEXT        NOT NULL,
  anon_id              TEXT        NOT NULL,
  score                SMALLINT    NOT NULL,
  rationale            TEXT        NOT NULL DEFAULT '',
  model                TEXT        NOT NULL,
  latency_ms           BIGINT      NOT NULL DEFAULT 0,
  prompt_hash          TEXT        NOT NULL DEFAULT '',
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, env_mode, run_id, strategy_key, symbol, as_of)
);
SELECT create_hypertable('ai_screener_results', 'as_of', if_not_exists => TRUE);
CREATE INDEX IF NOT EXISTS idx_ai_screener_as_of_desc ON ai_screener_results (as_of DESC);
CREATE INDEX IF NOT EXISTS idx_ai_screener_strategy_as_of ON ai_screener_results (strategy_key, as_of DESC);
CREATE INDEX IF NOT EXISTS idx_ai_screener_score ON ai_screener_results (as_of DESC, strategy_key, score DESC);
