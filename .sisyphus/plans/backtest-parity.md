# Backtest Pipeline Parity: omo-replay ↔ omo-core

## TL;DR

> **Quick Summary**: Bring omo-replay to full pipeline parity with omo-core by fixing backtest determinism (event bus sync, clock injection), extracting shared initialization into a `bootstrap` package, then wiring all missing components (execution guards, multi-strategy, PositionMonitor, SignalDebateEnricher) into omo-replay.
>
> **Deliverables**:
> - Deterministic backtest execution (event bus sync, injectable clocks, per-bar exit evaluation)
> - SimBroker implementing QuoteProvider + AccountPort for full guard chain compatibility
> - Shared `internal/app/bootstrap/` package used by both binaries
> - omo-replay with identical pipeline to omo-core (multi-strategy, all guards, PositionMonitor, enricher)
> - Integration test validating full pipeline event flow
> - omo-core fully regression-tested — zero behavioral changes
>
> **Estimated Effort**: Large
> **Parallel Execution**: YES — 6 waves
> **Critical Path**: T1 (EventBus sync) → T7-10 (bootstrap pkg) → T11 (omo-core rewire) → T12 (omo-replay rewire) → T13-16 (backtest wiring) → T20-21 (tests) → F1-F4 (final review)

---

## Context

### Original Request
Bring the backtest binary (omo-replay) to full pipeline parity with the live trading binary (omo-core). Currently omo-replay is a separate re-implementation with 12 identified gaps (8 critical, 4 moderate) leading to unrealistic backtest results. The solution must extract shared initialization into reusable code, not copy-paste.

### Interview Summary
**Key Discussions**:
- All 12 gaps verified through code exploration — confirmed single-strategy hardcoding, missing guards, no PositionMonitor, no enricher, hardcoded risk params
- SimBroker port gap matrix established: missing QuoteProvider.GetQuote and AccountPort.GetAccountBuyingPower
- No Clock port exists in ports/ — uses injected `func() time.Time` pattern already
- omo-core V2 strategy pipeline (behind STRATEGY_V2=true) registers 4 builtins with hook-based routing

**Research Findings**:
- PositionMonitor tick loop (1s wall-clock) is fundamentally broken for max-speed backtest — fires ~5 times across 10,000 bars
- PriceCache hardcodes `time.Now()` for ObservedAt — causes staleness mismatches with injected clocks
- Execution service uses `SubscribeAsync` — async processing means bars outpace fill processing at max speed
- Warmup uses `time.Now()` instead of `--from` date — warms from wrong session
- Strategy runner warmup missing entirely in omo-replay

### Metis Review
**Identified Gaps** (addressed):
- **EVENT BUS SYNC (CRITICAL)**: Added Phase 0 — `WaitPending()` method on memory bus to drain async handlers between bar groups
- **POSITION MONITOR TICK LOOP (CRITICAL)**: Added public `EvalExitRules(barTime)` method + `WithDisableTickLoop()` option — no changes to omo-core behavior
- **PRICE CACHE CLOCK (CRITICAL)**: Added `WithClock(func() time.Time)` option to PriceCache for bar-time ObservedAt
- **WARMUP DATE-AWARENESS**: Changed warmup to use `--from` date, not `time.Now()`; added strategy runner warmup
- **MULTI-DAY BOUNDARIES**: PositionMonitor needs day-boundary awareness for EODFlatten per simulated day
- **SPIKE FILTER SEEDING**: Adaptive filter may reject valid historical bars without warmup — needs seeding or validation
- **EXIT PENDING TIMEOUT**: Wall-clock 10s timeout in exit_eval means stuck exits in max-speed backtest — needs clock injection

---

## Work Objectives

### Core Objective
Make omo-replay execute the identical event pipeline as omo-core so that backtest results faithfully reflect live trading behavior — same strategies, same guards, same exit rules, same enrichment — with deterministic, reproducible results.

### Concrete Deliverables
- `internal/app/bootstrap/` — shared initialization package with execution, strategy, posmon, perf builders
- `internal/adapters/simbroker/broker.go` — expanded with QuoteProvider + AccountPort
- `internal/adapters/eventbus/memory/bus.go` — WaitPending() for backtest sync
- `internal/app/positionmonitor/` — backtest mode support (EvalExitRules, clock injection, disable tick loop)
- `internal/app/positionmonitor/price_cache.go` — injectable clock
- `internal/adapters/noop/` — consolidated no-op adapters
- `backend/cmd/omo-replay/main.go` — refactored to use shared bootstrap
- `backend/cmd/omo-core/services.go` — refactored to use shared bootstrap
- Integration test validating full pipeline

### Definition of Done
- [ ] `cd backend && go build -o bin/omo-core ./cmd/omo-core && go build -o bin/omo-replay ./cmd/omo-replay` — both compile
- [ ] `cd backend && go test ./...` — all tests pass
- [ ] `cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY` — produces events: BarSanitized, SignalCreated, OrderIntentCreated, FillReceived, ExitTriggered
- [ ] `cd backend && go run ./cmd/omo-replay/ --from 2025-06-02 --to 2025-06-02 --symbols SPY` — replay-only mode unchanged
- [ ] Same backtest run twice produces identical results (determinism)

### Must Have
- All 8 critical gaps closed (multi-strategy, enricher, guards, PositionMonitor, position lookup, directions, config-driven params, warmup)
- Deterministic event processing in backtest mode (no async race conditions)
- Per-bar exit rule evaluation using simulated time
- Clock injection in all time-dependent components (PriceCache, PositionMonitor, KillSwitch, DailyLossBreaker, TradingWindowGuard)
- `--no-ai` flag (default true in backtest) to skip SignalDebateEnricher
- omo-core zero behavioral changes — passes existing tests identically

### Must NOT Have (Guardrails)
- No changes to omo-core's `runTickLoop()` or `SubscribeAsync` behavior
- No SimBroker fill model changes (next-bar, volume-aware, partial fills — explicitly deferred)
- No realistic spread simulation in SimBroker QuoteProvider — returns close±slippage/2
- No modification to existing port interface signatures
- No multi-account orchestrator support in omo-replay (single-account only)
- No AI response caching or deterministic debate replays
- No changes to event bus ordering guarantees for live mode
- No new mandatory CLI flags — all existing flags work identically

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES — Go test infrastructure with `go test ./...`
- **Automated tests**: Tests-after (unit tests for new code, integration test for pipeline)
- **Framework**: `go test` (standard Go testing)

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Backend changes**: Use Bash — `go build`, `go test`, `go run` with grep for expected output
- **Integration**: Use Bash — run backtest, parse JSON output, assert event counts
- **Regression**: Use Bash — build both binaries, run test suite, compare outputs

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — backtest determinism primitives, 6 parallel):
├── Task 1: EventBus WaitPending() method [deep]
├── Task 2: PriceCache clock injection [quick]
├── Task 3: PositionMonitor backtest mode [deep]
├── Task 4: SimBroker port expansion (QuoteProvider + AccountPort) [unspecified-high]
├── Task 5: No-op adapter consolidation [quick]
└── Task 6: TradingWindowGuard clock parameter [quick]

Wave 2 (After Wave 1 — shared init extraction, 4 parallel):
├── Task 7: Bootstrap package scaffold + execution guard builder [deep]
├── Task 8: Strategy V2 pipeline builder [deep]
├── Task 9: PositionMonitor + PriceCache builder [unspecified-high]
└── Task 10: Ingestion + Monitor + Perf builder [unspecified-high]

Wave 3 (After Wave 2 — binary rewiring, SEQUENTIAL):
├── Task 11: Refactor omo-core to use bootstrap package [deep]
└── Task 12: Refactor omo-replay to use bootstrap + wire full pipeline [deep]

Wave 4 (After Wave 3 — backtest-specific wiring, 4 parallel):
├── Task 13: Replay loop event sync (WaitPending per bar group) [unspecified-high]
├── Task 14: Warmup fix (from-date, strategy runner warmup, spike filter) [deep]
├── Task 15: --no-ai flag + SignalDebateEnricher wiring [unspecified-high]
└── Task 16: Moderate gaps (SignalTracker, SetBaseSymbols, config-driven params) [unspecified-high]

Wave 5 (After Wave 4 — testing + verification, 3 parallel):
├── Task 17: Pipeline integration test [deep]
├── Task 18: SimBroker interface assertions + unit tests [unspecified-high]
└── Task 19: Regression tests (omo-core build, replay-only mode) [unspecified-high]

Wave FINAL (After ALL — independent review, 4 parallel):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real manual QA (unspecified-high)
└── Task F4: Scope fidelity check (deep)

Critical Path: T1 → T7 → T11 → T12 → T13 → T17 → F1-F4
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 6 (Wave 1)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| T1 (EventBus WaitPending) | — | T7, T11, T13 | 1 |
| T2 (PriceCache clock) | — | T9, T12 | 1 |
| T3 (PositionMonitor backtest) | — | T9, T12, T16 | 1 |
| T4 (SimBroker ports) | — | T7, T12, T18 | 1 |
| T5 (No-op adapters) | — | T10, T12 | 1 |
| T6 (TradingWindowGuard clock) | — | T7, T12 | 1 |
| T7 (Bootstrap: execution) | T1, T4, T6 | T11, T12 | 2 |
| T8 (Bootstrap: strategy) | — | T11, T12 | 2 |
| T9 (Bootstrap: posmon) | T2, T3 | T11, T12 | 2 |
| T10 (Bootstrap: ingestion+perf) | T5 | T11, T12 | 2 |
| T11 (omo-core rewire) | T7-T10 | T12, T19 | 3 |
| T12 (omo-replay rewire) | T11 | T13-T16 | 3 |
| T13 (Replay loop sync) | T12 | T17 | 4 |
| T14 (Warmup fix) | T12 | T17 | 4 |
| T15 (--no-ai + enricher) | T12 | T17 | 4 |
| T16 (Moderate gaps) | T12, T3 | T17 | 4 |
| T17 (Integration test) | T13-T16 | F1-F4 | 5 |
| T18 (SimBroker tests) | T4 | F1-F4 | 5 |
| T19 (Regression tests) | T11 | F1-F4 | 5 |
| F1-F4 | T17-T19 | — | FINAL |

### Agent Dispatch Summary

- **Wave 1**: **6 tasks** — T1 `deep`, T2 `quick`, T3 `deep`, T4 `unspecified-high`, T5 `quick`, T6 `quick`
- **Wave 2**: **4 tasks** — T7 `deep`, T8 `deep`, T9 `unspecified-high`, T10 `unspecified-high`
- **Wave 3**: **2 tasks** — T11 `deep`, T12 `deep` (SEQUENTIAL)
- **Wave 4**: **4 tasks** — T13 `unspecified-high`, T14 `deep`, T15 `unspecified-high`, T16 `unspecified-high`
- **Wave 5**: **3 tasks** — T17 `deep`, T18 `unspecified-high`, T19 `unspecified-high`
- **FINAL**: **4 tasks** — F1 `oracle`, F2 `unspecified-high`, F3 `unspecified-high`, F4 `deep`

