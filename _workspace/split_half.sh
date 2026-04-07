#!/bin/bash
# Split-half validation for a parameter change
set -euo pipefail
cd /home/ridopark/src/oh-my-opentrade

TOML="configs/strategies/avwap_v4.toml"
BACKUP="/tmp/avwap_v4_tune_backup.toml"
KEY="$1"
VALUE="$2"
SYMBOLS='["AAPL","AFRM","AMD","AMZN","AVGO","BA","COIN","CRM","GOOGL","HIMS","HOOD","IWM","JPM","LLY","META","MRNA","MRVL","MSFT","MU","NET","NFLX","NVDA","OXY","PLTR","QQQ","RBLX","RIVN","SMCI","SNOW","SOFI","SOXL","SPY","TSLA","XOM"]'

cp "$TOML" "$BACKUP"

# Apply change
python3 <<PYEOF
key = "$KEY"
value = "$VALUE"
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

run_bt() {
  local label="$1" from="$2" to="$3"
  RESP=$(curl -s -X POST http://localhost:8080/backtest/run \
    -H "Content-Type: application/json" \
    -d "{
      \"symbols\": $SYMBOLS,
      \"from\": \"$from\",
      \"to\": \"$to\",
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
  echo "$label backtest: $BT_ID"
  for i in $(seq 1 40); do
    sleep 15
    S=$(curl -s "http://localhost:8080/backtest/$BT_ID/status" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status','unknown'))" 2>/dev/null || echo "error")
    if [[ "$S" == "completed" ]]; then break; fi
    if [[ "$S" == "failed" ]]; then echo "$label FAILED"; return 1; fi
  done
  curl -s "http://localhost:8080/backtest/$BT_ID/results" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print(f\"$label: PF={d['profit_factor']:.4f} WR={d['win_rate_pct']:.1f}% PnL={d['total_pnl']:.0f} Trades={d['trade_count']} DD={d['max_drawdown_pct']:.1f}% Sharpe={d['sharpe_ratio']:.3f}\")
"
}

echo "=== Split-Half Validation: $KEY=$VALUE ==="
run_bt "FIRST_HALF" "2025-06-01" "2025-12-14"
run_bt "SECOND_HALF" "2025-12-14" "2026-03-27"
echo "=== Done ==="

cp "$BACKUP" "$TOML"
