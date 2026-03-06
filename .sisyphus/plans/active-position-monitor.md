# Active Position Monitor — Implementation Plan

## TL;DR

> **Quick Summary**: Build a new `positionmonitor` app service that continuously tracks open positions via event bus fills and market bars, evaluates 6 configurable exit conditions (trailing stop, profit target, max loss, max holding time, time-based exit, end-of-day flatten), and emits `EventOrderIntentCreated` with `DirectionCloseLong` through the existing execution pipeline. Follows hexagonal architecture and existing service patterns exactly.
>
> **Deliverables**:
> - Domain types: `ExitCondition`, `ExitPolicy`, `TrackedPosition`, new event type constants
> - App service: `backend/internal/app/positionmonitor/` (service, config parser, exit evaluator, tests)
> - Config: `[position_monitor]` TOML section in strategy DNA files
> - Wiring: Integration into `AccountHandle` and `main.go`
>
> **Estimated Effort**: Large
> **Parallel Execution**: YES — 4 waves
> **Critical Path**: Task 1 (domain types) → Task 3 (exit evaluator) → Task 5 (service core) → Task 8 (wiring) → Task 9 (integration tests)

---

## Context

### Original Request
Build an Active Position Monitor — a background service that continuously watches open positions in real-time, evaluates configurable exit conditions, and can force-close positions when conditions are met regardless of strategy signals. The system currently has pre-trade risk checks (kill switch, circuit breaker, daily loss limits) but NO active monitoring of open positions after entry.

### Interview Summary
**Key Discussions**:
- Monitor is a **safety net / additional layer**, NOT replacing strategy exits or broker-side stop-losses
- Must follow hexagonal architecture: domain logic pure, broker interactions through ports
- Six exit conditions: trailing stop, time-based exit, profit target, max holding time, max loss per position, end-of-day flatten
- Per-strategy configuration via TOML DNA `[position_monitor]` section
- Asset class aware: crypto 24/7 (no EOD flatten), equities RTH
- System is **long-only** — only `DirectionCloseLong` exits
- Risk parameters in **basis points** matching existing convention
- Multi-tenant: filter events by `TenantID`, per-account service instances

**Research Findings**:
- All app services follow `NewService(ports..., log) → Start(ctx) → Subscribe → Handle` pattern
- Event bus is synchronous in-memory (`memory.NewBus()`) — handlers execute in publisher's goroutine
- `BrokerPort.GetPositions()` returns `[]domain.Trade` (Alpaca adapter caches 2s)
- Existing services use `nowFunc func() time.Time` for deterministic time testing
- `TradingCalendar` interface with `NYSECalendar` and `Crypto24x7Calendar` + `CalendarFor(assetClass)` factory
- Manual mock structs with override function fields (not code-generated) in `shared_test.go`
- `OrderIntent` constructor requires `StopLoss > 0` — exit intents use market price as synthetic stop
- Exit orders skip risk/slippage checks and resolve quantity from `broker.GetPositions()`
- `BurntSushi/toml` silently ignores unknown sections — safe to add `[position_monitor]` to TOML
- `AccountHandle` struct in `orchestrator.go` holds per-account services — new field for position monitor
- Position keying convention: `"tenantID:envMode:symbol"` (matching `LedgerWriter`)

### Metis Review
**Identified Gaps (addressed)**:
- **Exit intent rejection handling**: Monitor marks position as "exiting" via state machine; if execution rejects, the next reconciliation cycle resets state if position still exists
- **Duplicate exit prevention**: IdempotencyKey with `"posmon:"` prefix + position state machine (`TRACKING → EXITING → CLOSED`)
- **Reconciliation frequency**: Background goroutine every 30 seconds, using `PriorityBackground` to avoid starving trading
- **Startup bootstrap**: On `Start()`, call `broker.GetPositions()` to seed initial position state (entry price/time from broker data)
- **Simultaneous exit conditions**: First condition that triggers wins — evaluation short-circuits
- **Trailing stop loss on restart**: Known limitation — high water mark resets to current price; documented but not persisted
- **FillReceived payload parsing**: Dedicated `parseFillPayload()` with exhaustive type assertions and error logging
- **Stale data**: If no bars for 60+ seconds for an actively-tracked symbol, log warning; not an auto-exit (too dangerous)

---

## Work Objectives

### Core Objective
Build a per-account `PositionMonitor` service that subscribes to fill events and market bars, tracks open positions with a state machine, evaluates 6 exit conditions per tick, and emits exit intents through the existing execution pipeline.

### Concrete Deliverables
- `backend/internal/domain/position_monitor.go` — Domain types: `ExitCondition`, `ExitPolicy`, `TrackedPosition`, `PositionState`
- `backend/internal/domain/position_monitor_test.go` — Unit tests for domain types and exit condition evaluation
- `backend/internal/app/positionmonitor/config.go` — TOML config parser for `[position_monitor]` section
- `backend/internal/app/positionmonitor/config_test.go` — Config parsing tests
- `backend/internal/app/positionmonitor/evaluator.go` — Pure exit condition evaluator (no I/O)
- `backend/internal/app/positionmonitor/evaluator_test.go` — Exhaustive evaluator tests
- `backend/internal/app/positionmonitor/service.go` — Core service: lifecycle, event handlers, reconciliation
- `backend/internal/app/positionmonitor/service_test.go` — Service integration tests
- `backend/internal/app/positionmonitor/shared_test.go` — Mock structs and test helpers
- Modified `backend/internal/app/orchestrator/orchestrator.go` — New `PositionMonitor` field in `AccountHandle`
- Modified `backend/cmd/omo-core/main.go` — Wire and start position monitor per account
- Modified `configs/strategies/avwap.toml` (and others) — Example `[position_monitor]` section
- 2 new event type constants in `backend/internal/domain/event.go`

### Definition of Done
- [ ] `cd backend && go test ./internal/app/positionmonitor/...` — all tests pass (0 failures)
- [ ] `cd backend && go test ./internal/domain/...` — all tests pass (including new domain tests)
- [ ] `cd backend && go build ./cmd/omo-core/` — compiles cleanly with position monitor wired in
- [ ] `cd backend && go vet ./...` — no vet errors
- [ ] All six exit conditions have dedicated test coverage
- [ ] Multi-tenant isolation tested (two tenants, same symbol, independent exits)
- [ ] Idempotency tested (duplicate exit condition triggers → single intent)

### Must Have
- All six exit conditions: trailing stop, time-based exit, profit target, max holding time, max loss per position, end-of-day flatten
- Per-strategy configuration from TOML DNA `[position_monitor]` section
- Position state machine: `TRACKING → EXITING → CLOSED`
- Event filtering by `TenantID` in every handler
- `IdempotencyKey` with `"posmon:"` prefix on all emitted events
- `nowFunc` injection for deterministic time testing
- Startup reconciliation from `broker.GetPositions()`
- Periodic background reconciliation (30s interval)
- Asset class awareness: skip EOD flatten for crypto
- `sync.Mutex` for position state protection

### Must NOT Have (Guardrails)
- ❌ NO modification of existing domain types (`Event`, `OrderIntent`, `Trade`, `Direction`)
- ❌ NO short-selling support (`DirectionCloseShort` or similar)
- ❌ NO P&L calculation (LedgerWriter's responsibility)
- ❌ NO direct notification integration (notify service handles that via events)
- ❌ NO position persistence to database (runtime-only service)
- ❌ NO per-symbol exit conditions (per-strategy only)
- ❌ NO exit condition prioritization/chaining (first trigger wins)
- ❌ NO position aggregation across strategies for same symbol
- ❌ NO options-specific exit conditions (theta/delta monitoring)
- ❌ NO historical position tracking or reporting
- ❌ NO `time.Sleep` in tests — use deterministic `nowFunc` and synchronous event bus
- ❌ NO broker calls inside bar event handlers (reconciliation on separate timer only)
- ❌ NO `as any` / `@ts-ignore` equivalent (`interface{}` assertions without validation)
- ❌ AI slop: No excessive comments, no over-abstraction, no generic variable names

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES — `go test ./...` with `testify` (assert/require), manual mocks, `memory.NewBus()`
- **Automated tests**: YES (Tests-alongside) — Each implementation file gets a corresponding `_test.go`
- **Framework**: `go test` with `testify/assert` and `testify/require`
- **Pattern**: Follow `backend/internal/app/execution/shared_test.go` for mock structs

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Go Tests**: Use `bash` — `go test -v -run TestName ./path/...` and capture output
- **Build Verification**: Use `bash` — `go build ./cmd/omo-core/` and `go vet ./...`
- **Code Quality**: Use `ast_grep_search` to verify no forbidden patterns

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — domain + config foundation):
├── Task 1: Domain types (ExitCondition, ExitPolicy, TrackedPosition, PositionState) [quick]
├── Task 2: Config parser for [position_monitor] TOML section [quick]
└── Task 3: Pure exit condition evaluator (no I/O) [deep]

Wave 2 (After Wave 1 — service core + orchestrator modification):
├── Task 4: Test helpers and mock structs (shared_test.go) [quick]
├── Task 5: Core service (lifecycle, event handlers, state management) [deep]
├── Task 6: Add PositionMonitor field to AccountHandle [quick]
└── Task 7: Add new event type constants to domain/event.go [quick]

Wave 3 (After Wave 2 — wiring + integration):
├── Task 8: Wire position monitor into main.go [unspecified-high]
└── Task 9: Integration tests (multi-tenant, idempotency, full lifecycle) [deep]

Wave 4 (After Wave 3 — config + polish):
├── Task 10: Add [position_monitor] example to strategy TOML files [quick]
└── Task 11: Full build + vet + test verification [deep]