---

## TODOs

> Implementation + Test = ONE Task. Never separate.
> EVERY task MUST have: Recommended Agent Profile + Parallelization info + QA Scenarios.

- [ ] 1. EventBus WaitPending() Method for Backtest Synchronization

  **What to do**:
  - Add a `WaitPending()` method to the in-memory event bus (`internal/adapters/eventbus/memory/bus.go`) that blocks until all async handler goroutine channels have drained and all in-flight handlers have completed
  - The memory bus uses `SubscribeAsync` which creates goroutines with buffered channels — `WaitPending()` must wait for these channels to empty AND for all handler goroutines to finish processing current messages
  - Implementation approach: track in-flight async handlers via a `sync.WaitGroup` — increment before sending to channel, decrement when handler returns. `WaitPending()` calls `wg.Wait()`
  - Add unit test verifying: publish event → handler is async → call WaitPending → handler has completed
  - This method is ONLY called by the backtest replay loop — omo-core never calls it

  **Must NOT do**:
  - Do NOT change `Subscribe` (sync) behavior
  - Do NOT change `SubscribeAsync` channel buffer sizes
  - Do NOT add "backtest mode" flag to the bus — `WaitPending` is just a method that anyone can call
  - Do NOT change event ordering guarantees

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Concurrent Go code requiring careful sync.WaitGroup placement — a goroutine correctness problem
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go concurrency patterns, sync primitives, channel drain mechanics

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3, 4, 5, 6)
  - **Blocks**: Tasks 7, 11, 13
  - **Blocked By**: None (can start immediately)

  **References**:

  **Pattern References** (existing code to follow):
  - `backend/internal/adapters/eventbus/memory/bus.go` — Current event bus implementation. Find `SubscribeAsync` to understand how goroutines and channels are used. The WaitGroup must wrap the goroutine's channel receive loop.
  - `backend/internal/ports/event_bus.go` — `EventBusPort` interface definition. `WaitPending()` should be added to the interface OR be a method on the concrete `Bus` struct only (prefer struct-only to avoid changing the port interface).

  **API/Type References**:
  - `backend/internal/ports/event_bus.go:EventBusPort` — Do NOT modify this interface. Add WaitPending as a method on the concrete Bus type.

  **Test References**:
  - `backend/internal/adapters/eventbus/memory/` — Check for existing bus tests; add WaitPending test alongside

  **WHY Each Reference Matters**:
  - `bus.go` SubscribeAsync: You need to see exactly where the goroutine spawns and where the channel send happens to place the WaitGroup.Add(1) and Done() correctly. Incorrect placement causes either deadlock (Wait before Done) or race (Done before handler completes).

  **Acceptance Criteria**:
  - [ ] `WaitPending()` method exists on `memory.Bus`
  - [ ] `cd backend && go build ./internal/adapters/eventbus/memory/` compiles
  - [ ] Unit test: async handler completes before WaitPending returns
  - [ ] Unit test: WaitPending with no pending events returns immediately
  - [ ] `cd backend && go test ./internal/adapters/eventbus/memory/` passes

  **QA Scenarios**:
  ```
  Scenario: WaitPending drains async handlers
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/adapters/eventbus/memory/ -run TestWaitPending -v
      2. Verify output contains "PASS"
    Expected Result: Test passes — WaitPending blocks until async handler completes
    Failure Indicators: "FAIL" in output, deadlock timeout, race detector warning
    Evidence: .sisyphus/evidence/task-1-waitpending-test.txt

  Scenario: No regression on existing bus behavior
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/adapters/eventbus/memory/ -v -race
      2. Verify all tests pass including existing ones
    Expected Result: All tests pass with race detector enabled
    Failure Indicators: "FAIL", "DATA RACE" in output
    Evidence: .sisyphus/evidence/task-1-bus-regression.txt
  ```

  **Commit**: YES
  - Message: `feat(eventbus): add WaitPending method for backtest sync`
  - Files: `backend/internal/adapters/eventbus/memory/bus.go`, `backend/internal/adapters/eventbus/memory/bus_test.go`
  - Pre-commit: `cd backend && go test ./internal/adapters/eventbus/memory/ -race`

---

- [ ] 2. PriceCache Clock Injection

  **What to do**:
  - Add `WithClock(fn func() time.Time)` functional option to `positionmonitor.NewPriceCache()`
  - Replace hardcoded `time.Now()` at `price_cache.go:44` (`ObservedAt: time.Now()`) with `pc.clock()` using the injected function
  - Default clock is `time.Now` when option not provided (backward compatible)
  - Add unit test: create PriceCache with mock clock returning fixed time → handle a bar → verify ObservedAt matches mock clock time

  **Must NOT do**:
  - Do NOT change the PriceCache's event subscription pattern
  - Do NOT change the PriceCachePort interface
  - Do NOT change how omo-core creates PriceCache (it will just not pass the option, getting default time.Now)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single file change, clear pattern, <30 lines of code
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go functional options pattern

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3, 4, 5, 6)
  - **Blocks**: Tasks 9, 12
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `backend/internal/app/positionmonitor/service.go:116-118` — Existing `WithNowFunc` option pattern on PositionMonitor Service. Copy this exact pattern for PriceCache's `WithClock`.
  - `backend/internal/app/positionmonitor/price_cache.go:44` — The exact line with `time.Now()` that needs to become `pc.clock()`.

  **API/Type References**:
  - `backend/internal/ports/price_cache.go` — PriceCachePort interface; verify it doesn't constrain the constructor signature.

  **WHY Each Reference Matters**:
  - `service.go:116-118` WithNowFunc: This is the canonical pattern in this package for clock injection — follow it exactly for consistency.
  - `price_cache.go:44`: This is THE specific line causing backtest staleness bugs — PriceCache timestamps bar observations with wall-clock time, but PositionMonitor's staleness check uses injected simulated time, creating mismatches.

  **Acceptance Criteria**:
  - [ ] `WithClock` option exists on PriceCache constructor
  - [ ] `price_cache.go` uses `pc.clock()` instead of `time.Now()`
  - [ ] Default behavior unchanged (uses time.Now when no option)
  - [ ] `cd backend && go test ./internal/app/positionmonitor/ -run TestPriceCache` passes

  **QA Scenarios**:
  ```
  Scenario: Clock injection changes ObservedAt timestamp
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/positionmonitor/ -run TestPriceCacheClock -v
      2. Verify test output shows mock time used for ObservedAt
    Expected Result: PriceCache uses injected clock for ObservedAt, not time.Now
    Failure Indicators: "FAIL" or ObservedAt showing current wall-clock time
    Evidence: .sisyphus/evidence/task-2-pricecache-clock.txt

  Scenario: Default PriceCache still uses time.Now
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/positionmonitor/ -v -race
      2. Verify all existing tests pass (no regression)
    Expected Result: All positionmonitor tests pass
    Failure Indicators: "FAIL" in output
    Evidence: .sisyphus/evidence/task-2-pricecache-regression.txt
  ```

  **Commit**: YES
  - Message: `feat(positionmonitor): add clock injection to PriceCache`
  - Files: `backend/internal/app/positionmonitor/price_cache.go`, `backend/internal/app/positionmonitor/price_cache_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/positionmonitor/ -race`

---

- [ ] 3. PositionMonitor Backtest Mode Support

  **What to do**:
  - Add public `EvalExitRules(barTime time.Time)` method to PositionMonitor Service that synchronously evaluates exit rules for all active positions using the provided barTime — this is what the replay loop calls after each bar group
  - Add `WithDisableTickLoop()` functional option that prevents `runTickLoop()` goroutine from starting in `Start()` — backtest mode calls `EvalExitRules` directly instead
  - Add `WithDisableReconcile()` functional option that prevents the reconciliation ticker from running — SimBroker reconciliation is meaningless in backtest
  - Inject the barTime into `nowFunc` context for the duration of `EvalExitRules` so all time-based evaluators (MaxHoldingTime, EODFlatten, TimeExit, StagnationExit) use simulated time
  - Fix `exitPendingTimeout` in `exit_eval.go` to use `nowFunc()` instead of wall-clock for the 10-second retry — in backtest, "10 seconds" means 10 simulated seconds
  - Unit test: create PositionMonitor with DisableTickLoop + mock clock → add position → call EvalExitRules with a time past EODFlatten → verify exit triggered

  **Must NOT do**:
  - Do NOT modify `runTickLoop()` logic — omo-core uses it as-is
  - Do NOT change how exit rules are evaluated — only change WHEN they are called
  - Do NOT change the event subscription pattern
  - Do NOT change existing method signatures

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Safety-critical component (exit rule evaluation), multiple files, careful time-injection needed
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go concurrency patterns, time injection, functional options

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 4, 5, 6)
  - **Blocks**: Tasks 9, 12, 16
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `backend/internal/app/positionmonitor/service.go:219` — `runTickLoop()` implementation. Understand exactly what it does on each tick so `EvalExitRules` can replicate the same logic path.
  - `backend/internal/app/positionmonitor/service.go:116-118` — `WithNowFunc` option pattern. Follow this for `WithDisableTickLoop` and `WithDisableReconcile`.
  - `backend/internal/app/positionmonitor/exit_eval.go` — Full exit evaluation logic. `EvalExitRules` should call the same code path as the tick loop but with the barTime set as the current time.

  **API/Type References**:
  - `backend/internal/app/positionmonitor/evaluators.go` — All exit rule evaluators (TrailingStop, ProfitTarget, EODFlatten, MaxHoldingTime, etc.). Each uses `nowFunc()` — verify they all go through the injectable clock.
  - `backend/internal/app/positionmonitor/exit_eval.go:23` — `exitPendingTimeout` (10 seconds). This comparison must use `nowFunc()` not wall-clock.

  **WHY Each Reference Matters**:
  - `service.go:219` runTickLoop: You need to understand the exact sequence: lock positions → for each position → evaluate exit rules → trigger if needed. EvalExitRules must follow this same sequence.
  - `exit_eval.go:23` exitPendingTimeout: In max-speed backtest, wall-clock 10 seconds means the entire backtest completes before any exit retries. This must use simulated time to allow proper retry behavior within the simulated timeline.

  **Acceptance Criteria**:
  - [ ] `EvalExitRules(barTime time.Time)` method exists and is public
  - [ ] `WithDisableTickLoop()` option exists and prevents tick goroutine
  - [ ] `WithDisableReconcile()` option exists and prevents reconcile ticker
  - [ ] Exit evaluators use `nowFunc()` consistently (no remaining `time.Now()`)
  - [ ] `cd backend && go test ./internal/app/positionmonitor/ -race` passes

  **QA Scenarios**:
  ```
  Scenario: EvalExitRules triggers EODFlatten at simulated time
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/positionmonitor/ -run TestEvalExitRules_EODFlatten -v
      2. Verify test creates position, calls EvalExitRules with 15:56 ET, exit triggers
    Expected Result: EODFlatten exit triggered at simulated 15:56, not wall-clock
    Failure Indicators: "FAIL", no exit event emitted
    Evidence: .sisyphus/evidence/task-3-evalexitrules-eodflatten.txt

  Scenario: DisableTickLoop prevents goroutine start
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/positionmonitor/ -run TestDisableTickLoop -v
      2. Verify PositionMonitor starts without tick goroutine
    Expected Result: No tick loop running, EvalExitRules is the only evaluation path
    Failure Indicators: Tick loop fires despite WithDisableTickLoop
    Evidence: .sisyphus/evidence/task-3-disable-tickloop.txt

  Scenario: No regression on existing PositionMonitor behavior
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/positionmonitor/ -v -race
    Expected Result: All existing tests pass unchanged
    Failure Indicators: "FAIL" or "DATA RACE"
    Evidence: .sisyphus/evidence/task-3-posmon-regression.txt
  ```

  **Commit**: YES
  - Message: `feat(positionmonitor): add backtest mode support with EvalExitRules and disable options`
  - Files: `backend/internal/app/positionmonitor/service.go`, `backend/internal/app/positionmonitor/exit_eval.go`, `backend/internal/app/positionmonitor/service_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/positionmonitor/ -race`

