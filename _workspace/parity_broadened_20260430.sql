-- Broadened parity SQL for issue #32
-- Compares post-PR-#27 live rows against backtest bt-134d2a5e55da2648
-- across all 3 AVWAP anchors (pd_high, pd_low, session_open) and the
-- core indicator fields produced by IndicatorCalculator (whose dedup
-- was added in PR #35 / M3 fix).
--
-- Live filter: ts >= 17:19 UTC (first post-PR-27 row), ts < 20:40 UTC
-- (before today's 20:40 UTC restart), payload.tag IS NULL.
-- Backtest filter: payload.tag = 'backtest_<bt_id>'.
--
-- Result: one row per (symbol, bar_time, anchor) where both envs emitted
-- a row. Columns show live/backtest deltas at 6-decimal precision.
-- All non-zero deltas indicate a divergence to investigate.

\set bt_id 'backtest_bt-ecd621bf22af4e6d'
\set live_from '2026-04-30 17:19:00+00'
\set live_to '2026-04-30 20:40:00+00'

WITH anchors(name) AS (
  VALUES ('pd_high'), ('pd_low'), ('session_open')
),
classified AS (
  SELECT
    e.symbol,
    e.payload->'bar'->>'time' AS bar_time,
    a.name AS anchor,
    CASE WHEN e.payload->>'tag' LIKE 'backtest_%' THEN 'backtest' ELSE 'live' END AS env,
    (e.payload->'avwapState'->'anchors'->a.name->>'slopeBPS')::numeric AS slope_bps,
    (e.payload->'avwapState'->'anchors'->a.name->>'vwap')::numeric    AS vwap,
    (e.payload->'avwapState'->'anchors'->a.name->>'barCount')::int    AS bars,
    (e.payload->'indicators'->>'rsi')::numeric         AS rsi,
    (e.payload->'indicators'->>'ema21')::numeric       AS ema21,
    (e.payload->'indicators'->>'ema50')::numeric       AS ema50,
    (e.payload->'indicators'->>'macdLine')::numeric    AS macd_line,
    (e.payload->'indicators'->>'macdSignal')::numeric  AS macd_signal,
    (e.payload->'indicators'->>'volumeSMA')::numeric   AS volume_sma
  FROM strategy_signal_events e
  CROSS JOIN anchors a
  WHERE e.payload->'avwapState'->'anchors'->a.name IS NOT NULL
    AND (
      (e.payload->>'tag' IS NULL AND e.ts >= :'live_from' AND e.ts < :'live_to')
      OR e.payload->>'tag' = :'bt_id'
    )
)
SELECT
  symbol,
  bar_time,
  anchor,
  MAX(CASE WHEN env='live'     THEN bars END) AS live_bars,
  MAX(CASE WHEN env='backtest' THEN bars END) AS bt_bars,
  ROUND(MAX(CASE WHEN env='live' THEN vwap END)
        - MAX(CASE WHEN env='backtest' THEN vwap END), 6) AS vwap_delta,
  ROUND(MAX(CASE WHEN env='live' THEN slope_bps END)
        - MAX(CASE WHEN env='backtest' THEN slope_bps END), 6) AS slope_delta,
  ROUND(MAX(CASE WHEN env='live' THEN rsi END)
        - MAX(CASE WHEN env='backtest' THEN rsi END), 6) AS rsi_delta,
  ROUND(MAX(CASE WHEN env='live' THEN ema21 END)
        - MAX(CASE WHEN env='backtest' THEN ema21 END), 6) AS ema21_delta,
  ROUND(MAX(CASE WHEN env='live' THEN ema50 END)
        - MAX(CASE WHEN env='backtest' THEN ema50 END), 6) AS ema50_delta,
  ROUND(MAX(CASE WHEN env='live' THEN macd_line END)
        - MAX(CASE WHEN env='backtest' THEN macd_line END), 6) AS macd_line_delta,
  ROUND(MAX(CASE WHEN env='live' THEN macd_signal END)
        - MAX(CASE WHEN env='backtest' THEN macd_signal END), 6) AS macd_signal_delta,
  ROUND(MAX(CASE WHEN env='live' THEN volume_sma END)
        - MAX(CASE WHEN env='backtest' THEN volume_sma END), 6) AS volume_sma_delta
FROM classified
GROUP BY symbol, bar_time, anchor
HAVING COUNT(DISTINCT env) = 2
ORDER BY symbol, bar_time, anchor;
