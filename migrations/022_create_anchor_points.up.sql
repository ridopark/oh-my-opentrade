CREATE TABLE IF NOT EXISTS anchor_points (
    id              TEXT PRIMARY KEY,
    symbol          TEXT NOT NULL,
    anchor_time     TIMESTAMPTZ NOT NULL,
    price           DOUBLE PRECISION NOT NULL,
    anchor_type     TEXT NOT NULL,
    timeframe       TEXT NOT NULL,
    strength        DOUBLE PRECISION NOT NULL DEFAULT 0,
    source          TEXT NOT NULL DEFAULT 'algo',
    ai_rank         INT,
    ai_confidence   DOUBLE PRECISION,
    ai_reason       TEXT,
    volume_context  JSONB,
    touch_count     INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expired_at      TIMESTAMPTZ,
    expired_reason  TEXT
);

CREATE INDEX IF NOT EXISTS idx_anchor_points_active
    ON anchor_points (symbol) WHERE expired_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_anchor_points_symbol_type
    ON anchor_points (symbol, anchor_type);
