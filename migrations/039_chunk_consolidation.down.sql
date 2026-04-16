-- 039_chunk_consolidation.down.sql
-- Revert chunk interval changes. Merged chunks cannot be unmerged;
-- the wider chunks remain but new data will use the old 1-day interval.

SELECT set_chunk_time_interval('market_bars', INTERVAL '1 day');
SELECT set_chunk_time_interval('darkpool_bars', INTERVAL '1 day');

SELECT remove_compression_policy('historical_option_chain', if_exists => TRUE);
ALTER TABLE historical_option_chain SET (timescaledb.compress = false);
