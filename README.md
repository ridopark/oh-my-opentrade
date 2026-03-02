# oh-my-opentrade

Professional-grade, broker-agnostic algorithmic trading ecosystem.

See [docs/PRD.md](docs/PRD.md) and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for details.

## Quick Start

```bash
# Run tests
cd backend && go test ./...

# Build
cd backend && go build -o bin/omo-core ./cmd/omo-core

# Run with Docker Compose
docker compose -f deployments/docker-compose.yml up
```

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
apps/dashboard/       - Next.js 15 frontend (Phase 7)
deployments/          - Docker Compose & Dockerfiles
configs/              - App config & strategy DNA TOML files
```
