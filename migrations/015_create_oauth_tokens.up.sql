CREATE TABLE IF NOT EXISTS oauth_tokens (
    provider        TEXT NOT NULL,
    tenant_id       TEXT NOT NULL,
    access_token    TEXT NOT NULL,
    refresh_token   TEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (provider, tenant_id)
);
