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

### Per-execution trade rows for multi-fill orders
Large orders split into N partial fills must produce N trade rows keyed
by `execution_id`, not one cumulative row. Writing one row with
cumulative qty + the last leg's ExecID loses per-leg price detail and
breaks under any OrderStatus/Fills race in the adapter — the 2026-04-24
SPY call incident landed a single `qty=1` row for a 34-contract fully
filled order because `tradeToOrderUpdate` read `t.Fills()[len-1]` while
`OrderStatus.Filled` was stale. Adapters must emit one OrderUpdate per
ExecID; execution writes per-leg via `RecordFill` (execution_id UNIQUE
dedups); `orders.filled_qty` is bumped via a monotonic `GREATEST(...)`
UPDATE with a `WHERE filled_qty <= $new` guard and CASE-based status
promotion. This makes the write idempotent under replay, out-of-order
delivery, and boot reconcile. The matrix is now four reconcilers:
add `reconcileFillsOnBoot` (via optional `ports.FillLister`) at fill-leg
granularity alongside the three order-level ones.

### Broker adapter: never derive terminal state from multi-source snapshots
When the adapter exposes both cumulative state (`OrderStatus.Filled`)
and per-exec state (`Trade.Fills()`), a single "terminal" OrderUpdate
that reads both is racy — ibsync can flip `OrderStatus.Status=Filled`
before the Fills slice catches up. Prefer per-ExecID events sourced from
one cache (`ib.Fills()` is authoritative, server-side), and treat
`OrderStatus.Status` only as the "label which leg is last" hint. Order-
level dedup keyed on `OrderID` (rather than `ExecID`) will silently
drop every leg after the first once ANY source emits a terminal — the
pre-fix adapter's `emittedDone` set did exactly this.

### `Instance.OnEvent` does not stamp `StrategyInstanceID`; only `OnBar` does
`instance.go`'s `OnBar` stamps `sig.StrategyInstanceID = inst.id` before
returning signals; `OnEvent` does not. Existing callers (`handleFill`,
`handleRejection`, `handleAuctionImbalance`, `handleTradeReceived`) discard
the returned signals, so the gap is invisible in practice. If a new
handler forwards OnEvent-returned signals via `r.emitSignal`, stamp the
instance ID post-hoc or `SignalCreated` events will have an empty ID and
the strategy-label metrics will bucket as `unknown`.

### `json:",omitempty"` does not skip zero-value structs
encoding/json honors `omitempty` only for nil pointers, nil maps,
empty slices, and zero primitives — not for zero-value embedded
structs. A field declared `Foo MyStruct \`json:"foo,omitempty"\`` will
always serialize as `"foo":{...}` with default values, regardless of
whether the parent ever populated it. The Phase-2 EntryGatedPayload
landed with this mistake on `AVWAPState` and emitted an empty stub on
every MACD block until simplify-pass review caught it. When a JSON
DTO has an "absent vs zero" semantic distinction, declare the field
as a pointer: `Foo *MyStruct \`json:"foo,omitempty"\``. Slices/maps
already work because their nil form is distinguishable from the
populated form. Same trap exists for `time.Time` (zero is a real
timestamp) — pointer-fy or use a custom marshaller.

### Builtin engine name vs TOML spec id on lifecycle payloads
Strategies hardcode their builtin engine name (`"avwap"`, `"macd"`) into
payload fields like `EntryGatedPayload.Strategy` because they don't have
access to their own TOML spec. The runner re-stamps the field at the
boundary in `instanceContext.EmitDomainEvent` using `instCtx.specID =
inst.configStrategyID()` set at every pool-Get site in `handleBarCore`.
When adding a new payload type that carries a `Strategy`/`StrategyID`
field, decide which side owns it: if the strategy hardcodes it, route the
emit through `instanceContext.EmitDomainEvent` so the override fires; if
the runner constructs the payload (e.g. liveness `RecordEval`), pass
`inst.configStrategyID()` directly. A mismatch silently breaks dashboard
joins on `strategy_signal_events.strategy` because rows for the same
instance get split between the engine name and the spec id.

