#!/bin/bash
# Usage: tune_param.sh <toml_key> <new_value>
# Modifies avwap_v4.toml, runs backtest, waits, prints summary metrics, restores file.
set -euo pipefail

TOML="/home/ridopark/src/oh-my-opentrade/configs/strategies/avwap_v4.toml"
BACKUP="/tmp/avwap_v4_tune_backup.toml"
KEY="$1"
VALUE="$2"

# Backup
cp "$TOML" "$BACKUP"

# Modify the TOML using python with string replacement (no regex backreference issues)
python3 <<PYEOF
import re
key = "$KEY"
value = "$VALUE"
with open("$TOML") as f:
    lines = f.readlines()
new_lines = []
for line in lines:
    stripped = line.lstrip()
    if stripped.startswith(key + " ") or stripped.startswith(key + "="):
        # Find the = sign and replace the value
        eq_idx = line.index("=")
        prefix = line[:eq_idx+1]
        # Check if there's a comment after the value
        rest = line[eq_idx+1:].strip()
        new_lines.append(f"{prefix} {value}\n")
    else:
        new_lines.append(line)
with open("$TOML", "w") as f:
    f.writelines(new_lines)
PYEOF

echo "=== Testing $KEY = $VALUE ==="

# Launch backtest
RESP=$(curl -s -X POST http://localhost:8080/backtest/run \
  -H "Content-Type: application/json" \
  -d '{
    "symbols": ["AAPL","AFRM","AMD","AMZN","AVGO","BA","COIN","CRM","GOOGL","HIMS","HOOD","IWM","JPM","LLY","META","MRNA","MRVL","MSFT","MU","NET","NFLX","NVDA","OXY","PLTR","QQQ","RBLX","RIVN","SMCI","SNOW","SOFI","SOXL","SPY","TSLA","XOM"],
    "from": "2025-06-01",
    "to": "2026-03-27",
    "timeframe": "5m",
    "initial_equity": 100000,
    "slippage_bps": 10,
    "strategies": ["avwap_v4"],
    "no_ai": true,
    "speed": "max",
    "max_positions": 6,
    "max_per_group": 2
  }')

BT_ID=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['backtest_id'])")
echo "Backtest ID: $BT_ID"

# Poll until done
for i in $(seq 1 40); do
  sleep 15
  STATUS=$(curl -s "http://localhost:8080/backtest/$BT_ID/status")
  S=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status','unknown'))" 2>/dev/null || echo "error")
  if [[ "$S" == "completed" ]]; then
    echo "Completed."
    break
  elif [[ "$S" == "failed" ]]; then
    echo "FAILED"
    cp "$BACKUP" "$TOML"
    exit 1
  fi
  PCT=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('progress',{}).get('pct',0))" 2>/dev/null || echo "?")
  echo "  ${PCT}%..."
done

# Get results
curl -s "http://localhost:8080/backtest/$BT_ID/results" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print(f\"PF={d['profit_factor']:.4f} WR={d['win_rate_pct']:.1f}% PnL={d['total_pnl']:.0f} Trades={d['trade_count']} DD={d['max_drawdown_pct']:.1f}% Sharpe={d['sharpe_ratio']:.3f} AvgW={d['avg_win']:.0f} AvgL={d['avg_loss']:.0f}\")
"

# Restore original
cp "$BACKUP" "$TOML"
echo "=== Done ==="
