-- DP Buy Ratio Predictive Power Test
-- Tests whether dark pool buy_ratio predicts forward returns
-- across multiple aggregation intervals and forward horizons.
--
-- Active strategies: AVWAP + MACD (no ORB)
-- Universe: 34 symbols, darkpool_bars 5m from 2025-01 to 2026-04
--
-- Run: PGPASSWORD=changeme psql -h localhost -U opentrade -d opentrade -f scripts/dp_buyratio_predictive_power.sql

\timing on
\pset format aligned

--------------------------------------------------------------------
-- 0. Materialized helpers
--------------------------------------------------------------------

-- 0a. Session-level DP aggregates (one row per symbol per trading day)
DROP TABLE IF EXISTS _dp_sessions;
CREATE TEMP TABLE _dp_sessions AS
WITH rth_bars AS (
    SELECT
        d.symbol,
        (d.time AT TIME ZONE 'America/New_York')::date AS trade_date,
        d.time,
        EXTRACT(HOUR FROM d.time AT TIME ZONE 'America/New_York') * 60
          + EXTRACT(MINUTE FROM d.time AT TIME ZONE 'America/New_York') AS mins,
        d.buy_volume,
        d.sell_volume,
        d.dp_volume,
        d.total_volume,
        d.large_print_volume
    FROM darkpool_bars d
    WHERE d.timeframe = '5m'
      AND EXTRACT(HOUR FROM d.time AT TIME ZONE 'America/New_York') >= 9
      AND EXTRACT(HOUR FROM d.time AT TIME ZONE 'America/New_York') < 16
      AND d.total_volume > 0
)
SELECT
    symbol,
    trade_date,
    -- Full day
    SUM(buy_volume)                                          AS day_buy_vol,
    SUM(sell_volume)                                         AS day_sell_vol,
    SUM(dp_volume)                                           AS day_dp_vol,
    SUM(total_volume)                                        AS day_total_vol,
    SUM(large_print_volume)                                  AS day_large_vol,
    CASE WHEN SUM(buy_volume + sell_volume) > 0
         THEN SUM(buy_volume) / SUM(buy_volume + sell_volume)
         ELSE 0.5 END                                        AS day_buy_ratio,
    -- Early 30 min (09:30-10:00)
    SUM(CASE WHEN mins >= 570 AND mins < 600 THEN buy_volume ELSE 0 END) AS early_buy_vol,
    SUM(CASE WHEN mins >= 570 AND mins < 600 THEN sell_volume ELSE 0 END) AS early_sell_vol,
    SUM(CASE WHEN mins >= 570 AND mins < 600 THEN dp_volume ELSE 0 END) AS early_dp_vol,
    SUM(CASE WHEN mins >= 570 AND mins < 600 THEN total_volume ELSE 0 END) AS early_total_vol,
    CASE WHEN SUM(CASE WHEN mins >= 570 AND mins < 600 THEN buy_volume + sell_volume ELSE 0 END) > 0
         THEN SUM(CASE WHEN mins >= 570 AND mins < 600 THEN buy_volume ELSE 0 END)
            / SUM(CASE WHEN mins >= 570 AND mins < 600 THEN buy_volume + sell_volume ELSE 0 END)
         ELSE 0.5 END                                        AS early_buy_ratio,
    -- Late 60 min (15:00-16:00)
    SUM(CASE WHEN mins >= 900 AND mins < 960 THEN buy_volume ELSE 0 END) AS late_buy_vol,
    SUM(CASE WHEN mins >= 900 AND mins < 960 THEN sell_volume ELSE 0 END) AS late_sell_vol,
    CASE WHEN SUM(CASE WHEN mins >= 900 AND mins < 960 THEN buy_volume + sell_volume ELSE 0 END) > 0
         THEN SUM(CASE WHEN mins >= 900 AND mins < 960 THEN buy_volume ELSE 0 END)
            / SUM(CASE WHEN mins >= 900 AND mins < 960 THEN buy_volume + sell_volume ELSE 0 END)
         ELSE 0.5 END                                        AS late_buy_ratio
FROM rth_bars
GROUP BY symbol, trade_date
HAVING SUM(total_volume) > 0;

CREATE INDEX ON _dp_sessions (symbol, trade_date);

-- 0b. Daily close prices from 1m bars (last 1m bar of RTH)
DROP TABLE IF EXISTS _daily_closes;
CREATE TEMP TABLE _daily_closes AS
SELECT DISTINCT ON (symbol, trade_date)
    symbol,
    (time AT TIME ZONE 'America/New_York')::date AS trade_date,
    close,
    time