Wave FINAL (After ALL tasks — independent review):
├── Task F1: Plan compliance audit [oracle]
├── Task F2: Code quality review [unspecified-high]
├── Task F3: Real QA — run service manually, verify event flow [unspecified-high]
└── Task F4: Scope fidelity check [deep]

Critical Path: Task 1 → Task 3 → Task 5 → Task 8 → Task 9 → F1-F4
Parallel Speedup: ~55% faster than sequential
Max Concurrent: 3 (Wave 1), 4 (Wave 2)
```

### Dependency Matrix

| Task | Depends On | Blocks |
|------|-----------|--------|
| 1 | — | 2, 3, 4, 5, 7 |
| 2 | 1 | 5, 8, 10 |
| 3 | 1 | 5, 9 |
| 4 | 1 | 5, 9 |
| 5 | 1, 2, 3, 4 | 8, 9 |
| 6 | — | 8 |
| 7 | — | 5, 8 |
| 8 | 2, 5, 6, 7 | 9, 11 |
| 9 | 3, 4, 5, 8 | 11 |
| 10 | 2 | 11 |
| 11 | 8, 9, 10 | F1-F4 |
| F1-F4 | 11 | — |

### Agent Dispatch Summary

- **Wave 1**: 3 tasks — T1 → `quick`, T2 → `quick`, T3 → `deep`
- **Wave 2**: 4 tasks — T4 → `quick`, T5 → `deep`, T6 → `quick`, T7 → `quick`
- **Wave 3**: 2 tasks — T8 → `unspecified-high`, T9 → `deep`
- **Wave 4**: 2 tasks — T10 → `quick`, T11 → `quick`
- **FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

> Implementation + Test = ONE Task. Never separate.
> EVERY task MUST have: Recommended Agent Profile + Parallelization info + QA Scenarios.

- [ ] 1. Domain Types — ExitCondition, ExitPolicy, TrackedPosition, PositionState

  **What to do**:
  - Create `backend/internal/domain/position_monitor.go` with the following types:
    - `PositionState` — string enum: `PositionStateTracking = "TRACKING"`, `PositionStateExiting = "EXITING"`
    - `ExitConditionType` — string enum: `ExitTrailingStop`, `ExitProfitTarget`, `ExitMaxLoss`, `ExitMaxHoldingTime`, `ExitTimeBasedExit`, `ExitEndOfDay`
    - `ExitPolicy` — struct holding all 6 exit condition configs (all in basis points or minutes):
      ```go
      type ExitPolicy struct {
          TrailingStopBPS    int  // 0 = disabled
          ProfitTargetBPS    int  // 0 = disabled
          MaxLossPerPosBPS   int  // 0 = disabled
          MaxHoldingMinutes  int  // 0 = disabled
          TimeExitMinutes    int  // 0 = disabled (minutes after entry to start tightening)
          EODFlattenEnabled  bool // false = disabled
          EODFlattenMinutes  int  // minutes before session close to flatten (default 5)
      }
      ```
    - `ExitPolicy.IsEmpty() bool` — returns true if all conditions are disabled (all zeros and EOD false)
    - `TrackedPosition` — struct for in-memory position tracking:
      ```go
      type TrackedPosition struct {
          TenantID      string
          EnvMode       EnvMode
          Symbol        Symbol
          Strategy      string
          AssetClass    AssetClass
          EntryPrice    float64
          EntryTime     time.Time
          Quantity       float64
          HighWaterMark float64  // highest price since entry (for trailing stop)
          State         PositionState
          ExitPolicy    ExitPolicy
      }
      ```
    - `TrackedPosition.Key() string` — returns `"tenantID:envMode:symbol"` matching LedgerWriter's keying convention
    - `TrackedPosition.UpdateHighWaterMark(price float64)` — updates HWM if price > current HWM
  - Create `backend/internal/domain/position_monitor_test.go`:
    - Test `ExitPolicy.IsEmpty()` with all-zero and non-zero policies
    - Test `TrackedPosition.Key()` formatting
    - Test `TrackedPosition.UpdateHighWaterMark()` — only updates when new price is higher
    - Test `PositionState` constants are correct string values

  **Must NOT do**:
  - Do NOT modify existing domain types (`OrderIntent`, `Trade`, `Event`, `Direction`)
  - Do NOT add methods that depend on external packages (keep domain pure)
  - Do NOT add P&L calculation fields or methods

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Small, well-scoped domain types with clear specifications — struct definitions and simple methods
  - **Skills**: []
    - No special skills needed — pure Go struct definitions and unit tests

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3)
  - **Blocks**: Tasks 2, 3, 4, 5, 7
  - **Blocked By**: None (can start immediately)

  **References**:

  **Pattern References**:
  - `backend/internal/domain/value.go:30-50` — How to define string enum types (`Direction`, `AssetClass`) with constants and `String()` method
  - `backend/internal/domain/entity.go:50-90` — Struct definition patterns with JSON tags and constructor validation
  - `backend/internal/app/perf/ledger_writer.go` — Search for `positionEntry` to find position keying pattern `"tenantID:envMode:symbol"`

  **API/Type References**:
  - `backend/internal/domain/value.go:EnvMode` — Used in `TrackedPosition` struct
  - `backend/internal/domain/value.go:Symbol` — Used in `TrackedPosition` struct
  - `backend/internal/domain/value.go:AssetClass` — Used in `TrackedPosition` for calendar selection

  **Test References**:
  - `backend/internal/domain/entity_test.go` — Test patterns for domain types using testify assert

  **WHY Each Reference Matters**:
  - `value.go` enum pattern: Copy the exact style for `PositionState` and `ExitConditionType` (type alias + const block)
  - `entity.go` struct pattern: Follow field ordering and JSON tag conventions
  - Ledger position key: The `TrackedPosition.Key()` format MUST match exactly for consistency

  **Acceptance Criteria**:
  - [ ] File exists: `backend/internal/domain/position_monitor.go`
  - [ ] File exists: `backend/internal/domain/position_monitor_test.go`
  - [ ] `cd backend && go test -v -run TestExitPolicy ./internal/domain/...` → PASS
  - [ ] `cd backend && go test -v -run TestTrackedPosition ./internal/domain/...` → PASS
  - [ ] `cd backend && go test -v -run TestPositionState ./internal/domain/...` → PASS

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Domain types compile and tests pass
    Tool: Bash
    Preconditions: backend/internal/domain/position_monitor.go and _test.go created
    Steps:
      1. Run: cd backend && go test -v -run "TestExitPolicy|TestTrackedPosition|TestPositionState" ./internal/domain/...
      2. Verify output contains "PASS" for each test function
      3. Run: cd backend && go vet ./internal/domain/...
    Expected Result: All tests pass, no vet errors
    Failure Indicators: Any "FAIL" in output or non-zero exit code
    Evidence: .sisyphus/evidence/task-1-domain-types-tests.txt

  Scenario: ExitPolicy.IsEmpty returns correct values
    Tool: Bash
    Preconditions: Tests include both empty and non-empty ExitPolicy cases
    Steps:
      1. Run: cd backend && go test -v -run TestExitPolicyIsEmpty ./internal/domain/...
      2. Verify ExitPolicy{} (zero value) returns true
      3. Verify ExitPolicy{TrailingStopBPS: 200} returns false
    Expected Result: Both cases pass
    Failure Indicators: Test output shows assertion failure
    Evidence: .sisyphus/evidence/task-1-exit-policy-empty.txt
  ```

  **Commit**: YES (group with Task 7)
  - Message: `feat(domain): add position monitor types and event constants`
  - Files: `backend/internal/domain/position_monitor.go`, `backend/internal/domain/position_monitor_test.go`
  - Pre-commit: `cd backend && go test ./internal/domain/...`

