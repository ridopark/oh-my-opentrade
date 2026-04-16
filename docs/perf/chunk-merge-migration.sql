-- TimescaleDB chunk consolidation migration
-- Applied: 2026-04-15
-- Context: Query planning on market_bars (1,240 daily chunks) took 0.8-1.6s
-- per query, dominating backtest startup time. This migration widens chunk
-- intervals, merges existing chunks, adds missing compression, and tunes
-- work_mem.
--
-- Results:
--   market_bars:              1,240 chunks -> 36    (planning 837ms -> 21ms)
--   darkpool_bars:              388 chunks -> 73    (planning 580ms -> 112ms)
--   historical_option_chain:  119 uncompressed -> 118 compressed (514MB -> 171MB)
--   work_mem:                 1.7MB -> 16MB

-- Step 1: Widen chunk intervals for future inserts
SELECT set_chunk_time_interval('market_bars', INTERVAL '7 days');
SELECT set_chunk_time_interval('darkpool_bars', INTERVAL '7 days');

-- Step 2: Raise work_mem (run outside transaction)
-- ALTER SYSTEM SET work_mem = '16MB';
-- SELECT pg_reload_conf();

-- Step 3: Merge existing compressed chunks
--
-- merge_chunks requires adjacent partitions (range_end of chunk N = range_start
-- of chunk N+1). Weekend gaps break adjacency, so group by contiguous runs.
-- Batch into groups of 50 to stay within max_locks_per_transaction (256).
--
-- Generate merge statements dynamically:
--
--   WITH chunks AS (
--     SELECT
--       chunk_schema || '.' || chunk_name as full_name,
--       range_start, range_end,
--       CASE WHEN range_start = LAG(range_end) OVER (ORDER BY range_start)
--            THEN 0 ELSE 1 END as new_group
--     FROM timescaledb_information.chunks
--     WHERE hypertable_name = 'market_bars' AND is_compressed = true
--     ORDER BY range_start
--   ),
--   groups AS (
--     SELECT full_name, range_start,
--       SUM(new_group) OVER (ORDER BY range_start) as grp
--     FROM chunks
--   ),
--   big_groups AS (
--     SELECT full_name, grp,
--       ROW_NUMBER() OVER (PARTITION BY grp ORDER BY range_start) as rn
--     FROM groups
--   ),
--   batched AS (
--     SELECT full_name, grp, (rn - 1) / 50 as batch_id
--     FROM big_groups
--   )
--   SELECT 'CALL merge_chunks(ARRAY[' ||
--     string_agg('''' || full_name || '''::regclass', ',' ORDER BY full_name) ||
--     ']);'
--   FROM batched
--   GROUP BY grp, batch_id
--   HAVING count(*) > 1
--   ORDER BY grp, batch_id;
--
-- Run the same query replacing 'market_bars' with 'darkpool_bars'.

-- Step 4: Enable compression on historical_option_chain (had none)
ALTER TABLE historical_option_chain SET (
  timescaledb.compress,
  timescaledb.compress_segmentby = 'symbol,call_put',
  timescaledb.compress_orderby = 'date DESC,expiration,strike'
);
SELECT add_compression_policy('historical_option_chain', INTERVAL '7 days');

-- Compress all existing chunks older than 7 days (skip already-compressed):
DO $$
DECLARE
  chunk_name text;
BEGIN
  FOR chunk_name IN
    SELECT format('%I.%I', c.chunk_schema, c.chunk_name)
    FROM timescaledb_information.chunks c
    WHERE c.hypertable_name = 'historical_option_chain'
      AND c.range_end < now() - INTERVAL '7 days'
    ORDER BY c.range_start
  LOOP
    BEGIN
      EXECUTE format('SELECT compress_chunk(%L::regclass)', chunk_name);
    EXCEPTION WHEN OTHERS THEN
      NULL; -- already compressed
    END;
  END LOOP;
END $$;

-- Verification queries:
-- SELECT hypertable_name, COUNT(*) FROM timescaledb_information.chunks
--   WHERE hypertable_name IN ('market_bars','darkpool_bars','historical_option_chain')
--   GROUP BY hypertable_name;
-- SHOW work_mem;