- [ ] 4. SimBroker Port Expansion (QuoteProvider + AccountPort)

  **What to do**:
  - Add `GetQuote(ctx context.Context, symbol domain.Symbol) (*Quote, error)` method to SimBroker satisfying the `execution.QuoteProvider` interface — return bid=close-slippage/2, ask=close+slippage/2 using existing slippageBPS config
  - Add `GetAccountBuyingPower(ctx context.Context) (float64, error)` method satisfying `ports.AccountPort` — track virtual equity as `initialEquity` field, compute buying power as `initialEquity - sum(open position costs)`
  - Add `GetAccountEquity(ctx context.Context) (float64, error)` method — return `initialEquity + unrealized PnL` computed from positions and current prices
  - Add `initialEquity float64` field to `Config` struct, set in constructor
  - Update position tracking to maintain cost basis for equity calculations
  - Add compile-time interface assertion tests: `var _ execution.QuoteProvider = (*Broker)(nil)`, `var _ ports.AccountPort = (*Broker)(nil)`

  **Must NOT do**:
  - Do NOT add realistic spread simulation — bid/ask derived from close±slippage is sufficient
  - Do NOT change the fill model (instant fill at close ± slippage)
  - Do NOT change existing method signatures on Broker
  - Do NOT add order book, volume-aware fills, or partial fill support

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Multiple methods to add with careful equity tracking math, but follows clear patterns
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go interface implementation, trading domain equity calculation

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 5, 6)
  - **Blocks**: Tasks 7, 12, 18
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/simbroker/broker.go` — Full current implementation. See `GetPrice()` (line 283-288) for the price lookup pattern to follow. See `position` struct (line 34-39) for how positions are tracked — extend this for cost basis.
  - `backend/cmd/omo-replay/main.go:736-746` — Existing `simQuoteProvider` wrapper that returns bid=ask=close. This code becomes unnecessary once SimBroker implements QuoteProvider directly — but keep it as reference for the expected Quote struct shape.

  **API/Type References**:
  - `backend/internal/app/execution/slippage.go` — `QuoteProvider` interface definition and `Quote` struct. SimBroker must return this exact struct.
  - `backend/internal/ports/account.go` — `AccountPort` interface with `GetAccountBuyingPower` and `GetAccountEquity` method signatures.
  - `backend/internal/adapters/simbroker/broker.go:18-21` — Current `Config` struct. Add `InitialEquity float64` field.

  **WHY Each Reference Matters**:
  - `slippage.go` QuoteProvider: The exact interface signature and Quote struct shape that SimBroker must match. SpreadGuard and SlippageGuard both call `GetQuote`.
  - `account.go` AccountPort: BuyingPowerGuard calls `GetAccountBuyingPower`. The Alpaca adapter returns DTBP; SimBroker should return a computed value based on virtual equity minus open positions.
  - `main.go:736-746` simQuoteProvider: Shows the bid=ask=close pattern currently used. Your implementation improves on this by returning bid=close-spread/2, ask=close+spread/2.

  **Acceptance Criteria**:
  - [ ] SimBroker implements `execution.QuoteProvider` (compile-time assertion)
  - [ ] SimBroker implements `ports.AccountPort` (compile-time assertion)
  - [ ] `GetQuote` returns bid < close < ask with spread derived from slippageBPS
  - [ ] `GetAccountBuyingPower` returns initialEquity minus open position costs
  - [ ] `GetAccountEquity` returns initialEquity plus unrealized PnL
  - [ ] `cd backend && go test ./internal/adapters/simbroker/ -race` passes

  **QA Scenarios**:
  ```
  Scenario: GetQuote returns synthetic bid/ask spread
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/adapters/simbroker/ -run TestGetQuote -v
      2. Verify: UpdatePrice(SPY, 100.0) → GetQuote returns bid=99.975, ask=100.025 (5bps spread)
    Expected Result: Bid and ask symmetrically offset from close by slippageBPS/2
    Failure Indicators: bid==ask, or spread doesn't match BPS config
    Evidence: .sisyphus/evidence/task-4-getquote.txt

  Scenario: GetAccountBuyingPower tracks virtual equity
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/adapters/simbroker/ -run TestBuyingPower -v
      2. Verify: initial=100000 → buy 100 shares at $50 → buying power = 95000
    Expected Result: Buying power decreases by position cost, increases on sell
    Failure Indicators: Buying power doesn't change after fills
    Evidence: .sisyphus/evidence/task-4-buying-power.txt

  Scenario: Interface compliance compiles
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/adapters/simbroker/ -run TestInterfaceCompliance -v
    Expected Result: Compile-time assertions pass
    Failure Indicators: Compilation error showing missing methods
    Evidence: .sisyphus/evidence/task-4-interface-compliance.txt
  ```

  **Commit**: YES
  - Message: `feat(simbroker): implement QuoteProvider and AccountPort for full guard compatibility`
  - Files: `backend/internal/adapters/simbroker/broker.go`, `backend/internal/adapters/simbroker/broker_test.go`
  - Pre-commit: `cd backend && go test ./internal/adapters/simbroker/ -race`

---

- [ ] 5. No-op Adapter Consolidation

  **What to do**:
  - Create `backend/internal/adapters/noop/` package with shared no-op implementations currently duplicated in omo-replay's `main.go`
  - Move `noopRepo` (implements RepositoryPort) and `noopPnLRepo` (implements PnLPort) from omo-replay inline structs to shared package
  - Add `noopDNAApprovalRepo` for backtest use
  - Each no-op struct should be exported (`NoopRepo`, `NoopPnLRepo`, `NoopDNAApprovalRepo`) with proper doc comments
  - These are used by the bootstrap package when building backtest pipelines

  **Must NOT do**:
  - Do NOT add complex behavior — these are pure no-ops returning nil/empty
  - Do NOT change how omo-replay currently functions (it will later import these)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Simple struct extraction, no logic, just moving code to shared package
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 4, 6)
  - **Blocks**: Tasks 10, 12
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-replay/main.go:732-770` (approximate) — Current inline `noopRepo` and `noopPnLRepo` struct definitions. Extract these verbatim, then export.

  **API/Type References**:
  - `backend/internal/ports/repository.go` — `RepositoryPort` interface that `NoopRepo` must satisfy
  - `backend/internal/ports/pnl.go` — `PnLPort` interface that `NoopPnLRepo` must satisfy

  **WHY Each Reference Matters**:
  - `main.go` inline structs: The exact no-op implementations to extract. Don't reinvent — move and export.
  - Port interfaces: Ensure the no-ops satisfy the full interface (add any missing stub methods).

  **Acceptance Criteria**:
  - [ ] `backend/internal/adapters/noop/` package exists with NoopRepo, NoopPnLRepo
  - [ ] Each satisfies respective port interface (compile-time assertion)
  - [ ] `cd backend && go build ./internal/adapters/noop/` compiles

  **QA Scenarios**:
  ```
  Scenario: No-op package compiles and satisfies interfaces
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/adapters/noop/ -v
    Expected Result: Package builds, interface assertions pass
    Failure Indicators: Compilation error
    Evidence: .sisyphus/evidence/task-5-noop-compile.txt
  ```

  **Commit**: YES
  - Message: `refactor(adapters): extract shared no-op adapters to noop package`
  - Files: `backend/internal/adapters/noop/repo.go`, `backend/internal/adapters/noop/pnl.go`, `backend/internal/adapters/noop/noop_test.go`
  - Pre-commit: `cd backend && go build ./internal/adapters/noop/`

---

- [ ] 6. TradingWindowGuard Clock Parameter

  **What to do**:
  - Verify that `TradingWindowGuard` uses an injectable time function rather than `time.Now()` — check the constructor and `Check()` method
  - If it currently uses `time.Now()` directly, add a `nowFunc func() time.Time` field with a `WithNowFunc` option, defaulting to `time.Now`
  - If it already accepts a time function, document this finding and mark task as no-op
  - The guard reads trading hours from `intent.Meta["trading_hours"]` and checks if current time is within the window — "current time" must be the simulated bar time in backtest
  - Unit test: create guard with mock clock set to outside trading window → verify order rejected

  **Must NOT do**:
  - Do NOT change the guard's logic for determining trading windows
  - Do NOT change how intent.Meta is parsed

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Likely a single-file verification or small change, follows established pattern
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go option pattern, time injection

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 4, 5)
  - **Blocks**: Tasks 7, 12
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `backend/internal/app/execution/trading_window_guard.go` — Full implementation. Check constructor and Check() method for `time.Now()` usage.
  - `backend/internal/app/execution/killswitch.go` — KillSwitch already takes `now func() time.Time` in constructor (line 241 in omo-core services.go). Follow this pattern if TradingWindowGuard doesn't have it.

  **API/Type References**:
  - `backend/internal/app/execution/trading_window_guard_test.go` — Existing tests. Check if they use a mock clock.

  **WHY Each Reference Matters**:
  - `trading_window_guard.go`: Need to see if it already has clock injection. If it calls `time.Now()` directly in Check(), we need to add injection. If it already accepts a func, this task is just verification.
  - `killswitch.go`: Shows the exact pattern to follow — constructor takes `now func() time.Time`.

  **Acceptance Criteria**:
  - [ ] TradingWindowGuard uses injectable clock (verified or added)
  - [ ] Unit test confirms guard respects injected time
  - [ ] `cd backend && go test ./internal/app/execution/ -run TestTradingWindow -race` passes

  **QA Scenarios**:
  ```
  Scenario: TradingWindowGuard uses injected clock
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/execution/ -run TestTradingWindow -v
      2. Verify test uses mock clock to test window boundaries
    Expected Result: Guard rejects orders outside window based on injected time, not wall-clock
    Failure Indicators: "FAIL" or test uses real time.Now
    Evidence: .sisyphus/evidence/task-6-trading-window-clock.txt
  ```

  **Commit**: YES (if changes needed) | NO (if verification only)
  - Message: `feat(execution): add clock injection to TradingWindowGuard`
  - Files: `backend/internal/app/execution/trading_window_guard.go`, `backend/internal/app/execution/trading_window_guard_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/execution/ -race`

