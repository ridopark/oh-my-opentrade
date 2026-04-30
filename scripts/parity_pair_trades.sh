#!/usr/bin/env bash
# parity_pair_trades.sh — Phase 0.3 of parity_trade_convergence_v2_plan.md
#
# Pairs live trades (table `trades`, env_mode=Paper) with backtest trades
# (table `backtest_run_trades`, scoped by --run-id or latest run for the
# given date) by (underlying, option_right) with a ±15min entry-time skew.
# Emits CSV to stdout with columns:
#
#   underlying, live_ts, bt_ts, direction, live_strike, bt_strike,
#   live_premium, bt_premium, entry_skew_min, pair_status
#
# pair_status ∈ {paired, live_only, backtest_only}.
#
# Usage:
#   scripts/parity_pair_trades.sh --date 2026-04-29 [--run-id <uuid>]
#
# Self-contained: sources .env for TIMESCALEDB_*. Writes to stdout.

set -euo pipefail

DATE=""
RUN_ID=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --date)   DATE="$2"; shift 2 ;;
    --run-id) RUN_ID="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if [[ -z "$DATE" ]]; then
  echo "usage: $0 --date YYYY-MM-DD [--run-id <uuid>]" >&2
  exit 2
fi

REPO="/home/ridopark/src/oh-my-opentrade"
set -a
# shellcheck disable=SC1091
source "$REPO/.env"
set +a

: "${TIMESCALEDB_HOST:?missing}"
: "${TIMESCALEDB_PORT:?missing}"
: "${TIMESCALEDB_USER:?missing}"
: "${TIMESCALEDB_PASSWORD:?missing}"
: "${TIMESCALEDB_NAME:?missing}"

# Resolve run_id: explicit arg, or latest backtest_run that has trades on $DATE.
if [[ -z "$RUN_ID" ]]; then
  RUN_ID=$(PGPASSWORD="$TIMESCALEDB_PASSWORD" psql \
    -h "$TIMESCALEDB_HOST" -p "$TIMESCALEDB_PORT" \
    -U "$TIMESCALEDB_USER" -d "$TIMESCALEDB_NAME" \
    -t -A -c "
      SELECT run_id::text
      FROM backtest_run_trades
      WHERE filled_at >= '${DATE} 00:00:00+00'
        AND filled_at <  '${DATE} 23:59:59+00'
      GROUP BY run_id
      ORDER BY MAX(filled_at) DESC
      LIMIT 1;
    " 2>/dev/null | tr -d '[:space:]')
  if [[ -z "$RUN_ID" ]]; then
    echo "no backtest_run_trades found for $DATE" >&2
    exit 1
  fi
fi

# Pairing SQL: parse OCC option_symbol on the backtest side, FULL OUTER
# JOIN on nearest (underlying, option_right) with time skew ≤ 15min.
read -r -d '' QUERY <<SQL || true
WITH live_t AS (
  SELECT
    t.time              AS live_ts,
    COALESCE(t.underlying, t.symbol) AS underlying,
    t.option_right,
    CASE WHEN t.side='BUY' THEN 'LONG' ELSE 'SHORT' END AS direction,
    t.strike            AS live_strike,
    t.premium           AS live_premium
  FROM trades t
  WHERE t.time >= '${DATE} 00:00:00+00'
    AND t.time <  '${DATE} 23:59:59+00'
    AND t.env_mode = 'Paper'
    AND t.status = 'FILLED'
    AND t.side = 'BUY'
),
bt_t AS (
  SELECT
    bt.filled_at AS bt_ts,
    substring(bt.symbol from '^([A-Z]+)\d') AS underlying,
    CASE substring(bt.symbol from '^[A-Z]+\d{6}([CP])')
      WHEN 'C' THEN 'CALL'
      WHEN 'P' THEN 'PUT'
    END AS option_right,
    bt.direction,
    cast(substring(bt.symbol from '^[A-Z]+\d{6}[CP](\d{8})$') as numeric) / 1000.0 AS bt_strike,
    bt.price AS bt_premium
  FROM backtest_run_trades bt
  WHERE bt.run_id = '${RUN_ID}'::uuid
    AND bt.filled_at >= '${DATE} 00:00:00+00'
    AND bt.filled_at <  '${DATE} 23:59:59+00'
    AND bt.side = 'buy'
),
pairs AS (
  SELECT
    l.underlying, l.live_ts, b.bt_ts, l.direction,
    l.live_strike, b.bt_strike, l.live_premium, b.bt_premium,
    EXTRACT(EPOCH FROM (b.bt_ts - l.live_ts)) / 60.0 AS entry_skew_min,
    'paired' AS pair_status
  FROM live_t l
  LEFT JOIN LATERAL (
    SELECT b.*
    FROM bt_t b
    WHERE b.underlying = l.underlying
      AND b.option_right = l.option_right
      AND ABS(EXTRACT(EPOCH FROM (b.bt_ts - l.live_ts))) <= 15 * 60
    ORDER BY ABS(EXTRACT(EPOCH FROM (b.bt_ts - l.live_ts)))
    LIMIT 1
  ) b ON TRUE
  WHERE b.bt_ts IS NOT NULL
),
live_only AS (
  SELECT
    l.underlying, l.live_ts, NULL::timestamptz AS bt_ts, l.direction,
    l.live_strike, NULL::numeric AS bt_strike,
    l.live_premium, NULL::numeric AS bt_premium,
    NULL::numeric AS entry_skew_min,
    'live_only' AS pair_status
  FROM live_t l
  WHERE NOT EXISTS (
    SELECT 1 FROM pairs p
    WHERE p.live_ts = l.live_ts AND p.underlying = l.underlying
  )
),
bt_only AS (
  SELECT
    b.underlying, NULL::timestamptz AS live_ts, b.bt_ts, b.direction,
    NULL::numeric AS live_strike, b.bt_strike,
    NULL::numeric AS live_premium, b.bt_premium,
    NULL::numeric AS entry_skew_min,
    'backtest_only' AS pair_status
  FROM bt_t b
  WHERE NOT EXISTS (
    SELECT 1 FROM pairs p
    WHERE p.bt_ts = b.bt_ts AND p.underlying = b.underlying
  )
)
SELECT * FROM (
  SELECT * FROM pairs
  UNION ALL
  SELECT * FROM live_only
  UNION ALL
  SELECT * FROM bt_only
) all_pairs
ORDER BY underlying, COALESCE(live_ts, bt_ts);
SQL

echo "underlying,live_ts,bt_ts,direction,live_strike,bt_strike,live_premium,bt_premium,entry_skew_min,pair_status"

PGPASSWORD="$TIMESCALEDB_PASSWORD" psql \
  -h "$TIMESCALEDB_HOST" -p "$TIMESCALEDB_PORT" \
  -U "$TIMESCALEDB_USER" -d "$TIMESCALEDB_NAME" \
  -t -A -F "," -c "$QUERY"