FROM market_bars
WHERE timeframe = '1m'
  AND EXTRACT(HOUR FROM time AT TIME ZONE 'America/New_York') >= 9
  AND EXTRACT(HOUR FROM time AT TIME ZONE 'America/New_York') < 16
  AND account_id = ''
ORDER BY symbol, trade_date, time DESC;

CREATE INDEX ON _daily_closes (symbol, trade_date);

-- 0c. Next-day and 3-day forward closes
DROP TABLE IF EXISTS _daily_returns;
CREATE TEMP TABLE _daily_returns AS
SELECT
    c.symbol,
    c.trade_date,
    c.close,
    LEAD(c.close, 1) OVER w AS next_day_close,
    LEAD(c.close, 3) OVER w AS fwd_3d_close
FROM _daily_closes c
WINDOW w AS (PARTITION BY c.symbol ORDER BY c.trade_date);

CREATE INDEX ON _daily_returns (symbol, trade_date);

--------------------------------------------------------------------
-- 1. PHASE 1: Session-fixed windows (daily granularity)
--------------------------------------------------------------------

-- Join DP sessions with forward returns and compute Z-scores
DROP TABLE IF EXISTS _phase1;
CREATE TEMP TABLE _phase1 AS
SELECT
    s.symbol,
    s.trade_date,
    s.day_buy_ratio,
    s.early_buy_ratio,
    s.late_buy_ratio,
    s.day_total_vol,
    s.early_total_vol,
    -- Z-scores (20-day trailing)
    (s.day_buy_ratio - AVG(s.day_buy_ratio) OVER w20) /
        NULLIF(STDDEV_SAMP(s.day_buy_ratio) OVER w20, 0) AS day_buy_ratio_z,
    (s.early_buy_ratio - AVG(s.early_buy_ratio) OVER w20) /
        NULLIF(STDDEV_SAMP(s.early_buy_ratio) OVER w20, 0) AS early_buy_ratio_z,
    (s.late_buy_ratio - AVG(s.late_buy_ratio) OVER w20) /
        NULLIF(STDDEV_SAMP(s.late_buy_ratio) OVER w20, 0) AS late_buy_ratio_z,
    -- Forward returns
    r.close AS day_close,
    (r.next_day_close - r.close) / NULLIF(r.close, 0) AS fwd_1d_ret,
    (r.fwd_3d_close - r.close) / NULLIF(r.close, 0) AS fwd_3d_ret
FROM _dp_sessions s
JOIN _daily_returns r ON r.symbol = s.symbol AND r.trade_date = s.trade_date
WHERE s.day_total_vol > 1000
WINDOW w20 AS (PARTITION BY s.symbol ORDER BY s.trade_date ROWS BETWEEN 20 PRECEDING AND 1 PRECEDING);

--------------------------------------------------------------------
-- Variant 7: Early 30min raw → EOD
-- (We approximate EOD return as close-to-close same day vs next-day)
-- Since we need intra-day EOD return, use 1d return as proxy
--------------------------------------------------------------------

\echo '============================================================'
\echo 'PHASE 1 RESULTS'
\echo '============================================================'

