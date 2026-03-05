# Implementation Plan: oh-my-opentrade

Last Updated: March 5, 2026 (Session 6 — Phase 12 Complete)

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
| 39 | End-to-end flow verification (bar replay → event pipeline → paper order) | 🟡 In Progress — startup verified, DNA path fixed, debug logging added. **New approach:** bar replay mode feeds historical bars through event bus, eliminating market-hours dependency. Strategy v2 pipeline ready via STRATEGY_V2=true flag. |
| 40 | Monitor setup detection tuning | 🟡 In Progress — debug logging added to monitor. **New approach:** run bar replay with backfilled data to collect indicator values and verify setup detection fires. No market-hours dependency. |
| 41 | LLM provider configuration (API keys, model selection) | ✅ Done — OpenRouter configured, smoke test passes with real API key, structured debate JSON verified (LONG, 0.72 confidence, full bull/bear/judge). |
| 42 | Alpaca paper trading order submission verification | ✅ Done — Account equity ($30,965.59), order submit/status/cancel lifecycle, quote retrieval all verified via smoke tests. |
| 43 | Integration test: Alpaca WS → Ingestion → TimescaleDB round-trip | ✅ Done |
| 44 | Integration test: SetupDetected → Debate → OrderIntent → Execution → Order | ✅ Done |
| 45 | Strategy DNA parameter tuning for live conditions | ✅ Done — DNA params (min_rvol=0.5, min_confidence=0.40) now flow to ORB tracker via `SetORBConfig()` |
| 46 | Notification wiring (Telegram/Discord alerts on order events) | ✅ Done |
| 47 | CI/CD pipeline setup (GitHub Actions) | ✅ Done |

### Phase 10 — Hardening & Infrastructure ✅
| # | Item | Status |
|---|------|--------|
| 48 | Backtesting framework — Replay historical bars through event pipeline with 5bps slippage model (PRD §3 nightly evolution) | ✅ Done — `omo-replay --backtest` with SimBroker, equity curve, trade stats |
| 49 | Candlestick chart mode (lightweight-charts supports it natively) | ✅ Done — Line/Candle toggle with OHLCV + volume histogram + EMA overlays |
| 50 | Auto-reconnect for Alpaca WebSocket with exponential backoff | ✅ Done — Exponential backoff, health monitoring, connection state tracking |
| 51 | Performance dashboard — P&L tracking, win rate, max drawdown, Sharpe ratio (PRD §7) | ✅ Done — `/performance` page with equity curve, daily P&L, trade stats |
| 52 | Observability stack — Prometheus metrics + Grafana dashboards for system health | ✅ Done — Prometheus metrics endpoint, Grafana provisioned dashboards |
| 53 | TanStack Query migration — Replace raw fetch() in dashboard pages with useQuery hooks (package already installed) | ✅ Done — All dashboard pages migrated to TanStack Query hooks |

### Phase 11 — Multi-Timeframe Analysis (MTFA) ✅

PRD §4.2 requires anchor (5m/15m) + trigger (1m) separation. Currently the monitor only processes 1m bars with no explicit anchor/trigger distinction.

| # | Item | Status |
|---|------|--------|
| 54 | Multi-timeframe bar aggregation — Aggregate 1m bars into 5m/15m candles in the monitor service | ✅ Done — BarAggregator domain type with 11 TDD tests |
| 55 | Anchor regime detection — Compute regime (trend/balance/reversal) on 5m/15m timeframes | ✅ Done — RegimeDetector with hysteresis on 5m/15m anchor timeframes |
| 56 | Trigger entry logic — Use 1m bars for entry/exit signals, gated by 5m/15m anchor regime | ✅ Done — ORB strategy gates on AnchorRegimes from 5m/15m |
| 57 | MTFA integration tests — Verify anchor+trigger pipeline end-to-end | ✅ Done — 13 integration tests in mtfa_test.go |