### Aggregator flush cadence vs downstream evaluation cadence
An event-driven aggregator that batches output on a periodic ticker will
silently starve any downstream consumer that evaluates on every input.
`livedarkpool.Service` flushed closed 5m DP buckets on a 1-minute ticker;
the strategy runner evaluated each 5m bar within ~150ms of close. The
just-closed bucket only landed in the lookup cache up to 60s later, so
the runner's `dpSource.Lookup` for the bar it was evaluating always
missed and the dark-pool confluence factor scored 0 every time. Symptom:
DB had the correct buckets persisted, in-memory cache lagged by exactly
one bucket. Fix: push-emit on bucket transition — when a tick arrives in
a strictly newer bucket than `latestBucket`, drain prior buckets via an
optional callback installed by the consumer, and keep the periodic
ticker as a safety net for symbols that go quiet without a transition
trade. Same shape applies to any other roll-on-boundary aggregator
(formingbar, ibkr/bar_aggregator) if a consumer ever polls them at a
finer cadence than their flush.

### Session-anchored aggregator silently drops pre-anchor bars during warmup
`domain.BarAggregator.Push` rejects any bar where `bar.Time.Before(a.sessionOpen)` and bumps an `aggRejectedSessionOpen` counter — no log, no error return. Use sites that call `InitAggregators(syms, todayOpen)` then push pre-today warmup bars (e.g. `monitor.Service.WarmUp(800 1m bars)`) silently get every bar dropped at the aggregator gate. The downstream `s.calculator.Update(closed_5m)` is never called and the per-(sym, "5m") `symbolState` stays un-seeded — invisible until the first live close emits a snapshot with `ema9=0`. Mitigations:
- For warmup paths that span pre-anchor history, bypass the aggregator and seed the calculator directly from native HTF bars (`monitor.Service.WarmUpNative`).
- Or anchor the aggregator at the first warmup bar's date and re-anchor at session boundary (`Runner.WarmUpHTF` does this, but the constructed aggregator is per-call and discarded — runtime aggregators stay anchored at todayOpen).
- Don't trust "WarmUp succeeded" log lines as evidence of HTF state — they reflect the 1m calc only. Verify HTF state with parity-diag (`PARITY_DIAG_ENABLED=true`) showing the per-(sym, tf) snapshot has non-zero EMAs.