\echo ''
\echo '--- Variant 7: Early 30min buy_ratio (raw) → next-day return ---'
SELECT
    'V7_early_raw_1d' AS variant,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN (early_buy_ratio - 0.5) * fwd_1d_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(early_buy_ratio, fwd_1d_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(early_buy_ratio, fwd_1d_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _phase1
WHERE early_buy_ratio IS NOT NULL
  AND fwd_1d_ret IS NOT NULL
  AND early_total_vol > 500;

\echo ''
\echo '--- Variant 8: Early 30min buy_ratio (Z-score) → next-day return ---'
SELECT
    'V8_early_z_1d' AS variant,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN early_buy_ratio_z * fwd_1d_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(early_buy_ratio_z, fwd_1d_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(early_buy_ratio_z, fwd_1d_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _phase1
WHERE early_buy_ratio_z IS NOT NULL
  AND fwd_1d_ret IS NOT NULL
  AND early_total_vol > 500;

\echo ''
\echo '--- Variant 14: Full-day buy_ratio (raw) → next-day return ---'
SELECT
    'V14_day_raw_1d' AS variant,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN (day_buy_ratio - 0.5) * fwd_1d_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(day_buy_ratio, fwd_1d_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(day_buy_ratio, fwd_1d_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _phase1
WHERE fwd_1d_ret IS NOT NULL;

\echo ''
\echo '--- Variant 15: Full-day buy_ratio (Z-score) → next-day return ---'
SELECT
    'V15_day_z_1d' AS variant,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN day_buy_ratio_z * fwd_1d_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(day_buy_ratio_z, fwd_1d_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(day_buy_ratio_z, fwd_1d_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _phase1
WHERE day_buy_ratio_z IS NOT NULL
  AND fwd_1d_ret IS NOT NULL;

\echo ''
\echo '--- Variant 16: Full-day buy_ratio (Z-score) → 3-day return ---'
SELECT
    'V16_day_z_3d' AS variant,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN day_buy_ratio_z * fwd_3d_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(day_buy_ratio_z, fwd_3d_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(day_buy_ratio_z, fwd_3d_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _phase1
WHERE day_buy_ratio_z IS NOT NULL
  AND fwd_3d_ret IS NOT NULL;

\echo ''
\echo '--- Variant 12: Late 60min buy_ratio (raw) → next-day return ---'
SELECT
    'V12_late_raw_1d' AS variant,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN (late_buy_ratio - 0.5) * fwd_1d_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(late_buy_ratio, fwd_1d_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(late_buy_ratio, fwd_1d_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _phase1
WHERE fwd_1d_ret IS NOT NULL;

\echo ''
\echo '--- Variant 13: Late 60min buy_ratio (Z-score) → next-day return ---'
SELECT
    'V13_late_z_1d' AS variant,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN late_buy_ratio_z * fwd_1d_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(late_buy_ratio_z, fwd_1d_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(late_buy_ratio_z, fwd_1d_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _phase1
WHERE late_buy_ratio_z IS NOT NULL
  AND fwd_1d_ret IS NOT NULL;

--------------------------------------------------------------------
-- 2. QUINTILE ANALYSIS (best variant from Phase 1 + all variants)
--------------------------------------------------------------------

\echo ''
\echo '============================================================'
\echo 'QUINTILE ANALYSIS'
\echo '============================================================'

\echo ''
\echo '--- Full-day buy_ratio quintiles → mean next-day return ---'
SELECT
    quintile,
    COUNT(*) AS n,
    ROUND(AVG(fwd_1d_ret * 10000)::numeric, 2) AS mean_ret_bps,
    ROUND(STDDEV_SAMP(fwd_1d_ret * 10000)::numeric, 2) AS std_bps,
    ROUND(AVG(day_buy_ratio)::numeric, 4) AS mean_buy_ratio
FROM (
    SELECT *,
        NTILE(5) OVER (ORDER BY day_buy_ratio) AS quintile
    FROM _phase1
    WHERE fwd_1d_ret IS NOT NULL
) q
GROUP BY quintile
ORDER BY quintile;

\echo ''
\echo '--- Full-day Z-scored buy_ratio quintiles → mean next-day return ---'
SELECT
    quintile,
    COUNT(*) AS n,
    ROUND(AVG(fwd_1d_ret * 10000)::numeric, 2) AS mean_ret_bps,
    ROUND(STDDEV_SAMP(fwd_1d_ret * 10000)::numeric, 2) AS std_bps,
    ROUND(AVG(day_buy_ratio_z)::numeric, 4) AS mean_z
FROM (
    SELECT *,
        NTILE(5) OVER (ORDER BY day_buy_ratio_z) AS quintile
    FROM _phase1
    WHERE fwd_1d_ret IS NOT NULL
      AND day_buy_ratio_z IS NOT NULL
) q
GROUP BY quintile
ORDER BY quintile;

\echo ''
\echo '--- Early 30min Z-scored buy_ratio quintiles → mean next-day return ---'
SELECT
    quintile,
    COUNT(*) AS n,
    ROUND(AVG(fwd_1d_ret * 10000)::numeric, 2) AS mean_ret_bps,
    ROUND(STDDEV_SAMP(fwd_1d_ret * 10000)::numeric, 2) AS std_bps,
    ROUND(AVG(early_buy_ratio_z)::numeric, 4) AS mean_z
FROM (
    SELECT *,
        NTILE(5) OVER (ORDER BY early_buy_ratio_z) AS quintile
    FROM _phase1
    WHERE fwd_1d_ret IS NOT NULL
      AND early_buy_ratio_z IS NOT NULL
      AND early_total_vol > 500
) q
GROUP BY quintile
ORDER BY quintile;

--------------------------------------------------------------------
-- 3. PHASE 2: Intraday rolling windows
--------------------------------------------------------------------

\echo ''
\echo '============================================================'
\echo 'PHASE 2: INTRADAY ROLLING WINDOWS'
\echo '============================================================'

-- 30m rolling buy_ratio → 60m forward return (from 1m bars)
-- We aggregate 6 consecutive 5m DP bars into 30m windows,
-- then compute forward 60m return from 1m market bars.

DROP TABLE IF EXISTS _intraday_30m;
CREATE TEMP TABLE _intraday_30m AS
WITH dp_30m AS (
    SELECT
        d.symbol,
        -- Anchor to 30m boundaries
        date_trunc('hour', d.time) +
          INTERVAL '30 min' * FLOOR(EXTRACT(MINUTE FROM d.time) / 30) AS window_start,
        SUM(d.buy_volume) AS buy_vol,
        SUM(d.sell_volume) AS sell_vol,
        SUM(d.dp_volume) AS dp_vol,
        SUM(d.total_volume) AS total_vol
    FROM darkpool_bars d
    WHERE d.timeframe = '5m'
      AND EXTRACT(HOUR FROM d.time AT TIME ZONE 'America/New_York') >= 10
      AND EXTRACT(HOUR FROM d.time AT TIME ZONE 'America/New_York') < 15
      AND d.total_volume > 0
    GROUP BY d.symbol, 2
    HAVING SUM(buy_volume + sell_volume) > 0
),
dp_with_ratio AS (
    SELECT
        symbol,
        window_start,
        buy_vol / (buy_vol + sell_vol) AS buy_ratio,
        total_vol,
        -- Z-score over trailing 20 same-window observations
        AVG(buy_vol / (buy_vol + sell_vol)) OVER w20 AS mean_br,
        STDDEV_SAMP(buy_vol / (buy_vol + sell_vol)) OVER w20 AS std_br
    FROM dp_30m
    WHERE buy_vol + sell_vol > 100
    WINDOW w20 AS (PARTITION BY symbol ORDER BY window_start ROWS BETWEEN 40 PRECEDING AND 1 PRECEDING)
),
fwd AS (
    SELECT DISTINCT ON (symbol, window_start)
        m.symbol,
        ws AS window_start,
        m.close AS fwd_close
    FROM dp_with_ratio dw
    CROSS JOIN LATERAL (SELECT dw.window_start + INTERVAL '60 min' AS ws) x
    JOIN market_bars m ON m.symbol = dw.symbol
        AND m.timeframe = '1m'
        AND m.time >= x.ws
        AND m.time < x.ws + INTERVAL '5 min'
        AND m.account_id = ''
    ORDER BY symbol, window_start, m.time ASC
),
current_price AS (
    SELECT DISTINCT ON (symbol, window_start)
        m.symbol,
        dw.window_start,
        m.close AS current_close
    FROM dp_with_ratio dw
    JOIN market_bars m ON m.symbol = dw.symbol
        AND m.timeframe = '1m'
        AND m.time >= dw.window_start + INTERVAL '30 min' - INTERVAL '1 min'
        AND m.time < dw.window_start + INTERVAL '30 min' + INTERVAL '1 min'
        AND m.account_id = ''
    ORDER BY symbol, window_start, m.time DESC
)
SELECT
    dw.symbol,
    dw.window_start,
    dw.buy_ratio,
    CASE WHEN dw.std_br > 0 THEN (dw.buy_ratio - dw.mean_br) / dw.std_br ELSE NULL END AS buy_ratio_z,
    dw.total_vol,
    (f.fwd_close - cp.current_close) / NULLIF(cp.current_close, 0) AS fwd_60m_ret
FROM dp_with_ratio dw
JOIN fwd f ON f.symbol = dw.symbol AND f.window_start = dw.window_start
JOIN current_price cp ON cp.symbol = dw.symbol AND cp.window_start = dw.window_start
WHERE cp.current_close > 0;

\echo ''
\echo '--- Variant 3: 30m rolling buy_ratio (raw) → 60m fwd return ---'
SELECT
    'V3_30m_raw_60m' AS variant,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN (buy_ratio - 0.5) * fwd_60m_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(buy_ratio, fwd_60m_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(buy_ratio, fwd_60m_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _intraday_30m
WHERE fwd_60m_ret IS NOT NULL;

\echo ''
\echo '--- Variant 4: 30m rolling buy_ratio (Z-score) → 60m fwd return ---'
SELECT
    'V4_30m_z_60m' AS variant,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN buy_ratio_z * fwd_60m_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(buy_ratio_z, fwd_60m_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(buy_ratio_z, fwd_60m_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _intraday_30m
WHERE buy_ratio_z IS NOT NULL
  AND fwd_60m_ret IS NOT NULL;

\echo ''
\echo '--- 30m rolling Z-scored quintiles → mean 60m fwd return ---'
SELECT
    quintile,
    COUNT(*) AS n,
    ROUND(AVG(fwd_60m_ret * 10000)::numeric, 2) AS mean_ret_bps,
    ROUND(STDDEV_SAMP(fwd_60m_ret * 10000)::numeric, 2) AS std_bps,
    ROUND(AVG(buy_ratio_z)::numeric, 4) AS mean_z
FROM (
    SELECT *,
        NTILE(5) OVER (ORDER BY buy_ratio_z) AS quintile
    FROM _intraday_30m
    WHERE fwd_60m_ret IS NOT NULL
      AND buy_ratio_z IS NOT NULL
) q
GROUP BY quintile
ORDER BY quintile;

--------------------------------------------------------------------
-- 4. LARGE-PRINT-ONLY VARIANT (bonus)
--------------------------------------------------------------------

\echo ''
\echo '============================================================'
\echo 'BONUS: LARGE-PRINT-ONLY BUY RATIO'
\echo '============================================================'

-- Large prints (>$200K notional) as fraction of total DP volume
-- High large_print_ratio + high buy_ratio = institutional accumulation signal
\echo ''
\echo '--- Full-day large-print ratio quintiles → next-day return ---'
SELECT
    quintile,
    COUNT(*) AS n,
    ROUND(AVG(fwd_1d_ret * 10000)::numeric, 2) AS mean_ret_bps,
    ROUND(AVG(large_ratio)::numeric, 4) AS mean_large_ratio
FROM (
    SELECT
        p.*,
        s.day_large_vol / NULLIF(s.day_dp_vol, 0) AS large_ratio,
        NTILE(5) OVER (ORDER BY s.day_large_vol / NULLIF(s.day_dp_vol, 0)) AS quintile
    FROM _phase1 p
    JOIN _dp_sessions s ON s.symbol = p.symbol AND s.trade_date = p.trade_date
    WHERE p.fwd_1d_ret IS NOT NULL
      AND s.day_dp_vol > 0
) q
GROUP BY quintile
ORDER BY quintile;

\echo ''
\echo '--- Interaction: high buy_ratio + high large-print → next-day return ---'
SELECT
    CASE WHEN day_buy_ratio > 0.52 THEN 'buy_high' ELSE 'buy_low' END AS buy_bucket,
    CASE WHEN large_ratio > 0.3 THEN 'large_high' ELSE 'large_low' END AS large_bucket,
    COUNT(*) AS n,
    ROUND(AVG(fwd_1d_ret * 10000)::numeric, 2) AS mean_ret_bps,
    ROUND(STDDEV_SAMP(fwd_1d_ret * 10000)::numeric, 2) AS std_bps
FROM (
    SELECT
        p.*,
        s.day_large_vol / NULLIF(s.day_dp_vol, 0) AS large_ratio
    FROM _phase1 p
    JOIN _dp_sessions s ON s.symbol = p.symbol AND s.trade_date = p.trade_date
    WHERE p.fwd_1d_ret IS NOT NULL
      AND s.day_dp_vol > 0
) q
GROUP BY 1, 2
ORDER BY 1, 2;

--------------------------------------------------------------------
-- 5. PER-SYMBOL BREAKDOWN (top signals)
--------------------------------------------------------------------

\echo ''
\echo '============================================================'
\echo 'PER-SYMBOL RANK IC (Full-day Z-scored → next-day return)'
\echo '============================================================'
SELECT
    symbol,
    COUNT(*) AS n,
    ROUND(AVG(CASE WHEN day_buy_ratio_z * fwd_1d_ret > 0 THEN 1.0 ELSE 0.0 END)::numeric, 4) AS hit_rate,
    ROUND(CORR(day_buy_ratio_z, fwd_1d_ret)::numeric, 5) AS rank_ic,
    ROUND((CORR(day_buy_ratio_z, fwd_1d_ret) * SQRT(COUNT(*)))::numeric, 2) AS t_stat
FROM _phase1
WHERE day_buy_ratio_z IS NOT NULL
  AND fwd_1d_ret IS NOT NULL
GROUP BY symbol
ORDER BY rank_ic DESC;

--------------------------------------------------------------------
-- Cleanup
--------------------------------------------------------------------
DROP TABLE IF EXISTS _dp_sessions;
DROP TABLE IF EXISTS _daily_closes;
DROP TABLE IF EXISTS _daily_returns;
DROP TABLE IF EXISTS _phase1;
DROP TABLE IF EXISTS _intraday_30m;

\echo ''
\echo 'Done. Interpretation guide:'
\echo '  Hit rate > 0.52 with n > 5000 = meaningful'
\echo '  Rank IC > 0.02 with t-stat > 2.0 = actionable'
\echo '  Monotonic quintile spread = strongest signal'