### Phase 12 — Pre-Market Screener & Approval Workflow ✅

PRD §3 requires 08:30–09:15 screener for "Stocks in Play" and 09:15–09:30 user approval of overnight DNA changes.

| # | Item | Status |
|---|------|--------|
| 58 | Screener service — Scan configured universe for Gap%, RVOL, and news at 08:30 ET | ✅ Done — Event-driven scheduler, gap/RVOL/news scoring, Alpaca snapshots adapter, TimescaleDB repo |
| 59 | Screener → monitor symbol routing — Feed screener output into monitor's active symbol list | ✅ Done — Pure resolver (replace|union|intersection|static), event-driven SymbolRouter, monitor symbol filtering |
| 60 | DNA approval workflow (backend) — State machine for DNA change approval (pending → approved / rejected) | ✅ Done — Domain types, versioning, approval service, REST API, TimescaleDB repo |
| 61 | DNA approval UI — Dashboard page with DNA diff view + approve/reject buttons (PRD §7) | ✅ Done — Master-detail layout, TOML diff viewer, approve/reject actions, sidebar nav |
| 62 | Morning approval gate — Block strategy execution until DNA changes are approved (09:15–09:30 window) | ✅ Done — Gate in monitor before SetupDetected, IsDNAApproved() check, fail-open on errors |

### Phase 13 — Additional Strategies 🔲

PRD §4.3 specifies 3 pluggable strategies. Only ORB is implemented.

