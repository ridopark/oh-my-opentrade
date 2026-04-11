# oh-my-opentrade

**Algorithmic trading, built like infrastructure.**

A broker-agnostic, hexagonal-architecture trading system for US equities and options.
Three deterministic strategies run on the hot path, eight gates enforce risk, and every
order intent is journaled before the broker sees it so the system can recover cleanly
from a hard crash.

- Single Go binary, Next.js dashboard, TimescaleDB, NATS event bus
- Alpaca + Interactive Brokers adapters
- Dark-pool and 13F whale accumulation confluence scoring
- Paper trading stable; live validation in progress on IBKR

See [docs/PRD.md](docs/PRD.md), [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md),
and [docs/plans/ROADMAP.md](docs/plans/ROADMAP.md) for deeper context.

## What it does

**Data**
- Real-time 1m/5m/15m bar streaming from Alpaca WebSocket with 4-sigma sanitization
- Historical backfill from Alpaca REST + YahooFinance
- Dark-pool print aggregation from SIP trade feed
- Whale accumulation scoring from SEC 13F filings
- Historical options chains sourced from DoltHub

**Strategies (3 live)**
- **ORB** — opening-range breakout, volume confirmation, regime-gated
- **AVWAP** — anchored VWAP mean reversion from 5m/15m regime extremes
- **MACD** — crossover with swing stops and dark-pool block-flow veto
- Confluence layer combines technical, dark-pool, and 13F signals into a 0-100 score
  that gates every entry

**Execution (8 gates)**
- short_direction, exposure_guard, portfolio_guard, risk_engine, slippage_guard,
  trading_window, spread_guard, buying_power_guard
- Write-ahead order journal survives crashes
- Startup reconciliation matches broker open orders against the journal before any
  new decision — protective stops are never blindly cancelled
- SystemD watchdog + Docker HEALTHCHECK for auto-restart

**Backtest**
- Full event-bus backtester with isolated SimBroker
- Parameterized bar aggregation, per-symbol session tracking
- Black-Scholes options pricing (see limitations below)
- Per-trade, per-day, per-symbol P&L; Sharpe, Sortino, max drawdown

**Operations**
- TimescaleDB hypertables for bars, trades, equity snapshots
- Prometheus metrics, Loki structured logs
- Discord/Telegram webhooks for trades and risk alerts

## Strengths

- **Hexagonal architecture** — ports and adapters cleanly separate the Go core from
  every external system. Swapping Alpaca for IBKR, in-memory bus for NATS, or backtester
  for live is a two-hour adapter rewrite, not a refactor.
- **Persistence-first order model** — order intents are journaled *before* the broker
  API is called. Startup reconciliation guarantees zero orphaned stops on crash.
- **Dark-pool + 13F confluence** — trades only fire when technical, dark-pool, and
  institutional positioning agree. Empirically cuts churn versus single-indicator
  systems.
- **Multi-timeframe regime detection** — 5m/15m anchors classify market state
  (TREND/BALANCE/REVERSAL) before 1m entries fire, filtering regime-mismatch churn.
- **Crash resilience** — order journal + broker reconciliation + position monitor
  tick + watchdog auto-restart. Recovery in under 30 seconds.

## Honest limitations

1. **Options backtests run ~20% optimistic.** Black-Scholes pricing assumes entry IV
   persists through exit and ignores bid-ask spread and theta bleed. Realistic fill
   models are queued for Sprint 7.
2. **IBKR live execution is unvalidated.** The adapter is fully implemented and runs
   in paper mode. Live validation on a funded account is pending.
3. **Universe is 34 hardcoded symbols.** Adding a symbol currently requires a code
   redeploy. Dynamic discovery is not on the roadmap yet.
4. **Single-broker dependency during outages.** Alpaca outages halt the system;
   multi-broker failover is not architected yet.

## Architecture

```
                           ALPACA
                             |
                SEC 13F  --- | ---  IBKR
                         \   |   /
                          +--+--+
                         /       \
              DOLTHUB --|  CORE   |-- TIMESCALE
                         \       /
                          +--+--+
                             |
                          NATS BUS

CORE = strategy engine + execution gates + order journal
Every edge is a port; every label is a swappable adapter.
```

## Quick start

```bash
# Run tests
cd backend && go test ./...

# Build the core binary
cd backend && go build -o bin/omo-core ./cmd/omo-core

# Run the full stack with Docker Compose
docker compose -f deployments/docker-compose.yml up

# Or run core + dashboard locally
cd backend && go run ./cmd/omo-core/
cd apps/dashboard && npm run dev

# Convenience scripts
./scripts/start.sh      # start everything
./scripts/shutdown.sh   # stop everything

# Logs
tmux attach -t omo-core       # backend logs
tmux attach -t omo-dashboard  # dashboard logs
```

The landing page lives at `/` in the dashboard. The signal monitor moved to `/signals`.

## Project structure

```
backend/
  cmd/omo-core/       Single binary entry point
  internal/
    domain/           Pure business logic, entities, events, value objects
    ports/            Interface definitions (hexagonal boundaries)
    app/              Application services (orchestrate domain + ports)
    adapters/         Port implementations (Alpaca, IBKR, TimescaleDB, ...)
migrations/           TimescaleDB SQL migrations
apps/dashboard/       Next.js 15 frontend
deployments/          Docker Compose + Dockerfiles
configs/              App config + strategy DNA TOML files
docs/plans/           Sprint plans and roadmap
```

## Roadmap

See [docs/plans/ROADMAP.md](docs/plans/ROADMAP.md). Currently in Sprint 3.5
(journal flag removal, pending 24h validation gate) with Sprint 4 queued for
risk-management gates and a 3-state kill switch.
