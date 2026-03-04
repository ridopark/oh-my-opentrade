# Architecture Document: oh-my-opentrade

> Derived from PRD v11.0, Oracle consultation, and technology research.  
> This document must be approved before implementation begins.

---

## 1. High-Level Architecture

                          ┌──────────────────────────────┐
                          │      Next.js Dashboard       │
                          │   (apps/dashboard — later)   │
                          └──────────┬───────────────────┘
                                     │ REST / WebSocket
                          ┌──────────▼───────────────────┐
                          │       API Gateway Layer       │
                          └──────────┬───────────────────┘
                                     │
              ┌──────────────────────┼──────────────────────┐
              │                      │                      │
    ┌─────────▼────────┐  ┌─────────▼────────┐  ┌─────────▼────────┐
    │  State Machine   │  │  StrategyRunner  │  │   Execution      │
    │  Monitor         │  │  & RiskSizer     │  │   Engine         │
    │                  │  │                  │  │                  │
    │  • Indicators    │  │  • Multi-Strategy│  │  • Risk Engine   │
    │  • Regime detect │  │  • Yaegi Sandbox │  │  • Kill Switch   │
    │  • Setup detect  │  │  • Blue/Green    │  │  • Circuit Break │
    └─────────┬────────┘  └─────────┬────────┘  └─────────┬────────┘
              │                     │                      │
              └──────────┬──────────┘──────────────────────┘
                         │
              ┌──────────▼───────────────────┐
              │     In-Memory Event Bus      │
              │   (EventBusPort interface)   │
              │                              │
              │  Future: swap to NATS        │
              └──────────┬───────────────────┘
                         │
         ┌───────────────┼───────────────┐
         │               │               │
┌────────▼──────┐ ┌──────▼──────┐ ┌──────▼──────┐
│ Alpaca Adapter│ │ TimescaleDB │ │  OpenCode   │
│               │ │  Adapter    │ │  Adapter    │
│ • WS Stream   │ │             │ │             │
│ • REST Orders │ │ • Bars      │ │ • Bull/Bear │
│ • Rate Limit  │ │ • Trades    │ │ • Judge     │
└───────────────┘ └─────────────┘ └─────────────┘
```

---

## 2. Decision Log

### D1: Monorepo — Single Go Module

**Decision:** Single `go.mod` at `backend/`, all services share one module.

**Rationale:**
- All services share domain types (`MarketBar`, `OrderIntent`, `TenantID`, etc.)
- Separate Go modules add version management friction while domain types are still churning
- Single module enables trivial refactoring across service boundaries
- Go workspaces (`go.work`) can be introduced later if needed

**Trade-off:** Can't version services independently. Acceptable for a single-team, single-VM deployment.

---

### D2: Modular Monolith MVP → Microservices Later

**Decision:** MVP ships as a **single binary** (`cmd/omo-core/`). All "services" are in-process Go packages wired together in `main.go`.

**Rationale:**
- Fastest path to working paper trading
- Eliminates container networking, service discovery, health checks for MVP
- On a 4 OCPU / 24 GB ARM VM, a single Go binary is trivially sufficient
- Hexagonal port interfaces ensure clean separation — splitting into separate binaries later requires only:
  1. New `cmd/<service>/main.go` with its own dependency injection
  2. Swap in-memory event bus adapter → NATS adapter

**Migration path:**
```
MVP:   cmd/omo-core/main.go  → wires all packages, in-memory bus
v2:    cmd/api-gateway/       → HTTP server, proxies to internal services
       cmd/executor/          → consumes OrderIntents from NATS
       cmd/monitor/           → state machine, publishes SetupDetected to NATS
