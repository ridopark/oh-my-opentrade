#!/bin/bash
# Pass 2 Coordinate Descent for avwap_v4
# Base: conf=8, slope=0.6, hold=5, stag=60
# PF=1.178, WR=41.8%, PnL=+25470, Trades=865, DD=11.0%
set -euo pipefail
cd /home/ridopark/src/oh-my-opentrade

TOML="configs/strategies/avwap_v4.toml"
BACKUP="/tmp/avwap_v4_pass2_backup.toml"
SYMBOLS='["AAPL","AFRM","AMD","AMZN","AVGO","BA","COIN","CRM","GOOGL","HIMS","HOOD","IWM","JPM","LLY","META","MRNA","MRVL","MSFT","MU","NET","NFLX","NVDA","OXY","PLTR","QQQ","RBLX","RIVN","SMCI","SNOW","SOFI","SOXL","SPY","TSLA","XOM"]'
LOG="_workspace/pass2_results.log"

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

reset() {
  cp "$BACKUP" "$TOML"
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
    if [[ "$S" == "failed" ]]; then echo "$label FAILED" | tee -a "$LOG"; reset; return 1; fi
  done
  METRICS=$(curl -s "http://localhost:8080/backtest/$BT_ID/results" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print(f\"PF={d['profit_factor']:.4f} WR={d['win_rate_pct']:.1f}% PnL={d['total_pnl']:.0f} Trades={d['trade_count']} DD={d['max_drawdown_pct']:.1f}% Sharpe={d['sharpe_ratio']:.3f} AvgW={d['avg_win']:.0f} AvgL={d['avg_loss']:.0f}\")
  ")
  echo "$label => $METRICS" | tee -a "$LOG"
  reset
}

echo "=== PASS 2 COORDINATE DESCENT ===" | tee "$LOG"
echo "Base: PF=1.1784 WR=41.8% PnL=+25470 Trades=865 DD=11.0% Sharpe=0.011" | tee -a "$LOG"
echo "" >> "$LOG"

# --- Swing Stop params ---
echo "--- Swing Stop ---" | tee -a "$LOG"
reset; apply_param "buffer_bps" "15"
run_bt "swing_buffer=15"

reset; apply_param "buffer_bps" "35"
run_bt "swing_buffer=35"

reset; apply_param "atr_buffer_mult" "2.5"
run_bt "atr_mult=2.5"

reset; apply_param "atr_buffer_mult" "3.5"
run_bt "atr_mult=3.5"

reset; apply_param "lookback" "15"
run_bt "swing_lookback=15"

reset; apply_param "lookback" "25"
run_bt "swing_lookback=25"

reset; apply_param "min_bars" "15"
run_bt "swing_min_bars=15"

reset; apply_param "min_bars" "25"
run_bt "swing_min_bars=25"

# --- Stagnation profit gate ---
echo "--- Stagnation profit gate ---" | tee -a "$LOG"
reset; apply_param "profit_gate_pct" "0.003"
run_bt "profit_gate=0.003"

reset; apply_param "profit_gate_pct" "0.008"
run_bt "profit_gate=0.008"

reset; apply_param "profit_gate_pct" "0.01"
run_bt "profit_gate=0.01"

# --- Pinch params ---
echo "--- Pinch params ---" | tee -a "$LOG"
reset; apply_param "pinch_max_bps" "20"
run_bt "pinch_max=20"

reset; apply_param "pinch_max_bps" "40"
run_bt "pinch_max=40"

reset; apply_param "pinch_min_bps" "3"
run_bt "pinch_min=3"

reset; apply_param "pinch_min_bps" "8"
run_bt "pinch_min=8"

# --- Gap reclaim ---
echo "--- Gap reclaim ---" | tee -a "$LOG"
reset; apply_param "gap_reclaim_bars" "4"
run_bt "gap_reclaim=4"

reset; apply_param "gap_reclaim_bars" "6"
run_bt "gap_reclaim=6"

# --- Slope lookback ---
echo "--- Slope lookback ---" | tee -a "$LOG"
reset; apply_param "slope_lookback" "10"
run_bt "slope_lookback=10"

reset; apply_param "slope_lookback" "20"
run_bt "slope_lookback=20"

# --- Options params ---
echo "--- Options ---" | tee -a "$LOG"
reset; apply_param "max_dte" "14"
run_bt "max_dte=14"

reset; apply_param "max_dte" "30"
run_bt "max_dte=30"

reset; apply_param "max_spread_pct" "0.06"
run_bt "max_spread=0.06"

reset; apply_param "max_spread_pct" "0.10"
run_bt "max_spread=0.10"

reset; apply_param "max_contracts" "3"
run_bt "max_contracts=3"

reset; apply_param "max_contracts" "7"
run_bt "max_contracts=7"

# --- Timing ---
echo "--- Timing ---" | tee -a "$LOG"
reset; apply_param "allowed_hours_start" '"09:30"'
run_bt "start=09:30"

reset; apply_param "allowed_hours_end" '"15:30"'
run_bt "end=15:30"

reset; apply_param "midday_volume_mult" "2.5"
run_bt "midday_vol=2.5"

reset; apply_param "midday_volume_mult" "1.5"
run_bt "midday_vol=1.5"

echo "=== PASS 2 COMPLETE ===" | tee -a "$LOG"
