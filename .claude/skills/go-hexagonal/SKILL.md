---
name: go-hexagonal
description: "oh-my-opentrade Go backend hexagonal architecture pattern guide. Use this skill when adding new services, ports, adapters, domain entities, or modifying existing backend code. Triggers on 'new service', 'new adapter', 'new port', 'add endpoint', 'new entity', 'migration', 'handler' keywords. Does NOT trigger for frontend (Next.js) work."
---

# Go Hexagonal Architecture Guide

Code authoring guide following oh-my-opentrade backend's hexagonal (ports/adapters) architecture.

## Layer Structure

```
backend/internal/
├── domain/           # Pure domain logic (no external dependencies)
│   ├── entity.go     # MarketBar, OrderIntent, Trade, BrokerOrder
│   ├── value.go      # Direction, Symbol, Timeframe, EnvMode
│   ├── event.go      # 60+ domain event types
│   ├── analytics.go  # Performance calculations (Sharpe, CAGR, drawdown)
│   └── strategy/     # Strategy types (StrategyID, Signal)
├── ports/            # Interface definitions (references domain only)
│   ├── broker.go     # BrokerPort
│   ├── market_data.go # MarketDataPort
│   ├── repository.go # RepositoryPort
│   └── event_bus.go  # EventBusPort
├── adapters/         # Port implementations (references ports + domain)
│   ├── alpaca/       # Alpaca broker adapter
│   ├── ibkr/         # Interactive Brokers adapter
│   ├── timescaledb/  # PostgreSQL/TimescaleDB repository
│   ├── http/         # HTTP handlers (REST API)
│   ├── eventbus/     # In-memory event bus
│   └── llm/          # AI advisor
├── app/              # Application services (business orchestration)
│   ├── ingestion/    # Market data ingestion
│   ├── execution/    # Order execution
│   ├── strategy/     # Strategy execution
│   ├── backtest/     # Backtesting
│   ├── risk/         # Risk engine
│   └── ...
└── config/           # Config loading
```

## Dependency Rules

```
domain ← ports ← adapters
                ← app (services depend on port interfaces)
```

- `domain/` — no external imports (stdlib only)
- `ports/` — imports `domain/` only
- `adapters/` — imports `ports/` + `domain/`, provides concrete implementations
- `app/` — imports `ports/` + `domain/`, never references adapters directly

## New Feature Patterns

### 1. New Domain Entity
```go
// domain/new_entity.go
type MyEntity struct {
    ID   uuid.UUID
    Name string
}

func NewMyEntity(name string) (MyEntity, error) {
    if name == "" {
        return MyEntity{}, errors.New("name must not be empty")
    }
    return MyEntity{ID: uuid.New(), Name: name}, nil
}
```

### 2. New Port Interface
```go
// ports/my_port.go
type MyPort interface {
    DoSomething(ctx context.Context, id uuid.UUID) (domain.MyEntity, error)
}
```

### 3. New Adapter
```go
// adapters/timescaledb/my_repo.go
type MyRepo struct {
    db  *sql.DB
    log zerolog.Logger
}

func NewMyRepo(db *sql.DB, log zerolog.Logger) *MyRepo {
    return &MyRepo{
        db:  db,
        log: log.With().Str("component", "my_repo").Logger(),
    }
}

func (r *MyRepo) DoSomething(ctx context.Context, id uuid.UUID) (domain.MyEntity, error) {
    // SQL query implementation
}
```

### 4. New HTTP Handler
```go
// adapters/http/my_handler.go
type MyHandler struct {
    svc ports.MyPort  // depends on port interface
    log zerolog.Logger
}

func (h *MyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // struct json tags define the API contract
}
```

### 5. New Service
```go
// app/myfeature/service.go
type Service struct {
    repo ports.MyPort
    bus  ports.EventBusPort
    log  zerolog.Logger
}
```

### 6. Wiring (cmd/omo-core/)
- `infra.go` — DB, broker, and infrastructure initialization
- `services.go` — service instantiation + dependency injection
- `http.go` — HTTP route registration

## Coding Conventions

### Error Wrapping
```go
return fmt.Errorf("myservice: save entity: %w", err)
```

### Structured Logging
```go
log := log.With().Str("component", "my_service").Logger()
log.Info().Str("symbol", sym.String()).Msg("processing")
```

### Test Pattern
```go
func TestMyEntity(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        wantErr bool
    }{
        {"valid", "test", false},
        {"empty name", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            _, err := domain.NewMyEntity(tt.input)
            if tt.wantErr {
                require.Error(t, err)
            } else {
                require.NoError(t, err)
            }
        })
    }
}
```

### SQL Migration
Filename: `migrations/NNNN_description.up.sql` / `.down.sql`
```sql
-- Hypertable pattern
CREATE TABLE IF NOT EXISTS my_table (
    time        TIMESTAMPTZ NOT NULL,
    account_id  TEXT NOT NULL,
    env_mode    TEXT NOT NULL,
    symbol      TEXT NOT NULL,
    value       DOUBLE PRECISION NOT NULL
);
SELECT create_hypertable('my_table', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);
CREATE INDEX IF NOT EXISTS idx_my_table_sym ON my_table (symbol, time DESC);
```