```

---

### D3: Event Bus — In-Memory with Port Interface

**Decision:** Define `EventBusPort` interface. MVP implementation uses Go channels. Future: NATS adapter behind the same interface.

**Interface:**
```go
type EventBusPort interface {
    Publish(ctx context.Context, event Event) error
    Subscribe(ctx context.Context, eventType string, handler EventHandler) error
    Unsubscribe(ctx context.Context, eventType string, handler EventHandler) error
}
```

**Rationale:**
- Go channels are zero-dependency, zero-latency for in-process communication
- NATS adds 64 MB RAM overhead + container — unnecessary for MVP
- The port interface means domain code never knows (or cares) about the transport

**Design constraint:** All events must carry an **idempotency key** from day one, so the system tolerates at-least-once delivery when we switch to NATS.

---

### D4: Hexagonal Boundaries — Four Core Ports

| Port Interface | Purpose | MVP Adapter |
|:---|:---|:---|
| `MarketDataPort` | Stream & pull market bars | Alpaca WebSocket + REST |
| `BrokerPort` | Submit/cancel/query orders, positions | Alpaca REST (rate-limited) |
| `AIAdvisorPort` | Request adversarial debate, get OrderIntent | OpenCode SDK |
| `EventBusPort` | Publish/subscribe to domain events | In-memory (Go channels) |
| `RepositoryPort` | Persist & query bars, trades, thoughts, DNA | TimescaleDB |

**Why separate `MarketDataPort` and `BrokerPort`?**
- Market data and order execution have different reliability requirements
- Market data is streaming (WebSocket), orders are request/response (REST)
- A future broker (Interactive Brokers, etc.) may use different protocols for each

**Why `AIAdvisorPort`?**
- AI is an external dependency, not a domain concept
- The port returns a strongly-typed `AdvisoryDecision` (not raw LLM text)
- Domain code validates the decision — AI cannot bypass risk checks

---

### D5: Database — TimescaleDB with Compression, No Space Partitioning

**Decision:** Use `compress_segmentby` for multi-tenant query performance. Do NOT use space partitioning (`add_dimension`).

**Rationale:**
- Space partitioning by `account_id` risks **partition explosion** (TimescaleDB docs explicitly warn)
- With < 20 accounts, `compress_segmentby = 'account_id, env_mode'` gives equivalent query performance
- Simpler operational model — one chunk timeline, no cross-partition headaches

**Compression policy:** All hypertables compress data older than 7 days.

**Chunk interval:** 1 day (appropriate for minute-level bar data).

---

### D6: Rate Limit Governor — Token Bucket at Adapter Layer

**Decision:** Wrap Alpaca REST client with `golang.org/x/time/rate` limiter at 200 req/min (3.33 req/s), burst of 10.

**Rationale:**
- Rate limiting lives in the adapter, not the domain — it's an infrastructure concern
- Token bucket (not leaky bucket) allows controlled bursts for startup hydration
- `rate.Limiter.Wait(ctx)` blocks until a token is available — backpressure is automatic

---

## 3. Project Structure

```
oh-my-opentrade/
├── backend/
│   ├── cmd/
│   │   ├── omo-core/main.go              # MVP: single binary, wires everything
│   │   └── omo-backfill/main.go           # Historical bar backfill CLI
│   │
│   ├── internal/
│   │   ├── domain/                        # Pure business logic — NO external imports
│   │   │   ├── entity.go                  # MarketBar, Trade, Position, Account
│   │   │   ├── event.go                   # Domain events (MarketBarSanitized, SetupDetected, etc.)
│   │   │   ├── value.go                   # Value objects (TenantID, EnvMode, Symbol, Timeframe)
│   │   │   ├── advisory.go                # AI advisory types (AdvisoryDecision, etc.)
│   │   │   ├── options.go                 # Options domain logic
│   │   │   ├── exchange_calendar.go        # NYSE market hours & holiday calendar (2025-2028)
│   │   │   └── strategy/                  # Strategy v2 domain types, contract, lifecycle
│   │   │
│   │   ├── ports/                         # Interface definitions — the hexagonal boundaries
│   │   │   ├── market_data.go             # MarketDataPort (stream bars, pull history)
│   │   │   ├── broker.go                  # BrokerPort (submit/cancel/query orders)
│   │   │   ├── ai_advisor.go              # AIAdvisorPort (request debate, get decision)
│   │   │   ├── event_bus.go               # EventBusPort (publish/subscribe)
│   │   │   ├── repository.go              # RepositoryPort (bars, trades, thoughts, DNA)
│   │   │   ├── notifier.go                # NotifierPort (Telegram/Discord webhooks)
│   │   │   ├── options_market_data.go      # OptionsMarketDataPort
│   │   │   └── strategy/                  # Store, Registry ports
│   │   │
│   │   ├── app/                           # Application services — orchestrate domain + ports
│   │   │   ├── ingestion/                 # Market data ingestion + Z-score sanitization
│   │   │   ├── monitor/                   # State machine monitor (indicators, regime, ORB tracker, setup detection)
│   │   │   ├── debate/                    # AI adversarial debate orchestration
│   │   │   ├── execution/                 # Order execution + risk engine + kill switch
│   │   │   ├── strategy/                  # Strategy v2 Runner, Router, Instance, RiskSizer, SwapManager, LifecycleSvc
│   │   │   │   └── builtin/               # Built-in strategies (orb_v1.go)
│   │   │   ├── backfill/                  # Historical data backfill service
│   │   │   ├── notify/                    # Notification service (event bus → notifier)
│   │   │   └── options/                   # Options contract selection & risk
│   │   │
│   │   ├── adapters/                      # Port implementations — external dependencies live here
│   │   │   ├── alpaca/                    # MarketDataPort + BrokerPort (13 files)
│   │   │   ├── timescaledb/               # RepositoryPort
│   │   │   ├── eventbus/memory/           # EventBusPort (in-memory Go channels)
│   │   │   ├── llm/                       # AIAdvisorPort (OpenAI-compatible: Claude, Ollama, LM Studio)
│   │   │   ├── notification/              # NotifierPort (Telegram, Discord, Multi fan-out)
│   │   │   ├── http/                      # Lifecycle and Strategy API handlers
│   │   │   ├── sse/                       # Server-Sent Events for dashboard real-time push
│   │   │   ├── middleware/                 # HTTP middleware (access logging)
│   │   │   └── strategy/                  # store_fs (filesystem), hooks_yaegi (Yaegi sandbox)
│   │   │
│   │   ├── config/                        # Configuration loading (.env + YAML)
│   │   └── logger/                        # Structured zerolog logging
│   │
│   ├── go.mod
│   └── go.sum
│
├── migrations/                            # 7 SQL migration files (up/down pairs)
│
├── apps/
│   └── dashboard/                         # Next.js 15 + TailwindCSS + shadcn/ui + lightweight-charts v5
│       ├── app/                           # Pages and API routes
│       │   ├── api/                       # Proxy routes (bars, events, health, strategies, debates, dna, execution)
│       │   ├── debates/                   # AI debate feed page
│       │   ├── dna/                       # Strategy DNA diff viewer page
│       │   ├── execution/                 # Order tracking page
│       │   ├── strategies/                # Strategy lifecycle management page
│       │   ├── layout.tsx
│       │   └── page.tsx                   # Multi-symbol chart home page
│       └── components/                    # React components (chart, sidebar, query-provider, ui/)
│
├── deployments/
│   ├── docker-compose.yml                 # Full stack: db, migrate, core, dashboard
│   ├── Dockerfile                         # Core service multi-stage build (ARM64)
│   └── Dockerfile.dashboard               # Dashboard service build
│
├── configs/
│   ├── strategies/                        # TOML strategy DNA files (hot-swappable)
│   │   └── orb_break_retest.toml
│   ├── config.yaml                        # App configuration
│   └── config.yaml.example
│
├── scripts/
│   ├── migrate.sh                         # Run DB migrations
│   ├── start.sh                           # Start all services
│   ├── shutdown.sh                        # Stop all services
│   └── debug-chrome.sh                    # Chrome DevTools debugging
│
├── docs/
│   ├── PRD.md
│   ├── ARCHITECTURE.md                    # This file
│   ├── IMPLEMENTATION_PLAN.md
│   ├── STRATEGY_SYSTEM.md                 # Strategy v2 documentation
│   └── STRATEGY_ARCHITECTURE_PLAN.md      # Strategy phases A-H plan
│
├── .github/workflows/ci.yml               # CI/CD pipeline
├── .env.example                           # Environment template
├── Makefile                               # 14+ targets (build, test, etc.)
└── README.md
```

---

## 4. Core Event Flow

Events are the nervous system of the platform. Every state transition is an event.

Alpaca WebSocket
       │
       ▼
  MarketBarReceived          ← Raw bar from broker
       │
       ▼ (Ingestion Service)
  MarketBarSanitized         ← Passed Z-score filter (or MarketBarRejected)
       │
       ▼ (Monitor Service)
  StateUpdated               ← New indicator snapshot computed
       │
       ▼
  [If STRATEGY_V2=true]
       │
       ▼ (Strategy Runner)
  SignalCreated              ← Strategy instance produced a signal
       │
       ▼ (RiskSizer)
  OrderIntentCreated         ← Signal converted to sized order intent
       │
       ▼ (Execution Service)
  OrderIntentValidated       ← Passed risk checks (or OrderIntentRejected)
       │
       ▼ (Broker Adapter)
  OrderSubmitted             ← Sent to broker
       │
       ▼
  OrderAccepted / OrderRejected  ← Broker response
       │
       ▼
  FillReceived               ← Trade executed
       │
       ▼
  PositionUpdated            ← Position state changed
       │
       ▼ [Safety Events]
  KillSwitchEngaged          ← 3 stops in 2 min → 15 min halt
  CircuitBreakerTripped      ← System-wide safety event

### Event Structure

Every event carries:

```go
type Event struct {
    ID            string    // UUID — unique per event
    Type          string    // e.g., "MarketBarSanitized"
    TenantID      string    // Account identifier
    EnvMode       string    // "Paper" or "Live"
    OccurredAt    time.Time // When the event happened
    IdempotencyKey string   // Stable key for deduplication (critical for NATS migration)
    Payload       any       // Strongly-typed per event type
}
```

---

## 5. Domain Types (Key Entities)

### MarketBar
```go
type MarketBar struct {
    Time      time.Time
    Symbol    string
    Timeframe string    // "1m", "5m", "15m"
    Open      float64
    High      float64
    Low       float64
    Close     float64
    Volume    float64
    Suspect   bool      // Flagged by Z-score filter
}
```

### OrderIntent
```go
type OrderIntent struct {
    ID              string
    TenantID        string
    EnvMode         string
    Symbol          string
    Direction       string    // "LONG" or "SHORT"
    LimitPrice      float64
    StopLoss        float64
    MaxSlippageBPS  int       // Basis points
    Quantity        float64
    Strategy        string    // Which strategy generated this
    Rationale       string    // Human-readable reasoning
    Confidence      float64   // 0.0 – 1.0 (from AI Judge or strategy)
    IdempotencyKey  string    // Prevents duplicate orders
}
```

### IndicatorSnapshot
```go
type IndicatorSnapshot struct {
    Time        time.Time
    Symbol      string
    Timeframe   string
    RSI         float64
    StochK      float64
    StochD      float64
    EMA9        float64
    EMA21       float64
    VWAP        float64
    Volume      float64
    VolumeSMA   float64   // For RVOL calculation
}
```

### MarketRegime
```go
type RegimeType string

