#!/usr/bin/env bash
# One-shot verification for PR #24 (parity-avwap-cross-day-state).
# Queries strategy_signal_events for AAPL at the first 5m close of the
# given DATE and posts a Discord summary classified by pd_high.barCount.
#
# Args:
#   $1 LABEL  — e.g. "Day 0" or "Day +1"   (default: "Day +1")
#   $2 DATE   — YYYY-MM-DD                 (default: 2026-05-01)
#
# Self-contained: sources .env for TIMESCALEDB_* and DISCORD_WEBHOOK_URL.
# Designed to run from cron with no inherited shell env.

set -euo pipefail

LABEL="${1:-Day +1}"
DATE="${2:-2026-05-01}"

REPO="/home/ridopark/src/oh-my-opentrade"
ENV_FILE="$REPO/.env"

if [[ ! -f "$ENV_FILE" ]]; then
  echo "ERROR: $ENV_FILE not found" >&2
  exit 1
fi
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

: "${TIMESCALEDB_HOST:?missing}"
: "${TIMESCALEDB_PORT:?missing}"
: "${TIMESCALEDB_USER:?missing}"
: "${TIMESCALEDB_PASSWORD:?missing}"
: "${TIMESCALEDB_NAME:?missing}"
: "${DISCORD_WEBHOOK_URL:?missing}"

QUERY="
SELECT to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') AS ts_utc,
       payload->'avwapState'->'anchors'->'pd_high'->>'vwap'      AS pd_high_vwap,
       payload->'avwapState'->'anchors'->'pd_high'->>'barCount'  AS pd_high_bars,
       payload->'avwapState'->'anchors'->'pd_low'->>'vwap'       AS pd_low_vwap,
       payload->'avwapState'->'anchors'->'pd_low'->>'barCount'   AS pd_low_bars
FROM strategy_signal_events
WHERE symbol = 'AAPL'
  AND ts >= '${DATE} 13:34:00+00' AND ts < '${DATE} 13:36:00+00'
  AND signal_id NOT LIKE '%backtest%'
ORDER BY ts ASC
LIMIT 5;
"

OUT_FILE=/tmp/parity-verify.out
PGPASSWORD="$TIMESCALEDB_PASSWORD" psql \
  -h "$TIMESCALEDB_HOST" -p "$TIMESCALEDB_PORT" \
  -U "$TIMESCALEDB_USER" -d "$TIMESCALEDB_NAME" \
  -t -A -F "|" -c "$QUERY" > "$OUT_FILE"

ROW=$(head -1 "$OUT_FILE" || true)
if [[ -z "${ROW:-}" ]]; then
  TITLE="parity-avwap-cross-day-state $LABEL verify: NO ROW"
  COLOR="red"
  HIGH_BARS="(no row)"
  HIGH_VWAP="(no row)"
  LOW_BARS="(no row)"
  LOW_VWAP="(no row)"
else
  IFS='|' read -r TS_UTC HIGH_VWAP HIGH_BARS LOW_VWAP LOW_BARS <<< "$ROW"
  if [[ -z "$HIGH_BARS" ]]; then
    TITLE="parity-avwap-cross-day-state $LABEL verify: NULL barCount"
    COLOR="red"
  elif (( HIGH_BARS <= 410 )); then
    TITLE="parity-avwap-cross-day-state $LABEL verify: PASS"
    COLOR="green"
  elif (( HIGH_BARS <= 780 )); then
    TITLE="parity-avwap-cross-day-state $LABEL verify: SUSPECT"
    COLOR="yellow"
  else
    TITLE="parity-avwap-cross-day-state $LABEL verify: FAIL"
    COLOR="red"
  fi
fi

BODY=$(printf 'AAPL @ first 5m close of %s RTH (%s):\n```\nts_utc=%s\npd_high.vwap=%s\npd_high.barCount=%s   (expected ~one fresh day RTH portion; ~780 = 2-day inflation)\npd_low.vwap=%s\npd_low.barCount=%s\n```\nFull row(s):\n```\n%s\n```' \
  "$DATE" "$LABEL" \
  "${TS_UTC:-n/a}" "${HIGH_VWAP:-n/a}" "${HIGH_BARS:-n/a}" "${LOW_VWAP:-n/a}" "${LOW_BARS:-n/a}" \
  "$(cat "$OUT_FILE")")

"$REPO/scripts/discord-notify.sh" "$TITLE" "$BODY" "$COLOR"

echo "[verify-cross-day-fix] done: $TITLE pd_high.barCount=${HIGH_BARS:-n/a}"