| # | Item | Status |
|---|------|--------|
| 63 | AVWAP strategy — Anchored VWAP breakout/bounce strategy using existing VWAP indicator (PRD §4.3 #2) | 🔲 Not Started |
| 64 | AI-Enhanced Scalping strategy — RSI/Stoch mean-reversion aligned with 5m regime (PRD §4.3 #3, depends on Phase 11 MTFA) | 🔲 Not Started |
| 65 | Strategy TOML configs — Create `avwap.toml` and `ai_scalping.toml` DNA files | 🔲 Not Started |
| 66 | Multi-strategy integration tests — Verify multiple strategies run simultaneously per symbol | 🔲 Not Started |

### Phase 14 — Nightly Evolution & Corporate Actions 🔲

PRD §3 nightly cycle: AI analyzes trades, updates DNA, runs backtests. Corporate action filter prevents math errors.

| # | Item | Status |
|---|------|--------|
| 67 | Nightly evolution service — Analyze ThoughtLogs + trade P&L, propose DNA parameter changes | 🔲 Not Started |
| 68 | Automated backtesting — Run proposed DNA changes through backtester with 5bps slippage before applying (depends on Phase 10 #48) | 🔲 Not Started |
| 69 | Corporate action check — Filter out tickers with upcoming splits/dividends from active trading (PRD §3) | 🔲 Not Started |
| 70 | Evolution audit trail — Log all DNA mutations with before/after + backtest results to strategy_dna_history | 🔲 Not Started |
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
| 1 | 45a. Fix min_confidence bug | Nothing | Remove hardcoded 0.75; use DNA `min_confidence` value |
| 2 | 45b. Verify min_rvol wiring | Nothing | Trace DNA `min_rvol` → ORB tracker; fix if disconnected |
| 3 | 39-replay. Build bar replay mode | Nothing | CLI command or HTTP endpoint that feeds historical bars through event bus |
| 4 | 39. E2E Verification | 45a, 45b, 39-replay | Run bar replay with backfilled data; verify all 7 event stages fire |
| 5 | 40. Monitor Tuning | 39-replay | Run replay, collect indicator values, adjust thresholds if too tight |
| 6 | 42f. Paper Order E2E | 39 | Replay during market hours to confirm order reaches Alpaca paper account |

---

## Section 4: Task Breakdown — Phase 9

### Task 39: End-to-End Flow Verification

The full event pipeline is wired in `main.go` but has never been tested with real market data flowing through all stages. **New approach:** instead of waiting for market hours, we build a bar replay mode that feeds historical bars (from `omo-backfill` or TimescaleDB) through the event bus as `MarketBarReceived` events, simulating a live session.

**Event chain to verify:**
```
1. Bar Replay          → MarketBarReceived          (replay service → event bus)
2. Ingestion            → MarketBarSanitized          (Z-score filter passes)
3. Monitor              → SetupDetected               (indicators trigger a setup)
4. Strategy Runner (v2) → SignalCreated               (ORB strategy emits signal)
5. RiskSizer            → OrderIntentCreated           (signal → sized order intent)
6. Execution            → Risk check → Slippage check → Kill switch check
                        → OrderSubmitted               (passes all gates)
7. Alpaca REST          → Paper order placed           (broker confirms)
```

**Subtasks:**
- [x] **39-pre.** Startup sequence verified: config loads, event bus, Alpaca adapter, TimescaleDB, account equity, Discord notifier, AI debate, all 6 services subscribe, SSE handler, HTTP server, indicator warmup, WebSocket stream. All initializes correctly.
- [x] **39-pre2.** Fixed strategy DNA path bug (absolute `/configs/` → relative `configs/`) and strategy HTTP handler base path.
- [x] **39-pre3.** Made log level configurable via `LOG_LEVEL` env var (supports `debug`, `info`, `warn`, `error`).
- [x] **39-pre4.** Fixed config.go env overlay bug — `APCA_API_BASE_URL` and `APCA_DATA_URL` from `.env` were silently ignored.
- [x] **39-pre5.** Fixed SubmitOrder stop_price bug — limit orders no longer send `stop_price` (Alpaca 422 fix).
- [ ] **39-replay.** Build bar replay mode — CLI subcommand or app service that reads bars from TimescaleDB and publishes `MarketBarReceived` events with configurable speed (1x, 10x, max).
- [ ] **39a.** Run replay with backfilled data, tail logs, verify steps 1–2 fire continuously
- [ ] **39b.** Observe Monitor logs — confirm indicator calculation + regime classification is running
- [ ] **39c.** Watch for SetupDetected events — may need to temporarily lower thresholds
- [ ] **39d.** Verify Strategy Runner receives setup and emits SignalCreated (requires STRATEGY_V2=true)
- [ ] **39e.** Verify Execution receives intent and runs risk/slippage/kill-switch checks
- [ ] **39f.** Confirm order reaches Alpaca paper account (run during market hours for actual fill)
### Task 40: Monitor Setup Detection Tuning

The monitor calculates SMA, EMA, RSI, MACD, Bollinger Bands, ATR, CCI, and relative volume, then classifies market regime and detects setups. Thresholds are in `configs/config.yaml`. **New approach:** use bar replay mode to feed backfilled data through the monitor and collect indicator values without waiting for market hours.

**Subtasks:**
- [x] **40a.** Debug logging added to monitor service — logs indicator snapshot (RSI, StochK/D, EMA9/21, VWAP, VolumeSMA), regime classification (type, strength, changed), and setup evaluation criteria (RSI cross conditions, EMA alignment) on every bar at DEBUG level.
- [ ] **40b.** Run bar replay with backfilled data, collect indicator values for tracked symbols
- [x] **40c.** Setup detection conditions reviewed — Long: RSI crosses 40 from below + EMA9 > EMA21. Short: RSI crosses 60 from above + EMA9 < EMA21. Regime filter: TREND only (EMA divergence > 1%).
- [ ] **40d.** Adjust thresholds if too tight (e.g., relax min_rvol, confidence)
- [ ] **40e.** Confirm at least one SetupDetected event fires during replay of a full trading session
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
      options.go               Options domain logic
      exchange_calendar.go     NYSE market hours & holiday calendar
    ports/                     Hexagonal port interfaces
      strategy/                store.go, registry.go
      options_market_data.go   Options data port
    app/
      strategy/                runner.go, router.go, instance.go, risk_sizer.go, swap_manager.go, lifecycle_svc.go, spec_loader.go, registry_mem.go, builtin/orb_v1.go
      ingestion/               Z-score filter, bar processing
        integration_test.go    Integration: WS → Ingestion → DB round-trip
      monitor/                 Service, indicators, regime & setup detectors
        indicators.go          SMA, EMA, RSI, MACD, Bollinger, ATR, CCI
        orb_tracker.go         ORB Break & Retest detection + RVOL
        regime_detector.go     Market regime classification
        setup_detector.go      Setup detection logic
      execution/               Risk engine, kill switch, slippage guard
      debate/                  AI adversarial debate orchestration
      options/                 Options contract selection & risk
      notify/                  Notification service (event bus → notifier)
      backfill/                Chunked historical download
      pipeline_integration_test.go  Integration: Setup → Debate → Order pipeline
    adapters/
      strategy/
        store_fs/              Filesystem strategy store
        hooks_yaegi/           Yaegi hot-swap runtime sandbox
      alpaca/                  MarketDataPort + BrokerPort (13 files)
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
  start.sh                     Start all services
  shutdown.sh                  Stop all services
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

### Phase 10–11 — Completed ✅
- [x] Backtesting framework replays historical data (#48)
- [x] Candlestick chart mode (#49)
- [x] WebSocket auto-reconnects on disconnect (#50)
- [x] P&L tracking dashboard (#51)
- [x] Observability stack (#52)
- [x] TanStack Query migration (#53)
- [x] Multi-timeframe anchor/trigger separation (#54-57)

### Phase 12 — Completed ✅
- [x] Pre-market screener service (#58-59)
- [x] DNA approval workflow + UI (#60-62)
- [ ] AVWAP strategy (#63)
- [ ] AI-Enhanced Scalping strategy (#64)
- [ ] Nightly evolution cycle (#67-68)
- [ ] Corporate action check (#69)
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
8. `008_create_pnl`
9. `010_create_screener_results`

**Migration Runner**: Use `./scripts/migrate.sh` to apply migrations.

**Symbols tracked** (from config.yaml): AAPL, MSFT, GOOGL, AMZN, TSLA, SOXL, U, PLTR, SPY, META

**Strategy DNA files**: 1 strategy — `orb_break_retest.toml` (ORB 30min, RVOL ≥ 0.5, confidence ≥ 0.40, TREND regime only)

---

## Section 8: PRD Gap Analysis

Comprehensive comparison of PRD v11.0 features vs actual implementation status (audited March 4, 2026).

### ✅ Fully Implemented

| PRD Feature | PRD Section | Implementation |
|---|---|---|
| Hexagonal Architecture (Golang) | §2 Backend | Full hex arch with domain/ports/adapters/app layers |
| Data Sanitization (4σ Z-Score) | §4.1 | Ingestion service with Z-score anomaly filter |
| Deterministic State Machine Monitor | §5.1 | Monitor service: RSI, Stoch, EMA, VWAP, MACD, Bollinger, ATR, CCI |
| Regime Detection | §4.2 | RegimeDetector: TREND/BALANCE/REVERSAL classification |
| Adversarial AI Debate (Bull/Bear/Judge) | §5.2 | Debate service with OpenRouter, structured JSON output |
| ORB Break & Retest Strategy | §4.3 #1 | Full implementation: ORBTracker + ORBStrategy + TOML config |
| Kill Switch (3 stops/2min → 15min halt) | §6 | Kill switch module with tenant+symbol isolation |
| Risk Engine (2% max, mandatory SL, LIMIT only) | §6 | Execution service with deterministic Go risk checks |
| Slippage Guard | §6 | Bid/ask comparison against max_slippage_bps |
| API Key Isolation | §6 | .env injection, never in DB or sent to AI |
| Multi-Tenant Schema | §4 | account_id + env_mode on all tables, runtime isolation in kill switch |
| Rate Limit Governor (200 req/min) | §2 | Token bucket in Alpaca adapter |
| TimescaleDB with Compression | §2 Database | 7 migrations, hypertable compression, 1-day chunks |
| Yaegi Hot-Swap Strategies | §2 Backend | Sandbox with import whitelist, AST validation, timeout, circuit breaker |
| Strategy DNA Engine | §4.3 #4 | TOML hot-reload (5s), DNA manager, blue/green swap |
| Notifications (Telegram/Discord) | §6 | Multi-notifier fan-out for order/kill-switch/fill events |
| Next.js Dashboard | §7 | Multi-symbol chart, debate feed, execution monitor, DNA diffs |
| Pub/Sub Event Bus | §2 Backend | In-memory event bus with EventBusPort interface |

### 🟡 Partially Implemented

| PRD Feature | PRD Section | Status | Gap |
|---|---|---|---|
| Multi-Timeframe Analysis | §4.2 | ✅ Fully Implemented | Bar aggregation (1m→5m/15m), anchor regime detection with hysteresis, trigger gating in ORB strategy |
| Options Trading | §4 | Contract selection + order execution | Only LONG direction, TREND regime; no full options strategy |
| TanStack Query | §2 Frontend | ✅ Fully Implemented | All dashboard pages use useQuery hooks |
| Multi-Account Execution | §4 | Schema + kill switch tenant isolation | Runtime is single-account via .env; no multi-account orchestration |
| Phase 9 Paper Trading | §9 | 10/11 items done | Needs market-hours E2E verification (#39) |

### ❌ Not Implemented

| PRD Feature | PRD Section | New Phase | Notes |
|---|---|---|---|
| ~~Pre-Market Screener (08:30–09:15)~~ | ~~§3~~ | ~~Phase 12 #58-59~~ | ✅ Implemented — Screener service with gap/RVOL/news scoring, symbol routing |
| ~~Morning DNA Approval Workflow (09:15–09:30)~~ | ~~§3, §7~~ | ~~Phase 12 #60-62~~ | ✅ Implemented — Full approval workflow + dashboard UI |
| AVWAP Strategy | §4.3 #2 | Phase 13 #63 | VWAP indicator exists but no anchored breakout strategy |
| AI-Enhanced Scalping Strategy | §4.3 #3 | Phase 13 #64 | No RSI/Stoch mean-reversion strategy; depends on MTFA |
| Nightly Evolution Cycle | §3 | Phase 14 #67-68 | No automated AI analysis of trades or DNA parameter optimization |
| Corporate Action Check | §3 | Phase 14 #69 | No dividend/split filtering for active tickers |
| ~~Backtesting Framework (5bps slippage)~~ | ~~§3~~ | ~~Phase 10 #48~~ | ✅ Implemented — `omo-replay --backtest` |
| ~~Performance Dashboard (P&L, win rate)~~ | ~~§7~~ | ~~Phase 10 #51~~ | ✅ Implemented — `/performance` page |
| ~~Observability (Prometheus/Grafana)~~ | ~~Ops~~ | ~~Phase 10 #52~~ | ✅ Implemented — Prometheus + Grafana |
| ~~WebSocket Auto-Reconnect~~ | ~~Ops~~ | ~~Phase 10 #50~~ | ✅ Implemented — Exponential backoff |

### 🐛 Known Bugs

| Bug | Location | Impact |
|---|---|---|
| ~~`min_rvol` not connected to monitor~~ | ~~orb_tracker.go / monitor service~~ | ✅ Fixed — DNA `min_rvol` flows via `SetORBConfig()` |
| ~~`min_confidence` hardcodes 0.75~~ | ~~strategy service~~ | ✅ Fixed — DNA `min_confidence` flows via `SetORBConfig()` |
