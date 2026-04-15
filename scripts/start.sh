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

# ── Worktree safety ──────────────────────────────────────────
# Refuse to run from a non-primary worktree by default. Running omo-core
# from a sandbox would collide with the live session on IBKR client_id
# (single-connection limit), HTTP port 8080, the shared tmux session name,
# and the shared TimescaleDB. The only safe path is to stop the primary's
# omo-core first, then override with OMO_ALLOW_SANDBOX_START=1.
check_worktree_safety() {
    local primary
    primary=$(git -C "$ROOT_DIR" worktree list --porcelain 2>/dev/null \
              | awk '/^worktree / { print $2; exit }' || true)
    if [[ -z "$primary" || "$ROOT_DIR" == "$primary" ]]; then
        return  # primary worktree or no worktree awareness — safe
    fi
    if [[ "${OMO_ALLOW_SANDBOX_START:-}" == "1" ]]; then
        warn "OMO_ALLOW_SANDBOX_START=1 set — proceeding in sandbox worktree"
        warn "(you are responsible for avoiding client_id / port / tmux / DB collisions)"
        return
    fi
    warn "refusing to run start.sh from a non-primary worktree"
    warn "  current:  $ROOT_DIR"
    warn "  primary:  $primary"
    warn ""
    warn "running omo-core from a sandbox would collide with the live session on:"
    warn "  - IBKR client_id (single-connection limit per id)"
    warn "  - HTTP port $OMO_PORT"
    warn "  - tmux session '$OMO_SESSION'"
    warn "  - shared TimescaleDB writes"
    warn ""
    warn "to override, stop the primary's omo-core first, then rerun with"
    warn "  OMO_ALLOW_SANDBOX_START=1 ./scripts/start.sh"
    exit 1
}

# ── .env bootstrap for worktrees ─────────────────────────────
# When running from a fresh worktree, .env is not copied from the primary
# because it's gitignored. Create a symlink so this worktree inherits the
# primary's environment (IBKR creds, Discord webhook, DB password, feature
# flags). Edits to the primary's .env are visible everywhere automatically.
ensure_env() {
    if [[ -e "$ROOT_DIR/.env" ]]; then
        return  # already present (file or existing symlink)
    fi
    local primary
    primary=$(git -C "$ROOT_DIR" worktree list --porcelain 2>/dev/null \
              | awk '/^worktree / { print $2; exit }' || true)
    if [[ -z "$primary" || "$primary" == "$ROOT_DIR" ]]; then
        warn "no .env found and no primary worktree detected — cannot bootstrap environment"
        exit 1
    fi
    if [[ ! -f "$primary/.env" ]]; then
        warn "primary worktree at $primary has no .env either — run setup first"
        exit 1
    fi
    info "symlinking .env from primary worktree: $primary/.env"
    ln -s "$primary/.env" "$ROOT_DIR/.env"
}

check_worktree_safety
ensure_env

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

kill_zombie_omo() {
  # Find any omo-core processes NOT in the current tmux session.
  # These zombies hold Alpaca WebSocket connections, blocking new instances.
  local pids
  pids=$(pgrep -f 'bin/omo-core' 2>/dev/null || true)
  if [[ -n "$pids" ]]; then
    warn "Killing zombie omo-core process(es): $pids"
    echo "$pids" | xargs kill 2>/dev/null || true
    sleep 2
    # Force-kill any survivors
    pids=$(pgrep -f 'bin/omo-core' 2>/dev/null || true)
    if [[ -n "$pids" ]]; then
      echo "$pids" | xargs kill -9 2>/dev/null || true
      sleep 1
    fi
  fi
}

# ── Start omo-core ───────────────────────────────────────────
if tmux has-session -t "$OMO_SESSION" 2>/dev/null; then
  warn "$OMO_SESSION tmux session already exists — skipping"
else
  kill_zombie_omo
  kill_port "$OMO_PORT"
  mkdir -p "$ROOT_DIR/logs"
  : > "$ROOT_DIR/logs/omo-core.log"
  info "Building omo-core..."
  (cd "$ROOT_DIR/backend" && go build -o bin/omo-core ./cmd/omo-core)
  info "Starting omo-core in tmux session..."
  tmux new-session -d -s "$OMO_SESSION" -c "$ROOT_DIR" \
    "set -a; source $ROOT_DIR/.env; set +a; GOMEMLIMIT=4GiB $ROOT_DIR/backend/bin/omo-core 2>&1 | tee -a $ROOT_DIR/logs/omo-core.log"
  info "omo-core started  →  tmux attach -t $OMO_SESSION"
fi

# ── Start dashboard ─────────────────────────────────────────
if tmux has-session -t "$DASH_SESSION" 2>/dev/null; then
  warn "$DASH_SESSION tmux session already exists — skipping"
else
  kill_port "$DASH_PORT"
  info "Starting dashboard in tmux session..."
  tmux new-session -d -s "$DASH_SESSION" -c "$ROOT_DIR" "$ROOT_DIR/scripts/next-watchdog.sh 3072"
  info "dashboard started →  tmux attach -t $DASH_SESSION"
fi

echo ""
info "All services launched. Useful commands:"
echo "  tmux attach -t $OMO_SESSION      # view backend logs"
echo "  tmux attach -t $DASH_SESSION   # view dashboard logs"
echo "  ./scripts/shutdown.sh            # stop omo-core + dashboard"
echo "  ./scripts/start-infra.sh         # start monitoring stack"
