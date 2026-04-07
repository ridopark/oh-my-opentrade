#!/bin/bash
# Explore lower confluence scores and combined params
set -euo pipefail
cd /home/ridopark/src/oh-my-opentrade

TOML="configs/strategies/avwap_v4.toml"
BACKUP="/tmp/avwap_v4_explore_backup.toml"
SYMBOLS='["AAPL","AFRM","AMD","AMZN","AVGO","BA","COIN","CRM","GOOGL","HIMS","HOOD","IWM","JPM","LLY","META","MRNA","MRVL","MSFT","MU","NET","NFLX","NVDA","OXY","PLTR","QQQ","RBLX","RIVN","SMCI","SNOW","SOFI","SOXL","SPY","TSLA","XOM"]'
LOG="_workspace/explore_results.log"
echo "=== Exploration: confluence scores + combos ===" > "$LOG"

cp "$TOML" "$BACKUP"

apply_param() {
  local key="$1" value="$2"
  python3 <<PYEOF
key = "$key"
value = "$value"
with open("$TOML") as f:
    lines = f.readlines()
new_lines = []
for line in lines:
    stripped = line.lstrip()
    if stripped.startswith(key + " ") or stripped.startswith(key + "="):
        eq_idx = line.index("=")
        prefix = line[:eq_idx+1]
        new_lines.append(f"{prefix} {value}\n")
    else:
        new_lines.append(line)
with open("$TOML", "w") as f:
    f.writelines(new_lines)
PYEOF
}

run_bt() {
  local label="$1"
  RESP=$(curl -s -X POST http://localhost:8080/backtest/run \
    -H "Content-Type: application/json" \
    -d "{
      \"symbols\": $SYMBOLS,
      \"from\": \"2025-06-01\",
      \"to\": \"2026-03-27\",
      \"timeframe\": \"5m\",
      \"initial_equity\": 100000,
      \"slippage_bps\": 10,
      \"strategies\": [\"avwap_v4\"],
      \"no_ai\": true,
      \"speed\": \"max\",
      \"max_positions\": 6,
      \"max_per_group\": 2
    }")
  BT_ID=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['backtest_id'])")
  for i in $(seq 1 40); do
    sleep 15
    S=$(curl -s "http://localhost:8080/backtest/$BT_ID/status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status','unknown'))" 2>/dev/null || echo "error")
    if [[ "$S" == "completed" ]]; then break; fi
    if [[ "$S" == "failed" ]]; then echo "$label FAILED" | tee -a "$LOG"; cp "$BACKUP" "$TOML"; return 1; fi
  done
  METRICS=$(curl -s "http://localhost:8080/backtest/$BT_ID/results" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print(f\"PF={d['profit_factor']:.4f} WR={d['win_rate_pct']:.1f}% PnL={d['total_pnl']:.0f} Trades={d['trade_count']} DD={d['max_drawdown_pct']:.1f}% Sharpe={d['sharpe_ratio']:.3f} AvgW={d['avg_win']:.0f} AvgL={d['avg_loss']:.0f}\")
  ")
  echo "$label => $METRICS" | tee -a "$LOG"
  cp "$BACKUP" "$TOML"
}

# Test confluence=7
cp "$BACKUP" "$TOML"
apply_param "min_confluence_score" "7"
run_bt "confluence=7"

# Test confluence=6
cp "$BACKUP" "$TOML"
apply_param "min_confluence_score" "6"
run_bt "confluence=6"

# Test confluence=5
cp "$BACKUP" "$TOML"
apply_param "min_confluence_score" "5"
run_bt "confluence=5"

# Test combo: confluence=8 + min_slope_bps=0.6
cp "$BACKUP" "$TOML"
apply_param "min_confluence_score" "8"
apply_param "min_slope_bps" "0.6"
run_bt "confluence=8+slope=0.6"

# Test combo: confluence=7 + min_slope_bps=0.6
cp "$BACKUP" "$TOML"
apply_param "min_confluence_score" "7"
apply_param "min_slope_bps" "0.6"
run_bt "confluence=7+slope=0.6"

echo "=== Exploration Complete ===" | tee -a "$LOG"
