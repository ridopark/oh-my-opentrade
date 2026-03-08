# oh-my-opentrade

Professional-grade, broker-agnostic algorithmic trading ecosystem.

## Project Structure

```
backend/
  cmd/omo-core/       - Single binary MVP entry point
  internal/
    domain/           - Pure business logic, entities, events, value objects
    ports/            - Interface definitions (hexagonal boundaries)
    app/              - Application services (orchestrate domain + ports)
    adapters/         - Port implementations (Alpaca, TimescaleDB, etc.)
migrations/           - TimescaleDB SQL migration files
apps/dashboard/       - Next.js 15 frontend
deployments/          - Docker Compose & Dockerfiles
configs/              - App config & strategy DNA TOML files
```

## Architecture

This project follows **Hexagonal Architecture** (Ports & Adapters):

- **Domain layer** (`internal/domain/`) contains pure business logic with zero external dependencies
- **Ports** (`internal/ports/`) define interfaces that the domain needs
- **Adapters** (`internal/adapters/`) implement ports for specific technologies (Alpaca API, TimescaleDB, etc.)
- **Application services** (`internal/app/`) orchestrate domain logic through ports

## Code Standards

- **Backend**: Go with strict linting, hexagonal architecture patterns
- **Frontend**: Next.js 15, TypeScript, React
- **Testing**: `go test ./...` for backend, standard Jest/Vitest for frontend
- **Build**: `go build -o bin/omo-core ./cmd/omo-core` for backend

## Debugging

Loki is the primary log store for runtime debugging. Always check Loki logs when investigating production/paper trading issues (missed fills, unexpected behavior, order failures).

```bash
# Check Loki is running
curl -s http://localhost:3100/ready

# Query recent logs (plain text filter)
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "SEARCH_TERM"' \
  --data-urlencode 'limit=30' \
  --data-urlencode 'direction=backward'

# Trace a specific order by intent ID or broker order ID
curl -sG 'http://localhost:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={job="omo-core"} |~ "ORDER_ID_HERE"' \
  --data-urlencode 'limit=50' \
  --data-urlencode 'direction=forward'
```

Logs are structured JSON. Parse with `python3 -c "import json, sys; ..."` to extract fields. Key components to filter: `execution`, `alpaca`, `position_monitor`, `risk_sizer`, `signal_debate_enricher`.

## Key References

- [docs/PRD.md](docs/PRD.md) — Product Requirements Document
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — Detailed architecture documentation
- [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md) — Implementation plan

## Development Commands

```bash
# Backend tests
cd backend && go test ./...

# Backend build
cd backend && go build -o bin/omo-core ./cmd/omo-core

# Run backend
cd backend && go run ./cmd/omo-core/

# Frontend dev server
cd apps/dashboard && npm run dev

# Docker Compose
docker compose -f deployments/docker-compose.yml up
```