- [ ] 7. Bootstrap Package: Scaffold + Execution Guard Builder

  **What to do**:
  - Create `backend/internal/app/bootstrap/` package with shared types and execution guard builder
  - Define `ExecutionDeps` struct containing all dependencies needed to build the execution service:
    ```
    EventBus, Broker (BrokerPort), Repo (RepositoryPort), QuoteProvider, AccountPort (nil = skip BuyingPowerGuard),
    Clock (func() time.Time), Config (*config.Config), InitialEquity float64, IsBacktest bool, Logger
    ```
  - Implement `BuildExecutionService(deps ExecutionDeps) (*execution.Service, *execution.PositionGate, error)` that:
    - Creates RiskEngine with `cfg.Trading.MaxRiskPercent` (config-driven, NOT hardcoded 0.02)
    - Creates SlippageGuard with QuoteProvider
    - Creates KillSwitch with config params AND the injected Clock
    - Creates DailyLossBreaker with config params AND the injected Clock
    - Creates PositionGate with Broker
    - Builds guard chain: PositionGate → ExposureGuard → SpreadGuard → TradingWindowGuard → optional BuyingPowerGuard
    - Returns the assembled execution.Service and PositionGate (needed by PositionMonitor)
  - The LedgerWriter creation should be part of this builder since DailyLossBreaker depends on it
  - Unit test: build execution service with mock deps, verify all guards are wired

  **Must NOT do**:
  - Do NOT modify any execution guard constructors
  - Do NOT change guard chain order (must match omo-core's initCoreServices)
  - Do NOT add backtest-specific guard behavior — same guards, different adapters

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Core architectural extraction — must perfectly match omo-core's guard chain, config handling
  - **Skills**: [`senior-backend`, `senior-architect`]
    - `senior-backend`: Go package design, dependency injection
    - `senior-architect`: Hexagonal architecture, shared initialization patterns

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 8, 9, 10)
  - **Blocks**: Tasks 11, 12
  - **Blocked By**: Tasks 1, 4, 6

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/services.go:79-216` — `initCoreServices()` function. This is the CANONICAL source for guard chain assembly. The builder must produce the IDENTICAL chain. Read lines 130-175 for the exact guard construction order and optional flags.
  - `backend/cmd/omo-replay/main.go:226-270` — Current omo-replay execution setup (what we're replacing). Compare against omo-core to see the drift.

  **API/Type References**:
  - `backend/internal/app/execution/service.go` — `NewService()` constructor signature and `With*` option functions. The builder must call these in the right order.
  - `backend/internal/app/execution/exposure_guard.go` — `NewExposureGuard(broker BrokerPort, equity float64, log)` — needs broker and equity
  - `backend/internal/app/execution/spread_guard.go` — `NewSpreadGuard(provider QuoteProvider, log)` — needs QuoteProvider
  - `backend/internal/app/execution/buying_power_guard.go` — `NewBuyingPowerGuard(account AccountPort, log)` — needs AccountPort (nil-check to skip)
  - `backend/internal/app/risk/daily_loss_breaker.go` — Constructor takes maxPct, maxUSD, ledgerWriter, nowFunc, logger

  **WHY Each Reference Matters**:
  - `services.go:130-175`: The EXACT order of guard chain assembly. If the builder produces a different order, guard behavior changes. This is the specification.
  - `execution/service.go` NewService: Need the exact constructor signature to know what parameters the builder must provide.

  **Acceptance Criteria**:
  - [ ] `backend/internal/app/bootstrap/` package exists
  - [ ] `ExecutionDeps` struct defined with all required fields
  - [ ] `BuildExecutionService` produces identical guard chain to omo-core's initCoreServices
  - [ ] Config-driven risk params (no hardcoded values)
  - [ ] Clock injection flows to KillSwitch, DailyLossBreaker, TradingWindowGuard
  - [ ] BuyingPowerGuard skipped when AccountPort is nil
  - [ ] `cd backend && go test ./internal/app/bootstrap/ -race` passes

  **QA Scenarios**:
  ```
  Scenario: Builder creates full guard chain with all guards
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/bootstrap/ -run TestBuildExecutionService_FullChain -v
      2. Verify test creates execution service with all guards wired
    Expected Result: Service builds with PositionGate, ExposureGuard, SpreadGuard, TradingWindowGuard, BuyingPowerGuard
    Failure Indicators: "FAIL", missing guard in chain
    Evidence: .sisyphus/evidence/task-7-exec-builder-full.txt

  Scenario: Builder skips BuyingPowerGuard when AccountPort is nil
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/bootstrap/ -run TestBuildExecutionService_NilAccount -v
    Expected Result: Service builds without BuyingPowerGuard, no panic
    Failure Indicators: nil pointer panic, "FAIL"
    Evidence: .sisyphus/evidence/task-7-exec-builder-nil-account.txt
  ```

  **Commit**: YES
  - Message: `feat(bootstrap): add shared execution guard builder package`
  - Files: `backend/internal/app/bootstrap/types.go`, `backend/internal/app/bootstrap/execution.go`, `backend/internal/app/bootstrap/execution_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/bootstrap/ -race`

---

- [ ] 8. Bootstrap Package: Strategy V2 Pipeline Builder

  **What to do**:
  - Add `BuildStrategyPipeline(deps StrategyDeps) (*StrategyPipeline, error)` to the bootstrap package
  - `StrategyDeps` struct: EventBus, SpecStore, AIAdvisor (ports.AIAdvisorPort), PositionLookupFn, MarketDataFn, Repo (optional), TenantID, EnvMode, Equity, Clock, Logger, DisableEnricher (bool)
  - `StrategyPipeline` return struct: Runner, Router, Enricher (nil if disabled), RiskSizer, LifecycleSvc
  - Implementation must:
    - Load ALL specs from SpecStore via `List()`
    - Register ALL builtins: `NewORBStrategy()`, `NewAVWAPStrategy()`, `NewAIScalperStrategy()`, `NewBreakRetestStrategy()`
    - Create Router with per-spec, per-symbol instances using hook-based routing (`spec.Hooks["signals"]` → registry lookup)
    - Set AllowedDirections from spec.Routing on each instance
    - Create Runner with SetPositionLookup
    - Create SignalDebateEnricher (or nil when DisableEnricher=true — the `--no-ai` path)
    - Create RiskSizer with config-driven params from SpecStore
  - Unit test: build pipeline with mock SpecStore returning 2 specs → verify router has instances for both

  **Must NOT do**:
  - Do NOT modify strategy runner, router, enricher, or risk sizer constructors
  - Do NOT add new builtins
  - Do NOT change hook routing logic
  - Do NOT add multi-account support (single tenant only)

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Complex multi-component wiring with hook routing, instance registration, spec iteration
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go package design, strategy pattern, registry pattern

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 7, 9, 10)
  - **Blocks**: Tasks 11, 12
  - **Blocked By**: None (strategy components have no Wave 1 dependencies)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/services.go:238-340` — `initStrategyPipeline()` V2 block. This is the CANONICAL source for strategy pipeline assembly. The builder must replicate this logic exactly.
  - `backend/internal/app/strategy/runner.go` — Runner constructor, SetPositionLookup method, how it processes bars
  - `backend/internal/app/strategy/router.go` — Router instance registration, InstancesForSymbol routing
  - `backend/internal/app/strategy/signal_debate_enricher.go` — Enricher constructor, options (WithRepository, WithMarketDataProvider, WithPositionLookup, WithDebateTimeout)

  **API/Type References**:
  - `backend/internal/domain/strategy_spec.go` (or similar) — StrategySpec struct, Hooks map, Routing struct with AllowedDirections
  - `backend/internal/ports/strategy/store.go` — SpecStore interface with List() method
  - `backend/internal/ports/strategy/registry.go` — Registry interface
  - `backend/internal/app/strategy/builtin/` — Builtin strategy constructors

  **WHY Each Reference Matters**:
  - `services.go:238-340`: The EXACT wiring sequence: load specs → register builtins → create router → iterate specs/symbols → create instances → register with router → create runner → set position lookup → create enricher → create risk sizer. Must follow this order.
  - `router.go`: Understanding how instances are registered and how the router resolves symbol→instance mapping is critical for correct wiring.

  **Acceptance Criteria**:
  - [ ] `BuildStrategyPipeline` loads all specs and registers all 4 builtins
  - [ ] Hook-based routing maps spec.Hooks["signals"] to builtin implementation
  - [ ] AllowedDirections set on each instance from spec.Routing
  - [ ] Runner.SetPositionLookup called with provided function
  - [ ] Enricher is nil when DisableEnricher=true
  - [ ] `cd backend && go test ./internal/app/bootstrap/ -run TestBuildStrategy -race` passes

  **QA Scenarios**:
  ```
  Scenario: Multi-strategy pipeline builds with all builtins
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/bootstrap/ -run TestBuildStrategyPipeline -v
      2. Verify output shows 4 builtins registered, specs loaded, instances created
    Expected Result: Router has instances for all spec/symbol combinations
    Failure Indicators: "FAIL", missing builtin registration
    Evidence: .sisyphus/evidence/task-8-strategy-builder.txt

  Scenario: Enricher disabled with DisableEnricher flag
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/bootstrap/ -run TestBuildStrategyPipeline_NoAI -v
    Expected Result: Pipeline builds with Enricher=nil, no AI advisor called
    Failure Indicators: Enricher is non-nil, AI advisor instantiated
    Evidence: .sisyphus/evidence/task-8-strategy-noai.txt
  ```

  **Commit**: YES
  - Message: `feat(bootstrap): add shared strategy V2 pipeline builder`
  - Files: `backend/internal/app/bootstrap/strategy.go`, `backend/internal/app/bootstrap/strategy_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/bootstrap/ -race`

---

