CREATE TABLE IF NOT EXISTS recap_digests (
    id               BIGSERIAL PRIMARY KEY,
    digest_date      DATE NOT NULL,
    tenant_id        TEXT NOT NULL DEFAULT 'default',
    env_mode         TEXT NOT NULL DEFAULT 'Paper',
    body             TEXT NOT NULL,
    trades_covered   INTEGER NOT NULL DEFAULT 0,
    net_pnl_today    DOUBLE PRECISION NOT NULL DEFAULT 0,
    prompt_version   TEXT NOT NULL,
    model            TEXT NOT NULL,
    generated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, env_mode, digest_date)
);
CREATE INDEX IF NOT EXISTS idx_recap_digests_date ON recap_digests (digest_date DESC);
