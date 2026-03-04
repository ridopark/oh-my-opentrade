# Implementation Plan: oh-my-opentrade

Last Updated: March 4, 2026 (Session 4)

## Section 1: Progress Summary

### Phase 1 — Foundation
| # | Item | Status |
|---|------|--------|
| 1 | TimescaleDB schema + migrations (7 migrations: accounts, bars, trades, thought_logs, strategy_history, orders, unique_constraints) | ✅ Done |
| 2 | Domain types (MarketBar, Advisory, OrderIntent, Trade, events, value objects) | ✅ Done |
| 3 | Port interfaces (MarketDataPort, BrokerPort, EventBusPort, RepositoryPort, AIAdvisorPort, NotifierPort, OptionsMarketDataPort) | ✅ Done |

### Phase 2 — Data Pipeline
| # | Item | Status |
|---|------|--------|
| 4 | In-memory event bus adapter | ✅ Done |
| 5 | Alpaca adapter (WebSocket + REST + Options Support + rate limiter @ 200 req/min) | ✅ Done |
| 6 | TimescaleDB adapter (SQL query builders, RepositoryPort impl — SaveMarketBar, GetMarketBars, etc.) | ✅ Done |
| 7 | Ingestion service (Z-score anomaly filter, bar sanitization) | ✅ Done |

### Phase 3 — Intelligence
| # | Item | Status |
|---|------|--------|
| 8 | Monitor service (SMA, EMA, RSI, MACD, Bollinger, ATR, CCI, regime classifier, setup detection) | ✅ Done |
| 9 | Execution service (risk engine 2% max, options risk, slippage guard, kill switch 3-in-2min→15min halt) | ✅ Done |

### Phase 4 — Wire & Run
| # | Item | Status |
|---|------|--------|
| 10 | omo-core main.go — Full DI wiring (320 lines), all services subscribed to event bus | ✅ Done |
| 11 | Docker Compose (timescaledb, migrate, omo-core, omo-dashboard) | ✅ Done |
| 12 | Config system (.env secrets + config.yaml templates, env overlay, validation) | ✅ Done |

### Phase 5 — Historical Data
| # | Item | Status |
|---|------|--------|
| 13 | `omo-backfill` CLI tool — Chunked historical bar download from Alpaca, progress logging | ✅ Done |
| 14 | SaveMarketBars batch insert + GetLatestMarketBarTime on RepositoryPort | ✅ Done |

### Phase 6 — AI & Strategy
| # | Item | Status |
|---|------|--------|
| 15 | LLM adapter — OpenAI-compatible (Claude, Ollama, LM Studio) for Bull/Bear/Judge debates | ✅ Done |
| 16 | Debate service — Subscribes to SetupDetected, runs AI adversarial debate, emits OrderIntentCreated | ✅ Done |
| 17 | Strategy DNA engine — Yaegi hot-swap runtime, TOML hot-reload (5s), position sizing | ✅ Done |
| 18 | ORB Break & Retest strategy TOML (`configs/strategies/orb_break_retest.toml`) | ✅ Done |
| 19 | Options contract selection, order, and risk modules | ✅ Done |

### Phase 7 — Dashboard
| # | Item | Status |
|---|------|--------|
| 20 | Next.js 15 app with TailwindCSS + shadcn/ui + lightweight-charts v5 | ✅ Done |
| 21 | Multi-symbol overlay chart (10 symbols, color-coded) | ✅ Done |
| 22 | Pan-left lazy loading with visible range detection | ✅ Done |
| 23 | Loading spinner ("Loading more data...") | ✅ Done |
| 24 | Off-market-hours shading (ET timezone, 9:30a–4p market hours) | ✅ Done |
| 25 | Gap break rendering for missing data periods | ✅ Done |
| 26 | Timeframe switching (1m, 5m, 15m, 1h, 1d) — preserves visible time range | ✅ Done |
| 27 | SSE real-time event stream hooks (debates, orders, DNA changes) | ✅ Done |
| 28 | Debates page — Full UI for debate feed | ✅ Done |
| 29 | Execution monitor page — Full UI for order tracking | ✅ Done |
| 30 | Strategy DNA page — Full UI with diff view | ✅ Done |
| 31 | Next.js API proxy for bars (`/api/bars`) and events (`/api/events`) | ✅ Done |

