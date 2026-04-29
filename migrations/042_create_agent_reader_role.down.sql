REVOKE ALL ON
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
FROM agent_reader;

REVOKE USAGE ON SCHEMA public FROM agent_reader;

DO $$
BEGIN
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM agent_reader', current_database());
END
$$;

DROP ROLE IF EXISTS agent_reader;