- [ ] 9. Bootstrap Package: PositionMonitor + PriceCache Builder

  **What to do**:
  - Add `BuildPositionMonitor(deps PosMonitorDeps) (*PosMonitorBundle, error)` to bootstrap package
  - `PosMonitorDeps` struct: EventBus, PositionGate, Broker (optional), Repo (optional), SpecStore (optional), SnapshotFn (optional), TenantID, EnvMode, Clock, IsBacktest, Logger
  - `PosMonitorBundle` return struct: PriceCache, Service, Revaluator (nil if backtest or no AI)
  - Implementation must:
    - Create PriceCache with `WithClock(deps.Clock)` (from Task 2)
    - Create PositionMonitor Service with proper options:
      - If IsBacktest: `WithDisableTickLoop()`, `WithDisableReconcile()`, `WithNowFunc(deps.Clock)` (from Task 3)
      - If not backtest: standard options (same as omo-core current behavior)
    - Optionally create Revaluator (only for live mode with AI enabled)
  - Unit test: build for backtest mode → verify tick loop disabled, clock injected

  **Must NOT do**:
  - Do NOT create Revaluator for backtest mode (AI revaluation is deferred for backtest)
  - Do NOT change PositionMonitor or PriceCache constructors

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Moderate complexity, clear pattern from omo-core, conditional logic for backtest mode
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go dependency injection, conditional wiring

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 7, 8, 10)
  - **Blocks**: Tasks 11, 12
  - **Blocked By**: Tasks 2 (PriceCache clock), 3 (PositionMonitor backtest mode)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/services.go:180-215` — PriceCache + PositionMonitor + Revaluator initialization in omo-core. Follow this for the live mode path.
  - `backend/internal/app/positionmonitor/service.go` — Service constructor with functional options

  **API/Type References**:
  - `backend/internal/app/positionmonitor/price_cache.go` — PriceCache constructor (with new WithClock from Task 2)
  - `backend/internal/app/positionmonitor/revaluator.go` — Revaluator constructor signature

  **WHY Each Reference Matters**:
  - `services.go:180-215`: The canonical PriceCache → PositionMonitor → Revaluator wiring order. Must replicate for live mode, simplify for backtest.

  **Acceptance Criteria**:
  - [ ] `BuildPositionMonitor` exists and builds PriceCache + Service
  - [ ] Backtest mode: tick loop disabled, reconcile disabled, clock injected
  - [ ] Live mode: standard behavior matching omo-core
  - [ ] `cd backend && go test ./internal/app/bootstrap/ -run TestBuildPosMonitor -race` passes

  **QA Scenarios**:
  ```
  Scenario: Backtest mode disables tick loop and reconciliation
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/bootstrap/ -run TestBuildPosMonitor_Backtest -v
    Expected Result: PositionMonitor created with DisableTickLoop and DisableReconcile
    Failure Indicators: Tick loop starts, reconciliation active
    Evidence: .sisyphus/evidence/task-9-posmon-backtest.txt
  ```

  **Commit**: YES
  - Message: `feat(bootstrap): add shared PositionMonitor and PriceCache builder`
  - Files: `backend/internal/app/bootstrap/posmon.go`, `backend/internal/app/bootstrap/posmon_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/bootstrap/ -race`

---

- [ ] 10. Bootstrap Package: Ingestion + Monitor + Perf Builder

  **What to do**:
  - Add `BuildIngestion(deps IngestionDeps) (*IngestionBundle, error)` — creates ingestion.Service with AdaptiveFilter, optionally AsyncBarWriter (live only)
  - Add `BuildMonitor(deps MonitorDeps) (*monitor.Service, error)` — creates monitor.Service
  - Add `BuildPerfServices(deps PerfDeps) (*PerfBundle, error)` — creates LedgerWriter + SignalTracker
  - `PerfDeps` includes: EventBus, PnLRepo, Broker (or nil for equity fetch), Repo (or nil), Equity, IsBacktest, Logger
  - For backtest mode: use NoopPnLRepo (from Task 5), skip async bar writer, LedgerWriter uses SimBroker for equity
  - For live mode: identical to current omo-core behavior

  **Must NOT do**:
  - Do NOT modify ingestion, monitor, or perf service constructors
  - Do NOT change adaptive filter parameters (20, 4.0)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Multiple related builders, straightforward extraction
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go package design

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 7, 8, 9)
  - **Blocks**: Tasks 11, 12
  - **Blocked By**: Task 5 (No-op adapters)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/services.go:80-100` — Ingestion + BarWriter init
  - `backend/cmd/omo-core/services.go:100-115` — Monitor init
  - `backend/cmd/omo-core/services.go:120-128` — LedgerWriter + SignalTracker init

  **API/Type References**:
  - `backend/internal/app/ingestion/service.go` — Ingestion Service constructor
  - `backend/internal/app/monitor/service.go` — Monitor Service constructor
  - `backend/internal/app/perf/ledger_writer.go` — LedgerWriter constructor
  - `backend/internal/app/perf/signal_tracker.go` — SignalTracker constructor

  **WHY Each Reference Matters**:
  - `services.go` sections: Exact constructor calls to replicate. The builder functions are thin wrappers around these calls with conditional logic for backtest vs live.

  **Acceptance Criteria**:
  - [ ] `BuildIngestion`, `BuildMonitor`, `BuildPerfServices` all exist
  - [ ] Backtest mode uses NoopPnLRepo, no AsyncBarWriter
  - [ ] `cd backend && go test ./internal/app/bootstrap/ -race` passes

  **QA Scenarios**:
  ```
  Scenario: Ingestion builder creates service with adaptive filter
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/app/bootstrap/ -run TestBuildIngestion -v
    Expected Result: Ingestion service created with filter params (20, 4.0)
    Failure Indicators: "FAIL"
    Evidence: .sisyphus/evidence/task-10-ingestion-builder.txt
  ```

  **Commit**: YES
  - Message: `feat(bootstrap): add shared ingestion, monitor, and perf builders`
  - Files: `backend/internal/app/bootstrap/ingestion.go`, `backend/internal/app/bootstrap/perf.go`, `backend/internal/app/bootstrap/ingestion_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/bootstrap/ -race`