### Phase 8 — Notifications & Observability
| # | Item | Status |
|---|------|--------|
| 32 | SSE handler for dashboard real-time push | ✅ Done |
| 33 | HTTP handlers (bars, health, services, strategy endpoints) | ✅ Done |
| 34 | Access logging middleware | ✅ Done |
| 35 | Structured zerolog logger | ✅ Done |
| 36 | Telegram notification adapter | ✅ Done |
| 37 | Discord notification adapter | ✅ Done |
| 38 | MultiNotifier fan-out | ✅ Done |
| 38b | Dockerfile.dashboard for containerized frontend | ✅ Done |

### Phase 8.5 — Strategy Architecture (Phases A–H)

| # | Item | Status |
|---|------|--------|
| A | Domain Contracts and Types — Strategy interface, Signal, InstanceID, lifecycle state machine, value objects (50+ tests) | ✅ Done |
| B | Wrap ORBTracker as Strategy — ORBStrategy adapter implementing Strategy interface, SetupCondition→Signal conversion (11 tests) | ✅ Done |
| C | StrategyRunner and Routing — Runner, Router, Instance with multi-symbol routing, bar dispatching (19 tests) | ✅ Done |
| D | TOML v2 Spec Loader — spec_loader.go with v1→v2 migration, store_fs filesystem adapter (7 tests) | ✅ Done |
| E1 | RiskSizer — Signal→OrderIntent pipeline with position sizing, limit/stop price computation (8 tests) | ✅ Done |
| E2 | Wire StrategyRunner into main.go — Feature-flagged v2 pipeline, STRATEGY_V2=true activates new path | ✅ Done |
| E3 | Integration test — Bar→Runner→Signal→RiskSizer→OrderIntent end-to-end test (3 tests) | ✅ Done |
| F1 | Lifecycle Service — Promote, Deactivate, Archive with state machine validation | ✅ Done |
| F2 | HTTP Lifecycle Endpoints — REST API for lifecycle management | ✅ Done |
| F3 | Dashboard Strategies Page — Next.js page showing strategy instances with lifecycle controls | ✅ Done |
| F4 | Lifecycle Tests — Full test coverage (8 tests) | ✅ Done |
| G1 | SwapManager — Blue/green swap orchestrator with shadow warmup | ✅ Done |
| G2 | Router.Replace — Atomic instance swap under write lock | ✅ Done |
| G3 | WarmupOnBar — Lifecycle-bypass method for shadow instances | ✅ Done |
| G4 | Swap Tests — Full test coverage (12 tests) | ✅ Done |
| H1 | Yaegi Sandbox — Import whitelist + AST validation | ✅ Done |
| H2 | HookExecutor — Timeout + circuit breaker for Yaegi scripts | ✅ Done |
| H3 | Sandbox Tests — Full test coverage (11 tests) | ✅ Done |
### Phase 9 — Paper Trading Readiness 🚧
| # | Item | Status |
|---|------|--------|
| 39 | End-to-end flow verification (live market → event pipeline → paper order) | 🟡 In Progress — startup verified, DNA path fixed, debug logging added. Needs market-hours testing (9:30 AM–4 PM ET) for full pipeline verification. Strategy v2 pipeline ready for parallel testing via STRATEGY_V2=true flag. |
| 40 | Monitor setup detection tuning with real market data | 🟡 In Progress — debug logging added to monitor (indicator snapshots, regime classification, setup evaluation on every bar). Ready for market-hours data collection. Set `LOG_LEVEL=debug` to enable. |
| 41 | LLM provider configuration (API keys, model selection) | ✅ Done — OpenRouter configured, smoke test passes with real API key, structured debate JSON verified (LONG, 0.72 confidence, full bull/bear/judge). |
| 42 | Alpaca paper trading order submission verification | ✅ Done — Account equity ($30,965.59), order submit/status/cancel lifecycle, quote retrieval all verified via smoke tests. |
| 43 | Integration test: Alpaca WS → Ingestion → TimescaleDB round-trip | ✅ Done |
| 44 | Integration test: SetupDetected → Debate → OrderIntent → Execution → Order | ✅ Done |
| 45 | Strategy DNA parameter tuning for live conditions | 🟡 In Progress — DNA reviewed, params reasonable for ORB strategy. Regime filter allows TREND only (conservative). `min_rvol` not connected to monitor; `min_confidence` in DNA not used by strategy service (hardcodes 0.75). Full tuning after market-hours data. |
| 46 | Notification wiring (Telegram/Discord alerts on order events) | ✅ Done |
| 47 | CI/CD pipeline setup (GitHub Actions) | ✅ Done |

