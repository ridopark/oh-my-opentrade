-- 012_create_strategy_perf.down.sql
-- Rollback: drop per-strategy performance tables.

DROP TABLE IF EXISTS strategy_signal_events;
DROP TABLE IF EXISTS strategy_equity_points;
DROP TABLE IF EXISTS strategy_daily_pnl;
