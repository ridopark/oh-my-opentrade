-- Same-binary parity diff: post-fix live (today's RTH) vs post-fix backtest.
-- Both sets emitted by binary at 84bcd74f (PR #67 merged).

WITH classified AS (
  SELECT
    e.symbol,
    e.payload->'bar'->>'time' AS bar_time,
    CASE WHEN e.payload->>'tag' = 'backtest_bt-75bd3c5b1235508b' THEN 'backtest'
         WHEN e.payload->>'tag' IS NULL THEN 'live'
         ELSE 'other' END AS env,
    (e.payload->'indicators'->>'rsi')::numeric         AS rsi,
    (e.payload->'indicators'->>'ema9')::numeric        AS ema9,
    (e.payload->'indicators'->>'ema21')::numeric       AS ema21,
    (e.payload->'indicators'->>'ema50')::numeric       AS ema50,
    (e.payload->'indicators'->>'macdLine')::numeric    AS macd_line,
    (e.payload->'indicators'->>'macdSignal')::numeric  AS macd_signal,
    (e.payload->'indicators'->>'volumeSMA')::numeric   AS volume_sma
  FROM strategy_signal_events e
  WHERE (e.payload->>'tag' = 'backtest_bt-75bd3c5b1235508b'
         OR (e.payload->>'tag' IS NULL
             AND ts >= '2026-05-01 11:00:00+00'))
),
paired AS (
  SELECT
    l.symbol, l.bar_time,
    l.rsi AS live_rsi, b.rsi AS bt_rsi,
    l.ema9 AS live_ema9, b.ema9 AS bt_ema9,
    l.ema21 AS live_ema21, b.ema21 AS bt_ema21,
    l.ema50 AS live_ema50, b.ema50 AS bt_ema50,
    l.macd_line AS live_macd_line, b.macd_line AS bt_macd_line,
    l.macd_signal AS live_macd_signal, b.macd_signal AS bt_macd_signal,
    l.volume_sma AS live_volume_sma, b.volume_sma AS bt_volume_sma
  FROM (SELECT DISTINCT ON (symbol, bar_time) * FROM classified WHERE env='live')     l
  JOIN (SELECT DISTINCT ON (symbol, bar_time) * FROM classified WHERE env='backtest') b
    ON l.symbol = b.symbol AND l.bar_time = b.bar_time
)
SELECT
  COUNT(*)                                              AS paired_bars,
  COUNT(*) FILTER (WHERE ABS(live_rsi - bt_rsi) > 1e-6)              AS diff_rsi,
  COUNT(*) FILTER (WHERE ABS(live_ema9 - bt_ema9) > 1e-6)            AS diff_ema9,
  COUNT(*) FILTER (WHERE ABS(live_ema21 - bt_ema21) > 1e-6)          AS diff_ema21,
  COUNT(*) FILTER (WHERE ABS(live_ema50 - bt_ema50) > 1e-6)          AS diff_ema50,
  COUNT(*) FILTER (WHERE ABS(live_macd_line - bt_macd_line) > 1e-6)  AS diff_macd_line,
  COUNT(*) FILTER (WHERE ABS(live_macd_signal - bt_macd_signal) > 1e-6) AS diff_macd_signal,
  COUNT(*) FILTER (WHERE ABS(live_volume_sma - bt_volume_sma) > 1e-6) AS diff_volume_sma
FROM paired;