### `WarmUpNative` + `Service.WarmUp` aggregator path is a double-feed risk
Once `WarmUpNative` is the canonical HTF seed, the legacy `Service.WarmUp(1m bars)` path becomes a hazard for any bars the aggregator accepts (today's pre-boot 1m bars, anchored at todayOpen): each closed 5m bar emitted by `s.aggregators[sym:5m]` flows into `s.calculator.Update`, double-counting bars that `WarmUpNative` will also seed. Symptom (2026-04-28): monitor and runner_htf agreed on close-only fields (rsi/ema9/ema21/regime_score) but diverged on cumulative fields (vwap $1.50, atr $1.17, vwap_sd $0.66) at runtime — the calc state had today's session integrated twice. Fix: pre-mark the (sym, tf) slots `WarmUpNative` will handle (`Service.ReserveHTFNative`) before any `Service.WarmUp` call, and gate the HTF block in `WarmUp` on the reservation set. Aggregator priming (`agg.Push`) must still run so the runtime first-close has full bucket coverage; only the `s.calculator.Update(closed)` side effect is skipped. Symbols/tfs without a registered HTF strategy (no WarmUpNative call) fall through to the legacy seeding unchanged. Companion fix on the runner side: `Runner.PrimeAggregators(orbBars)` pushes today's 1m through `r.aggregators` without firing `htfCalc.Update` — the prior code (`Runner.WarmUpHTF`) used a throwaway aggregator and a guarded `WarmUpTF`, leaving the runtime aggregator unprimed and emitting a partial first 5m close.

### Captured closures shared across goroutines need their own state
`omo-replay`'s sharded warmup called `makeSnapshotFn()` once outside the per-shard goroutine loop and passed the result to all shards. The captured `IndicatorCalculator` is not thread-safe, so concurrent shard goroutines hit `concurrent map read and map write` on its `states` map and the runtime panicked. Pattern: any factory whose return value is captured into a closure that goroutines invoke concurrently must either be called inside each goroutine, or the captured value must be explicitly thread-safe. Static analysis won't catch this — the panic is non-deterministic and depends on tick-rate.

### In-process backtest pollutes live parity-diag log if calc Label collides
A `/backtest/run` call creates its own `monitor.Service` via `bootstrap.BuildMonitor`, which calls `NewService` and sets `calc.Label = "monitor"` — the SAME label as the live monitor's calc. Both processes emit parity-diag rows into the same log file via the same logger, so `grep '"calc":"monitor"'` returns live + backtest emits intermixed. The backtest's calc has fresh state (no warmup), so it emits ema200=0, vwap=first-typical-price for early bars — looks like a divergence in live but is just a different process. `Service.TagBacktest(backtestID)` now also re-labels the calc to `"monitor_backtest_<id>"` so backtest emits are filterable. Lesson: any per-process diagnostic ID that appears in shared log output must be discriminated at the emit site; relying on `Label = "monitor"` being unique across all monitor instances was wrong.

### `time.Time` map keys include Location, so UTC vs local mismatch silently misses
Two `time.Time` values for the same instant compare unequal as map keys
when their `Location()` differs — the runtime hashes the wall+ext+loc
triple, not just the absolute instant. The DP aggregator built bucket
keys from `t.Truncate(5*time.Minute)` while the inbound trade timestamp
was in CDT (`America/Chicago`); the strategy runner queried with a UTC
bar timestamp. Diag dumped `latest=2026-04-27T10:35:00-05:00` and
`probe=2026-04-27T15:35:00Z` — same instant, different Location, no
match in `map[time.Time]*Bucket`. Fix: normalize to UTC at every site
that produces or consumes a time-keyed map entry (`AddTrade`,
`Snapshot`, `FlushClosed`, the consumer's `Lookup`). Equivalent rule
for any cross-process or cross-package time map: pick a canonical zone
(UTC) and normalize at the boundary; never trust upstream Location.

### Caches that return partial coverage as success silently truncate the answer
`SessionResolver.getBarsInRange` cached per-day bar slices and returned
whatever was in the cache when any day in the requested range hit —
even if other days in the range missed. Backtests that crossed a
weekend got Friday-only or Monday-only results depending on which day
was warm, and multi-anchor AVWAPs (`pd_high`, `pd_low`, `session_open`)
all converged to byte-identical state because each anchor replayed the
same truncated slice. Symptom: backtest reproduced live's narrow
behavior but the diag dump showed identical `vwap`/`slope`/`barCount`
across anchors that should diverge. Fix: track `allCached` across the
full range and only return cached data when every requested day was
present; otherwise fall through to the fetcher. General rule: a cache
read for a range is a hit iff *every* sub-key is present — anything
less is a miss, not a partial answer.

### Re-peg cancel/replace can race the original fill into a duplicate position
Exit-order re-pegging issues a `Cancel(old)` + `Submit(new)` pair, but
the broker can fill `old` after we requested cancel and `new` before
the cancel ack returns. Both fills then hit `insertFillLeg` and create
two separate trade rows for one logical exit, leaving `db_net_qty`
double-decremented (RIVN: SELL 47 at 18:30:01, REPEG SELL 47 at
18:30:12.281, both filled within 31ms; resulting `db_net_qty=-47` on a
position that was already flat). Fix: in `insertFillLeg`, gate exit
fills by current tracked position quantity — reject when
`legQty > pos.Quantity + epsilon` or when no position is tracked. This
is the only safe boundary because the cancel-vs-fill race is owned by
the broker and we never get a deterministic ordering from their async
acks. Same pattern applies to any cancel-replace flow: the application
must be idempotent against double-fills, never assume cancel won.

### In-place slice filter mutates the caller's backing array
The idiomatic Go pattern `out := s[:0]; for _, x := range s { if pred(x) { out = append(out, x) } }; return out` is allocation-free but **silently overwrites the caller's backing array**. The returned slice is correct, but the caller's slice (same backing) now has filtered values in positions 0..len(filtered)-1 and stale-original values beyond. Any caller that re-walks the original slice after calling such a filter will read corrupted data (`warmup.filterRTH` is one example; `warmup.TrimWithBoot1` initially picked the wrong boot+1 bar because it walked rawBars after Trim ran). Fix patterns:
- Capture whatever you need from the original slice **before** calling the filter (`cp := rawBars[i]; boot1 = &cp`).
- Or have the filter allocate a fresh slice instead of reusing the backing array — accepts the allocation cost in exchange for caller-safety.
- The function-local view always looks correct because tests only inspect the returned slice; this bug is invisible to the function's own test suite and only surfaces at call sites that touch the original slice afterwards.

## References
- Full port list: see `backend/internal/ports/*.go` directly
- Event list: `backend/internal/domain/event.go`
- Wiring examples: `backend/cmd/omo-core/services.go`
