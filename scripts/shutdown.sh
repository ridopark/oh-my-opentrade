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

# ── Stop omo-core ────────────────────────────────────────────
if tmux has-session -t "$OMO_SESSION" 2>/dev/null; then
  tmux send-keys -t "$OMO_SESSION" C-c
  sleep 1
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

info "All services stopped."
