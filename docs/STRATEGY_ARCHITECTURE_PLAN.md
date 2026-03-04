# Strategy Architecture Implementation Plan

## Dynamic, User-Defined, Swappable Strategy System

Last Updated: March 4, 2026

---

## Executive Summary

Evolve oh-my-opentrade from a single hardcoded strategy (ORB Break & Retest) to a **multi-strategy system** where strategies are pluggable, versionable, and swappable at runtime. Based on an industry survey (QuantConnect, Freqtrade, TradingView, NinjaTrader, Sierra Chart) and Oracle architectural consultation.

**Approach**: Hybrid StrategySpec model — TOML defines metadata/params/routing, optional Yaegi hooks are constrained to pure stateless functions. A Go `Strategy` interface executed by a `StrategyRunner` supports multi-strategy routing, lifecycle states, and blue/green swaps.

**Effort**: Medium (1–2d) for contracts + routing + TOML v2 + migration skeleton. Large (3d+) for strong sandboxing (out-of-process) + backtest parity.

---

## Industry Survey Summary

| Platform | Strategy Contract | Hot-Swap | Sandboxing | Multi-Strategy |
|----------|------------------|----------|------------|----------------|
| **QuantConnect** | Subclass `QCAlgorithm` with lifecycle callbacks (`Initialize`, `OnData`) | Redeploy/restart | Strong: cloud isolation, resource limits, whitelisted APIs | One algo orchestrates sub-modules |
| **Freqtrade/Jesse** | Plugin base classes with required callbacks (`populate_indicators`, `populate_signals`) | Restart required; config reload for params | Minimal (Python); container isolation | Config-driven universe; multiple bots/processes |
| **TradingView** | Pine Script: declarative series calculations + `strategy.*` calls | Edit → full recompute | Strong language sandbox: no fs/network, deterministic | Script per chart/symbol/timeframe |
| **NinjaTrader** | C# NinjaScript with platform callbacks | Recompile/reload | Weak at language level; user permissions | Multiple strategies per instrument |
| **Sierra Chart** | C++ custom studies with platform callbacks | Recompile/reload | Weak (native code); platform constraints | Strategies attached to instruments |
| **Alpaca Ecosystem** | No standard; users run own bots | Redeploy container | User's infrastructure (Docker/K8s) | Whatever user builds |

**Key Pattern**: Stable callback contract + config-driven universe routing + versioned lifecycle + backtest/live parity. True hot-swap mid-stream is rare outside sandboxed DSL runtimes (Pine-like).

---

## Architecture Design

### Strategy Interface Contract

```go
// internal/domain/strategy/contract.go
package strategy

type Strategy interface {
    Meta() Meta
    WarmupBars() int

    // Called once per (instance, symbol) on activation or swap.
    Init(ctx Context, symbol string, params map[string]any, prior State) (State, error)

    // Pure decision step: bar + state → signals + next state.
    OnBar(ctx Context, symbol string, bar Bar, st State) (next State, signals []Signal, err error)

    // Optional: react to non-bar events (fills, halts, risk events).
    OnEvent(ctx Context, symbol string, evt any, st State) (next State, signals []Signal, err error)
}
```

**Separation of concerns**: Strategies produce `Signal`s (entry/exit intent with strength/tags). An application-layer `RiskSizer` converts signals into `OrderIntentCreated` events after position sizing, risk checks, and conflict resolution.

### Signal Type

```go
type Signal struct {
    StrategyInstanceID string
    Symbol             string
    Type               SignalType  // "entry" | "exit" | "adjust" | "flat"
    Side               Side        // "buy" | "sell"
    Strength           float64     // optional scoring 0.0–1.0
    Tags               map[string]string // reason codes, regime, etc.
}
```

### Strategy State

```go
type State interface {
    Marshal() ([]byte, error)
    Unmarshal([]byte) error
}
```

