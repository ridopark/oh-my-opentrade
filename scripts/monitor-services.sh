#!/usr/bin/env bash
# monitor-services.sh — one-shot monitoring report for omo-core
# Usage: ./scripts/monitor-services.sh
# Run repeatedly (e.g. watch -n 60 ./scripts/monitor-services.sh) for continuous monitoring.

set -uo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="$ROOT_DIR/.env"
LOKI="http://localhost:3100"
DB_EXEC="docker exec -i omo-timescaledb psql -U opentrade -d opentrade"

# ── colours ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'
BOLD='\033[1m'; RESET='\033[0m'
ok()   { echo -e "  ${GREEN}✓${RESET} $*"; }
warn() { echo -e "  ${YELLOW}⚠${RESET}  $*"; }
crit() { echo -e "  ${RED}✗${RESET} $*"; }

# ── Step 0: prerequisites ─────────────────────────────────────────────────────
echo -e "\n${BOLD}── Prerequisites ──────────────────────────────────────────${RESET}"

LOKI_OK=0
curl -sf "$LOKI/ready" > /dev/null 2>&1 && { ok "Loki"; LOKI_OK=1; } || crit "Loki DOWN"

DB_OK=0
docker exec omo-timescaledb pg_isready -U opentrade -d opentrade > /dev/null 2>&1 \
  && { ok "TimescaleDB"; DB_OK=1; } || crit "TimescaleDB DOWN"

ALPACA_OK=0
if [[ -f "$ENV_FILE" ]]; then
  source "$ENV_FILE"
  HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
    https://paper-api.alpaca.markets/v2/account \
    -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
    -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY")
  [[ "$HTTP_CODE" == "200" ]] && { ok "Alpaca Paper API"; ALPACA_OK=1; } \
    || crit "Alpaca Paper API (HTTP $HTTP_CODE)"
else
  crit "Alpaca — .env not found at $ENV_FILE"
fi