- [ ] 2. Config Parser — Parse [position_monitor] TOML Section

  **What to do**:
  - Create `backend/internal/app/positionmonitor/config.go`:
    - Define `Config` struct mirroring `domain.ExitPolicy` with TOML tags:
      ```go
      type Config struct {
          TrailingStopBPS    int  `toml:"trailing_stop_bps"`
          ProfitTargetBPS    int  `toml:"profit_target_bps"`
          MaxLossPerPosBPS   int  `toml:"max_loss_per_pos_bps"`
          MaxHoldingMinutes  int  `toml:"max_holding_minutes"`
          TimeExitMinutes    int  `toml:"time_exit_minutes"`
          EODFlattenEnabled  bool `toml:"eod_flatten_enabled"`
          EODFlattenMinutes  int  `toml:"eod_flatten_minutes"`
      }
      ```
    - `ParseConfigFromTOML(path string) (Config, error)` — reads TOML file, extracts `[position_monitor]` section
      - Use `BurntSushi/toml` (already a dependency)
      - If `[position_monitor]` section is missing → return zero-value Config (all disabled), no error
      - If `eod_flatten_minutes` is 0 but `eod_flatten_enabled` is true → default to 5 minutes
    - `ParseConfigFromParams(params map[string]any) (Config, error)` — extracts config from strategy params map (for strategies that embed position monitor config in `[params]`)
    - `(c Config) ToExitPolicy() domain.ExitPolicy` — converts Config to domain ExitPolicy
  - Create `backend/internal/app/positionmonitor/config_test.go`:
    - Test parsing a TOML string with full `[position_monitor]` section
    - Test parsing a TOML string without `[position_monitor]` → all disabled
    - Test EOD flatten default (enabled but no minutes → defaults to 5)
    - Test `ToExitPolicy()` conversion correctness

  **Must NOT do**:
  - Do NOT modify the existing `dna_manager.go` or `spec_loader.go` files
  - Do NOT create a new port/interface for config loading — keep it as a simple utility
  - Do NOT validate business logic in config (evaluator does that)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Straightforward TOML parsing with clear input/output — minimal logic
  - **Skills**: []
    - No special skills needed — simple TOML parsing with BurntSushi/toml

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3)
  - **Blocks**: Tasks 5, 8, 10
  - **Blocked By**: Task 1 (needs domain.ExitPolicy type)

  **References**:

  **Pattern References**:
  - `backend/internal/app/strategy/dna_manager.go:32-45` — `tomlFile` struct showing how to define TOML section structs with tags
  - `backend/internal/app/strategy/dna_manager.go:64-127` — `Load()` function showing TOML parsing pattern with `BurntSushi/toml`
  - `backend/internal/app/strategy/spec_loader.go:164-274` — `loadV2()` showing how v2 TOML schema is parsed (section-by-section struct decoding)

  **API/Type References**:
  - `backend/internal/domain/position_monitor.go:ExitPolicy` — Target struct to convert to (created in Task 1)

  **Test References**:
  - `backend/internal/app/strategy/dna_manager_test.go` — TOML parsing test patterns
  - `backend/internal/app/strategy/spec_loader_test.go` — Strategy spec parsing tests

  **External References**:
  - `github.com/BurntSushi/toml` — Already in go.mod, use `toml.Decode(string, &struct)`

  **WHY Each Reference Matters**:
  - `dna_manager.go` TOML struct: Copy the pattern of defining a raw struct for TOML deserialization, then converting to domain type
  - `spec_loader.go` v2 parsing: Shows how `BurntSushi/toml` silently ignores unknown sections (confirmed: safe to add `[position_monitor]`)
  - Existing test patterns: Follow same assertion style and test organization

  **Acceptance Criteria**:
  - [ ] File exists: `backend/internal/app/positionmonitor/config.go`
  - [ ] File exists: `backend/internal/app/positionmonitor/config_test.go`
  - [ ] `cd backend && go test -v -run TestParseConfig ./internal/app/positionmonitor/...` → PASS

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Parse TOML with full [position_monitor] section
    Tool: Bash
    Preconditions: config.go and config_test.go created
    Steps:
      1. Run: cd backend && go test -v -run TestParseConfigFromTOML_Full ./internal/app/positionmonitor/...
      2. Verify test covers: trailing_stop_bps=200, profit_target_bps=500, max_loss_per_pos_bps=150
    Expected Result: All fields parsed correctly
    Failure Indicators: Assertion failure on any field value
    Evidence: .sisyphus/evidence/task-2-config-parse-full.txt

  Scenario: Missing [position_monitor] section returns zero config
    Tool: Bash
    Preconditions: Test includes TOML without [position_monitor]
    Steps:
      1. Run: cd backend && go test -v -run TestParseConfigFromTOML_Missing ./internal/app/positionmonitor/...
      2. Verify Config is all zeros / false
      3. Verify ToExitPolicy().IsEmpty() returns true
    Expected Result: Zero config returned, no error
    Failure Indicators: Error returned or non-zero config values
    Evidence: .sisyphus/evidence/task-2-config-parse-missing.txt
  ```

  **Commit**: YES (group with Task 3)
  - Message: `feat(positionmonitor): add config parser and exit evaluator`
  - Files: `backend/internal/app/positionmonitor/config.go`, `backend/internal/app/positionmonitor/config_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/positionmonitor/...`

- [ ] 3. Pure Exit Condition Evaluator (No I/O)

  **What to do**:
  - Create `backend/internal/app/positionmonitor/evaluator.go`:
    - `type ExitResult struct { ShouldExit bool; Condition domain.ExitConditionType; Reason string }`
    - `func Evaluate(pos domain.TrackedPosition, currentPrice float64, now time.Time, cal domain.TradingCalendar) ExitResult`
      - Pure function — NO side effects, NO I/O, NO mutex, NO broker calls
      - Evaluates exit conditions in order (first trigger wins, short-circuits):
        1. **Max Loss**: `(entryPrice - currentPrice) / entryPrice * 10000 >= MaxLossPerPosBPS`
        2. **End of Day Flatten**: If `EODFlattenEnabled` AND `cal.IsOpen(now)` AND time until session close ≤ `EODFlattenMinutes` minutes. Use `domain.NYSECloseTime(now)` for equities. Skip entirely if `cal` is `Crypto24x7Calendar`.
        3. **Max Holding Time**: `now.Sub(entryTime).Minutes() >= MaxHoldingMinutes`
        4. **Trailing Stop**: `(highWaterMark - currentPrice) / highWaterMark * 10000 >= TrailingStopBPS`
        5. **Profit Target**: `(currentPrice - entryPrice) / entryPrice * 10000 >= ProfitTargetBPS`
        6. **Time-Based Exit**: `now.Sub(entryTime).Minutes() >= TimeExitMinutes` (designed for strategies that want time-decay-like tightening)
      - Each condition only evaluates if its BPS/minutes value > 0 (disabled if 0)
      - Return `ExitResult{ShouldExit: false}` if no conditions trigger
      - Evaluation ORDER matters: max loss and EOD flatten are safety-critical and evaluated first
  - Create `backend/internal/app/positionmonitor/evaluator_test.go`:
    - Test each exit condition independently (6 tests minimum):
      - Trailing stop: entry $100, HWM $110, trail 200bps → exit at $107.80, no exit at $108.00
      - Profit target: entry $100, target 500bps → exit at $105.00, no exit at $104.99
      - Max loss: entry $100, max loss 150bps → exit at $98.50, no exit at $98.51
      - Max holding time: entry 60min ago, max 30min → exit; entry 20min ago → no exit
      - Time-based exit: entry 45min ago, time exit 30min → exit
      - EOD flatten: 4min before close, flatten at 5min → exit; 6min before → no exit; crypto → never
    - Test disabled conditions (BPS = 0 → condition not evaluated)
    - Test all-conditions-disabled → `ShouldExit: false`
    - Test first-trigger-wins (multiple conditions would trigger, verify first one reported)
    - Test crypto skips EOD flatten entirely
    - Use deterministic `time.Date(...)` values, `domain.NYSECalendar{}` and `domain.Crypto24x7Calendar{}`

  **Must NOT do**:
  - Do NOT add mutex, channels, or any concurrency primitives to the evaluator
  - Do NOT call any external services or ports
  - Do NOT use `time.Now()` — `now` is passed as parameter
  - Do NOT add logging — this is a pure function

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Core business logic with precise mathematical calculations in basis points — needs careful implementation and exhaustive testing
  - **Skills**: []
    - No special skills needed — pure Go computation and math

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2)
  - **Blocks**: Tasks 5, 9
  - **Blocked By**: Task 1 (needs domain types)

  **References**:

  **Pattern References**:
  - `backend/internal/app/execution/risk_engine.go` — Basis point calculation patterns (if file exists, search for `BPS` usage)
  - `backend/internal/app/strategy/risk_sizer.go:140-175` — How basis points are applied to price calculations in the sizing logic

  **API/Type References**:
  - `backend/internal/domain/position_monitor.go:ExitPolicy` — Config struct with all BPS fields (created in Task 1)
  - `backend/internal/domain/position_monitor.go:TrackedPosition` — Position state including HWM (created in Task 1)
  - `backend/internal/domain/position_monitor.go:ExitConditionType` — Enum for identifying which condition triggered
  - `backend/internal/domain/exchange_calendar.go:TradingCalendar` — Interface with `IsOpen(time.Time) bool`
  - `backend/internal/domain/exchange_calendar.go:NYSECloseTime(time.Time) time.Time` — Returns session close time for given date
  - `backend/internal/domain/exchange_calendar.go:NYSECalendar` — Equity calendar implementation
  - `backend/internal/domain/exchange_calendar.go:Crypto24x7Calendar` — Crypto calendar (always open, no session close)

  **Test References**:
  - `backend/internal/app/execution/risk_test.go` — Test patterns for risk/basis-point calculations

  **WHY Each Reference Matters**:
  - `risk_sizer.go` BPS calculations: Verify the basis point formula convention (`price * bps / 10000`) used in the codebase
  - `exchange_calendar.go`: MUST use the calendar interface correctly — `NYSECloseTime()` for determining session close, `IsOpen()` for checking if market is open, and detect `Crypto24x7Calendar` by checking if it implements a `Is24x7() bool` method or by type assertion
  - `risk_test.go`: Test assertion patterns for numerical precision

  **Acceptance Criteria**:
  - [ ] File exists: `backend/internal/app/positionmonitor/evaluator.go`
  - [ ] File exists: `backend/internal/app/positionmonitor/evaluator_test.go`
  - [ ] `cd backend && go test -v -run TestEvaluate ./internal/app/positionmonitor/...` → PASS (≥10 test cases)
  - [ ] Each of the 6 exit conditions has at least 2 test cases (trigger + no-trigger)

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Trailing stop calculation with high water mark
    Tool: Bash
    Preconditions: evaluator.go and evaluator_test.go created
    Steps:
      1. Run: cd backend && go test -v -run TestEvaluate_TrailingStop ./internal/app/positionmonitor/...
      2. Verify: entry=$100, HWM=$110, trail=200bps → trigger at $107.80 (110 * (1 - 200/10000))
      3. Verify: price=$108.00 → no trigger
    Expected Result: Both trigger and no-trigger cases pass
    Failure Indicators: Incorrect BPS calculation or wrong exit condition type reported
    Evidence: .sisyphus/evidence/task-3-trailing-stop.txt

  Scenario: EOD flatten skips crypto positions
    Tool: Bash
    Preconditions: evaluator_test.go includes crypto EOD test case
    Steps:
      1. Run: cd backend && go test -v -run TestEvaluate_EODFlatten ./internal/app/positionmonitor/...
      2. Verify: equity position with 4min to close + EOD enabled → triggers exit
      3. Verify: crypto position with same time + EOD enabled → does NOT trigger
    Expected Result: Crypto positions are never EOD-flattened
    Failure Indicators: Crypto position incorrectly triggers EOD exit
    Evidence: .sisyphus/evidence/task-3-eod-flatten.txt

  Scenario: All conditions disabled returns no-exit
    Tool: Bash
    Preconditions: evaluator_test.go includes disabled-conditions test
    Steps:
      1. Run: cd backend && go test -v -run TestEvaluate_AllDisabled ./internal/app/positionmonitor/...
      2. Verify: ExitPolicy{} (all zeros) → ShouldExit=false regardless of price/time
    Expected Result: No exit triggered when all conditions are disabled
    Failure Indicators: ShouldExit returns true with empty policy
    Evidence: .sisyphus/evidence/task-3-all-disabled.txt
  ```

  **Commit**: YES (group with Task 2)
  - Message: `feat(positionmonitor): add config parser and exit evaluator`
  - Files: `backend/internal/app/positionmonitor/evaluator.go`, `backend/internal/app/positionmonitor/evaluator_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/positionmonitor/...`

