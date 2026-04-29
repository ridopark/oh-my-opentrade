ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM agent_writer;

REVOKE ALL ON chat_sessions FROM agent_writer;
REVOKE USAGE, CREATE ON SCHEMA public FROM agent_writer;

DO $$
BEGIN
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM agent_writer', current_database());
END
$$;

DROP ROLE IF EXISTS agent_writer;