### Phase 10 — Hardening & Backtesting (Future)
| # | Item | Status |
|---|------|--------|
| 48 | Backtesting framework (replay historical bars through event pipeline) | 🔲 Not Started |
| 49 | Candlestick chart mode (lightweight-charts supports it natively) | 🔲 Not Started |
| 50 | Auto-reconnect for Alpaca WebSocket | 🔲 Not Started |
| 51 | Nightly evolution cycle (strategy parameter optimization) | 🔲 Not Started |
| 52 | Performance dashboard (P&L tracking, win rate, drawdown) | 🔲 Not Started |
| 53 | Observability stack (Prometheus/Grafana) | 🔲 Not Started |

---

## Section 2: Dependency Graph

```
Phase 1–8.5 (COMPLETE)
━━━━━━━━━━━━━━━━━━━

  Foundation ──→ Data Pipeline ──→ Intelligence ──→ Wire & Run ──→ Strategy Architecture (Phases A-H)
      │               │                │               │                     │
      │               ▼                │               │                     │
      │        Historical Data         │               │                     │
      │        (omo-backfill)          │               │                     ▼
      │               │                │               │               Yaegi Sandbox
      ▼               ▼                ▼               ▼                     │
  AI & Strategy ◄──── All services wired in main.go ────►  Dashboard          │
  (LLM, Debate,       │                                    (Next.js,         │
   DNA Engine,         │                                     Chart,           │
   Options)            │                                     SSE)             │
                       ▼                                                      │
               Notifications & Observability ◄────────────────────────────────┘
               (Telegram, Discord, SSE, Logging)


Phase 9: Paper Trading Readiness (NEXT)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  ┌─────────────────────────────────────────────────────────────────┐
  │                     End-to-End Flow                             │
  │                                                                 │
  │  Alpaca WS ──→ Ingestion ──→ Monitor ──→ Debate ──→ Strategy   │
  │  (bars)        (sanitize)    (setup      (AI         (DNA       │
  │                               detect)    debate)     sizing)    │
  │                                             │                   │
  │                                             ▼                   │
  │                               Execution ──→ Alpaca REST         │
  │                               (risk,       (paper order)        │
  │                                slippage,                        │
  │                                kill switch)                     │
  │                                             │                   │
  │                                             ▼                   │
  │                               Notifications + Dashboard SSE     │
  │                                                                 │
  └─────────────────────────────────────────────────────────────────┘

  Dependencies for first paper trade:
  ┌──────────────────┐     ┌──────────────────┐
  │ 41. LLM Config   │     │ 40. Monitor      │
  │ (API key, model) │     │ Tuning           │
  └────────┬─────────┘     └────────┬─────────┘
           │                        │
           ▼                        ▼
  ┌──────────────────────────────────────────┐
  │ 39. End-to-End Flow Verification         │
  │ (all events fire correctly in sequence)  │
  └────────────────────┬─────────────────────┘
                       │
           ┌───────────┼───────────┐
           ▼           ▼           ▼
    42. Paper      43–44.       46. Alert
    Order Test     Integration  Wiring
                   Tests
```

