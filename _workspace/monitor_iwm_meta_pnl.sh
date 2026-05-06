#!/usr/bin/env bash
# Track unrealized P&L for IWM + META options positions until they're closed.
# Peak P&L per symbol is held in memory; CSV is the durable log.
#
# Output:
#   stdout:        one line per poll, per matching position; "CROSSED ZERO" alert on first positive cross
#   logs/iwm_meta_pnl.csv: ts,symbol,underlying,qty,entry,current,unrealized_pnl,peak_pnl

set -u
ROOT="/home/ridopark/src/oh-my-opentrade"
CSV="$ROOT/logs/iwm_meta_pnl.csv"
mkdir -p "$(dirname "$CSV")"
[ -f "$CSV" ] || echo "ts,symbol,underlying,qty,entry,current,unrealized_pnl,peak_pnl" > "$CSV"

declare -A PEAK
declare -A EVER_POSITIVE
declare -A LAST_SEEN

INTERVAL=30

echo "[$(date -u +%H:%M:%SZ)] watcher started — polling every ${INTERVAL}s for IWM+META"

while true; do
  ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  resp=$(curl -s --max-time 5 http://localhost:8080/api/portfolio/positions)
  if [ -z "$resp" ]; then
    echo "[$ts] WARN: empty response from /api/portfolio/positions"
    sleep "$INTERVAL"
    continue
  fi

  current_syms=""
  while IFS=$'\t' read -r sym und qty entry curr pnl; do
    [ -z "$sym" ] && continue
    current_syms+=" $sym "

    peak=${PEAK[$sym]:-$pnl}
    new_peak=$(awk -v a="$pnl" -v b="$peak" 'BEGIN { printf "%.2f", (a>b)?a:b }')
    PEAK[$sym]=$new_peak
    LAST_SEEN[$sym]=$ts

    pos=${EVER_POSITIVE[$sym]:-no}
    if [ "$pos" = "no" ]; then
      crossed=$(awk -v p="$pnl" 'BEGIN { print (p>0)?"yes":"no" }')
      if [ "$crossed" = "yes" ]; then
        EVER_POSITIVE[$sym]=yes
        echo "[$ts] *** $sym CROSSED ZERO: pnl=$pnl ***"
      fi
    fi

    pnl_fmt=$(printf "%+8.2f" "$pnl")
    peak_fmt=$(printf "%+8.2f" "$new_peak")
    echo "[$ts] $und $sym qty=$qty entry=$entry curr=$curr  pnl=$pnl_fmt  peak=$peak_fmt"
    echo "$ts,$sym,$und,$qty,$entry,$curr,$pnl,$new_peak" >> "$CSV"
  done < <(echo "$resp" | jq -r '.positions[]? | select(.underlying=="IWM" or .underlying=="META") | [.symbol, .underlying, .quantity, .avg_entry_price, .current_price, .unrealized_pnl] | @tsv')

  # Detect close: tracked symbol no longer present in broker positions response.
  for sym in "${!LAST_SEEN[@]}"; do
    if [[ ! " $current_syms " == *" $sym "* ]]; then
      ever=${EVER_POSITIVE[$sym]:-no}
      peak=${PEAK[$sym]:-0}
      printf "[%s] %s CLOSED — peak_pnl=%+.2f, ever_profitable=%s\n" "$ts" "$sym" "$peak" "$ever"
      unset 'LAST_SEEN[$sym]'
    fi
  done

  sleep "$INTERVAL"
done