- [ ] 4. Test Helpers and Mock Structs (shared_test.go)

  **What to do**:
  - Create `backend/internal/app/positionmonitor/shared_test.go`:
    - `mockEventBus` — implements `ports.EventBusPort` with function field overrides:
      ```go
      type mockEventBus struct {
          publishFn    func(ctx context.Context, event domain.Event) error
          subscribeFn  func(ctx context.Context, eventType domain.EventType, handler ports.EventHandler) error
          published     []domain.Event  // captures all published events
          mu           sync.Mutex
      }
      ```
    - `mockBrokerPort` — implements `ports.BrokerPort` with function field overrides:
      ```go
      type mockBrokerPort struct {
          getPositionsFn func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)
          callCount      int
      }
      ```
    - `mockCalendarProvider` — returns the appropriate calendar for an asset class
    - Helper functions:
      - `newTestService(opts ...func(*Service)) *Service` — creates service with mock deps, zerolog.Nop(), deterministic nowFunc
      - `makeFillEvent(tenantID, symbol, side string, qty, price float64, strategy string) domain.Event` — creates a FillReceived event with correct payload map
      - `makeBarEvent(tenantID string, symbol string, close float64) domain.Event` — creates a MarketBarSanitized event
      - `makeTrackedPosition(symbol string, entryPrice float64, policy domain.ExitPolicy) domain.TrackedPosition` — convenience builder
    - Follow EXACTLY the pattern from `backend/internal/app/execution/shared_test.go` — function field overrides, call counters, captured events

  **Must NOT do**:
  - Do NOT use code-generated mocks (gomock, mockery, etc.)
  - Do NOT import test packages from other app packages
  - Do NOT create shared test utilities outside this package

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Boilerplate mock structs following established pattern — mechanical work
  - **Skills**: []
    - No special skills needed

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 5, 6, 7)
  - **Blocks**: Tasks 5, 9
  - **Blocked By**: Task 1 (needs domain types for mock function signatures)

  **References**:

  **Pattern References** (CRITICAL — copy these patterns exactly):
  - `backend/internal/app/execution/shared_test.go` — THE reference for mock struct patterns. Read the entire file. Copy: function field overrides, `publishFn`/`subscribeFn` pattern, call counters, event capture slice, sync.Mutex for captured events
  - `backend/internal/app/risk/circuit_breaker_test.go` — Additional mock patterns for simpler services

  **API/Type References**:
  - `backend/internal/ports/event_bus.go:EventBusPort` — Interface to mock (Publish, Subscribe, Unsubscribe methods)
  - `backend/internal/ports/broker.go:BrokerPort` — Interface to mock (GetPositions, SubmitOrder, etc.)
  - `backend/internal/domain/event.go:Event` — Event struct for captured events
  - `backend/internal/domain/event.go:EventFillReceived` — Event type constant for fill events
  - `backend/internal/domain/event.go:EventMarketBarSanitized` — Event type constant for bar events

  **WHY Each Reference Matters**:
  - `execution/shared_test.go`: This is the CANONICAL mock pattern. Every mock in this project follows this style. Deviating will fail code review.
  - `EventBusPort` interface: Must implement all methods even if most are no-ops (Go interface satisfaction)

  **Acceptance Criteria**:
  - [ ] File exists: `backend/internal/app/positionmonitor/shared_test.go`
  - [ ] `cd backend && go vet ./internal/app/positionmonitor/...` → no errors
  - [ ] Mock structs satisfy their interfaces (compilation check)

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Mock structs compile and satisfy interfaces
    Tool: Bash
    Preconditions: shared_test.go created with all mock structs
    Steps:
      1. Run: cd backend && go vet ./internal/app/positionmonitor/...
      2. Verify zero exit code (interfaces satisfied, no unused imports)
    Expected Result: Clean compilation
    Failure Indicators: "does not implement" errors or vet warnings
    Evidence: .sisyphus/evidence/task-4-mocks-compile.txt
  ```

  **Commit**: YES (group with Task 5)
  - Message: `feat(positionmonitor): add core service with event handlers and reconciliation`
  - Files: `backend/internal/app/positionmonitor/shared_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/positionmonitor/...`

- [ ] 5. Core Service — Lifecycle, Event Handlers, State Management, Reconciliation

  **What to do**:
  - Create `backend/internal/app/positionmonitor/service.go`:
    - **Constructor**: `NewService(bus ports.EventBusPort, broker ports.BrokerPort, log zerolog.Logger, nowFunc func() time.Time) *Service`
      - Required deps: event bus, broker port, logger, nowFunc
      - Functional options for optional deps: `WithMetrics(m *metrics.Metrics)`, `WithReconcileInterval(d time.Duration)`
    - **State**:
      ```go
      type Service struct {
          bus            ports.EventBusPort
          broker         ports.BrokerPort
          log            zerolog.Logger
          nowFunc        func() time.Time
          metrics        *metrics.Metrics
          reconcileEvery time.Duration  // default 30s
          mu             sync.Mutex
          positions      map[string]*domain.TrackedPosition  // keyed by "tenantID:envMode:symbol"
          policies       map[string]domain.ExitPolicy         // keyed by strategy name
          calendarFor    func(domain.AssetClass) domain.TradingCalendar  // default: domain.CalendarFor
      }
      ```
    - **Lifecycle**: `Start(ctx context.Context) error`
      1. Subscribe to `domain.EventFillReceived` → `handleFill`
      2. Subscribe to `domain.EventMarketBarSanitized` → `handleBar`
      3. Launch reconciliation goroutine: `go s.reconcileLoop(ctx)`
      4. Log startup
    - **RegisterPolicy(strategy string, policy domain.ExitPolicy)** — called during wiring to register per-strategy exit policies
    - **handleFill(ctx, event)**:
      1. Filter by TenantID (if service is per-account, TenantID should match)
      2. Parse fill payload using `parseFillPayload()` helper
      3. If BUY fill: create `TrackedPosition` with state=TRACKING, set entryPrice, entryTime, HWM=entryPrice, look up ExitPolicy by strategy name
      4. If SELL fill: find position by key, set state=CLOSED, remove from map
      5. Lock `s.mu` around all state mutations
    - **handleBar(ctx, event)**:
      1. Extract `domain.MarketBar` from payload
      2. Lock `s.mu`
      3. For each tracked position matching the bar's symbol:
         a. Update `HighWaterMark` if bar.Close > current HWM
         b. Call `Evaluate(pos, bar.Close, s.nowFunc(), calendar)` (from evaluator.go)
         c. If `ShouldExit`: set state=EXITING, emit exit intent, log
      4. Unlock
      5. IMPORTANT: Keep handler fast — O(positions) per bar, no I/O inside lock
    - **emitExitIntent(ctx, pos, result)**:
      1. Create `domain.OrderIntent` using `domain.NewOrderIntent()`:
         - Direction: `domain.DirectionCloseLong` (ALWAYS — hardcode this, never dynamic)
         - LimitPrice: current market price (from bar close)
         - StopLoss: current market price (synthetic — execution skips risk checks for exits anyway)
         - Quantity: 0 (execution service resolves from broker)
         - Strategy: pos.Strategy
         - IdempotencyKey: `fmt.Sprintf("posmon:%s:%s:%s:%d", pos.TenantID, pos.Symbol, result.Condition, pos.EntryTime.Unix())`
         - Confidence: 1.0
         - Rationale: `fmt.Sprintf("position monitor: %s - %s", result.Condition, result.Reason)`
      2. Wrap in `domain.NewEvent()` with type `domain.EventOrderIntentCreated`
      3. `s.bus.Publish(ctx, event)`
    - **parseFillPayload(payload any) (fillData, error)**:
      - Extract from `map[string]any`: symbol (string), side (string), quantity (float64), price (float64), strategy (string)
      - Use careful type assertions with comma-ok pattern for each field
      - Return error if any required field missing or wrong type
    - **reconcileLoop(ctx context.Context)**:
      1. Ticker at `s.reconcileEvery` (default 30s)
      2. On tick: call `s.broker.GetPositions(ctx, tenantID, envMode)` for each known tenant
      3. Compare broker positions with tracked positions:
         - Position in broker but not tracked: add with state=TRACKING (bootstrap case)
         - Position tracked but not in broker: remove (position was closed externally)
         - Quantity mismatch: update tracked quantity
      4. Log discrepancies at WARN level
    - **Bootstrap on Start**: Before subscribing, call `broker.GetPositions()` to seed initial state
  - Create `backend/internal/app/positionmonitor/service_test.go` (basic tests — integration tests in Task 9):
    - Test `handleFill` with BUY → position tracked
    - Test `handleFill` with SELL → position removed
    - Test `handleBar` triggers exit when trailing stop breached
    - Test `handleBar` does NOT trigger exit when position in EXITING state
    - Test `parseFillPayload` with valid and invalid payloads
    - Test `emitExitIntent` produces correct event structure
    - All tests use `memory.NewBus()`, `zerolog.Nop()`, mock broker, deterministic time

  **Must NOT do**:
  - Do NOT call `broker.GetPositions()` inside `handleBar` — only in reconcileLoop
  - Do NOT use `time.Now()` anywhere — always `s.nowFunc()`
  - Do NOT emit `DirectionLong` or `DirectionShort` — only `DirectionCloseLong`
  - Do NOT modify `positions` map outside of mutex lock
  - Do NOT add database persistence for positions
  - Do NOT calculate P&L

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Core service with concurrent state management, event handling, reconciliation loop, and multiple integration points — highest complexity task
  - **Skills**: [`testing-patterns`]
    - `testing-patterns`: Needed for proper mock wiring, event testing with memory bus, and TDD-style service construction

  **Parallelization**:
  - **Can Run In Parallel**: NO (depends on Wave 1 outputs)
  - **Parallel Group**: Wave 2 (with Tasks 4, 6, 7 — but depends on 1, 2, 3, 4)
  - **Blocks**: Tasks 8, 9
  - **Blocked By**: Tasks 1, 2, 3, 4

  **References**:

  **Pattern References** (CRITICAL — follow these exactly):
  - `backend/internal/app/execution/service.go:1-80` — Constructor pattern: required deps as params, functional options, field initialization
  - `backend/internal/app/execution/service.go:100-230` — Event handler pattern: extract payload, validate, act, emit result event
  - `backend/internal/app/risk/circuit_breaker.go:1-50` — `nowFunc` injection pattern and `sync.Mutex` state protection
  - `backend/internal/app/perf/ledger_writer.go` — Search for `Start(ctx)` method: subscribe pattern and position key format
  - `backend/internal/app/monitor/service.go` — Mutex-guarded state maps pattern for concurrent access

  **API/Type References**:
  - `backend/internal/domain/entity.go:125-165` — `NewOrderIntent()` constructor — all required params and validation (StopLoss > 0 required)
  - `backend/internal/domain/entity.go:50-90` — `OrderIntent` struct fields
  - `backend/internal/domain/entity.go:300-370` — `MarketBar` struct (payload of EventMarketBarSanitized)
  - `backend/internal/domain/event.go` — `domain.NewEvent()` constructor, all EventType constants
  - `backend/internal/domain/value.go:34` — `DirectionCloseLong` constant
  - `backend/internal/domain/value.go:40-41` — `IsExit()` method
  - `backend/internal/domain/exchange_calendar.go` — `CalendarFor(assetClass)` factory function
  - `backend/internal/ports/event_bus.go` — `EventBusPort` interface with Subscribe/Publish
  - `backend/internal/ports/broker.go` — `BrokerPort` interface with `GetPositions()`

  **Test References**:
  - `backend/internal/app/execution/service_test.go` — How to test event-driven services: publish event, assert handler behavior
  - `backend/internal/app/execution/shared_test.go:137-210` — `makeIntent()` and `createExitOrderIntentEvent()` helpers for creating test events
  - `backend/internal/app/risk/circuit_breaker_test.go` — Testing with deterministic nowFunc

  **External References**:
  - `github.com/rs/zerolog` — Logging: `zerolog.Nop()` for tests, `.With().Str("component", "position_monitor").Logger()` for prod
  - `github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory` — `memory.NewBus()` for synchronous testing

  **WHY Each Reference Matters**:
  - `execution/service.go` constructor: MUST follow identical pattern — required deps as constructor params, optional deps via `With*` options
  - `execution/service.go` handler: Shows how to extract typed payloads from `event.Payload`, how to emit follow-up events, error handling within handlers
  - `circuit_breaker.go` nowFunc: Exact pattern for time injection — `s.nowFunc()` not `time.Now()`
  - `NewOrderIntent` constructor: StopLoss MUST be > 0 — use current market price as synthetic value for exit intents
  - `memory.NewBus()`: Synchronous execution means tests can assert immediately after publish without waiting

  **Acceptance Criteria**:
  - [ ] File exists: `backend/internal/app/positionmonitor/service.go`
  - [ ] File exists: `backend/internal/app/positionmonitor/service_test.go`
  - [ ] `cd backend && go test -v ./internal/app/positionmonitor/...` → PASS (all tests)
  - [ ] `cd backend && go vet ./internal/app/positionmonitor/...` → no errors
  - [ ] Service subscribes to EventFillReceived and EventMarketBarSanitized
  - [ ] Exit intents use DirectionCloseLong exclusively

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: BUY fill creates tracked position
    Tool: Bash
    Preconditions: service.go, shared_test.go, service_test.go exist
    Steps:
      1. Run: cd backend && go test -v -run TestHandleFill_Buy ./internal/app/positionmonitor/...
      2. Verify: after BUY fill event, position appears in service.positions map
      3. Verify: position has correct entryPrice, entryTime, state=TRACKING
    Expected Result: Position tracked with correct fields
    Failure Indicators: Position not found or wrong field values
    Evidence: .sisyphus/evidence/task-5-fill-buy.txt

  Scenario: Bar triggers trailing stop exit
    Tool: Bash
    Preconditions: Position tracked, trailing stop BPS configured
    Steps:
      1. Run: cd backend && go test -v -run TestHandleBar_TrailingStop ./internal/app/positionmonitor/...
      2. Verify: EventOrderIntentCreated emitted with DirectionCloseLong
      3. Verify: IdempotencyKey contains "posmon:" prefix
      4. Verify: position state changed to EXITING
    Expected Result: Exit intent emitted, position marked as EXITING
    Failure Indicators: No event emitted, wrong direction, or position still TRACKING
    Evidence: .sisyphus/evidence/task-5-bar-trailing-stop.txt

  Scenario: EXITING position does not trigger duplicate exit
    Tool: Bash
    Preconditions: Position already in EXITING state
    Steps:
      1. Run: cd backend && go test -v -run TestHandleBar_ExitingSkipped ./internal/app/positionmonitor/...
      2. Verify: no new EventOrderIntentCreated emitted for EXITING position
    Expected Result: Bar processing skips EXITING positions
    Failure Indicators: Duplicate exit intent emitted
    Evidence: .sisyphus/evidence/task-5-exiting-skip.txt

  Scenario: parseFillPayload handles malformed payload gracefully
    Tool: Bash
    Preconditions: service_test.go includes malformed payload test
    Steps:
      1. Run: cd backend && go test -v -run TestParseFillPayload ./internal/app/positionmonitor/...
      2. Verify: missing "symbol" key returns error
      3. Verify: wrong type for "quantity" returns error
      4. Verify: valid payload returns correct fillData struct
    Expected Result: Graceful error handling for all malformed cases
    Failure Indicators: Panic on type assertion or missing error return
    Evidence: .sisyphus/evidence/task-5-parse-fill.txt
  ```

  **Commit**: YES (group with Task 4)
  - Message: `feat(positionmonitor): add core service with event handlers and reconciliation`
  - Files: `backend/internal/app/positionmonitor/service.go`, `backend/internal/app/positionmonitor/service_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/positionmonitor/...`