- [ ] 11. Refactor omo-core to Use Bootstrap Package

  **What to do**:
  - Refactor `backend/cmd/omo-core/services.go` to call the shared bootstrap builders instead of inline service construction
  - Replace execution guard assembly in `initCoreServices()` with `bootstrap.BuildExecutionService(deps)`
  - Replace strategy pipeline assembly in `initStrategyPipeline()` with `bootstrap.BuildStrategyPipeline(deps)`
  - Replace PositionMonitor init with `bootstrap.BuildPositionMonitor(deps)`
  - Replace ingestion/monitor/perf init with respective bootstrap builders
  - Pass `IsBacktest: false` to all builders so live behavior is identical
  - Pass `time.Now` as Clock, real Alpaca adapter as Broker/AccountPort/QuoteProvider
  - **CRITICAL**: After refactoring, omo-core MUST produce bit-for-bit identical behavior:
    - Same guard chain order
    - Same constructor arguments
    - Same service start order
    - All existing tests pass
  - Run full test suite AND manual build verification

  **Must NOT do**:
  - Do NOT change omo-core's runtime behavior in any way
  - Do NOT remove the multi-account/orchestrator code (it stays in services.go, not in bootstrap)
  - Do NOT change CLI flags or configuration
  - Do NOT change the HTTP server setup (http.go)
  - Do NOT change warmup or streaming (warmup.go) — those are omo-core specific

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Highest-risk task — must preserve live trading binary behavior exactly while swapping internals
  - **Skills**: [`senior-backend`, `senior-architect`]
    - `senior-backend`: Go refactoring, dependency wiring
    - `senior-architect`: Hexagonal architecture, safe refactoring patterns

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (Sequential — T11 before T12)
  - **Blocks**: Tasks 12, 19
  - **Blocked By**: Tasks 7, 8, 9, 10 (all bootstrap builders)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/services.go` — ENTIRE file. This is what you're refactoring. Read it fully to understand every function and what it constructs.
  - `backend/cmd/omo-core/infra.go` — Infrastructure deps struct that services.go uses. The bootstrap deps structs must be populated from this.
  - `backend/cmd/omo-core/main.go` — Initialization sequence calling infra → services → strategy → multi-account. The call order must not change.

  **WHY Each Reference Matters**:
  - `services.go`: THE file being modified. Every line matters. Compare before/after to ensure identical behavior.
  - `infra.go`: Provides the real adapters (Alpaca, DB) that populate bootstrap deps. Must map correctly.

  **Acceptance Criteria**:
  - [ ] omo-core services.go uses bootstrap builders for execution, strategy, posmon, ingestion, perf
  - [ ] Multi-account orchestrator code remains in services.go (NOT moved to bootstrap)
  - [ ] `cd backend && go build -o bin/omo-core ./cmd/omo-core` compiles
  - [ ] `cd backend && go test ./...` — ALL tests pass
  - [ ] `cd backend && go vet ./cmd/omo-core/` — no issues

  **QA Scenarios**:
  ```
  Scenario: omo-core builds and all tests pass after refactor
    Tool: Bash
    Preconditions: All Wave 1-2 tasks committed
    Steps:
      1. cd backend && go build -o bin/omo-core ./cmd/omo-core
      2. cd backend && go test ./... 2>&1 | tail -20
      3. cd backend && go vet ./cmd/omo-core/
    Expected Result: Build succeeds, all tests pass, no vet issues
    Failure Indicators: Compilation error, test failure, vet warning
    Evidence: .sisyphus/evidence/task-11-omocore-build.txt

  Scenario: omo-core starts without runtime errors
    Tool: Bash
    Preconditions: Infrastructure running (DB, etc.)
    Steps:
      1. cd backend && timeout 10 go run ./cmd/omo-core/ 2>&1 || true
      2. Grep output for "FATAL" or "panic"
    Expected Result: No fatal errors or panics in first 10 seconds of startup
    Failure Indicators: "panic:", "FATAL", nil pointer dereference
    Evidence: .sisyphus/evidence/task-11-omocore-startup.txt
  ```

  **Commit**: YES
  - Message: `refactor(omo-core): use shared bootstrap package for service initialization`
  - Files: `backend/cmd/omo-core/services.go`
  - Pre-commit: `cd backend && go build -o bin/omo-core ./cmd/omo-core && go test ./...`

---

- [ ] 12. Refactor omo-replay to Use Bootstrap + Wire Full Pipeline

  **What to do**:
  - Major refactor of `backend/cmd/omo-replay/main.go` to use the shared bootstrap package for ALL service initialization
  - Replace hardcoded single-strategy with `bootstrap.BuildStrategyPipeline()`:
    - Remove hardcoded `configs/strategies/orb_break_retest.toml` path
    - Remove single `builtin.NewORBStrategy()` registration
    - Use SpecStore to load ALL specs, register ALL builtins via hook routing
  - Replace limited execution setup with `bootstrap.BuildExecutionService()`:
    - Wire ALL guards (ExposureGuard, SpreadGuard, TradingWindowGuard, BuyingPowerGuard)
    - Use config-driven `cfg.Trading.MaxRiskPercent` instead of hardcoded `0.02`
  - Add PositionMonitor via `bootstrap.BuildPositionMonitor()`:
    - Backtest mode: DisableTickLoop, DisableReconcile, injected clock
  - Add SignalTracker via `bootstrap.BuildPerfServices()`
  - Wire `Runner.SetPositionLookup(posMonitor.LookupPosition)` through the strategy builder
  - Replace inline no-op structs with `noop.NoopRepo`, `noop.NoopPnLRepo` (from Task 5)
  - Remove `simQuoteProvider` wrapper (SimBroker now implements QuoteProvider directly from Task 4)
  - Add `--no-ai` CLI flag (default: true for backtest mode). When true, pass `DisableEnricher: true` to strategy builder
  - Pass SimBroker as both BrokerPort and QuoteProvider and AccountPort to bootstrap deps
  - Create a shared `clockFn` that returns the current replay bar's timestamp — passed to all builders as Clock
  - Keep the bar replay loop structure but add clock updates and event sync hooks (preparation for Tasks 13, 16)
  - Service start order must match omo-core's `startServices`: ingestion → monitor → ledger → signalTracker → execution → priceCache → posMonitor → runner → enricher → riskSizer

  **Must NOT do**:
  - Do NOT change CLI flag semantics for existing flags (--backtest, --from, --to, --symbols, etc.)
  - Do NOT add multi-account support
  - Do NOT modify the bar replay loop logic (speed control, multi-day handling) — only add hooks
  - Do NOT remove `--backtest=false` (replay-only) mode — it must still work without execution pipeline

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Largest single task — complete binary refactor touching ~300 lines, must maintain backward compatibility
  - **Skills**: [`senior-backend`, `senior-architect`]
    - `senior-backend`: Go refactoring, CLI flag handling
    - `senior-architect`: Pipeline architecture, hexagonal pattern

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (Sequential — after T11)
  - **Blocks**: Tasks 13, 14, 15, 16
  - **Blocked By**: Task 11 (omo-core must be verified first)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-replay/main.go` — ENTIRE file (841 lines). This is what you're refactoring. Critical sections: flag parsing (41-64), service init (136-292), service start (320-331), bar replay loop (390-453).
  - `backend/cmd/omo-core/services.go` — The `startServices()` function for correct service start order.
  - `backend/cmd/omo-core/services.go:238-340` — Strategy V2 pipeline wiring (what the bootstrap builder replicates).

  **API/Type References**:
  - `backend/internal/app/bootstrap/*.go` — All bootstrap builders (Tasks 7-10). These are what omo-replay will call.
  - `backend/internal/adapters/simbroker/broker.go` — SimBroker with new QuoteProvider + AccountPort (Task 4).
  - `backend/internal/adapters/noop/*.go` — Shared no-op adapters (Task 5).

  **WHY Each Reference Matters**:
  - `main.go` full file: You need to understand every section to know what to keep, what to replace, and what to add. The replay loop (390-453) stays; the service init (136-292) gets replaced with bootstrap calls.
  - `services.go` startServices: Service start ORDER matters for event subscription sequencing. Must match.

  **Acceptance Criteria**:
  - [ ] `cd backend && go build -o bin/omo-replay ./cmd/omo-replay` compiles
  - [ ] `--backtest` mode creates full pipeline (all guards, multi-strategy, PositionMonitor)
  - [ ] `--no-ai` flag exists, defaults to true in backtest mode
  - [ ] Replay-only mode (no `--backtest`) still works (no execution pipeline)
  - [ ] Service start order matches omo-core
  - [ ] No hardcoded `0.02` risk param remaining
  - [ ] No hardcoded `orb_break_retest.toml` path remaining
  - [ ] `cd backend && go test ./cmd/omo-replay/... -race` passes

  **QA Scenarios**:
  ```
  Scenario: omo-replay --backtest builds and runs with full pipeline
    Tool: Bash
    Preconditions: TimescaleDB running with historical data
    Steps:
      1. cd backend && go build -o bin/omo-replay ./cmd/omo-replay
      2. cd backend && timeout 60 go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --no-ai 2>&1 | grep -E "(strategy v2|guard|position_monitor)" | head -20
    Expected Result: Logs show strategy V2 pipeline, all guards, and PositionMonitor active
    Failure Indicators: Missing components in logs, compilation error, panic
    Evidence: .sisyphus/evidence/task-12-replay-full-pipeline.txt

  Scenario: Replay-only mode unchanged (no --backtest)
    Tool: Bash
    Preconditions: TimescaleDB running
    Steps:
      1. cd backend && timeout 30 go run ./cmd/omo-replay/ --from 2025-06-02 --to 2025-06-02 --symbols SPY 2>&1 | grep "EventSignalCreated" | wc -l
    Expected Result: Signal count > 0, same behavior as before refactor
    Failure Indicators: Zero signals, panic, error
    Evidence: .sisyphus/evidence/task-12-replay-only-mode.txt
  ```

  **Commit**: YES
  - Message: `refactor(omo-replay): wire full pipeline via shared bootstrap package`
  - Files: `backend/cmd/omo-replay/main.go`
  - Pre-commit: `cd backend && go build -o bin/omo-replay ./cmd/omo-replay && go build -o bin/omo-core ./cmd/omo-core`

- [ ] 13. Replay Loop Event Synchronization

  **What to do**:
  - In the bar replay loop (`omo-replay/main.go`), after publishing all bar events for a given timestamp group, call `eventBus.WaitPending()` (from Task 1) to ensure all async handlers (execution, position monitor fills, ledger writer) complete before processing the next bar group
  - This ensures deterministic execution: bar N's full pipeline (signal → enrichment → risk sizing → execution → fill → position update) completes before bar N+1 publishes
  - The call should happen after ALL bars at the same timestamp are published and BEFORE advancing to the next timestamp
  - Also call `posMonitor.EvalExitRules(barTime)` (from Task 3) after `WaitPending()` so exit rules evaluate with all fills processed and current prices updated
  - Update the replay clock function to return the current bar group's timestamp before each bar group publishes
  - Performance consideration: WaitPending adds overhead per bar group — measure impact and document in evidence

  **Must NOT do**:
  - Do NOT add WaitPending to the event bus port interface — call it on the concrete memory.Bus type
  - Do NOT modify the bar replay loop's multi-day reset logic
  - Do NOT change speed control behavior (max/1x/10x)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Targeted change in replay loop with important ordering semantics
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go concurrency, event-driven architecture

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Tasks 14, 15, 16)
  - **Blocks**: Task 17
  - **Blocked By**: Task 12

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-replay/main.go:390-453` — Bar replay loop. The WaitPending call goes after the inner loop (all bars at same timestamp published) and before advancing to next timestamp.

  **API/Type References**:
  - `backend/internal/adapters/eventbus/memory/bus.go` — WaitPending() method (from Task 1)
  - `backend/internal/app/positionmonitor/service.go` — EvalExitRules(barTime) method (from Task 3)

  **WHY Each Reference Matters**:
  - `main.go:390-453`: The exact insertion point for WaitPending. Must go after all publishes for a timestamp, before the next timestamp's min-time calculation.

  **Acceptance Criteria**:
  - [ ] WaitPending() called after each bar group in replay loop
  - [ ] EvalExitRules() called after WaitPending with current bar timestamp
  - [ ] Clock function updated to return current bar timestamp
  - [ ] Two identical backtest runs produce identical JSON output (determinism)
  - [ ] `cd backend && go build ./cmd/omo-replay/` compiles

  **QA Scenarios**:
  ```
  Scenario: Deterministic backtest results
    Tool: Bash
    Preconditions: TimescaleDB with historical data
    Steps:
      1. cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --no-ai --output-json /tmp/run1.json 2>/dev/null
      2. cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --no-ai --output-json /tmp/run2.json 2>/dev/null
      3. diff /tmp/run1.json /tmp/run2.json
    Expected Result: No diff between two runs — deterministic execution
    Failure Indicators: Diff shows different trade counts, prices, or timestamps
    Evidence: .sisyphus/evidence/task-13-determinism.txt

  Scenario: Exit rules evaluate per bar group
    Tool: Bash
    Preconditions: TimescaleDB with historical data
    Steps:
      1. cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --no-ai 2>&1 | grep -c "exit rule triggered"
    Expected Result: Count > 0 — exit rules are firing
    Failure Indicators: Count = 0 (exit rules never evaluate)
    Evidence: .sisyphus/evidence/task-13-exit-eval.txt
  ```

  **Commit**: YES
  - Message: `feat(replay): add per-bar event sync and exit evaluation for deterministic backtest`
  - Files: `backend/cmd/omo-replay/main.go`
  - Pre-commit: `cd backend && go build ./cmd/omo-replay/`

---

- [ ] 14. Warmup Fix: From-Date Awareness + Strategy Runner Warmup

  **What to do**:
  - Fix warmup in omo-replay to use `--from` date instead of `time.Now()`:
    - `domain.PreviousRTHSession(fromTime)` instead of `domain.PreviousRTHSession(time.Now())`
    - Strategy context initialization: pass `fromTime` instead of `time.Now()` as the context time
  - Add strategy runner warmup (currently missing from omo-replay):
    - Follow the pattern in `backend/cmd/omo-core/warmup.go:141-168` — create an `IndicatorCalculator` and call `runner.WarmUp(calc)`
    - This ensures strategies see warm indicators on their first bar, matching live behavior
  - Validate spike filter seeding: the adaptive filter uses the warmup bars to establish its baseline. Verify that the warmup period provides enough bars (at least 20) to seed the filter properly. If not, add explicit seeding from warmup bars.
  - Add ORB warmup if there's an active ORB session at the start of the backtest period (follow warmup.go pattern)

  **Must NOT do**:
  - Do NOT change omo-core's warmup.go
  - Do NOT change the adaptive filter parameters (20, 4.0)
  - Do NOT change how warmup bars are fetched from the database

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Multiple interconnected warmup subsystems, must understand indicator calculation flow
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go, trading system warmup patterns

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Tasks 13, 15, 16)
  - **Blocks**: Task 17
  - **Blocked By**: Task 12

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/warmup.go:80-168` — Full warmup procedure. Follow this pattern for omo-replay's warmup. Key sections: symbol list building, spike filter seeding, monitor warmup, strategy runner warmup, ORB warmup.
  - `backend/cmd/omo-replay/main.go:333-368` — Current warmup in omo-replay. This uses `time.Now()` at line 334 and lacks strategy runner warmup.

  **API/Type References**:
  - `backend/internal/app/strategy/runner.go` — `WarmUp(calc IndicatorCalculator)` method
  - `backend/internal/app/monitor/service.go` — Monitor's indicator calculation used by WarmUp

  **WHY Each Reference Matters**:
  - `warmup.go:80-168`: The CANONICAL warmup sequence. omo-replay must follow this but with fromTime instead of time.Now, and using DB bars (already available via repo) instead of Alpaca API bars.
  - `main.go:333-368`: The CURRENT warmup that needs fixing. Replace time.Now references and add strategy runner warmup after monitor warmup.

  **Acceptance Criteria**:
  - [ ] Warmup uses `--from` date for PreviousRTHSession, not time.Now
  - [ ] Strategy runner WarmUp called with indicator calculator
  - [ ] Spike filter has ≥20 bars seeded before main replay starts
  - [ ] `cd backend && go build ./cmd/omo-replay/` compiles

  **QA Scenarios**:
  ```
  Scenario: Warmup uses from-date for historical session
    Tool: Bash
    Preconditions: TimescaleDB with data
    Steps:
      1. cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --no-ai 2>&1 | grep -i "warmup"
    Expected Result: Warmup logs reference 2025-06-02 session, not today's date
    Failure Indicators: Warmup references current date
    Evidence: .sisyphus/evidence/task-14-warmup-date.txt

  Scenario: Strategy runner receives warm indicators
    Tool: Bash
    Preconditions: TimescaleDB with data
    Steps:
      1. cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --no-ai 2>&1 | grep -i "runner.*warm"
    Expected Result: Log shows strategy runner warmup completed
    Failure Indicators: No runner warmup log, or first signal has nil indicators
    Evidence: .sisyphus/evidence/task-14-runner-warmup.txt
  ```

  **Commit**: YES
  - Message: `fix(replay): use from-date for warmup and add strategy runner warmup`
  - Files: `backend/cmd/omo-replay/main.go`
  - Pre-commit: `cd backend && go build ./cmd/omo-replay/`

