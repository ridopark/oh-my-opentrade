#!/bin/bash
# Pass 1 exit param descent on top of conf=8+slope=0.6 base
set -euo pipefail
cd /home/ridopark/src/oh-my-opentrade

TOML="configs/strategies/avwap_v4.toml"
BACKUP="/tmp/avwap_v4_exit_backup.toml"
SYMBOLS='["AAPL","AFRM","AMD","AMZN","AVGO","BA","COIN","CRM","GOOGL","HIMS","HOOD","IWM","JPM","LLY","META","MRNA","MRVL","MSFT","MU","NET","NFLX","NVDA","OXY","PLTR","QQQ","RBLX","RIVN","SMCI","SNOW","SOFI","SOXL","SPY","TSLA","XOM"]'
LOG="_workspace/pass1_exits.log"

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

set_base() {
  cp "$BACKUP" "$TOML"
  apply_param "min_confluence_score" "8"
  apply_param "min_slope_bps" "0.6"
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
    if [[ "$S" == "failed" ]]; then echo "$label FAILED" | tee -a "$LOG"; return 1; fi
  done
  METRICS=$(curl -s "http://localhost:8080/backtest/$BT_ID/results" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print(f\"PF={d['profit_factor']:.4f} WR={d['win_rate_pct']:.1f}% PnL={d['total_pnl']:.0f} Trades={d['trade_count']} DD={d['max_drawdown_pct']:.1f}% Sharpe={d['sharpe_ratio']:.3f} AvgW={d['avg_win']:.0f} AvgL={d['avg_loss']:.0f}\")
  ")
  echo "$label => $METRICS" | tee -a "$LOG"
}

echo "=== Pass 1 Exit Params (base: conf=8+slope=0.6) ===" | tee "$LOG"
echo "Base: PF=1.1246 WR=39.5% PnL=+17189 Trades=848 DD=12.5%" | tee -a "$LOG"

# hold_bars on new base
set_base; apply_param "hold_bars" "5"
run_bt "hold_bars=5"

set_base; apply_param "hold_bars" "7"
run_bt "hold_bars=7"

# volume_mult on new base
set_base; apply_param "volume_mult" "2.25"
run_bt "volume_mult=2.25"

set_base; apply_param "volume_mult" "1.75"
run_bt "volume_mult=1.75"

# Stagnation minutes
set_base; apply_param "minutes" "75"
run_bt "stagnation=75"

set_base; apply_param "minutes" "60"
run_bt "stagnation=60"

set_base; apply_param "minutes" "105"
run_bt "stagnation=105"

# higher_lows_bars
set_base; apply_param "higher_lows_bars" "4"
run_bt "higher_lows_bars=4"

set_base; apply_param "higher_lows_bars" "6"
run_bt "higher_lows_bars=6"

# exit_hold_bars
set_base; apply_param "exit_hold_bars" "5"
run_bt "exit_hold_bars=5"

set_base; apply_param "exit_hold_bars" "8"
run_bt "exit_hold_bars=8"

# cooldown_seconds
set_base; apply_param "cooldown_seconds" "5400"
run_bt "cooldown=5400"

set_base; apply_param "cooldown_seconds" "3600"
run_bt "cooldown=3600"

echo "=== Done ===" | tee -a "$LOG"
cp "$BACKUP" "$TOML"
