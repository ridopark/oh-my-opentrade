-- chat_sessions: metadata for the dashboard's chat history sidebar.
-- Conversation content (messages, tool calls, state) is persisted by
-- LangGraph's AsyncPostgresSaver in checkpoint tables it owns and manages;
-- this table only tracks what the sidebar needs to list and label sessions.

CREATE TABLE IF NOT EXISTS chat_sessions (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    title         TEXT        NOT NULL DEFAULT 'New chat',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_turn_at  TIMESTAMPTZ,
    turn_count    INTEGER     NOT NULL DEFAULT 0,
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_chat_sessions_updated_live
    ON chat_sessions (updated_at DESC)
    WHERE deleted_at IS NULL;