## Event Publishing Pattern
```go
// Define domain event (add to domain/event.go)
const EventMyThingHappened EventType = "MyThingHappened"

// Publish from service
s.bus.Publish(domain.Event{
    Type:      domain.EventMyThingHappened,
    Payload:   myPayload,
    OccurredAt: time.Now(),
})
```

## Gotchas

### TOML array-of-tables decodes to `[]map[string]any`, not `[]any`
`[[params.foo]]` in a spec TOML arrives in `spec.Params["foo"]` as
`[]map[string]any`. Parsers that type-assert only `[]any` silently return
empty and the feature appears dead at runtime (no error, no log). Accept
both shapes when parsing, and add a spec-loader test that loads the real
TOML and asserts the list is non-empty — hand-built test params go through
the `[]any` path and hide the bug. Also: flat keys placed AFTER
`[[params.foo]]` in the same file attach to the last array entry, not back
to `[params]`. Put flat keys first.

### syncMode bus + in-handler publish re-enters the same goroutine
In backtest, `memory.Bus` runs in syncMode — `SubscribeAsync` becomes
`Subscribe`, and after `FreezeHandlers()` the fast-path publishes directly
on the caller's stack. If a strategy inside `Instance.OnEvent` (holding
`inst.mu`) emits a domain event whose subscribers call back into the same
instance (e.g. `handleCopytradeExitRejected` → `inst.IsActive()`), the
inner call tries to re-acquire `inst.mu` and deadlocks. Make state read
by any reentrant path lock-free (`atomic.Pointer[...]` for lifecycle
worked well), and defer the inner `Instance.OnEvent` dispatch into a
runner-level callback queue drained by every handler entry-point AFTER
`inst.mu` and `r.mu` are released. See `runner_copytrade_reentry_test.go`
for the reproduction.

### Copytrade handlers must use `event.OccurredAt`, not `time.Now()`
`handleFill` threads the fill payload's `filled_at` into `instCtx.now`
correctly, but `handleCopytradeSignal`, `handleCopytradeExitRejected`,
and `handleRejection` historically used `time.Now()`. In backtest the
event bus stamps sim-time via `NewBacktestEvent` but wall-clock is
~months ahead — any strategy code comparing `sig.PostedAt` (sim) against
`ctx.Now()` (wall) sees tens-of-millions-of-seconds deltas and any TTL
check fires immediately. Use the `Runner.handlerNow(event, handler)`
helper (picks `event.OccurredAt`, falls back to wall-clock with a canary
log if the envelope is zero) in every handler that populates
`instanceContext.now`.

### Reconciliation gaps + multi-write sequencing for crash recovery
The execution path has three reconcilers — `execution.reconcileOnBoot` (DB
orders table), `positionmonitor.reconcileOpenOrdersOnBoot` (broker open
orders), and the WS `handleStreamFill` in-memory path. They cover disjoint
(broker-state × DB-state) cells; a crash between `broker.SubmitOrder` and
`repo.SaveOrder` lands in the uncovered cell (broker=filled / DB=no-row)
and the fill is permanently lost. When adding a reconciler, enumerate
which cell of that matrix it covers and confirm no cell is orphaned.
`backfillFromBrokerHistory` fills the last gap via the optional
`ports.FilledOrderLister` broker capability.

When the repo lacks a single atomic call for a multi-step write (e.g.
SaveOrder + SaveTrade + UpdateOrderFill), sequence the writes so any
intermediate failure leaves the row in a state an EXISTING reconciler
already heals. Concretely: seed the order as `status="submitted"` BEFORE
writing the trade, and let `UpdateOrderFill` flip it to `filled` last.
If SaveTrade or UpdateOrderFill fail, `reconcileOnBoot` finds the
non-terminal row and pulls fill state via `broker.GetOrderDetails` on
the next tick. Writing `status="filled"` up front would orphan the row
with no trade attached — invisible to every reconciler.

### `Instance.OnEvent` does not stamp `StrategyInstanceID`; only `OnBar` does
`instance.go`'s `OnBar` stamps `sig.StrategyInstanceID = inst.id` before
returning signals; `OnEvent` does not. Existing callers (`handleFill`,
`handleRejection`, `handleAuctionImbalance`, `handleTradeReceived`) discard
the returned signals, so the gap is invisible in practice. If a new
handler forwards OnEvent-returned signals via `r.emitSignal`, stamp the
instance ID post-hoc or `SignalCreated` events will have an empty ID and
the strategy-label metrics will bucket as `unknown`.

## References
- Full port list: see `backend/internal/ports/*.go` directly
- Event list: `backend/internal/domain/event.go`
- Wiring examples: `backend/cmd/omo-core/services.go`