- [ ] 6. Add PositionMonitor Field to AccountHandle

  **What to do**:
  - Modify `backend/internal/app/orchestrator/orchestrator.go`:
    - Add import for `positionmonitor` package: `"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"`
    - Add field to `AccountHandle` struct (line ~52, after `SymbolRouter`):
      ```go
      PositionMonitor *positionmonitor.Service
      ```
    - In `startAccount()` method (line ~146), add after SymbolRouter start:
      ```go
      if h.PositionMonitor != nil {
          if err := h.PositionMonitor.Start(ctx); err != nil {
              return fmt.Errorf("orchestrator: tenant %q position monitor start: %w", h.TenantID, err)
          }
      }
      ```
    - In metrics propagation block (line ~150), add:
      ```go
      if h.PositionMonitor != nil {
          h.PositionMonitor.SetMetrics(o.shared.Metrics)
      }
      ```
  - This is a minimal change — only 3 additions to an existing file

  **Must NOT do**:
  - Do NOT modify any other methods in the orchestrator
  - Do NOT change the orchestrator constructor or SharedDeps
  - Do NOT add PositionMonitor to Stop() — context cancellation handles cleanup

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 3 small additions to an existing file — trivial modification
  - **Skills**: []
    - No special skills needed

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 5, 7)
  - **Blocks**: Task 8
  - **Blocked By**: None (just adds a typed field — can be done before service exists if import path is correct)

  **References**:

  **Pattern References**:
  - `backend/internal/app/orchestrator/orchestrator.go:44-62` — `AccountHandle` struct showing existing field pattern (Execution, LedgerWriter, etc.)
  - `backend/internal/app/orchestrator/orchestrator.go:146-200` — `startAccount()` method showing service start ordering and error handling
  - `backend/internal/app/orchestrator/orchestrator.go:150-163` — Metrics propagation pattern (nil-check + SetMetrics)

  **WHY Each Reference Matters**:
  - `AccountHandle` struct: Add new field in the same style — pointer to service, same naming convention
  - `startAccount()`: Start order matters — position monitor should start AFTER execution and ledger (it depends on the event bus being ready)

  **Acceptance Criteria**:
  - [ ] `AccountHandle` has `PositionMonitor *positionmonitor.Service` field
  - [ ] `startAccount()` starts PositionMonitor if non-nil
  - [ ] Metrics propagated to PositionMonitor if non-nil
  - [ ] `cd backend && go build ./cmd/omo-core/` → compiles (may need Task 5 first)

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Orchestrator compiles with new field
    Tool: Bash
    Preconditions: orchestrator.go modified, positionmonitor package exists
    Steps:
      1. Run: cd backend && go vet ./internal/app/orchestrator/...
      2. Run: cd backend && go test ./internal/app/orchestrator/...
    Expected Result: No compilation errors, existing tests still pass
    Failure Indicators: Import cycle, missing type, or test regression
    Evidence: .sisyphus/evidence/task-6-orchestrator-compile.txt
  ```

  **Commit**: YES (group with Tasks 7, 8)
  - Message: `feat(core): wire position monitor into orchestrator and main`
  - Files: `backend/internal/app/orchestrator/orchestrator.go`
  - Pre-commit: `cd backend && go build ./cmd/omo-core/`

- [ ] 7. Add New Event Type Constants to domain/event.go

  **What to do**:
  - Modify `backend/internal/domain/event.go`:
    - Add 2 new event type constants in the existing const block:
      ```go
      EventPositionMonitorExitTriggered EventType = "PositionMonitorExitTriggered"
      EventPositionMonitorReconciled    EventType = "PositionMonitorReconciled"
      ```
    - `EventPositionMonitorExitTriggered`: Emitted when the monitor triggers an exit (for notification/audit purposes, after the OrderIntentCreated)
    - `EventPositionMonitorReconciled`: Emitted after each reconciliation cycle (for observability)
    - These are INFORMATIONAL events — the actual exit uses the existing `EventOrderIntentCreated`
  - No test changes needed — these are just constants

  **Must NOT do**:
  - Do NOT modify existing event type constants
  - Do NOT change the Event struct
  - Do NOT add payload types here (payloads are defined in the service)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Adding 2 string constants to an existing const block — trivial
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 5, 6)
  - **Blocks**: Tasks 5, 8
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `backend/internal/domain/event.go:10-50` — Existing EventType constants (35+ already defined). Add new ones in alphabetical position or at the end of the block.

  **WHY Each Reference Matters**:
  - Existing constants: Follow exact naming convention `Event{Component}{Action}`

  **Acceptance Criteria**:
  - [ ] `EventPositionMonitorExitTriggered` constant exists in event.go
  - [ ] `EventPositionMonitorReconciled` constant exists in event.go
  - [ ] `cd backend && go build ./internal/domain/...` → compiles

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: New event constants compile
    Tool: Bash
    Preconditions: event.go modified with new constants
    Steps:
      1. Run: cd backend && go vet ./internal/domain/...
      2. Run: cd backend && go test ./internal/domain/...
    Expected Result: Compilation success, existing tests unaffected
    Failure Indicators: Duplicate constant, syntax error, or test regression
    Evidence: .sisyphus/evidence/task-7-event-constants.txt
  ```

  **Commit**: YES (group with Task 1)
  - Message: `feat(domain): add position monitor types and event constants`
  - Files: `backend/internal/domain/event.go`
  - Pre-commit: `cd backend && go test ./internal/domain/...`

