#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${GREEN}[liquidate]${NC} $*"; }
warn()  { echo -e "${YELLOW}[liquidate]${NC} $*"; }
err()   { echo -e "${RED}[liquidate]${NC} $*"; }
step()  { echo -e "${CYAN}[liquidate]${NC} $*"; }

if [ -f "$PROJECT_ROOT/.env" ]; then
  set -a
  source "$PROJECT_ROOT/.env"
  set +a
fi

if [ -z "${APCA_API_KEY_ID:-}" ] || [ -z "${APCA_API_SECRET_KEY:-}" ]; then
  err "Missing APCA_API_KEY_ID or APCA_API_SECRET_KEY in .env"
  exit 1
fi

BASE_URL="${APCA_BASE_URL:-https://paper-api.alpaca.markets}"
DB_CONTAINER="${OMO_DB_CONTAINER:-omo-timescaledb}"
DB_USER="${OMO_DB_USER:-opentrade}"
DB_NAME="${OMO_DB_NAME:-opentrade}"

alpaca() {
  curl -s "$@" \
    -H "APCA-API-KEY-ID: $APCA_API_KEY_ID" \
    -H "APCA-API-SECRET-KEY: $APCA_API_SECRET_KEY"
}

dbsql() {
  docker exec -i "$DB_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -tAc "$1"
}

SYMBOL="${1:-}"

step "1/4  Fetching open positions..."
POSITIONS_JSON=$(alpaca "$BASE_URL/v2/positions")

