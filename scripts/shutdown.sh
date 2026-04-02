#!/usr/bin/env bash
set -euo pipefail

OMO_SESSION="omo-core"
DASH_SESSION="omo-dashboard"

# ── Colors ───────────────────────────────────────────────────
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[shutdown]${NC} $*"; }
warn()  { echo -e "${YELLOW}[shutdown]${NC} $*"; }

# ── Stop Docker omo-core (should not run locally) ───────────
if docker ps -q -f name=omo-core 2>/dev/null | grep -q .; then
  docker stop omo-core >/dev/null 2>&1 || true
  info "stopped Docker omo-core container"
fi

# ── Kill anything on port 8080 ──────────────────────────────
if pid=$(fuser 8080/tcp 2>/dev/null); then
  kill "$pid" 2>/dev/null && sleep 1
  kill -9 "$pid" 2>/dev/null || true
  info "killed stale process on port 8080 (pid $pid)"
fi

# ── Stop omo-core ────────────────────────────────────────────
if tmux has-session -t "$OMO_SESSION" 2>/dev/null; then
  tmux send-keys -t "$OMO_SESSION" C-c
  info "waiting for omo-core to finish graceful shutdown..."
  for i in $(seq 1 15); do
    pgrep -f "bin/omo-core" > /dev/null || break
    sleep 1
  done
  tmux kill-session -t "$OMO_SESSION" 2>/dev/null || true
  info "omo-core stopped"
else
  warn "omo-core tmux session not found — already stopped?"
fi

# ── Stop dashboard ───────────────────────────────────────────
if tmux has-session -t "$DASH_SESSION" 2>/dev/null; then
  tmux send-keys -t "$DASH_SESSION" C-c
  sleep 1
  tmux kill-session -t "$DASH_SESSION" 2>/dev/null || true
  info "dashboard stopped"
else
  warn "dashboard tmux session not found — already stopped?"
fi

info "All services stopped. Monitoring stack (Grafana, Prometheus, Loki, Fluent Bit) left running."
