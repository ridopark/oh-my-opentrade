#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

OMO_SESSION="omo-core"
DASH_SESSION="omo-dashboard"
OMO_PORT=8080
DASH_PORT=8000

# ── Colors ───────────────────────────────────────────────────
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[start]${NC} $*"; }
warn()  { echo -e "${YELLOW}[start]${NC} $*"; }

kill_port() {
  local port=$1
  local pids
  pids=$(lsof -ti :"$port" 2>/dev/null || true)
  if [[ -n "$pids" ]]; then
    warn "Killing stale process(es) on port $port..."
    echo "$pids" | xargs kill -9 2>/dev/null || true
    sleep 1
  fi
}

# ── Start omo-core ───────────────────────────────────────────
if tmux has-session -t "$OMO_SESSION" 2>/dev/null; then
  warn "$OMO_SESSION tmux session already exists — skipping"
else
  kill_port "$OMO_PORT"
  mkdir -p "$ROOT_DIR/logs"
  : > "$ROOT_DIR/logs/omo-core.log"
  info "Building omo-core..."
  (cd "$ROOT_DIR/backend" && go build -o bin/omo-core ./cmd/omo-core)
  info "Starting omo-core in tmux session..."
  tmux new-session -d -s "$OMO_SESSION" -c "$ROOT_DIR" \
    "$ROOT_DIR/backend/bin/omo-core 2>&1 | tee -a $ROOT_DIR/logs/omo-core.log"
  info "omo-core started  →  tmux attach -t $OMO_SESSION"
fi

# ── Start dashboard ─────────────────────────────────────────
if tmux has-session -t "$DASH_SESSION" 2>/dev/null; then
  warn "$DASH_SESSION tmux session already exists — skipping"
else
  kill_port "$DASH_PORT"
  info "Starting dashboard in tmux session..."
  tmux new-session -d -s "$DASH_SESSION" -c "$ROOT_DIR/apps/dashboard" "npm run dev"
  info "dashboard started →  tmux attach -t $DASH_SESSION"
fi

echo ""
info "All services launched. Useful commands:"
echo "  tmux attach -t $OMO_SESSION      # view backend logs"
echo "  tmux attach -t $DASH_SESSION   # view dashboard logs"
echo "  ./scripts/shutdown.sh            # stop omo-core + dashboard"
echo "  ./scripts/start-infra.sh         # start monitoring stack"
