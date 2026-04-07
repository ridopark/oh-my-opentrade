#!/bin/bash
# Pass 1 Coordinate Descent for avwap_v4
# Tests each parameter tighter and looser, logs results
set -euo pipefail
cd /home/ridopark/src/oh-my-opentrade

LOG="_workspace/pass1_results.log"
echo "=== PASS 1 COORDINATE DESCENT ===" > "$LOG"
echo "Baseline: PF=0.9210 WR=35.8% PnL=-4435 Trades=363 DD=15.0% Sharpe=-0.003" >> "$LOG"
echo "" >> "$LOG"

run_test() {
  local key="$1"
  local value="$2"
  echo "--- $key=$value ---" | tee -a "$LOG"
  RESULT=$(bash _workspace/tune_param.sh "$key" "$value" 2>&1)
  # Extract the metrics line
  METRICS=$(echo "$RESULT" | grep "^PF=")
  if [[ -n "$METRICS" ]]; then
    echo "$key=$value => $METRICS" | tee -a "$LOG"
  else
    echo "$key=$value => ERROR: $RESULT" | tee -a "$LOG"
  fi
  echo "" >> "$LOG"
}

# Entry Filters
echo "--- Entry Filters ---" | tee -a "$LOG"
run_test "hold_bars" "5"
run_test "hold_bars" "7"
run_test "volume_mult" "2.25"
run_test "volume_mult" "1.75"
run_test "min_confluence_score" "8"
run_test "min_confluence_score" "9"
run_test "min_slope_bps" "0.6"
run_test "min_slope_bps" "0.4"

# Exit Rules
echo "--- Exit Rules ---" | tee -a "$LOG"
run_test "stop_bps" "65"
run_test "stop_bps" "85"
run_test "avwap_stop_bars" "1"
run_test "avwap_stop_bars" "3"

# Stagnation minutes — need to target the right key in exit_rules
# The key "minutes" appears under STAGNATION_EXIT section
run_test "minutes" "75"
run_test "minutes" "105"

# Max loss pct
run_test "pct" "0.015"
run_test "pct" "0.025"

echo "=== PASS 1 COMPLETE ===" | tee -a "$LOG"
cat "$LOG"
