#!/bin/bash
# Usage: run_backtest.sh <label> <from> <to> <config_path>
# Submits a backtest that uses configs/strategies/macd_only_v1.toml (must be
# updated by caller before invocation). Saves results to
# _workspace/macd_chandelier/<label>_results.json and prints a summary line.

set -euo pipefail

LABEL="$1"
FROM="$2"
TO="$3"
OUT="/home/ridopark/src/oh-my-opentrade/_workspace/macd_chandelier/${LABEL}_results.json"

SYMS='["MSFT","GOOGL","META","NVDA","AMD","MU","MRVL","SMCI","PLTR","CRM","SNOW","NET","SOFI","HOOD","AFRM","XOM","OXY","LLY","HIMS","RIVN","RBLX","BA","SPY","QQQ","IWM","SOXL"]'

BODY=$(cat <<EOF
{
  "symbols": ${SYMS},
  "from": "${FROM}",
  "to": "${TO}",
  "timeframe": "5m",
  "initial_equity": 100000,
  "slippage_bps": 10,
  "speed": "max",
  "compound_equity": true,
  "strategies": ["macd_only_v1"],
  "no_ai": true
}
EOF
)

BT_ID=$(curl -s -X POST http://localhost:8080/backtest/run \
  -H "Content-Type: application/json" \
  -d "${BODY}" | python3 -c "import json,sys; print(json.load(sys.stdin)['backtest_id'])")

echo "[${LABEL}] backtest_id=${BT_ID} from=${FROM} to=${TO}"

for i in $(seq 1 60); do
  sleep 10
  STATUS=$(curl -s http://localhost:8080/backtest/${BT_ID}/status \
    | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('status','?'))" 2>/dev/null || echo "err")
  if [ "$STATUS" = "completed" ]; then break; fi
  if [ "$STATUS" = "failed" ]; then echo "[${LABEL}] FAILED"; exit 1; fi
done

curl -s http://localhost:8080/backtest/${BT_ID}/results > "${OUT}"
python3 -c "
import json
d = json.load(open('${OUT}'))
pf = d.get('profit_factor', 0) or 0
wr = d.get('win_rate_pct', 0) or 0
pnl = d.get('total_pnl', 0) or 0
tc = d.get('trade_count', 0) or 0
dd = d.get('max_drawdown_pct', 0) or 0
sh = d.get('sharpe_ratio', 0) or 0
print(f'[${LABEL}] PF={pf:.3f} WR={wr:.1f}% PnL=\${pnl:,.0f} Trades={tc} DD={dd:.2f}% Sharpe={sh:.2f}')
"