---

- [ ] 15. --no-ai Flag + SignalDebateEnricher Wiring

  **What to do**:
  - Add `--no-ai` CLI flag to omo-replay (default: `true` for backtest mode) that controls whether SignalDebateEnricher is active
  - When `--no-ai=true`: pass `DisableEnricher: true` to `bootstrap.BuildStrategyPipeline` — enricher is nil, signals flow directly from runner to risk sizer
  - When `--no-ai=false`: create a `llm.NewAdvisor` (or `llm.NewNoOpAdvisor` if AI config not set) and pass to strategy builder — enricher runs AI debate on each signal
  - Ensure the event chain works in both paths:
    - With enricher: Runner emits SignalCreated → Enricher subscribes → emits SignalEnriched → RiskSizer subscribes
    - Without enricher: Runner emits SignalCreated → RiskSizer must subscribe to SignalCreated directly (or enricher passthrough)
  - Verify the event subscription chain: check if RiskSizer subscribes to SignalEnriched or SignalCreated — if it subscribes to SignalEnriched, then "no enricher" mode needs a passthrough that re-emits SignalCreated as SignalEnriched

  **Must NOT do**:
  - Do NOT add AI response caching
  - Do NOT change the enricher's debate logic
  - Do NOT make --no-ai affect anything other than enricher instantiation

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Requires understanding the event chain between enricher and risk sizer to handle the no-enricher case
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Event-driven pipeline, Go CLI flags

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Tasks 13, 14, 16)
  - **Blocks**: Task 17
  - **Blocked By**: Task 12

  **References**:

  **Pattern References**:
  - `backend/internal/app/strategy/signal_debate_enricher.go` — Enricher subscribes to SignalCreated, emits SignalEnriched
  - `backend/internal/app/strategy/risk_sizer.go` — Check what event RiskSizer subscribes to (SignalEnriched? SignalCreated?)
  - `backend/cmd/omo-core/services.go:300-320` — How omo-core creates the enricher with options

  **API/Type References**:
  - `backend/internal/ports/ai_advisor.go` — AIAdvisorPort interface, `llm.NewNoOpAdvisor()` for no-AI mode

  **WHY Each Reference Matters**:
  - `risk_sizer.go`: CRITICAL — need to know if RiskSizer subscribes to SignalEnriched (needs enricher or passthrough) or SignalCreated (can bypass enricher). This determines whether we need a passthrough component or if the bootstrap builder handles it.
  - `signal_debate_enricher.go`: Understanding the event subscription to know exactly what happens when enricher is nil.

  **Acceptance Criteria**:
  - [ ] `--no-ai` flag exists, defaults to true
  - [ ] With `--no-ai=true`: no AI calls, signals still reach risk sizer
  - [ ] With `--no-ai=false`: enricher active, signals go through debate
  - [ ] Event chain verified: signals → (optional enrichment) → risk sizing → execution
  - [ ] `cd backend && go build ./cmd/omo-replay/` compiles

  **QA Scenarios**:
  ```
  Scenario: Backtest runs without AI (default)
    Tool: Bash
    Preconditions: TimescaleDB with data
    Steps:
      1. cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY 2>&1 | grep -E "(signal|intent|fill)" | head -10
    Expected Result: Signals generated, intents created, fills produced — all without AI
    Failure Indicators: No intents (signals don't reach risk sizer), AI timeout errors
    Evidence: .sisyphus/evidence/task-15-no-ai-default.txt

  Scenario: Event chain intact without enricher
    Tool: Bash
    Preconditions: TimescaleDB with data
    Steps:
      1. cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --no-ai --output-json /tmp/noai.json 2>/dev/null
      2. python3 -c "import json; d=json.load(open('/tmp/noai.json')); print('trades:', d.get('total_trades', 0))"
    Expected Result: total_trades > 0 — full pipeline works without enricher
    Failure Indicators: total_trades = 0 (pipeline broken without enricher)
    Evidence: .sisyphus/evidence/task-15-noai-pipeline.txt
  ```

  **Commit**: YES
  - Message: `feat(replay): add --no-ai flag for SignalDebateEnricher control`
  - Files: `backend/cmd/omo-replay/main.go`
  - Pre-commit: `cd backend && go build ./cmd/omo-replay/`

---

