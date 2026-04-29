-- agent_writer: read-write Postgres role used by apps/agent-api for session
-- state and LangGraph checkpoint persistence. Distinct from agent_reader so
-- the SQL-agent tool path stays read-only: only session-metadata and the
-- LangGraph-owned checkpoint tables are writable here.
--
-- LangGraph's AsyncPostgresSaver.setup() creates its own tables (checkpoints,
-- checkpoint_blobs, checkpoint_writes, checkpoint_migrations) on first run.
-- This role needs CREATE on the public schema so setup() can provision them,
-- plus ALL on the tables once they exist. The easiest path that satisfies
-- both is to grant ALL PRIVILEGES on schema public plus default privileges,
-- which is acceptable because the role is only used by the sidecar's
-- session + checkpoint paths, never by untrusted input.

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'agent_writer') THEN
        CREATE ROLE agent_writer LOGIN PASSWORD 'changeme_agent_writer';
    END IF;
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO agent_writer', current_database());
END
$$;

GRANT USAGE, CREATE ON SCHEMA public TO agent_writer;

GRANT SELECT, INSERT, UPDATE, DELETE ON chat_sessions TO agent_writer;

-- Future LangGraph-created checkpoint tables: grant via default privileges.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO agent_writer;

ALTER ROLE agent_writer SET statement_timeout = '10s';
ALTER ROLE agent_writer SET idle_in_transaction_session_timeout = '30s';