# ── Step 1: detect last restart time ─────────────────────────────────────────
if [[ $LOKI_OK -eq 1 ]]; then
  SINCE_START=$(curl -sG "$LOKI/loki/api/v1/query_range" \
    --data-urlencode 'query={job="omo-core"} |~ "signal debate enricher subscribed|risk sizer subscribed"' \
    --data-urlencode 'limit=5' \
    --data-urlencode 'direction=backward' | python3 -c "
import json, sys, time
data = json.load(sys.stdin)
ts_list = [int(ts) for stream in data['data']['result'] for ts, _ in stream['values']]
print(max(ts_list) if ts_list else int((time.time() - 600) * 1e9))
")

  UPTIME_MINUTES=$(python3 -c "import time; print(max(1, int((time.time()*1e9 - $SINCE_START) / 60e9)))")
  START_TIME=$(python3 -c "
from datetime import datetime, timezone, timedelta
ct = datetime.fromtimestamp($SINCE_START / 1e9, tz=timezone(timedelta(hours=-6)))
print(ct.strftime('%I:%M %p CT'))
")
  NOW_TIME=$(date +"%I:%M %p %Z")
else
  SINCE_START=$(python3 -c "import time; print(int((time.time() - 600) * 1e9))")
  UPTIME_MINUTES=10
  START_TIME="unknown"
  NOW_TIME=$(date +"%I:%M %p %Z")
fi

echo -e "\n${BOLD}── Monitoring Report — $NOW_TIME (since restart at $START_TIME, uptime: ${UPTIME_MINUTES}m) ──${RESET}"

# ── Step 2: Loki errors ───────────────────────────────────────────────────────
echo -e "\n${BOLD}── Errors (since restart) ──────────────────────────────────${RESET}"
if [[ $LOKI_OK -eq 1 ]]; then
  ERROR_OUT=$(curl -sG "$LOKI/loki/api/v1/query_range" \
    --data-urlencode 'query={job="omo-core"} |~ "error|ERROR|panic|fatal|FATAL"' \
    --data-urlencode 'limit=100' \
    --data-urlencode 'direction=forward' \
    --data-urlencode "start=$SINCE_START" | python3 -c "
import json, sys
data = json.load(sys.stdin)
lines = []
for stream in data['data']['result']:
    for ts, msg in stream['values']:
        outer = json.loads(msg) if msg.startswith('{') else {'log': msg}
        lines.append((ts, outer.get('log', msg)))
lines.sort(key=lambda x: x[0])
for _, line in lines[-20:]:
    print(line.rstrip())
print(f'__COUNT__:{len(lines)}')
")
  ERROR_COUNT=$(echo "$ERROR_OUT" | grep '__COUNT__:' | sed 's/.*__COUNT__://' || echo 0)
  CLEAN_ERRORS=$(echo "$ERROR_OUT" | grep -v '__COUNT__:' || true)
  if [[ "$ERROR_COUNT" -eq 0 ]]; then
    ok "No errors"
  else
    crit "$ERROR_COUNT error(s) found:"
    echo "$CLEAN_ERRORS" | head -20 | sed 's/^/    /'
  fi
else
  warn "Loki unavailable — skipping"
fi

# ── Step 3: Loki warnings ─────────────────────────────────────────────────────
echo -e "\n${BOLD}── Warnings (since restart) ────────────────────────────────${RESET}"
if [[ $LOKI_OK -eq 1 ]]; then
  curl -sG "$LOKI/loki/api/v1/query_range" \
    --data-urlencode 'query={job="omo-core"} |~ "WARN|warn"' \
    --data-urlencode 'limit=200' \
    --data-urlencode 'direction=forward' \
    --data-urlencode "start=$SINCE_START" | python3 -c "
import json, sys
from collections import Counter
data = json.load(sys.stdin)
lines = []
for stream in data['data']['result']:
    for ts, msg in stream['values']:
        outer = json.loads(msg) if msg.startswith('{') else {'log': msg}
        lines.append(outer.get('log', msg))
cats = Counter()
for line in lines:
    if 'veto gate' in line:             cats['veto_gate_blocks'] += 1
    elif 'pre-LLM veto' in line:        cats['pre_llm_veto'] += 1
    elif 'entry rejected' in line:      cats['entry_rejected'] += 1
    elif 'exit cooldown' in line:       cats['exit_cooldown'] += 1
    elif 'AI direction gate' in line:   cats['ai_direction_gate'] += 1
    elif 'AI debate' in line:           cats['ai_debate_fail'] += 1
    elif 'trading_window' in line:      cats['trading_window'] += 1
    elif 'spread_guard' in line or 'slippage' in line: cats['spread_slippage'] += 1
    elif 'position_gate' in line:       cats['position_gate'] += 1
    elif 'revaluation gate' in line:    cats['revaluation_gate'] += 1
    elif 'websocket' in line.lower():   cats['websocket'] += 1
    elif 'reconnect' in line.lower():   cats['reconnect'] += 1
    elif 'notional too small' in line:  cats['notional_too_small'] += 1
    elif 'no builtin implementation' in line: cats['legacy_hook_warn'] += 1
    elif 'PendingEntry timeout' in line: cats['pending_entry_timeout'] += 1
    elif 'consecutive loss' in line:    cats['consecutive_loss'] += 1
    else:                               cats['other'] += 1
total = sum(cats.values())
print(f'Total: {total}')
for cat, cnt in cats.most_common():
    if cat in ('trading_window', 'position_gate', 'legacy_hook_warn', 'pending_entry_timeout'):
        print(f'  {cat}: {cnt}  (expected)')
    else:
        print(f'  {cat}: {cnt}')
"
fi

# ── Step 4: frozen bot detection ─────────────────────────────────────────────
echo -e "\n${BOLD}── Symbol Activity (last 3 min) ────────────────────────────${RESET}"
if [[ $LOKI_OK -eq 1 ]]; then
  THREE_MIN_AGO=$(date -u -d '3 minutes ago' +%s)000000000
  curl -sG "$LOKI/loki/api/v1/query_range" \
    --data-urlencode 'query={job="omo-core"} |~ "bar processed"' \
    --data-urlencode 'limit=100' \
    --data-urlencode 'direction=backward' \
    --data-urlencode "start=$THREE_MIN_AGO" | python3 -c "
import json, sys, re
from datetime import datetime, timezone, timedelta
data = json.load(sys.stdin)
symbols_seen = set()
for stream in data['data']['result']:
    for ts, msg in stream['values']:
        outer = json.loads(msg) if msg.startswith('{') else {'log': msg}
        m = re.search(r'symbol=(\S+)', outer.get('log', msg))
        if m: symbols_seen.add(m.group(1))
crypto  = {'BTC/USD','ETH/USD','SOL/USD','AVAX/USD','DOGE/USD','PEPE/USD'}
equity  = {'AAPL','MSFT','GOOGL','AMZN','TSLA','U','PLTR','META','NVDA','AMD'}
et = datetime.now(timezone(timedelta(hours=-5)))
is_rth  = et.weekday() < 5 and 930 <= et.hour * 100 + et.minute <= 1600
expected = crypto | equity if is_rth else crypto
label    = 'equity+crypto (RTH)' if is_rth else 'crypto only (outside RTH)'
missing  = expected - symbols_seen
status   = 'OK' if not missing else 'WARN'
print(f'{status}: {len(symbols_seen)}/{len(expected)} symbols active ({label})')
if missing:
    print(f'  Missing: {sorted(missing)}')
"
fi

# ── Step 5: DB position drift ─────────────────────────────────────────────────
echo -e "\n${BOLD}── Positions (DB vs Broker) ────────────────────────────────${RESET}"
if [[ $DB_OK -eq 1 ]]; then
  DB_POSITIONS=$($DB_EXEC -t -c "
SELECT symbol,
       ROUND(SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END)::numeric, 6) AS net_qty
FROM trades
WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '7 days'
GROUP BY symbol
HAVING ABS(SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END)) > 0.0001
ORDER BY symbol;
" 2>&1)

  if echo "$DB_POSITIONS" | grep -q "0 rows\|^$"; then
    ok "DB: no open positions"
  else
    echo "  DB positions:"
    echo "$DB_POSITIONS" | sed 's/^/    /'
  fi
fi

if [[ $ALPACA_OK -eq 1 ]]; then
  BROKER_POS=$(curl -s https://paper-api.alpaca.markets/v2/positions \
    -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
    -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY" | python3 -c "
import json, sys
positions = json.load(sys.stdin)
if not positions or isinstance(positions, dict):
    print('OK: no broker positions')
    sys.exit(0)
for p in positions:
    pnl = float(p.get('unrealized_pl', 0))
    print(f\"  {p['symbol']:<12} {p['side']:<5} qty={float(p['qty']):>12.6f}  pnl=\${pnl:>+,.2f}\")
")
  echo "$BROKER_POS" | sed 's/^/  /'
fi

# ── Step 6: stuck orders ──────────────────────────────────────────────────────
echo -e "\n${BOLD}── Stuck Orders (>10 min) ──────────────────────────────────${RESET}"
if [[ $DB_OK -eq 1 ]]; then
  STUCK=$($DB_EXEC -t -c "
SELECT broker_order_id, symbol, side, status, time
FROM orders
WHERE status NOT IN ('filled', 'canceled', 'expired', 'rejected')
  AND time < NOW() - INTERVAL '10 minutes'
ORDER BY time DESC LIMIT 10;
" 2>&1)
  if echo "$STUCK" | grep -qE "0 rows|^[[:space:]]*$"; then
    ok "No stuck orders"
  else
    crit "Stuck orders found:"
    echo "$STUCK" | sed 's/^/    /'
  fi
fi

# ── Step 7: duplicate trades ──────────────────────────────────────────────────
echo -e "\n${BOLD}── Duplicate Trades ────────────────────────────────────────${RESET}"
if [[ $DB_OK -eq 1 ]]; then
  DUPES=$($DB_EXEC -t -c "
SELECT trade_id, COUNT(*) AS cnt
FROM trades
WHERE time >= NOW() - INTERVAL '1 day'
GROUP BY trade_id HAVING COUNT(*) > 1 LIMIT 5;
" 2>&1)
  if echo "$DUPES" | grep -qE "0 rows|^[[:space:]]*$"; then
    ok "No duplicates"
  else
    crit "Duplicate trade IDs found:"
    echo "$DUPES" | sed 's/^/    /'
  fi
fi

# ── Step 8: bar gaps in DB ────────────────────────────────────────────────────
echo -e "\n${BOLD}── Bar Gaps (DB, last 30 min) ──────────────────────────────${RESET}"
if [[ $DB_OK -eq 1 ]]; then
  BAR_GAPS=$($DB_EXEC -t -c "
SELECT symbol,
       MAX(time) AS last_bar,
       ROUND(EXTRACT(EPOCH FROM (NOW() - MAX(time)))/60) AS minutes_stale
FROM market_bars
WHERE timeframe = '1m' AND time >= NOW() - INTERVAL '30 minutes'
GROUP BY symbol
HAVING EXTRACT(EPOCH FROM (NOW() - MAX(time)))/60 > 5
ORDER BY minutes_stale DESC;
" 2>&1 || true)
  if [[ -z "$(echo "$BAR_GAPS" | tr -d '[:space:]')" ]]; then
    ok "No stale symbols"
  else
    warn "Stale symbols:"
    echo "$BAR_GAPS" | sed 's/^/    /'
  fi
fi

# ── Step 9: trade activity ────────────────────────────────────────────────────
echo -e "\n${BOLD}── Trade Activity (last ${UPTIME_MINUTES}m) ─────────────────────────────${RESET}"
if [[ $DB_OK -eq 1 ]]; then
  $DB_EXEC -c "
SELECT strategy, side, COUNT(*) AS trades,
       ROUND(SUM(quantity * price)::numeric, 2) AS notional
FROM trades
WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '${UPTIME_MINUTES} minutes'
GROUP BY strategy, side
ORDER BY strategy, side;
" 2>&1 | sed 's/^/  /' || true

  RUNAWAYS=$($DB_EXEC -t -c "
SELECT strategy,
       COUNT(*) AS trades,
       ROUND((COUNT(*) / GREATEST(${UPTIME_MINUTES}::float / 60, 0.017))::numeric, 1) AS trades_per_hour
FROM trades
WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '${UPTIME_MINUTES} minutes'
GROUP BY strategy
HAVING COUNT(*) / GREATEST(${UPTIME_MINUTES}::float / 60, 0.017) > 20
ORDER BY trades_per_hour DESC;
" 2>&1 || true)
  if [[ -n "$(echo "$RUNAWAYS" | tr -d '[:space:]')" ]]; then
    warn "Possible runaway strategy detected: $RUNAWAYS"
  fi
fi

# ── Step 10: portfolio summary ────────────────────────────────────────────────
echo -e "\n${BOLD}── Portfolio ───────────────────────────────────────────────${RESET}"
if [[ $ALPACA_OK -eq 1 ]]; then
  curl -s https://paper-api.alpaca.markets/v2/account \
    -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
    -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY" | python3 -c "
import json, sys
a = json.load(sys.stdin)
equity        = float(a['equity'])
last_equity   = float(a['last_equity'])
buying_power  = float(a['buying_power'])
daily_change  = equity - last_equity
daily_pct     = (daily_change / last_equity * 100) if last_equity else 0
print(f'  Equity:       \${equity:>12,.2f}')
print(f'  Daily P&L:    \${daily_change:>+12,.2f}  ({daily_pct:+.2f}%)')
print(f'  Buying power: \${buying_power:>12,.2f}')
if daily_pct < -5:
    print(f'  CRITICAL: drawdown {daily_pct:.2f}% — consider pausing all strategies')
elif daily_pct < -2:
    print(f'  WARNING: drawdown {daily_pct:.2f}% exceeds -2% threshold')
"
fi

echo -e "\n${BOLD}────────────────────────────────────────────────────────────${RESET}\n"