State is opaque to the runner — each strategy manages its own internal state (e.g., ORBTracker's state machine, indicator buffers). State must be serializable for persistence/recovery.

### Strategy Context

```go
type Context interface {
    Now() time.Time
    Logger() Logger
    EmitDomainEvent(evt any) error // keeps strategy pure-ish; no direct adapter access
}
```

---

## TOML Schema Evolution (StrategySpec v2)

```toml
schema_version = 2

[strategy]
id = "orb_break_retest"
version = "1.2.0"
name = "ORB Break & Retest"
description = "Opening Range Breakout — Break & Retest with volume confirmation"
author = "system"
created_at = "2026-03-04T00:00:00Z"

[lifecycle]
state = "LiveActive"  # Draft | BacktestReady | PaperActive | LiveActive | Deactivated | Archived
paper_only = false

[routing]
symbols = ["AAPL", "MSFT", "GOOGL", "AMZN", "TSLA", "SOXL", "U", "PLTR", "SPY", "META"]
timeframes = ["1m"]
priority = 100
conflict_policy = "priority_wins"  # priority_wins | merge | vote
exclusive_per_symbol = false

[params]
orb_window_minutes = 30
min_rvol = 1.5
min_confidence = 0.65
breakout_confirm_bps = 2
touch_tolerance_bps = 2
hold_confirm_bps = 0
max_retest_bars = 15
allow_missing_bars = 1
max_signals_per_session = 1
stop_bps = 25
limit_offset_bps = 5
risk_per_trade_bps = 10

[regime_filter]
enabled = true
allowed_regimes = ["TREND"]
min_atr_pct = 0.8

[hooks]
# Each hook is optional and must match a fixed signature
signals = { engine = "builtin", name = "orb_v1" }  # builtin strategy logic
price = { engine = "yaegi", entrypoint = "ComputePrices" }  # optional Yaegi hook
# size = { engine = "builtin", name = "fixed_risk" }  # future
```

**Migration**: Existing `orb_break_retest.toml` evolves to v2 format. `schema_version = 1` (current) continues to work — the loader detects version and applies defaults for missing v2 fields.

---

## Event Flow Changes

### Before (current)

```
MarketBarSanitized → Monitor (indicators + ORB) → SetupDetected → Strategy Service → OrderIntentCreated
```

### After (multi-strategy)

```
BarClosed / MarketBarSanitized
       │
       ▼
  StrategyRunner (routes bar to assigned strategy instances)
       │
       ├── ORBStrategy.OnBar() → Signal{entry, buy, 0.85}
       ├── MomentumStrategy.OnBar() → Signal{flat}
       │
       ▼
  SignalCreated (domain event)
       │
       ▼
  RiskSizer (position sizing + risk checks + conflict resolution)
       │
       ▼
  OrderIntentCreated (domain event)
       │
       ▼
  Execution Service (existing: risk engine, kill switch, slippage guard)
```

`SetupDetected` becomes internal to strategies (or deprecated). The `StrategyRunner` replaces the current single-strategy lookup path.

---

## Multi-Strategy Routing

The unit of execution is a **StrategyInstance**:

- A `StrategySpec` version is immutable; activating it creates instances with assignments
- **Universe**: explicit symbols, tags, regex, or "all in watchlist X"
- **Concurrency**: `parallel_ok` (multiple strategies per symbol) vs `exclusive_per_symbol`
- **Priority**: deterministic conflict resolution when signals conflict
- **Conflict policy**: `priority_wins` (highest priority signal wins), `merge`, or `vote`

Start simple: allow multiple strategies per symbol, enforce deterministic conflict policy in the RiskSizer.

---

## Strategy Lifecycle

```
Draft → BacktestReady → PaperActive → LiveActive → Deactivated → Archived
```

- **Draft**: Editable, not running. User is configuring params/hooks.
- **BacktestReady**: Frozen inputs. Can run backtests but no live/paper trading.
- **PaperActive**: Running on paper account. Emits signals but orders go to paper only.
- **LiveActive**: Running on live account. Emits real orders.
- **Deactivated**: Stopped. State preserved for inspection. Can be reactivated.
- **Archived**: Read-only historical record.

**Promotion** = create new version + activate version. Never mutate an active version.

---

## Dynamic Swap (Blue/Green)

1. New version starts consuming the same bars with a **warmup window** (no signals emitted).
2. At a **bar boundary**, atomically switch "decision output" to the new version.
3. Archive the old version's state snapshot.
4. State handoff: `prior State` passed into `Init()`. If incompatible, strategy handles `nil` and re-warms.

---

## Yaegi Safety (In-Process)

### Current Constraints (viable now)

- Whitelist imports: only allow `math`, `fmt` (no `os`, `net`, `runtime`, `reflect`)
- Only allow calling a fixed function signature — no `go` statements, no goroutine creation
- Time budgets: 100ms timeout per hook call with circuit-breaker (3 failures → disable instance + alert)
- No filesystem/network/broker access exposed to Yaegi context

### Future (when untrusted uploads needed)

- Execute user hooks in a **separate worker process** with OS limits (CPU/time/memory)
- Narrow RPC contract (stdin/stdout JSON or local unix socket)
- Or: WASM modules with strict resource limits

---

## Package Structure

```
backend/internal/
  domain/
    strategy/
      contract.go      # Strategy interface, Signal, State, Context, Meta
      types.go          # ID, Version, Side, SignalType value objects
      lifecycle.go      # Lifecycle state machine, transitions, validation

  ports/
    strategy/
      store.go          # StrategySpecStore interface (list, get, save, watch)
      registry.go       # StrategyRegistry interface (register builtin strategies)

  app/
    strategy/
      runner.go         # StrategyRunner: routes bars → instances → signals
      router.go         # Symbol→Instance routing, conflict resolution
      instance.go       # StrategyInstance: wraps Strategy + state + assignment
      risk_sizer.go     # Signal → OrderIntent (position sizing, risk checks)
      spec_loader.go    # TOML v1/v2 loader (replaces current dna_manager.go)
      lifecycle_svc.go  # Lifecycle transitions (promote, deactivate, archive)
      service.go        # Existing service (evolves to use runner)
      dna_manager.go    # Existing (deprecated gradually, replaced by spec_loader)

    strategy/builtin/
      orb_v1.go         # ORBStrategy: wraps ORBTracker as Strategy implementation
      # momentum_v1.go  # Future: momentum strategy
      # mean_revert.go  # Future: mean reversion strategy

  adapters/
    strategy/
      store_fs/
        store.go        # Filesystem-based StrategySpecStore (TOML files)
      hooks_yaegi/
        compiler.go     # Yaegi hook compiler + whitelist bindings
        sandbox.go      # Import restrictions, timeout enforcement

configs/
  strategies/
    orb_break_retest.toml    # Migrated to v2 format
    # future strategies here
```

---

## Implementation Phases

### Phase A: Domain Contracts & Types (Day 1, Morning)

| # | Task | Dependencies | Est. |
|---|------|-------------|------|
| A1 | Create `internal/domain/strategy/` package with `Strategy` interface, `Signal`, `State`, `Context`, `Meta` types | None | 1h |
| A2 | Create `internal/domain/strategy/lifecycle.go` — lifecycle state enum, valid transitions, validation | A1 | 30m |
| A3 | Create `internal/ports/strategy/store.go` — `StrategySpecStore` interface | A1 | 30m |
| A4 | Create `internal/ports/strategy/registry.go` — `StrategyRegistry` interface for builtin strategies | A1 | 15m |
| A5 | Unit tests for lifecycle transitions, signal types, state serialization | A1–A4 | 30m |

**Exit criteria**: All types compile, tests pass, zero coupling to existing code.

### Phase B: Wrap ORBTracker as Strategy (Day 1, Afternoon)

| # | Task | Dependencies | Est. |
|---|------|-------------|------|
| B1 | Create `app/strategy/builtin/orb_v1.go` — implement `Strategy` interface wrapping existing `ORBTracker` | A1, existing ORBTracker | 2h |
| B2 | `orb_v1.go` state: implement `State` interface wrapping `ORBState` with Marshal/Unmarshal | B1 | 30m |
| B3 | `orb_v1.go` `OnBar()`: delegates to `ORBTracker.OnBar()`, maps ORB state transitions to `Signal`s | B1 | 1h |
| B4 | Unit tests: ORBStrategy produces correct signals for breakout/retest scenarios | B1–B3 | 1h |

**Exit criteria**: ORBStrategy wraps ORBTracker cleanly, produces entry signals on retest confirmation, all existing ORB tests still pass.

### Phase C: StrategyRunner & Routing (Day 2, Morning)

| # | Task | Dependencies | Est. |
|---|------|-------------|------|
| C1 | Create `app/strategy/router.go` — symbol→instance routing from TOML assignments | A1 | 1h |
| C2 | Create `app/strategy/instance.go` — `StrategyInstance` (wraps Strategy + state + assignment + lifecycle) | A1, B1 | 1h |
| C3 | Create `app/strategy/runner.go` — `StrategyRunner`: subscribes to bar events, routes to instances, collects signals, emits `SignalCreated` | C1, C2 | 2h |
| C4 | Unit tests: runner routes bars correctly, handles multiple instances per symbol, conflict resolution | C1–C3 | 1h |

**Exit criteria**: StrategyRunner can route bars to the ORBStrategy instance, emit signals, handle single and multi-instance scenarios.

### Phase D: TOML v2 Spec Loader (Day 2, Afternoon)

| # | Task | Dependencies | Est. |
|---|------|-------------|------|
| D1 | Create `app/strategy/spec_loader.go` — loads TOML v1 (backward compat) and v2 formats | A1 | 1h |
| D2 | Migrate `orb_break_retest.toml` to v2 format (add `[lifecycle]`, `[routing]`, `[hooks]` sections) | D1 | 30m |
| D3 | Create `adapters/strategy/store_fs/store.go` — filesystem `StrategySpecStore` with hot-reload (replaces DNAManager file watching) | D1, A3 | 1h |
| D4 | Unit tests: v1 compat, v2 loading, hot-reload detection | D1–D3 | 30m |

**Exit criteria**: Both TOML v1 and v2 load correctly, hot-reload works, existing config not broken.

### Phase E: Signal → Order Pipeline (Day 3, Morning)

| # | Task | Dependencies | Est. |
|---|------|-------------|------|
| E1 | Create `app/strategy/risk_sizer.go` — subscribes to `SignalCreated`, applies position sizing + risk checks, emits `OrderIntentCreated` | A1 | 1.5h |
| E2 | Wire `StrategyRunner` into `main.go` — replace current single-strategy subscription with runner | C3, D1 | 1h |
| E3 | Integration test: bar → runner → signal → risk_sizer → order intent | E1, E2 | 1h |

**Exit criteria**: Full pipeline works end-to-end. Existing behavior preserved (ORB signals produce same order intents as before).

### Phase F: Lifecycle Management (Day 3, Afternoon)

| # | Task | Dependencies | Est. |
|---|------|-------------|------|
| F1 | Create `app/strategy/lifecycle_svc.go` — promote, deactivate, archive operations | A2 | 1h |
| F2 | Add lifecycle HTTP endpoints: `GET /strategies`, `POST /strategies/{id}/promote`, `POST /strategies/{id}/deactivate` | F1 | 1h |
| F3 | Dashboard: strategy list with lifecycle state, promote/deactivate buttons | F2 | 1.5h |
| F4 | Unit tests for lifecycle transitions and API endpoints | F1–F2 | 30m |

**Exit criteria**: Strategies can be promoted/deactivated via API and dashboard.

### Phase G: Blue/Green Swap (Day 4)

| # | Task | Dependencies | Est. |
|---|------|-------------|------|
| G1 | Implement warmup detection in `StrategyInstance` — new version runs silently until warmup complete | C2 | 1h |
| G2 | Implement atomic swap at bar boundary — old instance archived, new instance takes over | G1 | 1.5h |
| G3 | State handoff: pass old state to new `Init()`, handle incompatible state gracefully | G2 | 1h |
| G4 | Tests: swap mid-session, verify no missed bars, verify state handoff | G1–G3 | 1h |

**Exit criteria**: Strategy version swap works without dropping bars or double-signaling.

### Phase H: Yaegi Safety Hardening (Day 4–5)

| # | Task | Dependencies | Est. |
|---|------|-------------|------|
| H1 | Create `adapters/strategy/hooks_yaegi/sandbox.go` — import whitelist (only `math`, `fmt`) | Existing Yaegi | 1h |
| H2 | Add timeout enforcement: 100ms per hook call, circuit-breaker after 3 failures | H1 | 1h |
| H3 | Tests: verify blocked imports, timeout behavior, circuit-breaker activation | H1–H2 | 1h |

**Exit criteria**: Yaegi hooks cannot access fs/network, hang detection works.

---

## Migration Path (from current to multi-strategy)

1. **Phase A–B**: New code alongside existing. Zero changes to current behavior.
2. **Phase C–D**: `StrategyRunner` coexists with current `service.go`. Feature flag to switch.
3. **Phase E**: Wire runner into `main.go` behind feature flag (`STRATEGY_V2=true`).
4. **Phase F**: Once stable, deprecate old `lookupDNA()` path.
5. **Phase G–H**: Hardening after core migration is complete.

At every phase, `go test ./...` and `go build` must pass. Existing ORB behavior must be preserved.

---

## Dependency Graph

```
Phase A (Contracts)
    │
    ├───────────────┐
    ▼               ▼
Phase B          Phase D
(Wrap ORB)       (TOML v2)
    │               │
    └───────┬───────┘
            ▼
        Phase C (Runner + Routing)
            │
            ▼
        Phase E (Signal → Order Pipeline)
            │
            ├───────────────┐
            ▼               ▼
        Phase F          Phase G
        (Lifecycle)      (Blue/Green Swap)
                            │
                            ▼
                        Phase H (Yaegi Safety)
```

---

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| ORBTracker wrapping introduces subtle behavior changes | Signals differ from current flow | Comprehensive test: same bars → same signals before/after |
| Multi-strategy conflict produces double orders | Over-trading, excess risk | Start with `exclusive_per_symbol = true` for ORB; RiskSizer enforces single active order per symbol |
| Yaegi hooks hang (infinite loop) | Strategy instance frozen | 100ms timeout + circuit-breaker; disable instance and alert |
| TOML v2 migration breaks existing config | System won't start | Backward compat: v1 loader auto-fills v2 defaults |
| State serialization breaks on version upgrade | Strategy can't recover after restart | `Init()` handles `nil` prior state; re-warm from scratch |

---

## Success Criteria

- [ ] ORBStrategy produces identical signals to current hardcoded ORB flow (verified by test)
- [ ] Two strategies can run simultaneously on different symbol sets
- [ ] Strategy can be deactivated without restart
- [ ] New strategy version can be swapped in without missing bars
- [ ] All existing tests continue to pass at every phase
- [ ] `go build` succeeds at every phase
- [ ] Dashboard shows strategy list with lifecycle states

---

## Open Questions (To Be Decided During Implementation)

1. **Should strategies own their indicator computation, or receive pre-computed indicators?**
   - Current: Monitor computes all indicators centrally. ORBTracker receives raw bars.
   - Recommendation: Strategies receive bars + indicator snapshots. Central indicator computation stays (efficient, consistent). Strategies can request additional custom indicators via hooks.

2. **How should backtesting integrate?**
   - Recommendation: Same `Strategy.OnBar()` interface. A `BacktestRunner` replays historical bars through the same `StrategyRunner`. Deferred to Phase 10 (existing implementation plan).

3. **When should we move to out-of-process Yaegi execution?**
   - Recommendation: When we support untrusted user uploads (marketplace-style). Current: trusted strategies only, in-process is acceptable.
