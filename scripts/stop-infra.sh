#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MONITORING_COMPOSE="$ROOT_DIR/deployments/docker-compose.monitoring.yml"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[infra]${NC} $*"; }
warn()  { echo -e "${YELLOW}[infra]${NC} $*"; }

if docker compose -f "$MONITORING_COMPOSE" ps --status running 2>/dev/null | grep -q "omo-"; then
  info "Stopping monitoring stack..."
  docker compose -f "$MONITORING_COMPOSE" down
  info "monitoring stopped"
else
  warn "monitoring stack not running"
fi
