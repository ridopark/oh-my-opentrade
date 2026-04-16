-- 039_chunk_consolidation.up.sql
-- Widen chunk intervals from 1 day to 7 days and add missing compression
-- on historical_option_chain. Existing chunks must be merged separately
-- (see merge procedure below) because merge_chunks is a runtime operation
-- that depends on the current chunk layout.
--
-- Context: market_bars accumulated 1,240 daily chunks over 3.4 years.
-- Query planning evaluated constraint exclusion on every chunk, taking
-- 0.8-1.6s per query and dominating backtest startup time.

-- 1. Widen chunk intervals for future inserts.
SELECT set_chunk_time_interval('market_bars', INTERVAL '7 days');
SELECT set_chunk_time_interval('darkpool_bars', INTERVAL '7 days');

-- 2. Add compression to historical_option_chain (migration 023 omitted it).
ALTER TABLE historical_option_chain SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'symbol, call_put',
    timescaledb.compress_orderby = 'date DESC, expiration, strike'
);
SELECT add_compression_policy('historical_option_chain', INTERVAL '7 days', if_not_exists => TRUE);

-- Compress existing chunks older than 7 days.
DO $$
DECLARE
    chunk_fqn text;
BEGIN
    FOR chunk_fqn IN
        SELECT format('%I.%I', c.chunk_schema, c.chunk_name)
        FROM timescaledb_information.chunks c
        WHERE c.hypertable_name = 'historical_option_chain'
          AND c.range_end < now() - INTERVAL '7 days'
        ORDER BY c.range_start
    LOOP
        BEGIN
            EXECUTE format('SELECT compress_chunk(%L::regclass)', chunk_fqn);
        EXCEPTION WHEN OTHERS THEN
            NULL; -- already compressed
        END;
    END LOOP;
END $$;

-- 3. Merge existing small chunks into larger ones.
--    merge_chunks requires adjacent partitions (range_end of chunk N =
--    range_start of chunk N+1). Group by contiguous runs, batch by 50
--    to stay within max_locks_per_transaction.
--
--    Generate merge statements for a given hypertable:
--
--    WITH chunks AS (
--        SELECT chunk_schema || '.' || chunk_name as full_name,
--               range_start, range_end,
--               CASE WHEN range_start = LAG(range_end) OVER (ORDER BY range_start)
--                    THEN 0 ELSE 1 END as new_group
--        FROM timescaledb_information.chunks
--        WHERE hypertable_name = '<TABLE>' AND is_compressed = true
--        ORDER BY range_start
--    ),
--    groups AS (
--        SELECT full_name, range_start,
--               SUM(new_group) OVER (ORDER BY range_start) as grp
--        FROM chunks
--    ),
--    numbered AS (
--        SELECT full_name, grp,
--               ROW_NUMBER() OVER (PARTITION BY grp ORDER BY range_start) as rn
--        FROM groups
--    ),
--    batched AS (
--        SELECT full_name, grp, (rn - 1) / 50 as batch_id
--        FROM numbered
--    )
--    SELECT 'CALL merge_chunks(ARRAY[' ||
--        string_agg('''' || full_name || '''::regclass', ','
--                    ORDER BY full_name) || ']);'
--    FROM batched
--    GROUP BY grp, batch_id
--    HAVING count(*) > 1
--    ORDER BY grp, batch_id;
--
--    Run for both market_bars and darkpool_bars, then execute the output.