---

## Section 3: Execution Order — Phase 9

| Step | Task | Depends On | Description |
|------|------|-----------|-------------|
| 1 | 41. LLM Config | Nothing | Set LLM_BASE_URL, LLM_MODEL, LLM_API_KEY in .env; verify advisor responds |
| 2 | 40. Monitor Tuning | Nothing | Review/adjust setup detection thresholds in config.yaml; test with backfilled data |
| 3 | 45. DNA Tuning | Nothing | Review orb_break_retest.toml parameters; add more strategies if needed |
| 4 | 39. E2E Verification | 41, 40, 45 | Run omo-core during market hours; verify all 7 event stages fire |
| 5 | 42. Paper Order | 39 | Confirm Alpaca paper account receives and fills orders |
| 6 | 43. Integration Test | 39 | Automated test: WS → Ingestion → DB round-trip |
| 7 | 44. Integration Test | 39 | Automated test: Setup → Debate → Order pipeline |
| 8 | 46. Notifications | 42 | Wire Telegram/Discord alerts to order/kill-switch events |
| 9 | 47. CI/CD | Nothing | Setup GitHub Actions for build and test automation |

---

## Section 4: Task Breakdown — Phase 9

### Task 39: End-to-End Flow Verification

The full event pipeline is wired in `main.go` but has never been tested with real market data flowing through all stages. Every service subscribes to the correct events, but we need to verify the chain actually fires in production conditions.

**Event chain to verify:**
```
1. Alpaca WebSocket → MarketBarReceived          (alpaca adapter → event bus)
2. Ingestion        → MarketBarSanitized          (Z-score filter passes)
3. Monitor          → SetupDetected               (indicators trigger a setup)
4. Debate           → DebateCompleted             (LLM produces bull/bear/judge)
                    → OrderIntentCreated           (if debate recommends trade)
5. Strategy         → OrderIntentCreated (sized)   (DNA applies position sizing)
6. Execution        → Risk check → Slippage check → Kill switch check
                    → OrderSubmitted               (passes all gates)
7. Alpaca REST      → Paper order placed           (broker confirms)
```

**Subtasks:**
- [x] **39-pre.** Startup sequence verified: config loads, event bus, Alpaca adapter, TimescaleDB, account equity, Discord notifier, AI debate, all 6 services subscribe, SSE handler, HTTP server, indicator warmup, WebSocket stream. All initializes correctly.
- [x] **39-pre2.** Fixed strategy DNA path bug (absolute `/configs/` → relative `configs/`) and strategy HTTP handler base path.
- [x] **39-pre3.** Made log level configurable via `LOG_LEVEL` env var (supports `debug`, `info`, `warn`, `error`).
- [x] **39-pre4.** Fixed config.go env overlay bug — `APCA_API_BASE_URL` and `APCA_DATA_URL` from `.env` were silently ignored.
- [x] **39-pre5.** Fixed SubmitOrder stop_price bug — limit orders no longer send `stop_price` (Alpaca 422 fix).
- [ ] **39a.** Run omo-core during market hours, tail logs, verify steps 1–2 fire continuously
- [ ] **39b.** Observe Monitor logs — confirm indicator calculation + regime classification is running
- [ ] **39c.** Watch for SetupDetected events — may need to temporarily lower thresholds
- [ ] **39d.** Verify Debate service receives setup and calls LLM (requires task 41 — ✅)
- [ ] **39e.** Verify Execution receives intent and runs risk/slippage/kill-switch checks
- [ ] **39f.** Confirm order reaches Alpaca paper account (requires task 42 — ✅)

### Task 40: Monitor Setup Detection Tuning

The monitor calculates SMA, EMA, RSI, MACD, Bollinger Bands, ATR, CCI, and relative volume, then classifies market regime and detects setups. Thresholds are in `configs/config.yaml`. We need to verify these fire with real market data.

