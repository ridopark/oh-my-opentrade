-- agent_reader: read-only Postgres role for the NL query sidecar (apps/agent-api).
-- Whitelisted SELECT on trading/performance tables only. Mutation is blocked at
-- role level (default_transaction_read_only = on), not just at the app layer.
-- Password must be rotated per environment via ALTER ROLE after apply.

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'agent_reader') THEN
        CREATE ROLE agent_reader LOGIN PASSWORD 'changeme_agent_reader';
    END IF;
    -- Use current_database() so the grant targets whatever DB is being
    -- migrated (prod=opentrade, CI/staging may differ). Hardcoding broke
    -- CI when the cluster did not have a literal `opentrade` database.
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO agent_reader', current_database());
END
$$;

GRANT USAGE ON SCHEMA public TO agent_reader;

GRANT SELECT ON
    trades,
    orders,
    daily_pnl,
    equity_curve,
    strategy_daily_pnl,
    strategy_equity_points,
    strategy_signal_events,
    strategy_trade_stats,
    backtest_runs,
    backtest_run_trades,
    recap_digests,
    thought_logs
TO agent_reader;

ALTER ROLE agent_reader SET statement_timeout = '5s';
ALTER ROLE agent_reader SET default_transaction_read_only = on;
ALTER ROLE agent_reader SET idle_in_transaction_session_timeout = '10s';
