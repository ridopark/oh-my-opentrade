#!/usr/bin/env bash
# Usage: ./scripts/run_backtest.sh [label]
# Runs a backtest and waits for completion, then prints summary stats.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DASH_SESSION="omo-dashboard"
LABEL="${1:-unlabeled}"
API="http://localhost:8080"

# ── Free resources: stop dashboard to reclaim RAM + disk IO ──
DASH_WAS_RUNNING=false
if tmux has-session -t "$DASH_SESSION" 2>/dev/null; then
  DASH_WAS_RUNNING=true
  tmux send-keys -t "$DASH_SESSION" C-c
  sleep 1
  tmux kill-session -t "$DASH_SESSION" 2>/dev/null || true
  echo "[$LABEL] Stopped dashboard to free resources for backtest"
fi

restart_dashboard() {
  if $DASH_WAS_RUNNING; then
    tmux new-session -d -s "$DASH_SESSION" -c "$ROOT_DIR/apps/dashboard" "npm run dev"
    echo "[$LABEL] Dashboard restarted"
  fi
}
trap restart_dashboard EXIT
SYMBOLS='["GOOGL","HOOD","MSFT","NFLX","PLTR","XOM","AAPL","AMZN","META","NVDA","TSLA","AMD","INTC","AVGO","QCOM","MU","MRVL","ON","SMCI","CRM","ORCL","SNOW","U","NET","DDOG","ZS","SOFI","COIN","SQ","PYPL","V","MA","BAC","JPM","GS","HIMS","RIVN","LCID","NIO","F","GM","WMT","COST","TGT","MRNA","PFE","ABBV","LLY","UNH","JNJ","CVX","OXY","SLB","SPY","QQQ","IWM","DIA","XLF","XLE","XLK","SOXL","TQQQ","SQQQ","MARA","RIOT","FUBO","AFRM","UPST","RBLX","BA","CAT","DE","UPS"]'

# Start backtest
RESP=$(curl -s -X POST "$API/backtest/run" \
  -H "Content-Type: application/json" \
  -d "{
    \"symbols\": $SYMBOLS,
    \"from\": \"2026-01-01\",
    \"to\": \"2026-03-27\",
    \"timeframe\": \"1m\",
    \"initial_equity\": 100000,
    \"slippage_bps\": 5,
    \"speed\": \"max\",
    \"strategies\": [\"avwap_v4\"],
    \"no_ai\": true
  }")

BT_ID=$(echo "$RESP" | grep -oP '"backtest_id"\s*:\s*"\K[^"]+')
if [ -z "$BT_ID" ]; then
  echo "ERROR: Failed to start backtest. Response: $RESP"
  exit 1
fi
echo "[$LABEL] Backtest started: $BT_ID"

# Poll for completion
while true; do
  STATUS_RESP=$(curl -s "$API/backtest/$BT_ID/status" 2>/dev/null || echo '{"status":"error"}')
  STATUS=$(echo "$STATUS_RESP" | grep -oP '"status"\s*:\s*"\K[^"]+' || echo "unknown")

  if [ "$STATUS" = "completed" ] || [ "$STATUS" = "complete" ]; then
    break
  elif [ "$STATUS" = "failed" ] || [ "$STATUS" = "error" ] || [ "$STATUS" = "cancelled" ]; then
    echo "[$LABEL] Backtest $STATUS: $STATUS_RESP"
    exit 1
  fi

  # Show progress
  PCT=$(echo "$STATUS_RESP" | grep -oP '"progress_pct"\s*:\s*\K[0-9.]+' || echo "?")
  echo -ne "\r[$LABEL] Running... ${PCT}%  "
  sleep 5
done

echo ""
echo "[$LABEL] Backtest complete. Fetching results..."

# Get results
RESULTS=$(curl -s "$API/backtest/$BT_ID/results")
echo "$RESULTS" | python3 -c "
import json, sys
data = json.load(sys.stdin)

# Try to extract metrics from various possible response shapes
metrics = data if isinstance(data, dict) else {}
if 'metrics' in metrics:
    metrics = metrics['metrics']

trades = metrics.get('total_trades', metrics.get('trades', '?'))
equity = metrics.get('final_equity', '?')
ret = metrics.get('total_return_pct', '?')
pf = metrics.get('profit_factor', '?')
wr = metrics.get('win_rate_pct', metrics.get('win_rate', '?'))
dd = metrics.get('max_drawdown_pct', metrics.get('max_drawdown', '?'))
sharpe = metrics.get('sharpe_ratio', metrics.get('sharpe', '?'))
pnl = metrics.get('total_pnl', '?')

print(f'')
print(f'=== [{\"$LABEL\"}] BACKTEST RESULTS ===')
print(f'Trades:      {trades}')
print(f'Win Rate:    {wr}%')
print(f'PF:          {pf}')
print(f'Total PnL:   \${pnl}')
print(f'Return:      {ret}%')
print(f'Final Eq:    \${equity}')
print(f'Max DD:      {dd}%')
print(f'Sharpe:      {sharpe}')
print(f'================================')
print(f'')
print(f'RAW: {json.dumps(data)}')
" 2>/dev/null || echo "[$LABEL] Raw results: $RESULTS"