**Subtasks:**
- [x] **40a.** Debug logging added to monitor service — logs indicator snapshot (RSI, StochK/D, EMA9/21, VWAP, VolumeSMA), regime classification (type, strength, changed), and setup evaluation criteria (RSI cross conditions, EMA alignment) on every bar at DEBUG level.
- [ ] **40b.** Run during market hours, collect indicator values for tracked symbols
- [x] **40c.** Setup detection conditions reviewed — Long: RSI crosses 40 from below + EMA9 > EMA21. Short: RSI crosses 60 from above + EMA9 < EMA21. Regime filter: TREND only (EMA divergence > 1%).
- [ ] **40d.** Adjust thresholds if too tight (e.g., relax min_rvol, confidence)
- [ ] **40e.** Confirm at least one SetupDetected event fires during a trading session

### Task 41: LLM Provider Configuration

The LLM adapter is OpenAI-compatible and resides in `backend/internal/adapters/llm/`. It supports any OpenAI-compatible endpoint (OpenRouter, Ollama, LM Studio, etc.). **OpenRouter is now the default provider.**

**Subtasks:**
- [x] **41a.** Choose LLM provider — **OpenRouter** selected as default (supports Claude, GPT-4o, Llama, etc. via single API)
- [x] **41b.** Set `LLM_ENABLED=true`, `LLM_BASE_URL=https://openrouter.ai/api`, `LLM_MODEL=anthropic/claude-sonnet-4`, `LLM_API_KEY` in `.env.example`; config default updated to OpenRouter
- [x] **41b2.** Added OpenRouter `HTTP-Referer` and `X-Title` headers to LLM adapter for app identification and routing priority
- [x] **41c.** Smoke test created (`smoke_test.go` with `//go:build smoke`). Verified OpenRouter returns structured debate JSON: LONG direction, 0.72 confidence, full bull/bear/judge reasoning in ~12s. PASS.
- [ ] **41d.** Tune system prompts for Bull/Bear/Judge roles if needed (deferred — current prompts produce quality output)

### Task 42: Alpaca Paper Trading Order Verification

The Alpaca adapter supports standard and options orders. Paper endpoint: `https://paper-api.alpaca.markets`.

**Subtasks:**
- [x] **42a.** Verified `.env` has `APCA_API_BASE_URL=https://paper-api.alpaca.markets`
- [x] **42b.** Smoke test submits LIMIT order via Go test (`smoke_test.go` with `//go:build smoke`)
- [x] **42c.** Order lifecycle verified: submit → status check → cancel
- [x] **42d.** GetOrderStatus returns correct state (accepted/pending_cancel)
- [x] **42e.** CancelOrder works — order transitions to cancelled
- [ ] **42f.** Test with the execution service (end-to-end — requires market hours)

### Tasks 43–44: Integration Tests

Integration tests run against real TimescaleDB via `make test-integration`. The Makefile target auto-creates the `opentrade_test` database, runs migrations, and sets `TEST_DATABASE_URL`.

**Subtasks:**
- [x] **43a.** Create integration test tag (`//go:build integration`)
- [x] **43b.** Test: connect to TimescaleDB → SaveMarketBar → GetMarketBars round-trip
- [x] **43c.** Test: publish MarketBarReceived → verify anomalous bar is rejected and not saved
- [x] **44a.** Test: publish SetupDetected → verify DebateCompleted + OrderSubmitted fires (with mock LLM/broker)
- [x] **44b.** Test: low-confidence debate skips order creation (mock AI returns below threshold)
- [x] **44c.** Test: full pipeline with mock broker → verify OrderSubmitted
- [x] **44d.** `make test-integration` self-contained — auto-creates `opentrade_test` DB, runs migrations, passes `TEST_DATABASE_URL`
- [x] **44e.** All integration tests verified passing against real TimescaleDB (omo-timescaledb container)