POSITION_COUNT=$(echo "$POSITIONS_JSON" | python3 -c "
import json, sys
data = json.load(sys.stdin)
if isinstance(data, list):
    print(len(data))
else:
    print(0)
" 2>/dev/null || echo "0")

if [ "$POSITION_COUNT" -eq 0 ]; then
  info "No open positions on broker. Skipping to DB reconciliation."
else
  echo "$POSITIONS_JSON" | python3 -c "
import json, sys
positions = json.load(sys.stdin)
sym_filter = '${SYMBOL}'
print()
print(f'  {\"Symbol\":<10} {\"Side\":<6} {\"Qty\":>14} {\"Entry\":>12} {\"Current\":>12} {\"P&L\":>12}')
print(f'  {\"-\"*10} {\"-\"*6} {\"-\"*14} {\"-\"*12} {\"-\"*12} {\"-\"*12}')
for p in positions:
    if sym_filter and p['symbol'].replace('/','') != sym_filter.replace('/','') and p['symbol'] != sym_filter:
        continue
    print(f'  {p[\"symbol\"]:<10} {p[\"side\"]:<6} {float(p[\"qty\"]):>14.8f} {float(p[\"avg_entry_price\"]):>12.2f} {float(p[\"current_price\"]):>12.2f} {float(p[\"unrealized_pl\"]):>+12.2f}')
"

  step "2/4  Liquidating positions on broker..."
  if [ -z "$SYMBOL" ]; then
    RESULT=$(alpaca -X DELETE "$BASE_URL/v2/positions?cancel_orders=true")
    echo "$RESULT" | python3 -c "
import json, sys
data = json.load(sys.stdin)
if not isinstance(data, list): data = [data]
for r in data:
    sym = r.get('symbol', '?')
    code = r.get('status', r.get('body', {}).get('status', '?'))
    print(f'  {sym}: {code}')
" 2>/dev/null || echo "  $RESULT"
  else
    alpaca -X DELETE "$BASE_URL/v2/orders?symbols=$SYMBOL" > /dev/null 2>&1 || true
    RESULT=$(alpaca -X DELETE "$BASE_URL/v2/positions/$SYMBOL")
    STATUS=$(echo "$RESULT" | python3 -c "
import json, sys
r = json.load(sys.stdin)
print(r.get('status', r.get('message', 'unknown')))
" 2>/dev/null || echo "unknown")
    info "$SYMBOL → $STATUS"
  fi

  info "Waiting for fills..."
  sleep 3

  REMAINING=$(alpaca "$BASE_URL/v2/positions" | python3 -c "
import json, sys
data = json.load(sys.stdin)
sym_filter = '${SYMBOL}'
if isinstance(data, list):
    if sym_filter:
        data = [p for p in data if p['symbol'].replace('/','') == sym_filter.replace('/','') or p['symbol'] == sym_filter]
    print(len(data))
else:
    print(0)
" 2>/dev/null || echo "?")

  if [ "$REMAINING" = "0" ]; then
    info "Broker is flat."
  else
    warn "$REMAINING position(s) still open — may need manual intervention."
  fi
fi

step "3/4  Checking OMO trade DB for orphaned positions..."
ORPHANS=$(dbsql "
  SELECT symbol,
         SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END) as net_qty
  FROM trades
  WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '90 days'
  GROUP BY symbol
  HAVING ABS(SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END)) > 0.0001
  ORDER BY symbol;
")

if [ -z "$ORPHANS" ]; then
  info "Trade DB is clean — no orphaned positions."
else
  step "4/4  Inserting reconciliation SELLs..."

  echo "$ORPHANS" | while IFS='|' read -r SYM NET_QTY; do
    SYM=$(echo "$SYM" | xargs)
    NET_QTY=$(echo "$NET_QTY" | xargs)

    if [ -n "$SYMBOL" ]; then
      NORM_SYM=$(echo "$SYM" | tr -d '/')
      NORM_ARG=$(echo "$SYMBOL" | tr -d '/')
      if [ "$NORM_SYM" != "$NORM_ARG" ] && [ "$SYM" != "$SYMBOL" ]; then
        continue
      fi
    fi

    PRICE=$(alpaca "$BASE_URL/v2/positions" 2>/dev/null | python3 -c "
import json, sys
data = json.load(sys.stdin)
sym = '${SYM}'
if isinstance(data, list):
    for p in data:
        if p['symbol'] == sym:
            print(p['current_price']); sys.exit(0)
# Position already closed — use latest bar from DB
" 2>/dev/null || echo "")

    if [ -z "$PRICE" ]; then
      PRICE=$(dbsql "
        SELECT close FROM market_bars
        WHERE symbol = '$SYM' AND timeframe = '1m'
        ORDER BY time DESC LIMIT 1;
      " | xargs)
    fi

    if [ -z "$PRICE" ]; then
      warn "  $SYM: no price found — skipping (manually reconcile)"
      continue
    fi

    if (( $(echo "$NET_QTY > 0" | bc -l) )); then
      SIDE="SELL"
      QTY="$NET_QTY"
    else
      SIDE="BUY"
      QTY=$(echo "$NET_QTY * -1" | bc -l)
    fi

    dbsql "
      INSERT INTO trades (time, account_id, env_mode, trade_id, symbol, side, quantity, price, commission, status, strategy, rationale)
      VALUES (NOW(), 'default', 'Paper', gen_random_uuid(), '$SYM', '$SIDE', $QTY, $PRICE, 0, 'FILLED', 'reconciliation', 'force-liquidate script');
    " > /dev/null

    info "  $SYM: $SIDE $QTY @ \$$PRICE (reconciliation)"
  done

  STILL_ORPHANED=$(dbsql "
    SELECT count(*) FROM (
      SELECT symbol
      FROM trades
      WHERE env_mode = 'Paper' AND time >= NOW() - INTERVAL '90 days'
      GROUP BY symbol
      HAVING ABS(SUM(CASE WHEN side='BUY' THEN quantity ELSE -quantity END)) > 0.0001
    ) sub;
  " | xargs)

  if [ "$STILL_ORPHANED" = "0" ]; then
    info "Trade DB reconciled — all net positions zeroed."
  else
    warn "$STILL_ORPHANED symbol(s) still have residual — check manually."
  fi
fi

echo
warn "Restart omo-core — position monitor bootstraps from trade DB on startup and has no hot-reload."
warn "Without restart, it will keep monitoring/revaluating liquidated positions as if they're still open."
warn "Run: ./scripts/shutdown.sh && ./scripts/start.sh"
echo
info "Done. Broker + DB are in sync."