- [ ] 16. Moderate Gaps: SignalTracker, SetBaseSymbols, Config-Driven Params

  **What to do**:
  - **SignalTracker**: Wire `perf.NewSignalTracker` into the omo-replay backtest pipeline via the bootstrap perf builder. It tracks signal lifecycle (created → validated → executed) and writes to PnL repo (uses NoopPnLRepo in backtest — writes discarded, but events still emitted for the collector)
  - **monitor.SetBaseSymbols**: After building the strategy pipeline, collect all unique symbols from loaded specs and call `monitorSvc.SetBaseSymbols(symbols)` — this ensures the monitor calculates indicators for all symbols that strategies care about, matching omo-core's behavior
  - **Config-driven risk params**: Verify that after the bootstrap refactor, no hardcoded risk values remain in omo-replay. Specifically:
    - `NewRiskEngine(0.02)` should be `NewRiskEngine(cfg.Trading.MaxRiskPercent)` — handled by bootstrap builder
    - `NewKillSwitch(3, 30*time.Minute, time.Hour, ...)` should use config values — verify
    - `NewDailyLossBreaker(...)` already uses config — verify
  - **DNAApproval**: Skip for backtest mode — DNAApproval gates strategy DNA versions based on human approval workflow, which doesn't apply to backtesting. Document this decision as a comment in the code.

  **Must NOT do**:
  - Do NOT add SymbolRouter to backtest (it manages live subscription routing — not relevant for DB-sourced bars)
  - Do NOT add DNAApproval gate to backtest pipeline
  - Do NOT change config.yaml structure

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Multiple small wiring tasks bundled together, each straightforward
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go service wiring, config management

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Tasks 13, 14, 15)
  - **Blocks**: Task 17
  - **Blocked By**: Task 12, Task 3

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/services.go:125-128` — SignalTracker initialization
  - `backend/cmd/omo-core/services.go:330-340` — SetBaseSymbols call from loaded specs
  - `backend/internal/app/perf/signal_tracker.go` — SignalTracker constructor and event subscriptions

  **WHY Each Reference Matters**:
  - `services.go:330-340`: Shows how omo-core collects symbols from specs and calls SetBaseSymbols. Must replicate in bootstrap or in omo-replay after pipeline build.

  **Acceptance Criteria**:
  - [ ] SignalTracker wired in backtest pipeline
  - [ ] monitor.SetBaseSymbols called with deduped symbols from loaded specs
  - [ ] No hardcoded risk values in omo-replay (grep for `0.02` and magic numbers)
  - [ ] DNAApproval NOT wired in backtest (explicit skip with comment)
  - [ ] `cd backend && go build ./cmd/omo-replay/` compiles

  **QA Scenarios**:
  ```
  Scenario: No hardcoded risk params remain
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && grep -rn "NewRiskEngine(0\." cmd/omo-replay/
      2. cd backend && grep -rn "NewKillSwitch(3," cmd/omo-replay/
    Expected Result: No matches — all values come from config
    Failure Indicators: Grep returns matches with hardcoded values
    Evidence: .sisyphus/evidence/task-16-no-hardcoded.txt

  Scenario: SetBaseSymbols called with correct symbols
    Tool: Bash
    Preconditions: TimescaleDB with data
    Steps:
      1. cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --no-ai 2>&1 | grep -i "base.symbols"
    Expected Result: Log shows SetBaseSymbols called
    Failure Indicators: No SetBaseSymbols log
    Evidence: .sisyphus/evidence/task-16-base-symbols.txt
  ```

  **Commit**: YES
  - Message: `feat(replay): wire SignalTracker, SetBaseSymbols, and config-driven params`
  - Files: `backend/cmd/omo-replay/main.go`
  - Pre-commit: `cd backend && go build ./cmd/omo-replay/`

- [ ] 17. Pipeline Integration Test

  **What to do**:
  - Create `backend/internal/app/bootstrap/pipeline_integration_test.go` (or `backend/cmd/omo-replay/pipeline_test.go`) with an integration test that exercises the FULL pipeline end-to-end:
    - Build pipeline using bootstrap functions with: memory EventBus, SimBroker, NoopRepo, NoopPnLRepo, mock SpecStore, mock clock
    - Feed a known sequence of bars (manually constructed, not from DB) through ingestion
    - Verify the complete event chain fires:
      1. `EventMarketBarReceived` → ingestion → `EventMarketBarSanitized`
      2. Monitor calculates indicators
      3. Strategy runner generates signal → `EventSignalCreated`
      4. (No enricher in test) → `EventSignalEnriched` or passthrough
      5. RiskSizer creates intent → `EventOrderIntentCreated`
      6. Execution validates → SimBroker fills → `EventFillReceived`
      7. PositionMonitor tracks position
      8. On appropriate bar: exit rule triggers → `EventExitTriggered`
    - Use `WaitPending()` after each bar to ensure deterministic processing
    - Assert specific event counts: e.g., at least 1 signal, 1 fill, 1 exit
    - The test should be self-contained with no external dependencies (no DB, no network)
  - This test serves as the canonical "does the pipeline work" regression guard

  **Must NOT do**:
  - Do NOT use real DB or external services — all mocked
  - Do NOT test AI enrichment — use --no-ai path
  - Do NOT test specific strategy logic — just that events flow through the pipeline

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Complex test orchestrating many services with event-driven assertions
  - **Skills**: [`senior-backend`, `testing-patterns`]
    - `senior-backend`: Go integration testing, event-driven testing
    - `testing-patterns`: Test structure, assertion patterns, mock strategies

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 5 (with Tasks 18, 19)
  - **Blocks**: F1-F4
  - **Blocked By**: Tasks 13, 14, 15, 16 (full pipeline must be wired)

  **References**:

  **Pattern References**:
  - `backend/internal/app/execution/service_test.go` — Execution service test patterns with mock bus, mock broker
  - `backend/internal/adapters/simbroker/broker_test.go` — SimBroker test patterns

  **API/Type References**:
  - `backend/internal/app/bootstrap/*.go` — All builder functions (subjects under test)
  - `backend/internal/adapters/eventbus/memory/bus.go` — Memory bus with WaitPending
  - `backend/internal/domain/events.go` — All event types for subscription verification

  **WHY Each Reference Matters**:
  - `service_test.go`: Shows how to set up mock dependencies for execution service testing. Follow this for the full pipeline test.
  - `domain/events.go`: Need exact event type constants for subscribing and asserting.

  **Acceptance Criteria**:
  - [ ] Integration test exists and exercises full pipeline
  - [ ] Test is self-contained (no external deps)
  - [ ] `cd backend && go test ./internal/app/bootstrap/ -run TestPipelineIntegration -v -race` passes
  - [ ] Test verifies: bars → signals → intents → fills → exits event chain

  **QA Scenarios**:
  ```
  Scenario: Integration test passes
    Tool: Bash
    Preconditions: None (self-contained test)
    Steps:
      1. cd backend && go test ./internal/app/bootstrap/ -run TestPipelineIntegration -v -race -timeout 60s
    Expected Result: Test passes, logs show full event chain
    Failure Indicators: "FAIL", timeout, race condition
    Evidence: .sisyphus/evidence/task-17-integration-test.txt
  ```

  **Commit**: YES
  - Message: `test(bootstrap): add full pipeline integration test for backtest parity`
  - Files: `backend/internal/app/bootstrap/pipeline_integration_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/bootstrap/ -race -timeout 60s`

---

- [ ] 18. SimBroker Interface Assertions + Unit Tests

  **What to do**:
  - Add compile-time interface assertion tests to `backend/internal/adapters/simbroker/broker_test.go`:
    ```go
    var _ ports.BrokerPort = (*simbroker.Broker)(nil)
    var _ ports.OrderStreamPort = (*simbroker.Broker)(nil) // if this interface exists
    var _ execution.QuoteProvider = (*simbroker.Broker)(nil)
    var _ ports.AccountPort = (*simbroker.Broker)(nil)
    ```
  - Add unit tests for all new methods (GetQuote, GetAccountBuyingPower, GetAccountEquity):
    - Test equity tracking through buy/sell cycles
    - Test buying power decreases on buys, increases on sells
    - Test quote spread matches slippageBPS configuration
    - Test edge cases: no price available, zero positions, close-and-reopen position
  - Verify no regression on existing SimBroker tests

  **Must NOT do**:
  - Do NOT change existing SimBroker test expectations

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Focused test writing for known interface, clear expectations
  - **Skills**: [`testing-patterns`]
    - `testing-patterns`: Go test patterns, compile-time assertions, table-driven tests

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 5 (with Tasks 17, 19)
  - **Blocks**: F1-F4
  - **Blocked By**: Task 4 (SimBroker port expansion)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/simbroker/broker_test.go` — Existing tests to extend
  - `backend/internal/ports/ports_test.go` — Mock implementations showing interface patterns

  **Acceptance Criteria**:
  - [ ] Compile-time assertions for all 4 interfaces
  - [ ] Unit tests for GetQuote, GetAccountBuyingPower, GetAccountEquity
  - [ ] Edge case tests (no price, zero positions, buy/sell cycle)
  - [ ] `cd backend && go test ./internal/adapters/simbroker/ -v -race` passes

  **QA Scenarios**:
  ```
  Scenario: SimBroker passes all interface and unit tests
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go test ./internal/adapters/simbroker/ -v -race
    Expected Result: All tests pass including new interface assertions and method tests
    Failure Indicators: "FAIL", compilation error on interface assertion
    Evidence: .sisyphus/evidence/task-18-simbroker-tests.txt
  ```

  **Commit**: YES
  - Message: `test(simbroker): add interface assertions and port expansion unit tests`
  - Files: `backend/internal/adapters/simbroker/broker_test.go`
  - Pre-commit: `cd backend && go test ./internal/adapters/simbroker/ -race`

---

- [ ] 19. Regression Tests: omo-core Build + Replay-Only Mode

  **What to do**:
  - Verify omo-core builds cleanly: `go build -o bin/omo-core ./cmd/omo-core`
  - Verify ALL existing tests pass: `go test ./...`
  - Verify omo-core vet passes: `go vet ./...`
  - Verify replay-only mode (without `--backtest`) produces identical behavior to pre-refactor:
    - Run replay-only and capture signal/intent counts
    - Compare against known baseline (or just verify counts > 0)
  - Verify `--backtest=false` with `--speed=1x` still works (non-max speed)
  - Create a simple test script or Go test that verifies both binaries compile

  **Must NOT do**:
  - Do NOT attempt to run omo-core live (would need real broker connection)
  - Do NOT modify any test expectations

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Verification task, running existing test suite and builds
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 5 (with Tasks 17, 18)
  - **Blocks**: F1-F4
  - **Blocked By**: Task 11 (omo-core refactor must be complete)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/` — omo-core binary source
  - `backend/cmd/omo-replay/` — omo-replay binary source

  **Acceptance Criteria**:
  - [ ] `cd backend && go build -o bin/omo-core ./cmd/omo-core` — exit code 0
  - [ ] `cd backend && go build -o bin/omo-replay ./cmd/omo-replay` — exit code 0
  - [ ] `cd backend && go test ./...` — exit code 0, no failures
  - [ ] `cd backend && go vet ./...` — no warnings
  - [ ] Replay-only mode produces signals (count > 0)

  **QA Scenarios**:
  ```
  Scenario: Both binaries compile and all tests pass
    Tool: Bash
    Preconditions: None
    Steps:
      1. cd backend && go build -o bin/omo-core ./cmd/omo-core && echo "omo-core: OK"
      2. cd backend && go build -o bin/omo-replay ./cmd/omo-replay && echo "omo-replay: OK"
      3. cd backend && go test ./... 2>&1 | tail -5
      4. cd backend && go vet ./... 2>&1
    Expected Result: Both build, all tests pass, no vet issues
    Failure Indicators: Any non-zero exit code
    Evidence: .sisyphus/evidence/task-19-regression.txt

  Scenario: Replay-only mode unchanged
    Tool: Bash
    Preconditions: TimescaleDB with data
    Steps:
      1. cd backend && timeout 30 go run ./cmd/omo-replay/ --from 2025-06-02 --to 2025-06-02 --symbols SPY 2>&1 | grep -c "EventSignalCreated"
    Expected Result: Signal count > 0
    Failure Indicators: Count = 0 or error
    Evidence: .sisyphus/evidence/task-19-replay-only.txt
  ```

  **Commit**: NO (verification only)

---

## Final Verification Wave

> 4 review agents run in PARALLEL. ALL must APPROVE. Rejection → fix → re-run.

- [ ] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (`go build`, grep for functions, check event subscriptions). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in `.sisyphus/evidence/`. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [ ] F2. **Code Quality Review** — `unspecified-high`
  Run `cd backend && go vet ./... && go build ./... && go test ./...`. Review all changed files for: type assertion panics, empty error handling, `time.Now()` in backtest paths, unused imports. Check AI slop: excessive comments, over-abstraction, generic variable names.
  Output: `Build [PASS/FAIL] | Vet [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [ ] F3. **Real Manual QA** — `unspecified-high`
  Start from clean state. Run full backtest: `cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --output-json /tmp/parity-test.json`. Verify JSON output contains trades with entries AND exits. Run replay-only mode and verify unchanged behavior. Run omo-core build and verify it starts without errors.
  Output: `Backtest [PASS/FAIL] | Replay [PASS/FAIL] | omo-core [PASS/FAIL] | VERDICT`

- [ ] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (`git log --oneline`, `git diff`). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance: no omo-core behavior changes, no fill model changes, no port interface changes. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **Wave 1**: One commit per task — `feat(simbroker): add QuoteProvider and AccountPort`, `feat(eventbus): add WaitPending for backtest sync`, etc.
- **Wave 2**: One commit per builder — `feat(bootstrap): add execution guard builder`, etc.
- **Wave 3**: One commit per binary — `refactor(omo-core): use shared bootstrap package`, `refactor(omo-replay): wire full pipeline via bootstrap`
- **Wave 4**: One commit per feature — `feat(replay): add per-bar event sync`, etc.
- **Wave 5**: One commit for tests — `test: add pipeline integration test and regression suite`

---

## Success Criteria

### Verification Commands
```bash
# Both binaries compile
cd backend && go build -o bin/omo-core ./cmd/omo-core && go build -o bin/omo-replay ./cmd/omo-replay

# All tests pass
cd backend && go test ./...

# Backtest produces full pipeline events
cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --output-json /tmp/test.json 2>&1 | grep -c "EventExitTriggered"
# Expected: > 0

# Replay-only mode unchanged
cd backend && go run ./cmd/omo-replay/ --from 2025-06-02 --to 2025-06-02 --symbols SPY 2>&1 | grep -c "EventSignalCreated"
# Expected: > 0

# Deterministic: two runs produce same result
cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --output-json /tmp/run1.json 2>/dev/null
cd backend && go run ./cmd/omo-replay/ --backtest --from 2025-06-02 --to 2025-06-02 --symbols SPY --output-json /tmp/run2.json 2>/dev/null
diff /tmp/run1.json /tmp/run2.json
# Expected: no diff

# SimBroker implements required ports (compile-time check via test)
cd backend && go test ./internal/adapters/simbroker/ -run TestInterfaceCompliance
```

### Final Checklist
- [ ] All 8 critical gaps closed
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass
- [ ] Both binaries compile and run
- [ ] Deterministic backtest results
