#!/bin/bash
# sweep.sh — run a sequence of (variant, activate, giveback) backtests on the IS window.
# Usage: sweep.sh <window_label> <from> <to> <configs_file>
# where configs_file is a TSV of "variant<TAB>activate<TAB>giveback<TAB>label" rows.

set -euo pipefail

WIN="$1"; FROM="$2"; TO="$3"; SPEC="$4"
ROOT=/home/ridopark/src/oh-my-opentrade
SCRIPT_DIR="${ROOT}/_workspace/macd_chandelier"

while IFS=$'\t' read -r variant activate giveback label; do
  [ -z "${variant:-}" ] && continue
  case "${variant}" in '#'*) continue;; esac
  echo "============================================================"
  echo "[${WIN}] variant=${variant} a=${activate} g=${giveback} label=${label}"
  if [ "${variant}" = "baseline" ]; then
    python3 "${SCRIPT_DIR}/apply_variant.py" baseline
  else
    python3 "${SCRIPT_DIR}/apply_variant.py" "${variant}" "${activate}" "${giveback}"
  fi
  "${SCRIPT_DIR}/run_backtest.sh" "${WIN}_${label}" "${FROM}" "${TO}"
done < "${SPEC}"

# Always restore baseline when done.
python3 "${SCRIPT_DIR}/apply_variant.py" baseline
echo "[${WIN}] sweep done — config restored to baseline"
