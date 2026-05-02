WITH classified AS (
  SELECT
    e.symbol,
    e.payload->'bar'->>'time' AS bar_time,
    CASE WHEN e.payload->>'tag' = 'backtest_bt-a8069d6d2c126b92' THEN 'backtest'
         WHEN e.payload->>'tag' IS NULL THEN 'live'
         ELSE 'other' END AS env,
    (e.payload->'indicators'->>'rsi')::numeric         AS rsi,
    (e.payload->'indicators'->>'ema9')::numeric        AS ema9,
    (e.payload->'indicators'->>'ema21')::numeric       AS ema21,
    (e.payload->'indicators'->>'ema50')::numeric       AS ema50,
    (e.payload->'indicators'->>'macdLine')::numeric    AS macd_line
  FROM strategy_signal_events e
  WHERE (e.payload->>'tag' = 'backtest_bt-a8069d6d2c126b92'
         OR (e.payload->>'tag' IS NULL
             AND ts >= '2026-04-29 00:00:00+00'
             AND ts <  '2026-05-01 00:00:00+00'))
),
paired AS (
  SELECT
    l.symbol, l.bar_time,
    l.rsi AS live_rsi, b.rsi AS bt_rsi,
    l.ema9 AS live_ema9, b.ema9 AS bt_ema9,
    l.ema21 AS live_ema21, b.ema21 AS bt_ema21,
    l.ema50 AS live_ema50, b.ema50 AS bt_ema50,
    l.macd_line AS live_macd_line, b.macd_line AS bt_macd_line
  FROM (SELECT DISTINCT ON (symbol, bar_time) * FROM classified WHERE env='live')     l
  JOIN (SELECT DISTINCT ON (symbol, bar_time) * FROM classified WHERE env='backtest') b
    ON l.symbol = b.symbol AND l.bar_time = b.bar_time
)
SELECT symbol, bar_time,
       round(live_rsi, 6) AS live_rsi, round(bt_rsi, 6) AS bt_rsi,
       round(ABS(live_rsi - bt_rsi), 6) AS d_rsi,
       round(live_ema21, 4) AS live_ema21, round(bt_ema21, 4) AS bt_ema21,
       round(ABS(live_ema21 - bt_ema21), 4) AS d_ema21,
       round(ABS(live_ema50 - bt_ema50), 4) AS d_ema50,
       round(ABS(live_macd_line - bt_macd_line), 6) AS d_macd
FROM paired
WHERE ABS(live_ema50 - bt_ema50) > 1e-6
ORDER BY ABS(live_ema50 - bt_ema50) DESC
LIMIT 15;