const (
    RegimeTrend    RegimeType = "TREND"
    RegimeBalance  RegimeType = "BALANCE"
    RegimeReversal RegimeType = "REVERSAL"
)

type MarketRegime struct {
    Symbol    string
    Timeframe string
    Type      RegimeType
    Since     time.Time
    Strength  float64   // 0.0 – 1.0
}
```

---

## 6. Database Schema Summary

All tables are TimescaleDB hypertables with 1-day chunk intervals and 7-day compression policies.

| Table | Hypertable On | Compress Segmentby | Purpose |
|:---|:---|:---|:---|
| `accounts` | — (regular table) | — | Account configuration, API key references |
| `market_bars` | `time` | `account_id, env_mode, symbol, timeframe` | OHLCV candle data |
| `trades` | `time` | `account_id, env_mode, symbol` | Executed trades |
| `thought_logs` | `time` | `account_id, env_mode` | AI reasoning (JSONB) |
| `strategy_dna_history` | `time` | `account_id, env_mode, strategy_id` | Strategy parameter evolution |

**Note:** `accounts` is a regular PostgreSQL table (not a hypertable) — it's low-cardinality, rarely updated config data.

---

## 7. MVP Scope — Vertical Slice to Paper Trading

The minimum path to "data flows in → state machine computes → paper trade executes":

### Phase 1: Foundation (COMPLETED)
1. **TimescaleDB schema** — All 5 tables, compression policies, indexes
2. **Domain types** — `MarketBar`, `OrderIntent`, `IndicatorSnapshot`, `MarketRegime`, events
3. **Port interfaces** — `MarketDataPort`, `BrokerPort`, `EventBusPort`, `RepositoryPort`

### Phase 2: Data Pipeline (COMPLETED)
4. **In-memory event bus** — Go channel implementation of `EventBusPort`
5. **Alpaca adapter** — WebSocket bar streaming + REST order submission + rate limiter
6. **TimescaleDB adapter** — Repository implementation (persist bars, trades)
7. **Ingestion service** — Subscribe to `MarketBarReceived`, apply Z-score filter, emit `MarketBarSanitized`

### Phase 3: Intelligence (COMPLETED)
8. **Monitor service** — Compute indicators on each `MarketBarSanitized`, detect setups
9. **Execution service** — Risk engine + kill switch + slippage guard + broker submission

### Phase 4: Strategy Architecture v2 (COMPLETED)
10. **Domain Contracts** — Strategy interface, Signal, State, Context, Lifecycle states
11. **StrategyRunner & Router** — Multi-symbol routing and instance management
12. **RiskSizer** — Signal to sized OrderIntent conversion pipeline
13. **Lifecycle Service** — State transitions and TOML v2 spec loading
14. **Blue/Green Swap** — Warmup and atomic instance swapping
15. **Yaegi Sandbox** — Whitelisted imports and execution safety

### Phase 5: Wire & Run
16. **`omo-core` main.go** — Dependency injection, wire all services, start
17. **Docker Compose** — TimescaleDB + omo-core containers
18. **Config** — `.env` for API keys, `config.yaml` for thresholds

---

## 8. Key Technical Constraints

| Constraint | Value | Enforced By |
|:---|:---|:---|
| Max risk per trade | 2% of account | Risk engine (deterministic Go) |
| Mandatory stop-loss | Required on every order | Risk engine validation |
| Order type | LIMIT only | Risk engine validation |
| API rate limit | 200 req/min (Alpaca) | Token bucket in adapter |
| Circuit breaker | 3 stops in 2 min → 15 min halt | Kill switch module |
| Slippage guard | Reject if bid/ask exceeds `max_slippage_bps` | Execution service |
| Data sanitization | 4σ Z-score without matching volume → reject | Ingestion service |
| Tenant isolation | Every row has `account_id` + `env_mode` | Domain types + DB schema |
| API keys | `.env` only — never in DB, never sent to AI | Adapter configuration |

---

## 9. Deployment Target

- **VM:** Oracle Cloud ARM (4 OCPUs, 24 GB RAM)
- **OS:** Ubuntu (ARM64)
- **Runtime:** Docker Compose
- **Containers:**
  - `timescaledb` — TimescaleDB 2.x (PostgreSQL 16)
  - `omo-core` — Single Go binary (all services in-process)
  - `omo-dashboard` — Next.js 15 frontend
- **Build:** Multi-stage Dockerfile targeting `linux/arm64`

---

## 10. Resolved Questions

1. **Backfill strategy:** ✅ Resolved — `omo-backfill` CLI tool fetches historical bars. On startup, the indicator warmup system fetches 120 bars from the previous RTH session via Alpaca REST. Rate limiter enforces 200 req/min.

2. **Account management:** ✅ Resolved — Single account via `.env` for MVP. Schema supports multi-tenant via `account_id` + `env_mode` columns, but runtime is single-account.

3. **Logging strategy:** ✅ Resolved — Structured zerolog → stdout. `LOG_LEVEL` env var controls verbosity (debug/info/warn/error).

4. **Testing approach:** ✅ Resolved — Unit tests on domain + app layers (320+ tests). Integration tests run against real TimescaleDB in Docker via `make test-integration`.
---


---

## 11. Strategy Architecture (v2)

The system implements a multi-strategy, multi-instance architecture where strategies are decoupled from infrastructure and risk management.

### Strategy Interface
The core contract is defined by the `Strategy` interface in `backend/internal/domain/strategy/`:
- `Init(ctx, params, prior_state) State`: Initializes instance for a symbol, optionally from a prior state.
- `OnBar(ctx, symbol, bar, state) (next_state, []Signal)`: Decision logic executed on every bar.
- `OnEvent(ctx, symbol, event, state) (next_state, []Signal)`: Optional reaction to fills or halts.

### Pipeline: Signal to Order
1. **Runner**: Subscribes to `MarketBarSanitized`, queries `Router` for active instances, calls `OnBar()`.
2. **Signal**: Strategies emit `Signal` objects containing symbol, direction, confidence, and reference price.
3. **RiskSizer**: Consumes `SignalCreated`, reads instance `limit_offset_bps` and `stop_bps` to set price levels, and calculates `quantity` from account equity using `risk_per_trade_bps`.
4. **OrderIntent**: Resulting `OrderIntentCreated` is processed by the existing Execution Service risk engine.

### Lifecycle & Blue/Green Swap
Strategies transition through states: `Draft → BacktestReady → PaperActive → LiveActive → Deactivated → Archived`.

Updating a strategy uses a Blue/Green swap:
1. **Shadow Warmup**: A new instance (Green) is created and fed live bars via `WarmupOnBar()` to sync indicators.
2. **Atomic Swap**: Once Green is warm (bars processed >= `WarmupBars()`), the `SwapManager` replaces Blue with Green in the `Router` at the next bar boundary.

### Yaegi Sandbox
Pluggable strategy hooks can be executed via the Yaegi interpreter with strict security:
- **Whitelisted Imports**: Only `math` and `fmt` are allowed.
- **AST Validation**: Code is inspected before execution to prevent `go` statements or prohibited operations.
- **Runtime Safety**: 100ms execution timeout and a circuit breaker that disables instances after 3 failures.

### TOML v2 Spec
Strategy instances are defined in `configs/strategies/*.toml`:
- `[metadata]`: ID, version, name, author.
- `[lifecycle]`: Target state, paper_only flag.
- `[routing]`: Assigned symbols, timeframes, and priority.
- `[params]`: Strategy-specific parameters and risk settings.
- `[hooks]`: Selection of builtin or Yaegi logic engines.

*This document is the source of truth for architectural decisions. Update it when decisions change.*
