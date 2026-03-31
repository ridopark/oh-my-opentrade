#!/usr/bin/env bash
# iterate_backtest.sh — Run config variations in parallel batches of 2.
#
# Usage: ./scripts/iterate_backtest.sh [variations_file]
set -euo pipefail

API="http://localhost:8080"
CONFIG="configs/strategies/avwap_v4.toml"
RESULTS_DIR="/tmp/bt_iterations_$(date +%Y%m%d_%H%M%S)"
BATCH_SIZE=1
mkdir -p "$RESULTS_DIR"

SYMBOLS='["GOOGL","HOOD","MSFT","NFLX","PLTR","XOM","AAPL","AMZN","META","NVDA","TSLA","AMD","INTC","AVGO","QCOM","MU","MRVL","ON","SMCI","CRM","ORCL","SNOW","U","NET","DDOG","ZS","SOFI","COIN","SQ","PYPL","V","MA","BAC","JPM","GS","HIMS","RIVN","LCID","NIO","F","GM","WMT","COST","TGT","MRNA","PFE","ABBV","LLY","UNH","JNJ","CVX","OXY","SLB","SPY","QQQ","IWM","DIA","XLF","XLE","XLK","SOXL","TQQQ","SQQQ","MARA","RIOT","FUBO","AFRM","UPST","RBLX","BA","CAT","DE","UPS"]'
FROM="2026-01-01"
TO="2026-03-27"

# --------------------------------------------------------------------------
# VARIATIONS — "label|sed_expr1|sed_expr2|..."
# --------------------------------------------------------------------------
VARIATIONS=(
  "base|"
  "conf6|s/min_confluence_score = 7/min_confluence_score = 6/"
  "conf8|s/min_confluence_score = 7/min_confluence_score = 8/"
  "vol2|s/^volume_mult = 2.5/volume_mult = 2.0/"
  "vol3|s/^volume_mult = 2.5/volume_mult = 3.0/"
)

if [ "${1:-}" != "" ] && [ -f "$1" ]; then
  echo "Loading variations from $1"
  mapfile -t VARIATIONS < "$1"
fi

# --- Functions ---

launch_backtest() {
  local label="$1" rest="$2"
  local tmpdir
  tmpdir=$(mktemp -d /tmp/bt_strat_XXXX)
  cp "$CONFIG" "$tmpdir/"

  if [ -n "$rest" ]; then
    IFS='|' read -ra SED_PARTS <<< "$rest"
    for s in "${SED_PARTS[@]}"; do
      [ -n "$s" ] && sed -i "$s" "$tmpdir/avwap_v4.toml"
    done
  fi

  local resp bt_id
  resp=$(curl -s -X POST "$API/backtest/run" \
    -H "Content-Type: application/json" \
    -d "{
      \"symbols\": $SYMBOLS,
      \"from\": \"$FROM\", \"to\": \"$TO\",
      \"timeframe\": \"1m\", \"initial_equity\": 100000,
      \"slippage_bps\": 5, \"speed\": \"max\",
      \"strategies\": [\"avwap_v4\"], \"no_ai\": true,
      \"strategy_dir\": \"$tmpdir\"
    }")

  bt_id=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('backtest_id',''))" 2>/dev/null)
  if [ -z "$bt_id" ]; then
    echo "[$label] FAILED: $resp" >&2
    rm -rf "$tmpdir"
    return 1
  fi
  echo "[$label] Started: $bt_id"
  # Return id and tmpdir
  echo "$bt_id $tmpdir" > "$RESULTS_DIR/${label}.meta"
}

wait_for_backtests() {
  local -a labels=("$@")
  while true; do
    local all_done=true status_line=""
    for label in "${labels[@]}"; do
      [ ! -f "$RESULTS_DIR/${label}.meta" ] && continue
      local id
      id=$(awk '{print $1}' "$RESULTS_DIR/${label}.meta")
      local resp st pct
      resp=$(curl -s "$API/backtest/$id/status" 2>/dev/null)
      st=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status','?'))" 2>/dev/null)
      pct=$(echo "$resp" | python3 -c "import sys,json; p=json.load(sys.stdin).get('progress',{}); print(int(p.get('pct',0)))" 2>/dev/null || echo "?")
      status_line+="$label:${pct}% "
      if [ "$st" = "running" ] || [ "$st" = "paused" ]; then
        all_done=false
      fi
    done
    echo -ne "\r  $status_line    "
    if $all_done; then break; fi
    sleep 10
  done
  echo ""
}

collect_result() {
  local label="$1"
  [ ! -f "$RESULTS_DIR/${label}.meta" ] && return
  local id tmpdir
  read -r id tmpdir < "$RESULTS_DIR/${label}.meta"
  curl -s "$API/backtest/$id/results" > "$RESULTS_DIR/${label}.json"
  rm -rf "$tmpdir"
}

# --- Main ---

echo "=== Running ${#VARIATIONS[@]} variations in batches of $BATCH_SIZE ==="
echo ""

ALL_LABELS=()
batch_labels=()
batch_count=0

for var in "${VARIATIONS[@]}"; do
  IFS='|' read -r label rest <<< "$var"
  ALL_LABELS+=("$label")
  launch_backtest "$label" "$rest"
  batch_labels+=("$label")
  batch_count=$((batch_count + 1))

  if [ "$batch_count" -ge "$BATCH_SIZE" ]; then
    echo "  Waiting for batch: ${batch_labels[*]}"
    wait_for_backtests "${batch_labels[@]}"
    for bl in "${batch_labels[@]}"; do collect_result "$bl"; done
    batch_labels=()
    batch_count=0
  fi
done

# Final partial batch
if [ ${#batch_labels[@]} -gt 0 ]; then
  echo "  Waiting for batch: ${batch_labels[*]}"
  wait_for_backtests "${batch_labels[@]}"
  for bl in "${batch_labels[@]}"; do collect_result "$bl"; done
fi

echo ""
echo "=== RESULTS COMPARISON ==="
printf "%-12s %7s %6s %7s %11s %6s %9s %9s\n" "Variation" "Trades" "WR%" "PF" "PnL" "DD%" "AvgWin" "AvgLoss"
printf "%-12s %7s %6s %7s %11s %6s %9s %9s\n" "----------" "-------" "------" "-------" "-----------" "------" "---------" "---------"

for label in "${ALL_LABELS[@]}"; do
  python3 << PYEOF
import json
try:
    data = json.load(open("$RESULTS_DIR/${label}.json"))
    tc = data.get('trade_count', 0)
    wr = data.get('win_rate_pct', 0)
    pf = data.get('profit_factor', 0)
    pnl = data.get('total_pnl', 0)
    dd = data.get('max_drawdown_pct', 0)
    aw = data.get('avg_win', 0)
    al = data.get('avg_loss', 0)
    print(f'{"${label}":<12s} {tc:>7d} {wr:>6.1f} {pf:>7.3f} {pnl:>11,.0f} {dd:>6.1f} {aw:>9,.0f} {al:>9,.0f}')
except Exception as e:
    print(f'{"${label}":<12s} ERROR: {e}')
PYEOF
done

echo ""
echo "Results saved to: $RESULTS_DIR/"
