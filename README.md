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
- Dark-pool print aggregation from SIP trade feed (5m bars: volume, buy/sell,
  large prints, VWAP)
- Late-session DP Z-score indicator (14:00-15:30 ET buy ratio, 20-day rolling
  normalization) for next-day directional bias
- Whale accumulation scoring from SEC 13F filings
- Historical options chains sourced from DoltHub

**Strategies (3 live + 1 staged)**
- **ORB** — opening-range breakout, volume confirmation, regime-gated
- **AVWAP** — anchored VWAP mean reversion from 5m/15m regime extremes, with
  late-session dark-pool Z-score entry suppression (Sharpe +0.39)
- **MACD** — crossover with swing stops, dark-pool block-flow veto, and inverted
  Z-score regime filter that blocks entries in losing reversal regimes (PF +0.08)
- **Overnight Z-Score Bias** — daily-horizon strategy using late-session DP buy_ratio
  Z-score as a next-day directional signal. Equity shares only, no options. Staged
  for paper validation.
- Confluence layer combines technical, dark-pool, and 13F signals into a 0-100 score
  that gates every entry
- **Late-session DP Z conditioning** — the 14:00-15:30 ET dark-pool buy ratio,
  normalized over 20 trading days, predicts next-day returns (IC=-0.039, t=-3.92).
  AVWAP and MACD respond oppositely: AVWAP benefits from low-Z days (mean-reversion
  tailwind), MACD benefits from high-Z days (momentum continuation). Each strategy
  applies the signal in its own direction.

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
- **86% faster** after a 5-phase optimization sprint (see below)

**Operations**
- TimescaleDB hypertables for bars, trades, equity snapshots
- Prometheus metrics, Loki structured logs
- Discord/Telegram webhooks for trades and risk alerts

## Backtest performance

A five-phase optimization sprint reduced wall-time by **86%** across all workloads while
preserving deterministic signal-count parity:

| Workload | Before | After | Reduction |
|---|---|---|---|
| 30 symbols / 1 year | ~130 s | **18 s** | -86% |
| 8 symbols / 1 year | 31.6 s | **5.0 s** | -84% |
| 8 symbols / 3 months | 10.1 s | **1.9 s** | -81% |

Each phase was gated by a benchmark — we only advanced when the prior phase wasn't enough:

1. **Phase 1 — Direct dispatch.** Replaced the pub/sub event bus with direct function calls
   (`ingestion.ProcessBar → monitor.HandleMarketBar → runner.HandleBarDirect`), shrunk
   indicator windows from 250 → 60 bars, and reordered struct layouts for cache locality.
2. **Phase 2 — Shard infrastructure.** Cloned monitor + runner per shard so each goroutine
   owns disjoint mutable state. This phase missed its gate (per-tick barriers cost more than
   the work they parallelized) but delivered the sharding scaffolding Phase 3 needed.
3. **Phase 3 — Slice-to-completion.** Each shard runs its full bar stream to completion with
   no per-tick synchronization, then a k-way merge replays signals in tick order. Eliminated
   240 k barrier wake-ups per year of data.
4. **Phase 4+5 — Allocation reduction.** Typed hot paths bypass `domain.Event` construction.
   Pre-sized slabs, zero-alloc ingestion, and eliminated idempotency-key string concatenation
   cut total allocations from 19 GB → 9.7 GB per run. GC CPU dropped from 12 s → 8 s.

See [docs/perf/README.md](docs/perf/README.md) for the full roadmap, pprof analysis, and
commit history.

## Strengths

- **Hexagonal architecture** — ports and adapters cleanly separate the Go core from
  every external system. Swapping Alpaca for IBKR, in-memory bus for NATS, or backtester
  for live is a two-hour adapter rewrite, not a refactor.
- **Persistence-first order model** — order intents are journaled *before* the broker
  API is called. Startup reconciliation guarantees zero orphaned stops on crash.
- **Dark-pool + 13F confluence** — trades only fire when technical, dark-pool, and
  institutional positioning agree. Empirically cuts churn versus single-indicator
  systems.
- **Late-session DP Z conditioning** — late-hour (14:00-15:30 ET) dark-pool flow
  predicts next-day returns. AVWAP and MACD each apply the signal in their own
  direction (mean-reversion vs momentum), validated across 13,204 trades.
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
