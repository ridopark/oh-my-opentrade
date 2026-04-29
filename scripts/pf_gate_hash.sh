#!/usr/bin/env bash
# pf_gate_hash.sh — Run a backtest via /backtest/run, wait for completion,
# and emit a stable sha256 of the result JSON for byte-equality checks.
#
# Usage: ./scripts/pf_gate_hash.sh <strategy> <from> <to> [extra_json_keys]
#   extra_json_keys: optional JSON fragment merged into the request body
#                    (e.g. '"option_impact_scale_bps": 100')
#
# Stable hash strips run-id, timestamps, durations, and any field whose
# value is non-deterministic across runs.
set -euo pipefail

API="${API:-http://localhost:8080}"
STRATEGY="${1:-avwap_v4}"
FROM="${2:-2026-01-01}"
TO="${3:-2026-03-27}"
EXTRA="${4:-}"

SYMBOLS='["GOOGL","HOOD","MSFT","NFLX","PLTR","XOM","AAPL","AMZN","META","NVDA","TSLA","AMD","INTC","AVGO","QCOM","MU","MRVL","ON","SMCI","CRM","ORCL","SNOW","U","NET","DDOG","ZS","SOFI","COIN","SQ","PYPL","V","MA","BAC","JPM","GS","HIMS","RIVN","LCID","NIO","F","GM","WMT","COST","TGT","MRNA","PFE","ABBV","LLY","UNH","JNJ","CVX","OXY","SLB","SPY","QQQ","IWM","DIA","XLF","XLE","XLK","SOXL","TQQQ","SQQQ","MARA","RIOT","FUBO","AFRM","UPST","RBLX","BA","CAT","DE","UPS"]'

BODY=$(printf '{
  "symbols": %s,
  "from": "%s",
  "to": "%s",
  "timeframe": "1m",
  "initial_equity": 100000,
  "slippage_bps": 5,
  "speed": "max",
  "strategies": ["%s"],
  "no_ai": true%s
}' "$SYMBOLS" "$FROM" "$TO" "$STRATEGY" "${EXTRA:+, $EXTRA}")

RESP=$(curl -sf -X POST "$API/backtest/run" -H "Content-Type: application/json" -d "$BODY")
BT_ID=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['backtest_id'])")
[ -n "$BT_ID" ] || { echo "FAIL: no backtest_id in $RESP" >&2; exit 1; }

echo "[$STRATEGY] backtest_id=$BT_ID" >&2

# Poll until done
while true; do
  ST=$(curl -sf "$API/backtest/$BT_ID/status" || echo '{"status":"error"}')
  STATUS=$(echo "$ST" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status','?'))" 2>/dev/null || echo "?")
  case "$STATUS" in
    completed|complete) break ;;
    failed|error|cancelled)
      echo "[$STRATEGY] backtest $STATUS: $ST" >&2
      exit 1
      ;;
  esac
  PCT=$(echo "$ST" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('progress_pct','?'))" 2>/dev/null || echo "?")
  printf "\r[%s] %s%%   " "$STRATEGY" "$PCT" >&2
  sleep 5
done
echo "" >&2

RESULTS=$(curl -sf "$API/backtest/$BT_ID/results")

# Persist raw + stripped artefacts so the next backtest's eviction doesn't
# erase the prior run before comparison. Output dir defaults to
# /tmp/pf_gate but can be overridden via PF_GATE_DIR.
OUT_DIR="${PF_GATE_DIR:-/tmp/pf_gate}"
mkdir -p "$OUT_DIR"
echo "$RESULTS" > "$OUT_DIR/${BT_ID}.raw.json"

# Strip non-deterministic fields and compute sha256.
HASH=$(echo "$RESULTS" | python3 -c "
import json, sys
d = json.load(sys.stdin)
for k in ('id', 'started_at', 'finished_at', 'duration_ms', 'runner_id',
          'completed_at', 'created_at', 'updated_at', 'elapsed_ms'):
    d.pop(k, None)
DROP = {
    'id', 'trade_id', 'order_id',
    # Tide-deviation confluence tags carry per-run microstructure noise from
    # the live signal-tracker pipeline; not impacted by the broker fill path.
    'qqq_tide_dev_bps', 'spy_tide_dev_bps',
    'confluence_components', 'confluence_detail',
}
def strip(obj):
    if isinstance(obj, dict):
        for k in list(obj.keys()):
            if k in DROP:
                obj.pop(k, None)
            else:
                strip(obj[k])
    elif isinstance(obj, list):
        for x in obj:
            strip(x)
strip(d)
print(json.dumps(d, sort_keys=True, separators=(',', ':')))
" | tee "$OUT_DIR/${BT_ID}.stripped.json" | sha256sum | awk '{print $1}')
echo "$HASH"

# Also dump the metrics block to stderr for human inspection.
echo "$RESULTS" | python3 -c "
import sys, json
d = json.load(sys.stdin)
m = d.get('metrics', d)
if 'metrics' in d:
    m = d['metrics']
keys = ['total_trades', 'profit_factor', 'win_rate_pct', 'win_rate',
        'total_return_pct', 'final_equity', 'max_drawdown_pct', 'sharpe_ratio']
fields = []
for k in keys:
    if k in m:
        fields.append(f'{k}={m[k]}')
print('  '.join(fields), file=sys.stderr)
" 2>/dev/null || true