- [ ] 8. Wire Position Monitor into main.go

  **What to do**:
  - Modify `backend/cmd/omo-core/main.go` to create and wire a `positionmonitor.Service` instance:
    - Add import: `"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"`
    - **Single-account path** (v2 pipeline, lines ~207-322): After creating `riskSizer` (line 293), create the position monitor:
      ```go
      posMonLog := log.With().Str("component", "position_monitor").Logger()
      posMon := positionmonitor.NewService(
          eventBus,
          alpacaAdapter, // BrokerPort for reconciliation
          time.Now,
          posMonLog,
      )
      ```
    - After the `dnaPaths` loop (line ~174-187), iterate over loaded TOML files to parse `[position_monitor]` sections and register policies:
      ```go
      for _, p := range dnaPaths {
          policy, err := positionmonitor.ParsePolicyFromTOML(p)
          if err != nil {
              // ParsePolicyFromTOML returns nil policy + nil err if section absent
              log.Warn().Err(err).Str("path", p).Msg("failed to parse position monitor config")
              continue
          }
          if policy != nil {
              posMon.RegisterPolicy(policy.StrategyID, policy)
              log.Info().Str("strategy", policy.StrategyID).Msg("position monitor: policy registered")
          }
      }
      ```
    - In service start block (line ~429+), add after `executionSvc.Start(ctx)`:
      ```go
      if posMon != nil {
          if err := posMon.Start(ctx); err != nil {
              log.Fatal().Err(err).Msg("failed to start position monitor")
          }
      }
      ```
    - **Multi-account path** (lines ~342-412): Inside the `for _, acct := range accounts` loop, create a per-account position monitor:
      ```go
      acctPosMon := positionmonitor.NewService(
          eventBus,
          acctAdapter,
          time.Now,
          acctLog.With().Str("component", "position_monitor").Logger(),
      )
      // Register policies from same dnaPaths loop
      for _, p := range dnaPaths {
          policy, err := positionmonitor.ParsePolicyFromTOML(p)
          if err != nil { continue }
          if policy != nil {
              acctPosMon.RegisterPolicy(policy.StrategyID, policy)
          }
      }
      ```
    - Add to `AccountHandle` initialization (line ~394-407):
      ```go
      PositionMonitor: acctPosMon,
      ```
  - The position monitor MUST be started AFTER execution service (it depends on the event bus being ready and execution handling exit intents)

  **Must NOT do**:
  - Do NOT create a global shared position monitor for multi-account — each account gets its own instance
  - Do NOT start position monitor before execution service
  - Do NOT modify any existing service creation — only ADD the new service
  - Do NOT add config fields to the app `Config` struct — position monitor config comes from strategy TOML files

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Modifying a 1032-line main.go requires care — must add in correct locations, respect start ordering, handle both single-account and multi-account paths
  - **Skills**: []
    - No special skills needed — Go wiring pattern is straightforward
  - **Skills Evaluated but Omitted**:
    - `senior-backend`: Not needed — this is wiring, not designing new architecture

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (sequential — depends on Tasks 2, 5, 6, 7)
  - **Blocks**: Task 9, Task 11
  - **Blocked By**: Task 2 (config parser), Task 5 (service), Task 6 (AccountHandle field), Task 7 (event constants)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/main.go:100-146` — Single-account service creation pattern: logger → dependencies → NewService() → options
  - `backend/cmd/omo-core/main.go:342-412` — Multi-account loop: per-account adapter, equity, services, AccountHandle wiring
  - `backend/cmd/omo-core/main.go:394-407` — AccountHandle initialization: exact field names and ordering
  - `backend/cmd/omo-core/main.go:429-485` — Service start ordering: ingestion → monitor → ledger → execution → strategy → orchestrator → notification
  - `backend/cmd/omo-core/main.go:174-187` — DNA paths glob loop: iterates over `configs/strategies/*.toml` files

  **API/Type References**:
  - `backend/internal/app/positionmonitor/service.go:NewService()` — Constructor signature (created in Task 5)
  - `backend/internal/app/positionmonitor/config.go:ParsePolicyFromTOML()` — Config parser (created in Task 2)
  - `backend/internal/app/orchestrator/orchestrator.go:AccountHandle` — Struct with PositionMonitor field (modified in Task 6)

  **WHY Each Reference Matters**:
  - `main.go:100-146` single-account: Follow this exact pattern — create logger, then service, then options. Position monitor goes after execution service creation.
  - `main.go:342-412` multi-account loop: Each account gets its own adapter, equity, and services. Position monitor must follow same pattern — one per account, using acctAdapter.
  - `main.go:394-407` AccountHandle: Add PositionMonitor field in same style as existing fields (last position).
  - `main.go:429-485` start ordering: Position monitor must start AFTER executionSvc (line 445-447) because it emits EventOrderIntentCreated that execution handles.
  - `main.go:174-187` DNA paths: Reuse same glob result to parse position monitor configs — no need for a separate glob.

  **Acceptance Criteria**:
  - [ ] `cd backend && go build ./cmd/omo-core/` → compiles with zero errors
  - [ ] Position monitor created in single-account v2 path
  - [ ] Position monitor created per-account in multi-account path
  - [ ] Position monitor started AFTER execution service
  - [ ] Policies parsed from strategy TOML files and registered
  - [ ] AccountHandle includes PositionMonitor field assignment

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Full binary compiles with position monitor wired
    Tool: Bash
    Preconditions: All Tasks 1-7 complete, main.go modified
    Steps:
      1. Run: cd backend && go build -o /dev/null ./cmd/omo-core/
      2. Run: cd backend && go vet ./cmd/omo-core/...
    Expected Result: Exit code 0, no compilation errors
    Failure Indicators: Import cycle, missing method, wrong type
    Evidence: .sisyphus/evidence/task-8-build.txt

  Scenario: Multi-account path includes PositionMonitor in AccountHandle
    Tool: Bash
    Preconditions: main.go modified
    Steps:
      1. Run: cd backend && grep -n 'PositionMonitor' cmd/omo-core/main.go
      2. Verify: at least 2 occurrences — one in AccountHandle init, one in service creation
      3. Run: cd backend && grep -A2 'PositionMonitor:' cmd/omo-core/main.go
      4. Verify: field assignment uses acctPosMon (per-account instance, not shared)
    Expected Result: Per-account position monitor wired into AccountHandle
    Failure Indicators: Single shared instance, missing from multi-account path
    Evidence: .sisyphus/evidence/task-8-multi-account.txt

  Scenario: Service start order is correct
    Tool: Bash
    Preconditions: main.go modified
    Steps:
      1. Run: cd backend && grep -n 'Start(ctx)' cmd/omo-core/main.go | head -20
      2. Verify: position monitor Start() appears AFTER executionSvc.Start()
    Expected Result: Position monitor starts after execution service
    Failure Indicators: posMon.Start before executionSvc.Start in line ordering
    Evidence: .sisyphus/evidence/task-8-start-order.txt
  ```

  **Commit**: YES (group with Task 6)
  - Message: `feat(core): wire position monitor into orchestrator and main`
  - Files: `backend/cmd/omo-core/main.go`
  - Pre-commit: `cd backend && go build ./cmd/omo-core/`

- [ ] 9. Integration Tests — Full Lifecycle, Multi-Tenant Isolation, Idempotency

  **What to do**:
  - Create/extend `backend/internal/app/positionmonitor/service_test.go` with integration-level test cases:
    - **Full position lifecycle test**: BUY fill → position tracked → bars arrive → high-water mark updates → bar triggers trailing stop → EventOrderIntentCreated emitted → SELL fill → position removed from state
    - **Multi-tenant isolation test**: Two tenants each get a BUY fill for the same symbol. Verify each tenant's position tracked independently. Trigger exit for tenant A — verify tenant B's position unaffected.
    - **Idempotency test**: Trigger an exit condition → verify EventOrderIntentCreated emitted → trigger same condition again → verify NO duplicate event emitted (position is in EXITING state)
    - **All 6 exit conditions end-to-end**: For each exit condition (trailing stop, time-based exit, profit target, max holding time, max loss, EOD flatten), create a test that:
      1. Creates a service with a policy containing only that exit condition
      2. Publishes a BUY fill event
      3. Publishes bars that should (or should not) trigger the condition
      4. Asserts the correct EventOrderIntentCreated is emitted (or not)
    - **Reconciliation test**: Start service → publish BUY fill → verify tracked. Mock broker returns empty positions → reconciliation runs → verify position removed (CLOSED state). Mock broker returns position still open after EXITING → verify state reset to TRACKING.
    - **Startup bootstrap test**: Configure mock broker to return 2 existing positions. Start service. Verify positions are seeded into state without needing fill events.
  - Use `memory.NewBus()` for synchronous event testing (all assertions immediate after publish)
  - Use shared test helpers from Task 4 (`shared_test.go`)
  - Use deterministic `nowFunc` to control time progression (e.g., advance 30 minutes for max holding time test)

  **Must NOT do**:
  - Do NOT test broker adapter behavior — only test through mock ports
  - Do NOT create additional mock files — use the shared_test.go helpers from Task 4
  - Do NOT test evaluator logic in isolation here — those are unit tests in evaluator_test.go (Task 3)

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Integration tests require careful orchestration of events, state transitions, and timing — need deep reasoning to get multi-step scenarios right
  - **Skills**: [`testing-patterns`]
    - `testing-patterns`: Test factory functions, mocking strategies, TDD patterns — directly applicable to creating integration test scenarios
  - **Skills Evaluated but Omitted**:
    - `senior-backend`: Integration tests are test code, not backend architecture

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (after Tasks 1-7, can run alongside Task 8 if service.go is complete)
  - **Blocks**: Task 11
  - **Blocked By**: Task 3 (evaluator), Task 4 (test helpers), Task 5 (service)

  **References**:

  **Pattern References**:
  - `backend/internal/app/execution/service_test.go` — Integration test pattern: create service with mocks → publish events → assert outcomes. Shows how to verify events emitted via the bus.
  - `backend/internal/app/execution/shared_test.go:137-210` — `makeIntent()` and `createExitOrderIntentEvent()` helpers for constructing test events
  - `backend/internal/app/risk/circuit_breaker_test.go` — Time-based testing: uses `nowFunc` to advance time deterministically, verifies state transitions
  - `backend/internal/app/positionmonitor/shared_test.go` — Test helpers created in Task 4: mock bus, mock broker, helper functions for creating fill events and bars

  **API/Type References**:
  - `backend/internal/app/positionmonitor/service.go:NewService()` — Constructor with eventBus, brokerPort, nowFunc, log
  - `backend/internal/app/positionmonitor/service.go:RegisterPolicy()` — How to configure per-strategy exit policies
  - `backend/internal/domain/entity.go:MarketBar` — Bar struct shape for mock data
  - `backend/internal/domain/value.go:DirectionCloseLong` — Exit direction constant used in assertions
  - `backend/internal/domain/event.go:EventOrderIntentCreated` — Event type to assert on

  **WHY Each Reference Matters**:
  - `execution/service_test.go`: Shows the canonical pattern for testing event-driven services — publish input event, capture output events, assert. Follow this exact pattern.
  - `circuit_breaker_test.go`: Critical for time-based tests (max holding time, EOD flatten) — shows how to inject a `nowFunc` that returns controlled values and advance time between operations.
  - `shared_test.go` (Task 4): Provides ready-made helpers — don't recreate mock structs or event factories. Import and use.

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -v -run TestIntegration ./internal/app/positionmonitor/...` → all PASS
  - [ ] Full lifecycle test (BUY → bars → exit → SELL → removal) passes
  - [ ] Multi-tenant isolation test passes — tenant A exit doesn't affect tenant B
  - [ ] Idempotency test passes — no duplicate exit intents
  - [ ] All 6 exit conditions have dedicated end-to-end test cases
  - [ ] Reconciliation test passes — broker-state sync works correctly
  - [ ] Startup bootstrap test passes — existing positions seeded on Start()
  - [ ] `cd backend && go test -race ./internal/app/positionmonitor/...` → no races

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Full lifecycle — BUY through exit to cleanup
    Tool: Bash
    Preconditions: All positionmonitor source files exist, service_test.go updated
    Steps:
      1. Run: cd backend && go test -v -run TestIntegration_FullLifecycle ./internal/app/positionmonitor/...
      2. Verify output contains: position tracked, HWM updated, exit triggered, position closed
      3. Verify exit event payload contains DirectionCloseLong
    Expected Result: PASS — complete lifecycle from entry to cleanup
    Failure Indicators: Position stuck in TRACKING, no exit event, position not removed after SELL fill
    Evidence: .sisyphus/evidence/task-9-full-lifecycle.txt

  Scenario: Multi-tenant isolation verified
    Tool: Bash
    Preconditions: service_test.go includes multi-tenant test
    Steps:
      1. Run: cd backend && go test -v -run TestIntegration_MultiTenant ./internal/app/positionmonitor/...
      2. Verify: tenant A exit does not trigger exit for tenant B
      3. Verify: each tenant's position count is independent
    Expected Result: PASS — tenants fully isolated
    Failure Indicators: Cross-tenant event leakage, shared state mutation
    Evidence: .sisyphus/evidence/task-9-multi-tenant.txt

  Scenario: Idempotency — no duplicate exits
    Tool: Bash
    Preconditions: service_test.go includes idempotency test
    Steps:
      1. Run: cd backend && go test -v -run TestIntegration_Idempotency ./internal/app/positionmonitor/...
      2. Verify: exactly 1 EventOrderIntentCreated emitted despite multiple triggering bars
    Expected Result: PASS — single exit intent per position
    Failure Indicators: 2+ exit intents for same position, or panic on duplicate
    Evidence: .sisyphus/evidence/task-9-idempotency.txt

  Scenario: Race condition detection
    Tool: Bash
    Preconditions: All tests exist and pass individually
    Steps:
      1. Run: cd backend && go test -race -count=3 ./internal/app/positionmonitor/...
    Expected Result: PASS — no race conditions detected across 3 runs
    Failure Indicators: "DATA RACE" in output
    Evidence: .sisyphus/evidence/task-9-race.txt
  ```

  **Commit**: YES (group with Task 10)
  - Message: `test(positionmonitor): add integration tests and example config`
  - Files: `backend/internal/app/positionmonitor/service_test.go`
  - Pre-commit: `cd backend && go test ./internal/app/positionmonitor/...`

- [ ] 10. Add [position_monitor] Example Section to Strategy TOML Files

  **What to do**:
  - Add a `[position_monitor]` section to `configs/strategies/avwap.toml` with example configuration:
    ```toml
    [position_monitor]
    enabled = true
    trailing_stop_bps = 50           # 50 basis points = 0.5% trailing stop
    profit_target_bps = 200          # 200 basis points = 2% profit target
    max_loss_bps = 100               # 100 basis points = 1% max loss per position
    max_holding_minutes = 120        # 2 hours max holding time
    eod_flatten = true               # Flatten all positions before market close
    eod_flatten_minutes_before = 5   # 5 minutes before close
    time_exit_after_minutes = 60     # Exit if position held > 60 mins without profit target
    ```
  - Add a commented-out (disabled) example to any other TOML files in `configs/strategies/` to show the format:
    ```toml
    # [position_monitor]
    # enabled = false
    # trailing_stop_bps = 50
    # profit_target_bps = 200
    # max_loss_bps = 100
    # max_holding_minutes = 120
    # eod_flatten = true
    ```
  - This is a documentation/config task — no Go code changes
  - The values chosen should be reasonable defaults for the AVWAP strategy (equity, intraday)

  **Must NOT do**:
  - Do NOT modify the existing `[params]`, `[strategy]`, `[routing]`, `[lifecycle]`, `[regime_filter]`, or `[hooks]` sections
  - Do NOT add position_monitor fields to the `[params]` section — it MUST be a separate top-level `[position_monitor]` section
  - Do NOT change the `schema_version` — the position monitor config is orthogonal to strategy schema version

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Adding a TOML section to 1-2 files — trivial modification
  - **Skills**: []
    - No special skills needed

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Task 8 and Task 9, since it's independent config)
  - **Blocks**: Task 11
  - **Blocked By**: Task 2 (config parser must define the expected fields — but the TOML section can be added independently since TOML ignores unknown sections)

  **References**:

  **Pattern References**:
  - `configs/strategies/avwap.toml` — Current file structure: `schema_version`, `[strategy]`, `[lifecycle]`, `[routing]`, `[params]`, `[regime_filter]`, `[hooks]`. New section goes at the end.

  **API/Type References**:
  - `backend/internal/app/positionmonitor/config.go:ParsePolicyFromTOML()` — The parser (Task 2) defines what field names and types are expected. Field names in TOML must match the struct tags.

  **WHY Each Reference Matters**:
  - `avwap.toml`: Must see existing section ordering and style (no trailing comments on values, consistent spacing). New `[position_monitor]` section goes after `[hooks]` to maintain logical grouping (hooks = execution config, position_monitor = risk config).
  - `config.go`: Field names in TOML must exactly match the parser's struct tags. If parser expects `trailing_stop_bps`, don't write `trailingStopBps`.

  **Acceptance Criteria**:
  - [ ] `configs/strategies/avwap.toml` contains `[position_monitor]` section with `enabled = true`
  - [ ] All field names match what `ParsePolicyFromTOML()` expects
  - [ ] Values are in basis points (matching project convention)
  - [ ] `BurntSushi/toml` can parse the file: `cd backend && go run -exec 'echo' ./cmd/omo-core/` doesn't error on TOML load
  - [ ] Existing sections (`[strategy]`, `[params]`, etc.) are unchanged

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: TOML file is valid after adding position_monitor section
    Tool: Bash
    Preconditions: avwap.toml modified
    Steps:
      1. Run: cd backend && go test -v -run TestParsePolicyFromTOML ./internal/app/positionmonitor/...
      2. The config parser test (Task 2) should be able to parse avwap.toml successfully
      3. Run: cd backend && python3 -c "import tomllib; tomllib.load(open('../configs/strategies/avwap.toml', 'rb'))" 2>&1 || echo 'TOML parse error'
    Expected Result: TOML parses without errors, all fields extracted correctly
    Failure Indicators: Parse error, missing required field, wrong field type
    Evidence: .sisyphus/evidence/task-10-toml-parse.txt

  Scenario: Existing TOML sections are unmodified
    Tool: Bash
    Preconditions: avwap.toml modified
    Steps:
      1. Run: cd backend && git diff ../configs/strategies/avwap.toml
      2. Verify: only additions at end of file, no modifications to existing sections
    Expected Result: diff shows only additions (+ lines), no deletions or modifications
    Failure Indicators: Modified lines in [strategy], [params], [routing], etc.
    Evidence: .sisyphus/evidence/task-10-diff.txt
  ```

  **Commit**: YES (group with Task 9)
  - Message: `test(positionmonitor): add integration tests and example config`
  - Files: `configs/strategies/avwap.toml`
  - Pre-commit: `cd backend && go test ./internal/app/positionmonitor/...`

- [ ] 11. Full Build, Vet, and Test Verification

  **What to do**:
  - This is a verification-only task — no code changes, only running commands and asserting results
  - Run the complete verification suite to confirm everything works together:
    1. `cd backend && go vet ./...` — All packages pass vet (not just positionmonitor)
    2. `cd backend && go build -o /dev/null ./cmd/omo-core/` — Binary compiles cleanly
    3. `cd backend && go test ./...` — ALL tests pass (including existing tests not related to position monitor)
    4. `cd backend && go test -race ./internal/app/positionmonitor/...` — No race conditions
    5. `cd backend && go test -v ./internal/app/positionmonitor/... | grep -c 'PASS\|FAIL'` — Count test cases, expect 15+ PASS and 0 FAIL
  - If any command fails, investigate and fix the issue (likely in the files created by Tasks 1-10)
  - This task acts as a gate before the Final Verification Wave

  **Must NOT do**:
  - Do NOT skip running `go test ./...` (full suite) — position monitor must not break existing tests
  - Do NOT add new source files — this is verification only
  - Do NOT fix issues by disabling tests or adding `// nolint` directives

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: If failures occur, debugging requires reading error messages, tracing through multiple files, and understanding how the new code interacts with existing code
  - **Skills**: [`testing-patterns`]
    - `testing-patterns`: Helps debug test failures if any occur during full-suite run
  - **Skills Evaluated but Omitted**:
    - `senior-backend`: Not debugging architecture, just running verification commands

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 4 (sequential — must run after ALL other tasks)
  - **Blocks**: Final Verification Wave (F1-F4)
  - **Blocked By**: Tasks 1-10 (all must be complete)

  **References**:

  **Pattern References**:
  - All files created/modified in Tasks 1-10 — this task validates the complete set

  **WHY Each Reference Matters**:
  - This is a meta-verification task. References are implicit — the entire codebase is the reference. The agent should follow error messages to the relevant files if any test fails.

  **Acceptance Criteria**:
  - [ ] `cd backend && go vet ./...` → exit 0
  - [ ] `cd backend && go build -o /dev/null ./cmd/omo-core/` → exit 0
  - [ ] `cd backend && go test ./...` → ALL PASS (zero failures)
  - [ ] `cd backend && go test -race ./internal/app/positionmonitor/...` → no races
  - [ ] Position monitor tests: 15+ test cases passing

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Full project builds and passes all tests
    Tool: Bash
    Preconditions: All Tasks 1-10 complete
    Steps:
      1. Run: cd backend && go vet ./... 2>&1
      2. Assert: exit code 0, no output (clean vet)
      3. Run: cd backend && go build -o /dev/null ./cmd/omo-core/ 2>&1
      4. Assert: exit code 0
      5. Run: cd backend && go test ./... 2>&1
      6. Assert: every package shows "ok" or "[no test files]", zero "FAIL"
      7. Run: cd backend && go test -race ./internal/app/positionmonitor/... 2>&1
      8. Assert: no "DATA RACE" in output
    Expected Result: Clean build, zero vet issues, all tests pass, no races
    Failure Indicators: Any non-zero exit code, "FAIL" in test output, "DATA RACE" detected
    Evidence: .sisyphus/evidence/task-11-full-verification.txt

  Scenario: Position monitor test count meets minimum
    Tool: Bash
    Preconditions: All positionmonitor tests pass
    Steps:
      1. Run: cd backend && go test -v ./internal/app/positionmonitor/... 2>&1 | grep -c '--- PASS'
      2. Assert: count >= 15 (unit tests from Tasks 1-5 + integration tests from Task 9)
    Expected Result: 15+ individual test cases passing
    Failure Indicators: Fewer than 15 tests, or any test marked SKIP without justification
    Evidence: .sisyphus/evidence/task-11-test-count.txt
  ```

  **Commit**: NO (verification only — no files to commit)
  - If fixes were needed, commit with: `fix(positionmonitor): address verification failures`
---

## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Rejection → fix → re-run.

- [ ] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in `.sisyphus/evidence/`. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [ ] F2. **Code Quality Review** — `unspecified-high`
  Run `go vet ./...` + `go build ./cmd/omo-core/` + `go test ./...`. Review all new files for: unvalidated type assertions, empty error handling, `fmt.Println` in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names (data/result/item/temp). Verify all exported types have doc comments.
  Output: `Build [PASS/FAIL] | Vet [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [ ] F3. **Real QA — Run Service** — `unspecified-high`
  Start from clean state. Run `go test -v ./internal/app/positionmonitor/...` and capture full output. Verify each exit condition has at least 2 test cases. Verify multi-tenant isolation test exists. Check test names follow convention. Run `go test -race ./internal/app/positionmonitor/...` to detect races.
  Output: `Tests [N/N pass] | Race [CLEAN/N issues] | Coverage [N conditions tested] | VERDICT`

- [ ] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual files created/modified. Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Creep [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

| Group | Message | Files | Pre-commit |
|-------|---------|-------|------------|
| Tasks 1, 7 | `feat(domain): add position monitor types and event constants` | `domain/position_monitor.go`, `domain/position_monitor_test.go`, `domain/event.go` | `go test ./internal/domain/...` |
| Tasks 2, 3 | `feat(positionmonitor): add config parser and exit evaluator` | `positionmonitor/config.go`, `config_test.go`, `evaluator.go`, `evaluator_test.go` | `go test ./internal/app/positionmonitor/...` |
| Tasks 4, 5 | `feat(positionmonitor): add core service with event handlers and reconciliation` | `positionmonitor/shared_test.go`, `service.go`, `service_test.go` | `go test ./internal/app/positionmonitor/...` |
| Tasks 6, 7, 8 | `feat(core): wire position monitor into orchestrator and main` | `orchestrator/orchestrator.go`, `cmd/omo-core/main.go` | `go build ./cmd/omo-core/` |
| Tasks 9, 10 | `test(positionmonitor): add integration tests and example config` | `positionmonitor/service_test.go`, `configs/strategies/*.toml` | `go test ./internal/app/positionmonitor/...` |
| Task 11 | `chore: verify full build and test suite` | — | `go test ./... && go vet ./...` |

---

## Success Criteria

### Verification Commands
```bash
cd backend && go test -v ./internal/app/positionmonitor/...  # Expected: all PASS
cd backend && go test -v ./internal/domain/...               # Expected: all PASS
cd backend && go build ./cmd/omo-core/                       # Expected: exit 0
cd backend && go vet ./...                                   # Expected: exit 0
cd backend && go test -race ./internal/app/positionmonitor/... # Expected: no races
```

### Final Checklist
- [ ] All "Must Have" items present and tested
- [ ] All "Must NOT Have" items verified absent
- [ ] All six exit conditions individually tested
- [ ] Multi-tenant isolation tested
- [ ] Idempotency tested
- [ ] Startup reconciliation tested
- [ ] Build compiles, vet passes, all tests pass
- [ ] No race conditions detected
