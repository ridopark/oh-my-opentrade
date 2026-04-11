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

OMO_API="${OMO_API_URL:-http://localhost:8080}"
DB_CONTAINER="${OMO_DB_CONTAINER:-omo-timescaledb}"
DB_USER="${OMO_DB_USER:-opentrade}"
DB_NAME="${OMO_DB_NAME:-opentrade}"

# Verify omo-core is reachable (positions endpoint doubles as health check).
if ! curl -sf "$OMO_API/api/portfolio/positions" > /dev/null 2>&1; then
  err "omo-core not reachable at $OMO_API — is it running?"
  exit 1
fi

dbsql() {
  docker exec -i "$DB_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -tAc "$1"
}

SYMBOL="${1:-}"

step "1/4  Fetching open positions from IBKR (via omo-core)..."
POSITIONS_JSON=$(curl -sf "$OMO_API/api/portfolio/positions")

POSITION_COUNT=$(echo "$POSITIONS_JSON" | python3 -c "
import json, sys
data = json.load(sys.stdin)
positions = data.get('positions', data) if isinstance(data, dict) else data
if isinstance(positions, list):
    if '${SYMBOL}':
        positions = [p for p in positions if p.get('symbol','') == '${SYMBOL}']
    print(len(positions))
else:
    print(0)
" 2>/dev/null || echo "0")

if [ "$POSITION_COUNT" -eq 0 ]; then
  info "No matching positions on broker. Skipping to DB reconciliation."
else
  echo "$POSITIONS_JSON" | python3 -c "
import json, sys
data = json.load(sys.stdin)
positions = data.get('positions', data) if isinstance(data, dict) else data
sym_filter = '${SYMBOL}'
print()
print(f'  {\"Symbol\":<25} {\"Side\":<6} {\"Qty\":>10} {\"Entry\":>12} {\"Current\":>12} {\"P&L\":>12}')
print(f'  {\"-\"*25} {\"-\"*6} {\"-\"*10} {\"-\"*12} {\"-\"*12} {\"-\"*12}')
for p in positions:
    if sym_filter and p.get('symbol','') != sym_filter:
        continue
    print(f'  {p[\"symbol\"]:<25} {p.get(\"side\",\"?\"):<6} {p.get(\"quantity\",0):>10.2f} {p.get(\"avg_entry_price\",0):>12.2f} {p.get(\"current_price\",0):>12.2f} {p.get(\"unrealized_pnl\",0):>+12.2f}')
"

  step "2/4  Liquidating positions via IBKR..."
  if [ -z "$SYMBOL" ]; then
    RESULT=$(curl -sf -X DELETE "$OMO_API/api/portfolio/positions")
    echo "$RESULT" | python3 -c "
import json, sys
data = json.load(sys.stdin)
results = data.get('results', [data]) if isinstance(data, dict) else data
for r in results:
    sym = r.get('symbol', '?')
    status = r.get('status', r.get('order_id', '?'))
    print(f'  {sym}: {status}')
" 2>/dev/null || echo "  $RESULT"
  else
    RESULT=$(curl -sf -X DELETE "$OMO_API/api/portfolio/positions/$SYMBOL")
    STATUS=$(echo "$RESULT" | python3 -c "
import json, sys
r = json.load(sys.stdin)
oid = r.get('order_id', '')
status = r.get('status', 'unknown')
print(f'{status} (order_id={oid})' if oid else status)
" 2>/dev/null || echo "unknown")
    info "$SYMBOL -> $STATUS"
  fi

  info "Waiting for fills..."
  sleep 5

  REMAINING=$(curl -sf "$OMO_API/api/portfolio/positions" | python3 -c "
import json, sys
data = json.load(sys.stdin)
positions = data.get('positions', data) if isinstance(data, dict) else data
sym_filter = '${SYMBOL}'
if isinstance(positions, list):
    if sym_filter:
        positions = [p for p in positions if p.get('symbol','') == sym_filter]
    print(len(positions))
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

    # Try to get current price from broker positions, fall back to DB.
    PRICE=$(curl -sf "$OMO_API/api/portfolio/positions" 2>/dev/null | python3 -c "
import json, sys
data = json.load(sys.stdin)
positions = data.get('positions', data) if isinstance(data, dict) else data
sym = '${SYM}'
if isinstance(positions, list):
    for p in positions:
        if p.get('symbol','') == sym:
            print(p.get('current_price', p.get('avg_entry_price', 0))); sys.exit(0)
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
