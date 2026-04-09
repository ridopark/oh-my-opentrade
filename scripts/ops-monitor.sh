#!/usr/bin/env bash
# ops-monitor.sh — Lean live-trading health check
# Exit 0 + no stdout = healthy.  Exit 1 + one-line-per-issue = problems.
# Format: SEVERITY|summary|detail
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
[ -f "$ROOT_DIR/.env" ] && source "$ROOT_DIR/.env"

LOKI="http://localhost:3100"
PROM="http://localhost:9090"
OMO="http://localhost:8080"
DB_EXEC="docker exec -i omo-timescaledb psql -U opentrade -d opentrade -tAc"

ISSUES=()

# ── 1. omo-core alive ────────────────────────────────────────────────────────
HEALTH=$(curl -sf "$OMO/healthz/services" 2>/dev/null) || {
  ISSUES+=("CRITICAL|omo-core unreachable|healthz non-200")
  # Can't check further if omo-core is dead
  printf '%s\n' "${ISSUES[@]}"
  exit 1
}

# ── 2. Feed health (from healthz response) ───────────────────────────────────
UNHEALTHY_FEEDS=$(echo "$HEALTH" | python3 -c "
import json,sys
try:
  data=json.load(sys.stdin)
  for s in data.get('services',[]):
    if not s.get('healthy',True):
      print(s.get('name','?')+': '+s.get('detail',''))
except: pass
" 2>/dev/null)

if [ -n "$UNHEALTHY_FEEDS" ]; then
  while IFS= read -r line; do
    ISSUES+=("WARNING|unhealthy service|$line")
  done <<< "$UNHEALTHY_FEEDS"
fi

# ── 3. Circuit breaker ───────────────────────────────────────────────────────
CB_VAL=$(curl -sf "$PROM/api/v1/query" --data-urlencode \
  'query=omo_risk_circuit_breaker_active' 2>/dev/null \
  | python3 -c "
import json,sys
try:
  r=json.load(sys.stdin)['data']['result']
  print(max(float(v[1]) for v in r) if r else '0')
except: print('0')
" 2>/dev/null || echo "0")

if [ "$CB_VAL" != "0" ] && [ "$CB_VAL" != "0.0" ]; then
  ISSUES+=("CRITICAL|circuit breaker active|value=$CB_VAL")
fi

# ── 4. Recent errors (count only — no content) ───────────────────────────────
ERR_COUNT=$(curl -sG "$LOKI/loki/api/v1/query" \
  --data-urlencode "query=count_over_time({job=\"omo-core\"} |~ \"error|ERROR|panic\" [5m])" \
  2>/dev/null | python3 -c "
import json,sys
try:
  r=json.load(sys.stdin)['data']['result']
  print(int(sum(float(v[1]) for v in r)) if r else 0)
except: print(0)
" 2>/dev/null || echo "0")

if [ "$ERR_COUNT" -gt 10 ] 2>/dev/null; then
  ISSUES+=("WARNING|$ERR_COUNT errors in last 5 min|error_count=$ERR_COUNT")
fi

# ── 5. Stuck orders (submitted > 60s ago, not terminal) ──────────────────────
STUCK=$($DB_EXEC "
  SELECT symbol || '|' || id || '|' || EXTRACT(EPOCH FROM NOW()-created_at)::int || 's'
  FROM orders
  WHERE status IN ('SUBMITTED','PENDING')
    AND created_at < NOW() - INTERVAL '60 seconds'
  LIMIT 5;
" 2>/dev/null || echo "")

if [ -n "$STUCK" ]; then
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    SYM=$(echo "$line" | cut -d'|' -f1)
    OID=$(echo "$line" | cut -d'|' -f2)
    AGE=$(echo "$line" | cut -d'|' -f3)
    ISSUES+=("WARNING|stuck order $SYM|order_id=$OID age=$AGE")
  done <<< "$STUCK"
fi

# ── 6. Catastrophic loss check (daily P&L vs equity) ─────────────────────────
LOSS_CHECK=$(curl -sf "$OMO/api/portfolio/account" 2>/dev/null | python3 -c "
import json,sys
try:
  d=json.load(sys.stdin)
  equity=float(d.get('equity',0))
  daily_pnl=float(d.get('daily_pnl',0))
  if equity > 0 and daily_pnl < 0:
    loss_pct=abs(daily_pnl)/equity*100
    if loss_pct >= 10.0:
      print(f'EMERGENCY|daily loss {loss_pct:.1f}% of equity|loss=\${abs(daily_pnl):.0f} equity=\${equity:.0f}')
except: pass
" 2>/dev/null || echo "")

if [ -n "$LOSS_CHECK" ]; then
  ISSUES+=("$LOSS_CHECK")
fi

# ── 7. Positions after hours (only flag strategies with EOD_FLATTEN) ─────────
HOUR=$(TZ=America/New_York date +%H)
MIN=$(TZ=America/New_York date +%M)
DOW=$(TZ=America/New_York date +%u)  # 1=Mon 7=Sun
ET_MINS=$(( 10#$HOUR * 60 + 10#$MIN ))

# Outside RTH (before 9:30 or after 16:05) on weekdays
if [ "$DOW" -le 5 ] && { [ "$ET_MINS" -lt 570 ] || [ "$ET_MINS" -gt 965 ]; }; then
  # Build list of strategies that have EOD_FLATTEN exit rules
  EOD_STRATEGIES=""
  for toml in "$ROOT_DIR"/configs/strategies/*.toml; do
    if grep -q 'EOD_FLATTEN' "$toml" 2>/dev/null; then
      SID=$(grep -m1 '^id\s*=' "$toml" | sed 's/.*=\s*"\(.*\)"/\1/')
      [ -n "$SID" ] && EOD_STRATEGIES="$EOD_STRATEGIES '$SID'"
    fi
  done

  if [ -n "$EOD_STRATEGIES" ]; then
    # Get open position symbols from API
    OPEN_SYMS=$(curl -sf "$OMO/api/portfolio/positions" 2>/dev/null | python3 -c "
import json,sys
try:
  d=json.load(sys.stdin)
  positions=d if isinstance(d,list) else d.get('positions',[])
  for p in positions:
    print(p.get('symbol',''))
except: pass
" 2>/dev/null)

    if [ -n "$OPEN_SYMS" ]; then
      # For each open position, check if its strategy has EOD_FLATTEN
      STALE_COUNT=0
      STALE_DETAILS=""
      while IFS= read -r SYM; do
        [ -z "$SYM" ] && continue
        STRAT=$($DB_EXEC "
          SELECT strategy FROM orders
          WHERE (symbol='$SYM' OR option_symbol='$SYM')
            AND side='BUY' AND LOWER(status)='filled'
          ORDER BY time DESC LIMIT 1;
        " 2>/dev/null)
        STRAT=$(echo "$STRAT" | tr -d '[:space:]')
        # Only flag if this position's strategy requires EOD flatten
        if echo "$EOD_STRATEGIES" | grep -q "'$STRAT'"; then
          STALE_COUNT=$((STALE_COUNT + 1))
          STALE_DETAILS="$STALE_DETAILS $SYM($STRAT)"
        fi
      done <<< "$OPEN_SYMS"

      if [ "$STALE_COUNT" -gt 0 ]; then
        ISSUES+=("WARNING|$STALE_COUNT EOD_FLATTEN position(s) open after hours|$STALE_DETAILS")
      fi
    fi
  fi
fi

# ── Output ────────────────────────────────────────────────────────────────────
if [ ${#ISSUES[@]} -eq 0 ]; then
  exit 0
fi

printf '%s\n' "${ISSUES[@]}"
exit 1
