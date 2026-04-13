#!/usr/bin/env bash
# next-watchdog.sh — Restarts next dev if RSS exceeds a threshold.
# Prevents OOM kills that take down tmux and other processes.
#
# Usage: next-watchdog.sh [max_rss_mb]   (default: 3072 = 3 GB)

set -euo pipefail

MAX_RSS_MB="${1:-3072}"
MAX_RSS_KB=$((MAX_RSS_MB * 1024))
CHECK_INTERVAL=30  # seconds between RSS checks

DIR="$(cd "$(dirname "$0")/.." && pwd)/apps/dashboard"

echo "[watchdog] next dev will be restarted if RSS exceeds ${MAX_RSS_MB} MB"
echo "[watchdog] working directory: $DIR"

cleanup() {
  echo "[watchdog] shutting down..."
  if [[ -n "${NEXT_PID:-}" ]] && kill -0 "$NEXT_PID" 2>/dev/null; then
    kill "$NEXT_PID" 2>/dev/null
    wait "$NEXT_PID" 2>/dev/null || true
  fi
  exit 0
}
trap cleanup SIGINT SIGTERM

descendants() {
  # Recursively collect all descendant PIDs of a given PID
  local parent=$1
  local children
  children=$(pgrep -P "$parent" 2>/dev/null || true)
  for child in $children; do
    echo "$child"
    descendants "$child"
  done
}

while true; do
  echo "[watchdog] starting next dev..."
  cd "$DIR"
  npm run dev &
  NEXT_PID=$!

  # Wait a few seconds for the process tree to spawn
  sleep 5

  while kill -0 "$NEXT_PID" 2>/dev/null; do
    # Sum RSS of entire process tree (next dev spawns deeply nested workers)
    TOTAL_RSS=0
    for pid in $NEXT_PID $(descendants "$NEXT_PID"); do
      if [[ -f "/proc/$pid/status" ]]; then
        rss=$(awk '/^VmRSS:/ {print $2}' "/proc/$pid/status" 2>/dev/null || echo 0)
        TOTAL_RSS=$((TOTAL_RSS + rss))
      fi
    done

    TOTAL_RSS_MB=$((TOTAL_RSS / 1024))
    echo "[watchdog] RSS: ${TOTAL_RSS_MB} MB / ${MAX_RSS_MB} MB"
    if [[ $TOTAL_RSS -gt $MAX_RSS_KB ]]; then
      echo "[watchdog] RSS ${TOTAL_RSS_MB} MB exceeds limit ${MAX_RSS_MB} MB — restarting"
      # Kill the entire process group to reach deeply nested next-server
      kill -- -"$NEXT_PID" 2>/dev/null || kill "$NEXT_PID" 2>/dev/null
      wait "$NEXT_PID" 2>/dev/null || true
      sleep 2
      break  # restart outer loop
    fi

    sleep "$CHECK_INTERVAL"
  done

  # If next dev exited on its own (crash), wait before restart
  if ! kill -0 "$NEXT_PID" 2>/dev/null; then
    EXIT_CODE=0
    wait "$NEXT_PID" 2>/dev/null || EXIT_CODE=$?
    if [[ $EXIT_CODE -ne 0 ]]; then
      echo "[watchdog] next dev exited with code $EXIT_CODE — restarting in 3s"
      sleep 3
    fi
  fi
done
