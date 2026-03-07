#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MONITORING_COMPOSE="$ROOT_DIR/deployments/docker-compose.monitoring.yml"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[infra]${NC} $*"; }
warn()  { echo -e "${YELLOW}[infra]${NC} $*"; }

if docker compose -f "$MONITORING_COMPOSE" ps --status running 2>/dev/null | grep -q "omo-timescaledb"; then
  warn "infra already running — skipping"
else
  mkdir -p "$ROOT_DIR/logs"
  export HOST_IP
  HOST_IP=$(hostname -I | awk '{print $1}')
  info "Starting infra... [HOST_IP=$HOST_IP]"
  docker compose -f "$MONITORING_COMPOSE" up -d
  info "TimescaleDB  localhost:5432"
  info "Grafana      http://localhost:3001"
  info "Prometheus   http://localhost:9090"
  info "Loki         http://localhost:3100"
fi
