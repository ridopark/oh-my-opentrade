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