---

## Section 5: Architecture Reference

```
backend/
  cmd/
    omo-core/main.go          Full DI wiring, all services
    omo-backfill/main.go       Historical data CLI
  internal/
    domain/                    
      strategy/                contract.go, lifecycle.go, types.go, errors.go
      entity.go                Entities (MarketBar, Trade, Advisory)
      value.go                 Value objects (OrderIntent, Regime)
      event.go                 Event definitions
      advisory.go              AI Advisor types
    ports/                     Hexagonal port interfaces
      strategy/                store.go, registry.go
      options_market_data.go   Options data port
    app/
      strategy/                runner.go, router.go, instance.go, risk_sizer.go, swap_manager.go, lifecycle_svc.go, spec_loader.go, registry_mem.go, builtin/orb_v1.go
      ingestion/               Z-score filter, bar processing
      entity.go                Entities (MarketBar, Trade, Advisory)
      value.go                 Value objects (OrderIntent, Regime)
      event.go                 Event definitions
      advisory.go              AI Advisor types
    ports/                     Hexagonal port interfaces
      options_market_data.go   Options data port
    app/
      ingestion/               Z-score filter, bar processing
        integration_test.go    Integration: WS → Ingestion → DB round-trip
      monitor/                 Service, indicators, regime & setup detectors
        indicators.go          SMA, EMA, RSI, MACD, Bollinger, ATR, CCI
        regime_detector.go     Market regime classification
        setup_detector.go      Setup detection logic
      execution/               Risk engine, kill switch, slippage guard
      debate/                  AI adversarial debate orchestration
      strategy/                DNA engine, Yaegi hot-swap, TOML loading
      options/                 Options service
      notify/                  Notification service (event bus → notifier)
      backfill/                Chunked historical download
      pipeline_integration_test.go  Integration: Setup → Debate → Order pipeline
    adapters/
      strategy/
        store_fs/              Filesystem strategy store
        hooks_yaegi/           Yaegi hot-swap runtime sandbox
      alpaca/                  
      alpaca/                  
        adapter.go             Main adapter
        options_rest.go        Options REST client
        options_order.go       Options order execution
      timescaledb/             
        repository.go          Repository implementation
        db_sql.go              SQL query builders
      eventbus/memory/         In-memory pub/sub
      llm/                     OpenAI-compatible adapter (supports Claude, Ollama)
      http/                    Bars, health, services, strategy, lifecycle handlers
      sse/                     Server-sent events for dashboard
      notification/            
        multi.go               Fan-out notifier
        telegram.go            Telegram adapter
        discord.go             Discord adapter
      middleware/              Access logging
    config/                    .env + YAML loader
    logger/                    Zerolog structured logging

apps/dashboard/                Next.js 15 + lightweight-charts v5
configs/
  config.yaml                  Runtime thresholds and symbols
  config.yaml.example          Config template
  strategies/*.toml            Strategy DNA files (hot-reloaded)
deployments/
  docker-compose.yml           Full stack: db, migrate, core, dashboard
  Dockerfile                   Core service multi-stage build
  Dockerfile.dashboard         Dashboard service build
migrations/                    7 SQL migration files (up/down pairs)
scripts/
  migrate.sh                   Migration runner
  debug-chrome.sh              Chrome DevTools debugging helper
Makefile                       14 targets (build, test, test-integration, migrate, etc.)
.env.example                   Environment template
```

---

## Section 6: Verification Checklist

