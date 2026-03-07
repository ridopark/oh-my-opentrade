CREATE TABLE IF NOT EXISTS dna_versions (
    id              TEXT PRIMARY KEY,
    strategy_key    TEXT NOT NULL,
    content_toml    TEXT NOT NULL,
    content_hash    TEXT NOT NULL,
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_dna_versions_strategy_hash ON dna_versions (strategy_key, content_hash);

CREATE TABLE IF NOT EXISTS dna_approvals (
    id              TEXT PRIMARY KEY,
    version_id      TEXT NOT NULL REFERENCES dna_versions(id),
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    decided_by      TEXT,
    decided_at      TIMESTAMPTZ,
    comment         TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_dna_approvals_status ON dna_approvals (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_dna_approvals_version ON dna_approvals (version_id);
