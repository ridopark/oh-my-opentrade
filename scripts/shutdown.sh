#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

OMO_SESSION="omo-core"
DASH_SESSION="omo-dashboard"

# ── Colors ───────────────────────────────────────────────────
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[shutdown]${NC} $*"; }
warn()  { echo -e "${YELLOW}[shutdown]${NC} $*"; }

# ── Worktree safety ──────────────────────────────────────────
# The omo-core tmux session name is global across the machine, so invoking
# shutdown.sh from a sandbox worktree would kill the LIVE session running
# in the primary worktree. Refuse unless explicitly overridden.
check_worktree_safety() {
    local primary
    primary=$(git -C "$ROOT_DIR" worktree list --porcelain 2>/dev/null \
              | awk '/^worktree / { print $2; exit }' || true)
    if [[ -z "$primary" || "$ROOT_DIR" == "$primary" ]]; then
        return  # primary worktree or no worktree awareness — safe
    fi
    if [[ "${OMO_ALLOW_SANDBOX_SHUTDOWN:-}" == "1" ]]; then
        warn "OMO_ALLOW_SANDBOX_SHUTDOWN=1 set — proceeding from sandbox worktree"
        return
    fi
    warn "refusing to run shutdown.sh from a non-primary worktree"
    warn "  current:  $ROOT_DIR"
    warn "  primary:  $primary"
    warn ""
    warn "the '$OMO_SESSION' tmux session name is shared globally, so shutting"
    warn "down from a sandbox would kill the live session running in the primary."
    warn ""
    warn "to override, rerun with OMO_ALLOW_SANDBOX_SHUTDOWN=1"
    exit 1
}
check_worktree_safety

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
  # Sprint 1 Phase B order drain runs up to 30s polling ib.OpenTrades()
  # when there are working orders at shutdown time, plus ~5s for the
  # HTTP server shutdown. Wait 45s total (30s drain + 5s HTTP + 10s
  # buffer) so tmux kill-session does NOT SIGHUP the process mid-drain
  # and corrupt the in-flight order state that Sprint 1 was designed
  # to preserve.
  for i in $(seq 1 45); do
    pgrep -f "bin/omo-core" > /dev/null || break
    if (( i == 15 )); then
      info "still waiting (15s) — drain may be polling working orders..."
    elif (( i == 30 )); then
      info "still waiting (30s) — drain deadline reached, HTTP shutdown next..."
    elif (( i == 44 )); then
      warn "still waiting (44s) — process not exiting, will force-kill tmux"
    fi
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

# ── Kill anything on port 8000 ──────────────────────────────
# Catches bare `npm/pnpm dev` launches that never entered the $DASH_SESSION
# tmux session — e.g. ad-hoc `nohup pnpm dev` runs from debugging. Without
# this, start.sh's dashboard launch dies with EADDRINUSE on the next cycle.
if pid=$(fuser 8000/tcp 2>/dev/null); then
  kill "$pid" 2>/dev/null && sleep 1
  kill -9 "$pid" 2>/dev/null || true
  info "killed stale process on port 8000 (pid $pid)"
fi

info "All services stopped. Monitoring stack (Grafana, Prometheus, Loki, Fluent Bit) left running."