### Completed ✅
- [x] All unit tests pass: `cd backend && go test ./...` — **320+ test functions across 17 packages**
- [x] `go vet ./...` clean
- [x] Binary builds: `go build -o bin/omo-core ./cmd/omo-core`
- [x] Backfill tool works: `go run ./cmd/omo-backfill/ -symbols AAPL,MSFT -days 7`
- [x] TimescaleDB stores and retrieves bars correctly
- [x] Dashboard connects to backend API and renders charts
- [x] SSE stream connects and hooks are ready
- [x] All dashboard pages render (debates, execution, DNA)
- [x] Chart: pan-left loading, spinner, off-market shading, timeframe switching
- [x] Kill switch triggers after 3 stops in 2 minutes (unit tested)
- [x] Rate limiter stays under 200 req/min (unit tested)
- [x] Clean code: No TODO/FIXME/HACK comments in the codebase
- [x] Makefile automation: All 14 targets verified
- [x] Strategy architecture Phases A-H: 130+ tests across domain, app, adapter layers
- [x] v2 pipeline wired in main.go with STRATEGY_V2=true feature flag
- [x] Full order pipeline traced: Signal → RiskSizer → OrderIntentCreated → ExecutionService → Alpaca
- [x] Yaegi sandbox with import whitelist, AST validation, timeout, circuit breaker
- [x] Blue/green swap with shadow warmup and atomic Router.Replace()
- [x] Strategy lifecycle state machine (6 states, validated transitions)
- [x] Dashboard strategies page with lifecycle management UI
- [x] docs/STRATEGY_SYSTEM.md and docs/STRATEGY_ARCHITECTURE_PLAN.md created

### Phase 9 — Paper Trading Readiness 🟡
- [x] LLM advisor responds to debate requests — smoke test verified with real OpenRouter API key (Claude Sonnet 4, structured JSON output)
- [ ] Monitor detects at least one setup during market hours
- [ ] Full event chain fires: WS → Ingestion → Monitor → Debate → Execution → Order
- [ ] Paper order appears in Alpaca dashboard
- [ ] Notifications arrive on Telegram/Discord for order events
- [x] OpenRouter configured as default LLM provider (`.env.example`, config defaults, adapter headers)
- [x] Alpaca paper account verified: equity $30,965.59, order submit/status/cancel lifecycle works, quote retrieval works
- [x] Config env overlay bug fixed: `APCA_API_BASE_URL` and `APCA_DATA_URL` now correctly read from `.env`
- [x] SubmitOrder stop_price bug fixed: limit orders no longer send `stop_price` field (Alpaca 422 fix)
- [x] Strategy DNA path fixed: absolute `/configs/` → relative `configs/` for running from backend directory
- [x] Log level configurable via `LOG_LEVEL` env var (supports debug/info/warn/error)
- [x] Monitor debug logging added: indicator snapshots, regime classification, setup evaluation on every bar
- [x] Notification service wired to event bus for order/kill-switch/circuit-breaker/fill/rejection events
- [x] Integration tests pass against real TimescaleDB (`make test-integration`)
- [x] `opentrade_test` database auto-provisioned by Makefile target
- [x] CI/CD pipeline configured in `.github/workflows/ci.yml` (lint, test, integration, build)
- [x] Full test suite passes: 320+ tests across 16 packages, zero failures
- [x] omo-core startup sequence verified: all services subscribe, config loads from .env, WebSocket streams

### Phase 10 — Hardening 🔲
- [ ] Backtesting framework replays historical data
- [ ] WebSocket auto-reconnects on disconnect
- [ ] P&L tracking dashboard
- [ ] Candlestick chart mode
- [ ] Prometheus/Grafana dashboards for system health

---

## Section 7: Data State

**Database**: TimescaleDB in Docker on port 5432.
**Migration Order**:
1. `001_create_accounts`
2. `002_create_market_bars`
3. `003_create_trades`
4. `004_create_thought_logs`
5. `005_create_strategy_dna_history`
6. `006_create_orders`
7. `007_market_bars_unique`

**Migration Runner**: Use `./scripts/migrate.sh` to apply migrations.

**Symbols tracked** (from config.yaml): AAPL, MSFT, GOOGL, AMZN, TSLA, SOXL, U, PLTR, SPY, META

**Strategy DNA files**: 1 strategy — `orb_break_retest.toml` (ORB 30min, RVOL ≥ 1.5, confidence ≥ 0.65, TREND regime only)
