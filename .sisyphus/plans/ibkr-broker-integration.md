# IBKR as Execution Broker — Complete Integration Plan

## TL;DR

> **Quick Summary**: The IBKR adapter code (~80% complete) exists in `adapters/ibkr/` and `infra.go`
> already switches on `BROKER=ibkr`. This plan finishes the work: extract a testable `ibClient`
> interface, write comprehensive unit tests (zero exist today), fix 3 confirmed bugs + 7 crash/silent-
> failure points in `services.go`, wire Alpaca as a parallel data-only adapter when broker=ibkr,
> and verify the stack end-to-end against IB Gateway paper.
>
> **Deliverables**:
> - `backend/internal/adapters/ibkr/ib_client.go` — `ibClient` interface + wrapper
> - `backend/internal/adapters/ibkr/*_test.go` — full unit test suite (≥1 test/source file)
> - `backend/internal/adapters/ibkr/smoke_test.go` — `//go:build smoke` integration tests
> - `backend/cmd/omo-core/infra.go` — hybrid wiring: IBKR exec + Alpaca data-only
> - `backend/cmd/omo-core/services.go` — guarded service wiring for broker=ibkr
> - Bug fixes: `order_stream.go`, `market_data.go`, `config.go`
>
> **Estimated Effort**: Large (3–5 days)
> **Parallel Execution**: YES — 4 waves, max 7 concurrent tasks
> **Critical Path**: T1 (ibClient interface) → T2 (bug fixes) → T3-T7 (unit tests) → T8 (services.go wiring) → T9 (smoke test) → Final Wave

---

## Context

### Original Request
Build a detailed phased implementation plan for IBKR as execution broker while keeping Alpaca as market data provider, with TDD approach and atomic commits.

### Key Discoveries (Research)
- **Adapter is ~80% implemented** — 9 files in `adapters/ibkr/`, all BrokerPort + OrderStreamPort methods exist, ibsync v0.10.44 already in go.mod
- **infra.go already has the switch** — `case "ibkr":` block creates IBKR adapter; but when broker=ibkr, `concreteAlpaca == nil`
- **Zero unit tests** — no `*_test.go` in `adapters/ibkr/` at all
- **`ibsync.IB` is a concrete struct** — not testable without interface extraction
- **3 confirmed bugs**: PendingCancel event mapping, nil-nil return on disconnect, unsafe type assertion in SubscribeSymbols
- **7 crash/failure points in services.go** — screener/AI screener/IV collector passed IBKR adapter for ports that return errDeferred → crash at startup or silent failure

### Metis Review — Critical Findings Addressed
- `ibClient` interface extraction is the TDD prerequisite
- Alpaca data-only adapter must be initialized alongside IBKR when broker=ibkr
- Multi-account must be explicitly guarded (hard-codes Alpaca internally)
- warmup.go trade tick + feed health paths are silently skipped when concreteAlpaca==nil
- IB Gateway healthcheck start_period=90s may not be enough for first run

---

## Work Objectives

### Core Objective
Reach `BROKER=ibkr go test ./...` green + `BROKER=ibkr ./omo-core` starts cleanly and routes execution to IB Gateway while Alpaca serves all market data.

### Concrete Deliverables
- `backend/internal/adapters/ibkr/ib_client.go` — `ibClient` interface (10 methods from ibsync.IB used by adapter)
- `backend/internal/adapters/ibkr/broker_test.go` — unit tests for all BrokerPort methods
- `backend/internal/adapters/ibkr/order_stream_test.go` — unit tests for SubscribeOrderUpdates + tradeToOrderUpdate
- `backend/internal/adapters/ibkr/account_test.go` — unit tests for account/quote methods
- `backend/internal/adapters/ibkr/contract_test.go` — unit tests for newContract
- `backend/internal/adapters/ibkr/connection_test.go` — unit tests for mapStatus, mapStatusToEvent
- `backend/internal/adapters/ibkr/market_data_test.go` — unit tests for durationStr, barSizeStr
- `backend/internal/adapters/ibkr/smoke_test.go` — `//go:build smoke` integration tests
- Modified `infra.go` — hybrid wiring; Alpaca data-only adapter when broker=ibkr
- Modified `services.go` — IBKR-safe service wiring with fallbacks/guards
- Modified `order_stream.go` — PendingCancel bug fix
- Modified `market_data.go` — nil-nil → error fix + type assertion fix

### Definition of Done
- [ ] `cd backend && go test ./internal/adapters/ibkr/...` passes (≥1 test per source file)
- [ ] `cd backend && go test ./... -count=1` passes (no regressions)
- [ ] `cd backend && go build ./cmd/omo-core` compiles with `BROKER=ibkr`
- [ ] `cd backend && go vet ./internal/adapters/ibkr/...` reports zero warnings
- [ ] Running omo-core with `BROKER=ibkr` + Alpaca data env vars → clean startup log showing both adapters
- [ ] Running omo-core with `BROKER=ibkr` + no Alpaca vars → graceful fatal with clear message

### Must Have
- `ibClient` interface extracted — TDD prerequisite
- All 3 confirmed bugs fixed (PendingCancel, nil-nil, type assertion)
- services.go crash-proofed for broker=ibkr (no errDeferred passed to required ports)
- Alpaca data-only adapter wired when broker=ibkr
- Unit tests: ≥1 meaningful test per adapter source file
- Smoke test with `//go:build smoke` tag

### Must NOT Have (Guardrails)
- **MUST NOT** change any port interface signatures in `ports/`
- **MUST NOT** modify any Alpaca adapter code (`adapters/alpaca/`)
- **MUST NOT** change domain layer (`domain/`)
- **MUST NOT** implement GetOptionChain, ListTradeable, GetSnapshots for IBKR — keep errDeferred
- **MUST NOT** support IBKR for multi-account (add guard; separate epic)
- **MUST NOT** add gomock or testify/mock — use inline struct mocks only
- **MUST NOT** add new go.mod dependencies
- **MUST NOT** change strategy, monitor, or application service logic
- **MUST NOT** implement live trading (port 4001) — paper only in this plan
- **MUST NOT** make BROKER=alpaca behavior change in any way

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES — `testing` + `testify` (assert/require)
- **Automated tests**: TDD where possible (RED → GREEN → REFACTOR) for new files; GREEN first for bug fixes
- **Framework**: standard `go test` + `testify`
- **Smoke tests**: `//go:build smoke` tag (same pattern as `adapters/alpaca/smoke_test.go`)

### QA Policy
- **Go unit tests**: `go test ./internal/adapters/ibkr/...`
- **Build check**: `go build ./cmd/omo-core`
- **Lint**: `go vet ./...`
- **Integration**: `go test -tags smoke ./internal/adapters/ibkr/ -timeout 60s` (requires IB Gateway running)
- Evidence saved to `.sisyphus/evidence/task-{N}-{slug}.{ext}`

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — prerequisite interface + bug fixes, can run in parallel):
├── Task 1: Extract ibClient interface + update connection.go [quick]
├── Task 2: Fix 3 confirmed bugs (PendingCancel, nil-nil, type assert) [quick]
└── Task 3: contract_test.go + mapStatus/mapStatusToEvent unit tests [quick]

Wave 2 (After Wave 1 — all unit tests, MAX PARALLEL):
├── Task 4: broker_test.go — unit tests for all BrokerPort methods [unspecified-high]
├── Task 5: order_stream_test.go — pollOrderUpdates + tradeToOrderUpdate tests [unspecified-high]
├── Task 6: account_test.go — GetAccountEquity, GetAccountBuyingPower, GetQuote tests [quick]
└── Task 7: market_data_test.go — durationStr, barSizeStr, GetHistoricalBars edge cases [quick]

Wave 3 (After Wave 2 — wiring changes):
├── Task 8: infra.go hybrid wiring — Alpaca data-only adapter alongside IBKR [deep]
└── Task 9: services.go IBKR-safe wiring — guards + fallbacks for all 7 crash points [deep]

Wave 4 (After Wave 3 — smoke test + verification):
├── Task 10: smoke_test.go — //go:build smoke integration tests [unspecified-high]
└── Task 11: adapter.go — IBKRConfig.PaperMode usage + AccountID field in config [quick]

Wave FINAL (After ALL — independent parallel review):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality + go test ./... (unspecified-high)
├── Task F3: Real QA — BROKER=ibkr startup verification (unspecified-high)
└── Task F4: Scope fidelity check (deep)
```

### Dependency Matrix
- **T1**: None (start immediately)
- **T2**: None (start immediately)
- **T3**: None (start immediately, pure logic tests)
- **T4**: T1 (needs ibClient interface for mock)
- **T5**: T1, T2 (needs ibClient + fixed mapStatusToEvent)
- **T6**: T1 (needs ibClient for mock)
- **T7**: T1, T2 (needs ibClient + fixed nil-nil)
- **T8**: T1, T2 (infra wiring needs clean adapter)
- **T9**: T8 (services wiring needs hybrid infra)
- **T10**: T8, T9 (smoke test needs full wiring)
- **T11**: T1 (independent config change)
- **F1-F4**: T10, T11

### Agent Dispatch Summary
- **Wave 1**: 3 tasks → `quick`, `quick`, `quick`
- **Wave 2**: 4 tasks → `unspecified-high`, `unspecified-high`, `quick`, `quick`
- **Wave 3**: 2 tasks → `deep`, `deep`
- **Wave 4**: 2 tasks → `unspecified-high`, `quick`
- **FINAL**: 4 tasks → `oracle`, `unspecified-high`, `unspecified-high`, `deep`

---

## TODOs

- [ ] 1. Extract `ibClient` interface from `ibsync.IB` usages

  **What to do**:
  - Create `backend/internal/adapters/ibkr/ib_client.go`
  - Define `ibClient` interface with exactly the methods that adapter files call on `*ibsync.IB`:
    - `PlaceOrder(contract *ibsync.Contract, order *ibsync.Order) *ibsync.Trade`
    - `CancelOrder(order *ibsync.Order, cancel ibsync.OrderCancel)`
    - `OpenTrades() []*ibsync.Trade`
    - `Trades() []*ibsync.Trade`
    - `Positions() []ibsync.Position`
    - `ReqGlobalCancel()`
    - `ReqAccountSummary(group, tags string) ([]ibsync.AccountValue, error)`
    - `Snapshot(contract *ibsync.Contract) (*ibsync.Ticker, error)`
    - `ReqHistoricalData(contract *ibsync.Contract, endDateTime, duration, barSizeSetting, whatToShow string, useRTH bool, formatDate int) (chan ibsync.Bar, ibsync.CancelFunc)`
    - `IsConnected() bool`
  - Add a `realIBClient` struct that wraps `*ibsync.IB` and implements `ibClient` by forwarding all calls
  - Update `connection.go`: change the `ib` field type from `*ibsync.IB` to `ibClient`; update `IB()` return type to `ibClient`; in `connect()`, wrap the newly created `ibsync.IB` in `realIBClient{}`
  - Update adapter files that call `a.conn.IB()` — the returned type is now `ibClient` (interface), so all subsequent method calls compile against the interface (no code changes needed in broker.go/order_stream.go/etc. if method names match)
  - Add compile-time assertion: `var _ ibClient = (*realIBClient)(nil)`
  - Use `ast_grep_search` to find ALL call sites of `a.conn.IB()` across adapter files and verify each called method exists in the interface

  **Must NOT do**:
  - Do NOT change any port interface signatures
  - Do NOT change any adapter method behavior — pure refactor
  - Do NOT expose the `ibClient` interface publicly (lowercase, package-private)
  - Do NOT add methods to the interface beyond what is actually called — keep it minimal

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Pure interface extraction refactor, no logic changes, single package
  - **Skills**: []
  - **Skills Evaluated but Omitted**:
    - `senior-backend`: Not needed for pure interface extraction

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3)
  - **Blocks**: Tasks 4, 5, 6, 7, 8 (all need ibClient interface)
  - **Blocked By**: None (start immediately)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/ibkr/broker.go:15-50` — every call to `a.conn.IB()` followed by ibsync method calls; these methods must ALL be in the interface
  - `backend/internal/adapters/ibkr/order_stream.go:16-25` — uses `ib.Trades()` and iterates `*ibsync.Trade`
  - `backend/internal/adapters/ibkr/account.go:12-68` — uses `ib.ReqAccountSummary()`, `ib.Snapshot()`
  - `backend/internal/adapters/ibkr/market_data.go:15-174` — uses `ib.ReqHistoricalData()`, `ib.IsConnected()`
  - `backend/internal/adapters/ibkr/connection.go:33-130` — `ib *ibsync.IB` field to become `ib ibClient`; `connect()` creates and wraps `ibsync.NewIB()`

  **API/Type References**:
  - `github.com/scmhub/ibsync` — `ibsync.IB` methods: `PlaceOrder`, `CancelOrder`, `OpenTrades`, `Trades`, `Positions`, `ReqGlobalCancel`, `ReqAccountSummary`, `Snapshot`, `ReqHistoricalData`, `IsConnected`
  - `ibsync.OrderCancel` — used in `CancelOrder` call (broker.go:65)
  - `ibsync.NewOrderCancel()` — how to create OrderCancel

  **Acceptance Criteria**:
  - [ ] `backend/internal/adapters/ibkr/ib_client.go` created with `ibClient` interface + `realIBClient` wrapper
  - [ ] `connection.go` `ib` field is `ibClient` type
  - [ ] `connection.go` `IB()` method returns `ibClient`
  - [ ] `cd backend && go build ./...` passes (no compilation errors)
  - [ ] `cd backend && go vet ./internal/adapters/ibkr/...` passes

  **QA Scenarios**:

  ```
  Scenario: Build compiles after interface extraction
    Tool: Bash
    Preconditions: All ibkr adapter files unmodified except connection.go + new ib_client.go
    Steps:
      1. Run: cd backend && go build ./...
      2. Assert exit code 0
      3. Run: cd backend && go vet ./internal/adapters/ibkr/...
      4. Assert no output (no warnings)
    Expected Result: Clean build and zero vet warnings
    Failure Indicators: Compilation errors about undefined methods or type mismatches
    Evidence: .sisyphus/evidence/task-1-build.txt

  Scenario: Interface covers all actual call sites
    Tool: Bash
    Preconditions: ib_client.go created with interface
    Steps:
      1. Run: grep -n "\.conn\.IB()\." backend/internal/adapters/ibkr/*.go
      2. For each method X found, verify X is declared in ibClient interface
      3. Assert no method called on IB() return value that's absent from ibClient
    Expected Result: Every method called on ibClient return value exists in interface
    Failure Indicators: Build error "interface does not have method X"
    Evidence: .sisyphus/evidence/task-1-interface-coverage.txt
  ```

  **Evidence to Capture**:
  - [ ] `.sisyphus/evidence/task-1-build.txt` — `go build ./...` output
  - [ ] `.sisyphus/evidence/task-1-interface-coverage.txt` — grep output showing all call sites covered

  **Commit**: YES
  - Message: `feat(ibkr): extract ibClient interface for unit testability`
  - Files: `backend/internal/adapters/ibkr/ib_client.go`, `backend/internal/adapters/ibkr/connection.go`
  - Pre-commit: `cd backend && go build ./... && go vet ./internal/adapters/ibkr/...`

---

- [ ] 2. Fix 3 confirmed bugs in the IBKR adapter

  **What to do**:
  
  **Bug 1 — PendingCancel event mapping** (`order_stream.go`):
  - In `mapStatusToEvent()`, the `default` case maps `ibsync.PendingCancel` to `"new"` which is wrong
  - Add explicit case: `case ibsync.PendingCancel: return "pending_cancel"`
  
  **Bug 2 — nil-nil return on disconnect** (`market_data.go`):
  - In `GetHistoricalBars()`, line 113: `return nil, nil` when IB is nil/disconnected
  - Change to: `return nil, fmt.Errorf("ibkr: not connected")`
  - This is critical: callers checking `err == nil` wrongly assume success
  
  **Bug 3 — Unsafe type assertion** (`market_data.go`):
  - In `SubscribeSymbols()`, line 212: `streamContext.(context.Context)` panics if `barCtx` is not a full `context.Context`
  - The `barCtx` field in `adapter.go` is typed as `interface{ Done() <-chan struct{} }`, which is NOT `context.Context`
  - Fix: Change the `barCtx` field type in `adapter.go` from `interface{ Done() <-chan struct{} }` to `context.Context`
  - This makes the type assertion safe and eliminates the panic potential
  - Verify `StreamBars()` already passes a `context.Context` (it does — line 15)
  
  **Also fix during this task**:
  - `IBKRConfig.PaperMode` is set but never used in adapter — add a comment noting it's reserved for live/paper guard (will be wired in Task 11)

  **Must NOT do**:
  - Do NOT change any other logic beyond these 3 bugs
  - Do NOT change port interface signatures
  - Do NOT change Alpaca adapter

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Three targeted one-line fixes in existing files
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3)
  - **Blocks**: Tasks 5, 7 (tests that verify these fixes)
  - **Blocked By**: None (start immediately)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/ibkr/order_stream.go:112-128` — `mapStatusToEvent` switch statement, add PendingCancel case before default
  - `backend/internal/adapters/ibkr/market_data.go:110-113` — nil check and nil,nil return to fix
  - `backend/internal/adapters/ibkr/market_data.go:208-212` — type assertion `streamContext.(context.Context)` to fix
  - `backend/internal/adapters/ibkr/adapter.go:28` — `barCtx` field type to change from narrow interface to `context.Context`
  - `backend/internal/adapters/ibkr/broker.go:279-298` — `mapStatus` has correct PendingCancel case; use same pattern for `mapStatusToEvent`

  **API/Type References**:
  - `ibsync.PendingCancel` — the Status constant value (same package as other ibsync.Status constants in order_stream.go)
  - `context.Context` — standard library, no import changes needed

  **Acceptance Criteria**:
  - [ ] `mapStatusToEvent(ibsync.PendingCancel)` returns `"pending_cancel"` (not `"new"`)
  - [ ] `GetHistoricalBars` when `ib == nil` returns `nil, error` (not `nil, nil`)
  - [ ] `SubscribeSymbols` no longer has a type assertion that could panic
  - [ ] `cd backend && go build ./...` passes
  - [ ] `cd backend && go vet ./internal/adapters/ibkr/...` passes

  **QA Scenarios**:

  ```
  Scenario: PendingCancel maps to correct event string
    Tool: Bash
    Preconditions: Bug fix applied in mapStatusToEvent
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run TestMapStatusToEvent -v
      2. Assert test passes and "pending_cancel" is returned for PendingCancel input
    Expected Result: TestMapStatusToEvent PASS
    Failure Indicators: "FAIL" or wrong string returned
    Evidence: .sisyphus/evidence/task-2-pending-cancel.txt

  Scenario: GetHistoricalBars returns error when disconnected
    Tool: Bash
    Preconditions: Bug fix applied in market_data.go
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run TestGetHistoricalBars_Disconnected -v
      2. Assert test returns non-nil error
    Expected Result: TestGetHistoricalBars_Disconnected PASS
    Failure Indicators: Test expects error but gets nil
    Evidence: .sisyphus/evidence/task-2-nil-error.txt
  ```

  **Evidence to Capture**:
  - [ ] `.sisyphus/evidence/task-2-build.txt` — `go build` output after fixes

  **Commit**: YES
  - Message: `fix(ibkr): PendingCancel event mapping, nil-nil disconnect return, SubscribeSymbols type assertion`
  - Files: `backend/internal/adapters/ibkr/order_stream.go`, `backend/internal/adapters/ibkr/market_data.go`, `backend/internal/adapters/ibkr/adapter.go`
  - Pre-commit: `cd backend && go build ./... && go vet ./internal/adapters/ibkr/...`

---

- [ ] 3. Unit tests: `contract_test.go` + `mapStatus`/`mapStatusToEvent` tests

  **What to do**:
  - Create `backend/internal/adapters/ibkr/contract_test.go` — table-driven tests for `newContract()`:
    - Test: equity symbol `"AAPL"` → `SecType="STK"`, `Exchange="SMART"`, `Currency="USD"`, `Symbol="AAPL"`
    - Test: equity symbol `"TSLA"` → same pattern
    - Test: crypto symbol `"BTC/USD"` → `SecType="CRYPTO"`, `Exchange="PAXOS"`, `Currency="USD"`, `Symbol="BTC"`
    - Test: crypto symbol `"ETH/USD"` → `Symbol="ETH"`
    - Use `github.com/stretchr/testify/assert` for assertions
  - Create `backend/internal/adapters/ibkr/mapping_test.go` — table-driven tests for `mapStatus()` and `mapStatusToEvent()`:
    - `mapStatus` tests: every ibsync.Status variant → expected string
    - `mapStatusToEvent` tests: every ibsync.Status variant → expected event string (including `PendingCancel` → `"pending_cancel"` after Task 2 fix)
    - `directionToAction` tests: `domain.DirectionLong` → `"BUY"`, anything else → `"SELL"`
    - `intentOrderType` tests: `"market"` → `"MKT"`, `"stop_limit"` → `"STP LMT"`, default → `"LMT"`
    - `intentTIF` tests: `"day"` → `"DAY"`, `"ioc"` → `"IOC"`, default → `"GTC"`
    - `durationStr` tests: 1-day range → `"1 D"`, 3-day → `"3 D"`, 8-day → `"2 W"`, 31-day → `"2 M"`, 366-day → `"2 Y"`
    - `barSizeStr` tests: `"1m"` → `"1 min"`, `"5m"` → `"5 mins"`, `"15m"` → `"15 mins"`, `"1h"` → `"1 hour"`, `"1d"` → `"1 day"`
  - Follow the `shared_test.go` pattern from `execution` package: package-level `*_test` package name

  **Must NOT do**:
  - Do NOT test `newConnection` (requires real ibsync.IB)
  - Do NOT add any non-testify dependencies
  - Do NOT test private fields directly — test through public behavior

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Pure table-driven tests for mapping functions, no I/O or mocking needed
  - **Skills**: [`testing-patterns`]
    - `testing-patterns`: Table-driven test patterns, factory functions

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2)
  - **Blocks**: None directly (but provides test patterns for Tasks 4-7)
  - **Blocked By**: None (these functions have no dependencies on ibClient interface)

  **References**:

  **Pattern References**:
  - `backend/internal/app/execution/shared_test.go` — package naming (`package execution_test`), testify assert style, table-driven structure
  - `backend/internal/adapters/ibkr/contract.go` — `newContract()` function being tested
  - `backend/internal/adapters/ibkr/broker.go:248-298` — `directionToAction`, `intentOrderType`, `intentTIF`, `mapStatus`
  - `backend/internal/adapters/ibkr/order_stream.go:112-128` — `mapStatusToEvent` (after Task 2 fix adds PendingCancel)
  - `backend/internal/adapters/ibkr/market_data.go:218-252` — `durationStr`, `barSizeStr`
  - `backend/internal/adapters/alpaca/ratelimit_test.go` — good example of table-driven tests in alpaca package

  **API/Type References**:
  - `github.com/scmhub/ibsync` — `ibsync.Filled`, `ibsync.Cancelled`, `ibsync.ApiCancelled`, `ibsync.Inactive`, `ibsync.Submitted`, `ibsync.PreSubmitted`, `ibsync.PendingSubmit`, `ibsync.ApiPending`, `ibsync.PendingCancel`
  - `github.com/oh-my-opentrade/backend/internal/domain` — `domain.Symbol`, `domain.DirectionLong`

  **Acceptance Criteria**:
  - [ ] `backend/internal/adapters/ibkr/contract_test.go` created
  - [ ] `backend/internal/adapters/ibkr/mapping_test.go` created
  - [ ] `cd backend && go test ./internal/adapters/ibkr/... -v` passes all tests in these files
  - [ ] All ibsync.Status variants covered in `mapStatus` and `mapStatusToEvent` tests
  - [ ] `PendingCancel → "pending_cancel"` specifically verified in `mapStatusToEvent` test

  **QA Scenarios**:

  ```
  Scenario: All mapping tests pass
    Tool: Bash
    Preconditions: Task 2 (PendingCancel fix) is committed
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run "TestNewContract|TestMapStatus|TestMapStatusToEvent|TestDirectionToAction|TestIntentOrderType|TestIntentTIF|TestDurationStr|TestBarSizeStr" -v
      2. Assert all tests PASS
      3. Assert no "FAIL" in output
    Expected Result: All table-driven tests pass
    Failure Indicators: Any test failure, especially PendingCancel case
    Evidence: .sisyphus/evidence/task-3-mapping-tests.txt

  Scenario: Crypto contract has correct exchange
    Tool: Bash
    Preconditions: contract_test.go written
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run "TestNewContract/crypto" -v
      2. Assert Exchange field is "PAXOS" for BTC/USD symbol
    Expected Result: TestNewContract/crypto PASS
    Failure Indicators: Wrong Exchange or Symbol value
    Evidence: .sisyphus/evidence/task-3-crypto-contract.txt
  ```

  **Commit**: YES
  - Message: `test(ibkr): contract, mapStatus, mapStatusToEvent unit tests`
  - Files: `backend/internal/adapters/ibkr/contract_test.go`, `backend/internal/adapters/ibkr/mapping_test.go`
  - Pre-commit: `cd backend && go test ./internal/adapters/ibkr/...`

---

- [ ] 4. Unit tests: `broker_test.go` — BrokerPort methods

  **What to do**:
  - Create `backend/internal/adapters/ibkr/broker_test.go`
  - Define `mockIBClient` struct implementing the `ibClient` interface (from Task 1) with configurable func fields, following `shared_test.go` pattern:
    ```go
    type mockIBClient struct {
        PlaceOrderFunc          func(contract *ibsync.Contract, order *ibsync.Order) *ibsync.Trade
        CancelOrderFunc         func(order *ibsync.Order, cancel ibsync.OrderCancel)
        OpenTradesFunc          func() []*ibsync.Trade
        TradesFunc              func() []*ibsync.Trade
        PositionsFunc           func() []ibsync.Position
        ReqGlobalCancelFunc     func()
        ReqAccountSummaryFunc   func(group, tags string) ([]ibsync.AccountValue, error)
        SnapshotFunc            func(contract *ibsync.Contract) (*ibsync.Ticker, error)
        ReqHistoricalDataFunc   func(...) (chan ibsync.Bar, ibsync.CancelFunc)
        IsConnectedFunc         func() bool
    }
    ```
  - Helper: `newTestAdapter(client ibClient) *Adapter` that creates an Adapter with a mock connection
  - Test `SubmitOrder`:
    - Happy path: intent with direction=Long, quantity=100, symbol="AAPL" → PlaceOrder called with action="BUY", TotalQuantity=100, contract.Symbol="AAPL"
    - Market order: OrderType="market" → IBKR order.OrderType="MKT"
    - LimitOrder: OrderType="limit" → IBKR order.OrderType="LMT", LmtPrice set
    - StopLimit order: OrderType="stop_limit" → IBKR order.OrderType="STP LMT"
    - PlaceOrder returns nil → error "PlaceOrder returned nil trade"
    - Not connected (IB() returns nil mock that returns nil trade) → error "not connected"
  - Test `CancelOrder`:
    - Happy path: orderID in open trades → CancelOrder called
    - Order not found → error "open order X not found"
    - Invalid orderID format → error "invalid orderID"
  - Test `CancelOpenOrders`:
    - Matching symbol+side → count returned correctly
    - Side "LONG" → converted to "BUY" before matching
    - No matches → returns 0, nil
  - Test `CancelAllOpenOrders`:
    - N open trades → ReqGlobalCancel called, returns N
    - Empty → returns 0, nil
  - Test `GetOrderStatus`:
    - Order found with status Filled → returns "filled"
    - Order not found → error "order X not found"
    - Invalid orderID → error "invalid orderID"
  - Test `GetOrderDetails`:
    - Order found → OrderDetails populated correctly from Trade fields
    - Order has fills → FilledAt populated from last fill
    - Order not found → error
  - Test `GetPositions`:
    - 2 positions, 1 long + 1 short → correct side + abs qty
    - Zero quantity position → filtered out
  - Test `GetPosition`:
    - Symbol found → returns position qty
    - Symbol not found → returns 0, nil (not an error)
  - Test `ClosePosition`:
    - Long position → action="SELL"
    - Short position (negative qty) → action="BUY"
    - No position → returns "", nil

  **Must NOT do**:
  - Do NOT use real ibsync.IB or network connections
  - Do NOT add integration-style tests here (those go in smoke_test.go)
  - Do NOT test connection lifecycle (separate concern)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Large number of test cases requiring careful mock setup and thorough coverage
  - **Skills**: [`testing-patterns`]
    - `testing-patterns`: Mock pattern, table-driven tests

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 5, 6, 7)
  - **Blocks**: None
  - **Blocked By**: Task 1 (needs ibClient interface to define mockIBClient)

  **References**:

  **Pattern References**:
  - `backend/internal/app/execution/shared_test.go:26-78` — exact mock broker pattern with configurable func fields; replicate this exactly for mockIBClient
  - `backend/internal/adapters/ibkr/broker.go` — every function being tested
  - `backend/internal/adapters/ibkr/ib_client.go` (Task 1 output) — ibClient interface methods
  - `backend/internal/adapters/alpaca/options_order_test.go` — adapter test patterns in alpaca package (for reference)

  **API/Type References**:
  - `ibsync.Trade` — has fields `Order *ibsync.Order`, `OrderStatus ibsync.OrderStatus`, `Contract *ibsync.Contract`
  - `ibsync.OrderStatus` — fields: `Status ibsync.Status`, `Filled ibsync.Decimal`, `AvgFillPrice float64`, `OrderID int64`
  - `ibsync.Position` — fields: `Contract ibsync.Contract`, `Position ibsync.Decimal`, `AvgCost float64`
  - `ibsync.Filled`, `ibsync.Submitted`, `ibsync.Cancelled` etc. — Status constants

  **Acceptance Criteria**:
  - [ ] `broker_test.go` created with ≥15 test cases
  - [ ] `mockIBClient` defined implementing full `ibClient` interface
  - [ ] `cd backend && go test ./internal/adapters/ibkr/... -run "TestBroker|TestSubmitOrder|TestCancelOrder|TestGetPositions|TestGetPosition|TestClosePosition" -v` all PASS
  - [ ] Tests cover happy path AND error paths for every BrokerPort method

  **QA Scenarios**:

  ```
  Scenario: SubmitOrder routes BUY correctly
    Tool: Bash
    Preconditions: mockIBClient defined, Task 1 ibClient interface exists
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run TestSubmitOrder -v
      2. Assert all TestSubmitOrder/* subtests PASS
      3. Verify "action=BUY" for DirectionLong test case
    Expected Result: All SubmitOrder tests pass
    Failure Indicators: FAIL or wrong action value in mock verification
    Evidence: .sisyphus/evidence/task-4-submit-order.txt

  Scenario: GetPositions filters zero-qty positions
    Tool: Bash
    Preconditions: broker_test.go written
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run TestGetPositions -v
      2. Assert zero-qty position is NOT in returned trades
      3. Assert short position returns Side="SELL" with positive qty
    Expected Result: TestGetPositions PASS
    Failure Indicators: Zero-qty position included or short position has wrong side
    Evidence: .sisyphus/evidence/task-4-get-positions.txt
  ```

  **Commit**: YES
  - Message: `test(ibkr): BrokerPort method unit tests`
  - Files: `backend/internal/adapters/ibkr/broker_test.go`
  - Pre-commit: `cd backend && go test ./internal/adapters/ibkr/...`

---

- [ ] 5. Unit tests: `order_stream_test.go` — polling loop + event mapping

  **What to do**:
  - Create `backend/internal/adapters/ibkr/order_stream_test.go`
  - Reuse the `mockIBClient` defined in `broker_test.go` (same package)
  - Test `tradeToOrderUpdate()` (pure function, no mock needed):
    - Trade with filled status, 1 fill → fills last fill time + execID + fillQty + fillPrice populated
    - Trade with no fills → filledAt zero, execID empty
    - Filled trade → Event = "fill"
    - Submitted trade → Event = "new"
    - PendingCancel trade → Event = "pending_cancel" (verifies Task 2 fix)
    - Cancelled trade → Event = "canceled"
    - BrokerOrderID set from `os.OrderID`
  - Test `pollOrderUpdates()` goroutine:
    - New trade appears in Trades() → emits exactly one update on the channel
    - Same trade polled twice with same status → no duplicate emission (seen map working)
    - Status changes from Submitted to Filled → emits second update
    - Partial fill (status=Submitted, filled qty increases) → emits update for increased fill
    - Context cancel → channel closed cleanly
  - Test `SubscribeOrderUpdates`:
    - Not connected (mockIBClient returns nil) → returns error "not connected"
    - Connected → returns non-nil channel
  - Note: For `pollOrderUpdates`, inject a fast ticker (use `time.NewTicker` override if needed, or use a very short poll interval in tests with context timeout)

  **Must NOT do**:
  - Do NOT test with real IB Gateway connections
  - Do NOT use time.Sleep in tests — use context cancellation for cleanup
  - Do NOT test internal ticker timing (test behavior, not timing)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Goroutine-based polling test requires careful channel + context handling
  - **Skills**: [`testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 6, 7)
  - **Blocks**: None
  - **Blocked By**: Tasks 1, 2 (needs ibClient interface + PendingCancel fix)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/ibkr/order_stream.go` — `pollOrderUpdates`, `tradeToOrderUpdate`, `mapStatusToEvent`
  - `backend/internal/adapters/ibkr/broker_test.go` (Task 4) — mockIBClient definition to reuse
  - `backend/internal/adapters/alpaca/trade_stream_test.go` — how Alpaca tests channel-based streaming

  **API/Type References**:
  - `ibsync.Trade` — struct with `Order`, `OrderStatus`, `Fills()` method
  - `ibsync.Fill` — struct with `Time time.Time`, `Execution *ibsync.Execution`
  - `ibsync.Execution` — struct with `ExecID string`, `Shares ibsync.Decimal`, `Price float64`
  - `ports.OrderUpdate` — the output type being tested

  **Acceptance Criteria**:
  - [ ] `order_stream_test.go` created
  - [ ] `tradeToOrderUpdate` tests cover all status → event mappings including PendingCancel
  - [ ] `pollOrderUpdates` deduplication test passes (same trade not re-emitted)
  - [ ] `cd backend && go test ./internal/adapters/ibkr/... -run "TestOrderStream|TestTradeToOrderUpdate|TestPollOrderUpdates" -v` all PASS

  **QA Scenarios**:

  ```
  Scenario: Order deduplication works — same trade not re-emitted
    Tool: Bash
    Preconditions: order_stream_test.go written, Task 1 ibClient exists
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run TestPollOrderUpdates_Deduplication -v
      2. Assert only 1 update emitted for same trade polled twice
    Expected Result: TestPollOrderUpdates_Deduplication PASS
    Failure Indicators: 2 updates received when only 1 expected
    Evidence: .sisyphus/evidence/task-5-dedup.txt

  Scenario: Context cancel closes channel
    Tool: Bash
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run TestSubscribeOrderUpdates_ContextCancel -v -timeout 10s
      2. Assert test completes within 5 seconds
      3. Assert channel is closed (range ends)
    Expected Result: Test completes without timeout
    Failure Indicators: Test times out or goroutine leak
    Evidence: .sisyphus/evidence/task-5-context-cancel.txt
  ```

  **Commit**: YES
  - Message: `test(ibkr): OrderStream polling and event mapping unit tests`
  - Files: `backend/internal/adapters/ibkr/order_stream_test.go`
  - Pre-commit: `cd backend && go test ./internal/adapters/ibkr/...`

---

- [ ] 6. Unit tests: `account_test.go` — equity, buying power, quote

  **What to do**:
  - Create `backend/internal/adapters/ibkr/account_test.go`
  - Test `GetAccountEquity`:
    - `ReqAccountSummary` returns list with `Tag="NetLiquidation"`, `Value="125000.50"` → returns 125000.50, nil
    - `ReqAccountSummary` returns list WITHOUT NetLiquidation tag → error "NetLiquidation tag not found"
    - `ReqAccountSummary` returns error → error wrapped with "ibkr: ReqAccountSummary"
    - Not connected → error "not connected"
  - Test `GetAccountBuyingPower`:
    - Returns correct BuyingPower, DayTradingBuyingPower, PatternDayTrader=true when Value="1"
    - PatternDayTrader=true when Value="Y"
    - PatternDayTrader=false when Value="0"
    - Multiple tags parsed correctly
  - Test `GetQuote`:
    - Happy path: `Snapshot` returns ticker with bid=100.50, ask=100.55 → returns those values
    - `Snapshot` returns error → error wrapped
    - Not connected → error "not connected"
  - Test `GetOptionPrices`:
    - Should return `errDeferred` — verify this sentinel error is returned (not nil)
    - This is NOT a bug; it's the intended deferred behavior

  **Must NOT do**:
  - Do NOT mock the whole Adapter — inject mockIBClient at the connection level
  - Do NOT implement GetOptionPrices (keep errDeferred)
  - Do NOT test NewAdapter (requires real connection)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Straightforward mock → parse → assert pattern, no concurrency
  - **Skills**: [`testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 5, 7)
  - **Blocks**: None
  - **Blocked By**: Task 1 (needs ibClient interface)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/ibkr/account.go` — functions being tested
  - `backend/internal/adapters/ibkr/broker_test.go` — mockIBClient reuse pattern
  - `backend/internal/adapters/alpaca/options_rest_test.go` — adapter unit test structure

  **API/Type References**:
  - `ibsync.AccountValue` — struct with `Tag string`, `Value string`, `Currency string`, `Account string`
  - `ibsync.Ticker` — has `Bid()` and `Ask()` methods returning float64

  **Acceptance Criteria**:
  - [ ] `account_test.go` created with ≥8 test cases
  - [ ] `cd backend && go test ./internal/adapters/ibkr/... -run "TestGetAccountEquity|TestGetAccountBuyingPower|TestGetQuote|TestGetOptionPrices" -v` all PASS
  - [ ] `GetOptionPrices` test verifies it returns `errDeferred` (sentinel error check)

  **QA Scenarios**:

  ```
  Scenario: GetAccountEquity parses NetLiquidation correctly
    Tool: Bash
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run TestGetAccountEquity -v
      2. Assert 125000.50 returned for matching tag
      3. Assert error returned when tag absent
    Expected Result: All TestGetAccountEquity/* pass
    Evidence: .sisyphus/evidence/task-6-account.txt
  ```

  **Commit**: YES
  - Message: `test(ibkr): account equity, buying power, quote unit tests`
  - Files: `backend/internal/adapters/ibkr/account_test.go`
  - Pre-commit: `cd backend && go test ./internal/adapters/ibkr/...`

---

- [ ] 7. Unit tests: `market_data_test.go` — helpers + edge cases

  **What to do**:
  - Create `backend/internal/adapters/ibkr/market_data_test.go`
  - Test `GetHistoricalBars` when disconnected:
    - `ib == nil` → returns `nil, error` (verifies Task 2 fix — NOT nil, nil)
    - `ib.IsConnected() == false` → returns `nil, nil` (current behavior, document the distinction)
  - Test `durationStr` (via market_data package-level function — verify these match expected IBKR format):
    - from=now-1h, to=now → `"1 D"`
    - from=now-24h, to=now → `"1 D"`
    - from=now-48h, to=now → `"2 D"`
    - from=now-8days, to=now → `"2 W"` (rounds up to nearest week)
    - from=now-31days, to=now → `"2 M"`
    - from=now-366days, to=now → `"2 Y"`
  - Test `barSizeStr`:
    - `"1m"` → `"1 min"`, `"5m"` → `"5 mins"`, `"15m"` → `"15 mins"`, `"1h"` → `"1 hour"`, `"1d"` → `"1 day"`, unknown → `"1 min"`
  - Test `timeframePeriod`:
    - Each timeframe string → correct `time.Duration` value
  - Test `barAggregator.add()`:
    - First tick → no bar emitted (incomplete period)
    - Tick in same period → updates OHLCV, no bar emitted
    - Tick in new period → emits previous period bar with correct OHLC values
    - Volume accumulates within period
  - Test `SubscribeSymbols` no-op when `barHdl == nil`:
    - Call SubscribeSymbols before StreamBars → returns nil, no goroutine started

  **Must NOT do**:
  - Do NOT test StreamBars end-to-end (that's a smoke test concern)
  - Do NOT test real IB Gateway historical data

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Mostly pure function tests and simple struct tests
  - **Skills**: [`testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 5, 6)
  - **Blocks**: None
  - **Blocked By**: Tasks 1, 2 (needs ibClient + nil-nil fix)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/ibkr/market_data.go` — functions being tested
  - `backend/internal/adapters/ibkr/bar_aggregator.go` — `barAggregator.add()` logic

  **Acceptance Criteria**:
  - [ ] `market_data_test.go` created
  - [ ] `GetHistoricalBars` on disconnected adapter returns error (not nil, nil)
  - [ ] `barAggregator` period boundary test passes
  - [ ] `cd backend && go test ./internal/adapters/ibkr/... -run "TestGetHistoricalBars|TestDurationStr|TestBarSizeStr|TestBarAggregator|TestTimeframePeriod" -v` all PASS

  **QA Scenarios**:

  ```
  Scenario: barAggregator emits bar on period boundary
    Tool: Bash
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run TestBarAggregator -v
      2. Assert first add() returns nil (no completed bar)
      3. Assert add() with new period timestamp returns non-nil completed bar
      4. Assert completed bar has correct Open/High/Low/Close from prior period
    Expected Result: All TestBarAggregator/* pass
    Evidence: .sisyphus/evidence/task-7-bar-aggregator.txt

  Scenario: GetHistoricalBars returns error when not connected
    Tool: Bash
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -run TestGetHistoricalBars_Disconnected -v
      2. Assert non-nil error returned
      3. Assert error message contains "not connected"
    Expected Result: PASS (verifies Task 2 bug fix)
    Evidence: .sisyphus/evidence/task-7-disconnected.txt
  ```

  **Commit**: YES
  - Message: `test(ibkr): market data helpers and edge case unit tests`
  - Files: `backend/internal/adapters/ibkr/market_data_test.go`
  - Pre-commit: `cd backend && go test ./internal/adapters/ibkr/...`

---

- [ ] 8. `infra.go` — Hybrid wiring: Alpaca data-only adapter alongside IBKR

  **What to do**:
  - Modify `backend/cmd/omo-core/infra.go`
  - In the `case "ibkr":` block, AFTER successfully creating the IBKR adapter, also create an Alpaca adapter if Alpaca credentials are available:
    ```go
    case "ibkr":
        // ... existing IBKR adapter creation with retryWithBackoff ...

        // When broker=ibkr, also create Alpaca as a market-data-only adapter
        // if credentials are present. Many services need MarketDataPort,
        // SnapshotPort, OptionsMarketDataPort, UniverseProviderPort.
        if cfg.Alpaca.APIKeyID != "" && cfg.Alpaca.APISecretKey != "" {
            alpacaLog := log.With().Str("component", "alpaca-data").Logger()
            if err := retryWithBackoff(log, "alpaca_data_adapter", 3, 2*time.Second, 15*time.Second, func() error {
                a, err := alpaca.NewAdapter(cfg.Alpaca, alpacaLog)
                if err != nil {
                    return err
                }
                concreteAlpaca = a
                return nil
            }); err != nil {
                log.Warn().Err(err).Msg("Alpaca data-only adapter unavailable — market data features degraded")
                // NOT fatal — IBKR can provide limited market data via polling
            } else {
                log.Info().Msg("Alpaca data-only adapter initialized alongside IBKR execution adapter")
            }
        } else {
            log.Info().Msg("No Alpaca credentials — market data served by IBKR polling only")
        }
    ```
  - The `concreteAlpaca` field in `infraDeps` already exists (see infra.go:42-43)
  - When `concreteAlpaca != nil`, it will be used in `warmup.go` lines 359-395 (trade ticks + health) — this is already gated on `infra.concreteAlpaca != nil`
  - Add a `dataProvider` field to `infraDeps` that is Alpaca when available, otherwise IBKR:
    ```go
    type infraDeps struct {
        // ... existing fields ...
        dataProvider brokerAdapter  // Alpaca when available, IBKR when not
    }
    ```
  - Set `dataProvider = concreteAlpaca` if non-nil, else `dataProvider = broker`
  - This `dataProvider` will be used in services.go for all market data / snapshot / universe ports (Task 9)
  - Add validation: if `broker=ibkr` and `cfg.Alpaca.APIKeyID == ""`, log a clear warning listing which features are degraded (real-time chart, screener, IV collector, options pipeline)
  - Add guard for multi-account: in `initInfra`, after broker initialization, if `cfg.Broker == "ibkr" && cfg.MultiAccount` → `log.Fatal().Msg("multi-account not supported with IBKR broker — set MULTI_ACCOUNT=false")`

  **Must NOT do**:
  - Do NOT change the `brokerAdapter` interface in infra.go
  - Do NOT change the Alpaca adapter initialization in the `default:` case
  - Do NOT make the Alpaca data adapter creation fatal when credentials are absent — it must be degraded-mode only

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Needs careful understanding of infraDeps flow, warmup.go dependencies, and startup sequencing
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Wiring patterns, startup sequencing, graceful degradation

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (with Task 9, but 8 should commit first for Task 9 to reference)
  - **Blocks**: Task 9, Task 10
  - **Blocked By**: Tasks 1, 2 (needs clean adapter)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/infra.go:77-151` — full `initInfra` function; the `case "ibkr":` block at line 91-101 is where Alpaca data adapter gets added
  - `backend/cmd/omo-core/infra.go:25-37` — `brokerAdapter` interface and `infraDeps` struct
  - `backend/cmd/omo-core/infra.go:103-115` — `default:` Alpaca-only case to understand the pattern we're extending
  - `backend/cmd/omo-core/warmup.go:359-395` — already gated on `infra.concreteAlpaca != nil`; this gate makes the feature automatically work once concreteAlpaca is set
  - `backend/cmd/omo-core/services.go:374-469` — multi-account block to guard

  **API/Type References**:
  - `config.AlpacaConfig` — `APIKeyID`, `APISecretKey` fields to check for credentials
  - `alpaca.NewAdapter(cfg.Alpaca, log)` — same constructor used in default case
  - `retryWithBackoff(log, desc, maxAttempts, initialDelay, maxDelay, fn)` — use same retry helper

  **Acceptance Criteria**:
  - [ ] `case "ibkr":` in `initInfra` creates optional Alpaca data-only adapter
  - [ ] `infraDeps` has `dataProvider brokerAdapter` field set to Alpaca (if available) or IBKR (fallback)
  - [ ] Multi-account + IBKR → `log.Fatal` with clear message
  - [ ] `cd backend && go build ./cmd/omo-core` compiles
  - [ ] `cd backend && go test ./... -count=1` still passes (no regressions in existing tests)

  **QA Scenarios**:

  ```
  Scenario: Build still compiles after infra wiring changes
    Tool: Bash
    Steps:
      1. Run: cd backend && go build ./cmd/omo-core
      2. Assert exit code 0
    Expected Result: Clean build
    Evidence: .sisyphus/evidence/task-8-build.txt

  Scenario: Existing tests unaffected
    Tool: Bash
    Steps:
      1. Run: cd backend && go test ./... -count=1
      2. Assert no new test failures
    Expected Result: Same pass/fail as before this task
    Evidence: .sisyphus/evidence/task-8-regression.txt
  ```

  **Commit**: YES
  - Message: `feat(infra): hybrid wiring — Alpaca data + IBKR execution when BROKER=ibkr`
  - Files: `backend/cmd/omo-core/infra.go`
  - Pre-commit: `cd backend && go build ./cmd/omo-core && go test ./...`

---

- [ ] 9. `services.go` — IBKR-safe service wiring with guards and fallbacks

  **What to do**:
  - Modify `backend/cmd/omo-core/services.go`
  - Fix all 7 crash/failure points identified in Metis review. For each service that receives `infra.broker` as a port that returns `errDeferred` for IBKR, switch to `infra.dataProvider` (from Task 8) instead:
  
  **Fix 1 — OptionsPrice in PosMonitor (line ~162)**:
  ```go
  OptionsPrice: infra.dataProvider,  // Alpaca when available (has real options), IBKR fallback (errDeferred but logged)
  ```
  
  **Fix 2 — OptionsMarket in Strategy Pipeline (line ~310)**:
  ```go
  OptionsMarket: infra.dataProvider,  // ditto
  ```
  
  **Fix 3-4 — Screener crashes** (lines ~596-599):
  - The screener and AI screener pass `infra.broker` as SnapshotPort and UniverseProviderPort
  - When broker=ibkr and no Alpaca data, these return errDeferred → crash at NewService
  - Fix: wrap screener initialization in a check:
    ```go
    // Screener requires SnapshotPort and UniverseProviderPort
    // — skip if neither is available (IBKR-only mode without Alpaca data)
    screenerCanRun := cfg.Broker != "ibkr" || infra.concreteAlpaca != nil
    if screenerEnabled && !screenerCanRun {
        log.Warn().Msg("screener disabled: BROKER=ibkr without Alpaca data adapter — SnapshotPort/UniverseProviderPort unavailable")
        screenerEnabled = false
    }
    ```
  - Pass `infra.dataProvider` as snapshot/universe ports in the screener constructors
  
  **Fix 5-6 — AI Screener** (lines ~618-620): same guard pattern as Fix 3-4
  
  **Fix 7 — IV Collector** (lines ~689-690):
  - If broker=ibkr, IV collector silently fails (options + snapshots → errDeferred)
  - Fix: wrap in check:
    ```go
    if cfg.Broker == "ibkr" && infra.concreteAlpaca == nil {
        log.Warn().Msg("IV collector disabled: BROKER=ibkr without Alpaca data adapter")
    } else {
        // ... existing IV collector initialization ...
        svc.ivCollector = ivcollector.NewService(..., infra.dataProvider, infra.dataProvider, ...)
    }
    ```
  
  **Fix 8 — MarketData in Orchestrator** (line ~387):
  - Change `MarketData: infra.broker` to `MarketData: infra.dataProvider`
  - Note: Multi-account is guarded in Task 8 to fatal when BROKER=ibkr
  
  **Fix 9 — Activation service** (lines ~542-543):
  - Change both `infra.broker` references to `infra.dataProvider` for historical data + symbol subscription

  **Must NOT do**:
  - Do NOT change any strategy, monitor, or execution service logic
  - Do NOT remove or bypass the existing guard logic in services.go
  - Do NOT change the Alpaca-only path (default case should be identical to today)
  - Do NOT change how `infra.broker` is passed for BrokerPort, OrderStreamPort, AccountPort — those are IBKR-native and correct

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Requires understanding which ports each service needs and why, and correctly threading dataProvider through ~9 call sites
  - **Skills**: [`senior-backend`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (after Task 8)
  - **Blocks**: Task 10 (smoke test needs correct wiring)
  - **Blocked By**: Task 8 (needs `infra.dataProvider`)

  **References**:

  **Pattern References**:
  - `backend/cmd/omo-core/services.go` — FULL file (all lines 88-698 in scope)
  - `backend/cmd/omo-core/infra.go` (Task 8 output) — `infraDeps.dataProvider` field
  - Metis review "Exhaustive `infra.broker` Dependency Map" — exact line numbers for every fix point
  - `backend/cmd/omo-core/warmup.go` — note that `infra.concreteAlpaca` is already used correctly here; no changes needed in warmup.go

  **API/Type References**:
  - `infraDeps.dataProvider` — `brokerAdapter` type (set in Task 8)
  - `infraDeps.concreteAlpaca` — `*alpaca.Adapter` (used for nil check to guard optional features)
  - `ivcollector.NewService(cfg, optionsPort, snapshotPort, repo, log)` — first 2 args are the critical ports
  - `screenerapp.NewService(log, cfg, tenant, env, symbols, assetClass, bus, marketDataPort, snapshotPort, universePort, repo, notifier)` — positions 8, 9, 10 need dataProvider

  **Acceptance Criteria**:
  - [ ] All 7 errDeferred call sites in services.go fixed to use `infra.dataProvider`
  - [ ] Screener + AI Screener guarded with `screenerCanRun` check
  - [ ] IV Collector guarded with IBKR + no-Alpaca check
  - [ ] `cd backend && go build ./cmd/omo-core` passes
  - [ ] `cd backend && go test ./... -count=1` passes (no regressions)
  - [ ] `BROKER=alpaca go test ./...` identical pass/fail as before (default path untouched)

  **QA Scenarios**:

  ```
  Scenario: Build compiles with all fixes
    Tool: Bash
    Steps:
      1. Run: cd backend && go build ./cmd/omo-core
      2. Assert exit code 0
    Expected Result: Clean build
    Evidence: .sisyphus/evidence/task-9-build.txt

  Scenario: No regressions in full test suite
    Tool: Bash
    Steps:
      1. Run: cd backend && go test ./... -count=1 2>&1 | tail -20
      2. Assert all packages PASS or SKIP (no new FAILs)
    Expected Result: Full suite green
    Evidence: .sisyphus/evidence/task-9-regression.txt

  Scenario: Screener guard fires correctly in log
    Tool: Bash (review code path statically)
    Steps:
      1. Read services.go screener initialization block
      2. Verify `cfg.Broker == "ibkr" && infra.concreteAlpaca == nil` guard exists
      3. Verify warning log message is present
    Expected Result: Guard code present with descriptive log message
    Failure Indicators: No guard, or guard condition is wrong
    Evidence: .sisyphus/evidence/task-9-guard-review.txt
  ```

  **Commit**: YES
  - Message: `fix(services): IBKR-safe service wiring — guards and dataProvider fallbacks`
  - Files: `backend/cmd/omo-core/services.go`
  - Pre-commit: `cd backend && go build ./cmd/omo-core && go test ./...`

---

- [ ] 10. `smoke_test.go` — Integration tests against IB Gateway paper

  **What to do**:
  - Create `backend/internal/adapters/ibkr/smoke_test.go` with build tag `//go:build smoke`
  - Follow exact pattern from `backend/internal/adapters/alpaca/smoke_test.go` (read it first)
  - The test must skip automatically if `IBKR_GATEWAY_HOST` env var is not set (same as Alpaca smoke tests skip without API keys)
  - Tests (each independently skippable, use `t.Skip("IBKR_GATEWAY_HOST not set")`):
    - `TestSmoke_Connect`: create `NewAdapter(cfg, log)` → assert no error, `conn.isConnected() == true`
    - `TestSmoke_GetAccountEquity`: call `GetAccountEquity(ctx)` → assert equity > 0 (paper account has at least some equity)
    - `TestSmoke_GetPositions`: call `GetPositions(ctx, "test", domain.EnvModePaper)` → assert no error (may return empty slice)
    - `TestSmoke_GetHistoricalBars`: call `GetHistoricalBars(ctx, "AAPL", "1d", yesterday, today)` → assert len(bars) > 0 during market hours
    - `TestSmoke_SubmitOrder_MarketBuy`: submit a small market order for 1 share of AAPL → assert no error, orderID non-empty. THEN immediately cancel it.
    - `TestSmoke_SubscribeOrderUpdates`: subscribe to order updates channel → place a small test order → assert at least one update received within 5s
  - Config loading: use `config.IBKRConfig{Host: os.Getenv("IBKR_GATEWAY_HOST"), Port: 4002, ClientID: 99, PaperMode: true}`
  - Use `//go:build smoke` tag on the file (same as alpaca/smoke_test.go)
  - Use `-timeout 60s` for the test run

  **Must NOT do**:
  - Do NOT place real live orders (ClientID=99 distinguishes smoke test connections)
  - Do NOT leave open orders — cancel every submitted order in t.Cleanup
  - Do NOT fail if market is closed — add appropriate skip or tolerance
  - Do NOT add credentials to test files — read from env vars only

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Needs careful IB Gateway interaction, cleanup, and environment handling
  - **Skills**: [`senior-backend`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 11)
  - **Parallel Group**: Wave 4
  - **Blocks**: None
  - **Blocked By**: Tasks 8, 9 (needs full hybrid wiring to test end-to-end)

  **References**:

  **Pattern References**:
  - `backend/internal/adapters/alpaca/smoke_test.go` — READ FIRST; use exact same `//go:build smoke` tag, skip pattern, env var check
  - `backend/internal/adapters/ibkr/adapter.go:34-44` — `NewAdapter` constructor to call in smoke tests
  - `backend/internal/adapters/ibkr/broker.go` — functions being smoke-tested

  **API/Type References**:
  - `config.IBKRConfig` — use env vars: `IBKR_GATEWAY_HOST`, `IBKR_GATEWAY_PORT`, `IBKR_CLIENT_ID`
  - `domain.EnvModePaper` — use for all positions and trades

  **Acceptance Criteria**:
  - [ ] `smoke_test.go` created with `//go:build smoke` build tag
  - [ ] Tests skip cleanly when `IBKR_GATEWAY_HOST` is unset: `cd backend && go test ./internal/adapters/ibkr/... -tags smoke -v` → all SKIP (not FAIL)
  - [ ] Against running IB Gateway: `cd backend && go test -tags smoke ./internal/adapters/ibkr/ -v -timeout 60s` → all PASS
  - [ ] Every submitted order is cleaned up (cancel in t.Cleanup)

  **QA Scenarios**:

  ```
  Scenario: Smoke tests skip without IB Gateway env vars
    Tool: Bash
    Preconditions: IBKR_GATEWAY_HOST unset (default CI environment)
    Steps:
      1. Run: cd backend && go test ./internal/adapters/ibkr/... -tags smoke -v 2>&1
      2. Assert output contains "SKIP" or "--- SKIP" for all smoke test functions
      3. Assert no "FAIL" in output
    Expected Result: All tests skip gracefully
    Failure Indicators: Any test FAIL without IB Gateway running
    Evidence: .sisyphus/evidence/task-10-skip-test.txt

  Scenario: Smoke tests pass against IB Gateway paper
    Tool: Bash
    Preconditions: IB Gateway paper running at $IBKR_GATEWAY_HOST:4002
    Steps:
      1. Run: IBKR_GATEWAY_HOST=<host> cd backend && go test -tags smoke ./internal/adapters/ibkr/ -v -timeout 60s
      2. Assert all TestSmoke_* tests PASS
      3. Assert no open orders left (cleanup verified by GetOrderStatus check in Cleanup)
    Expected Result: All smoke tests pass within 60s
    Evidence: .sisyphus/evidence/task-10-smoke-output.txt
  ```

  **Commit**: YES
  - Message: `test(ibkr): smoke tests against IB Gateway paper (//go:build smoke)`
  - Files: `backend/internal/adapters/ibkr/smoke_test.go`
  - Pre-commit: `cd backend && go test ./internal/adapters/ibkr/... -tags smoke -v` (must show SKIP, not FAIL)

---

- [ ] 11. Config and adapter cleanup — `PaperMode` usage + `AccountID` in `IBKRConfig`

  **What to do**:
  - Modify `backend/internal/config/config.go`:
    - Add `AccountID string` field to `IBKRConfig`:
      ```go
      type IBKRConfig struct {
          Host      string `yaml:"host"`
          Port      int    `yaml:"port"`
          ClientID  int    `yaml:"client_id"`
          AccountID string `yaml:"account_id"`  // optional; required for multi-account IBKR (future)
          PaperMode bool   `yaml:"paper_mode"`
      }
      ```
    - Add env var overlay in `Load()`: `if val := os.Getenv("IBKR_ACCOUNT_ID"); val != "" { cfg.IBKR.AccountID = val }`
  - Modify `backend/internal/adapters/ibkr/adapter.go`:
    - The `Adapter` struct already stores `cfg config.IBKRConfig`
    - Add a startup warning if `cfg.PaperMode && cfg.IBKR.Port == 4001`:
      - In `NewAdapter()`, after creation: `if !cfg.PaperMode && cfg.Port == 4002 { log.Warn().Msg("ibkr: PaperMode=false but connected to paper port 4002 — verify intentional") }`
      - And: `if cfg.PaperMode && cfg.Port == 4001 { log.Warn().Msg("ibkr: PaperMode=true but connected to live port 4001 — LIVE TRADING MODE") }`
  - Modify `backend/internal/adapters/ibkr/connection.go`:
    - Document the expected port usage in a comment block:
      ```go
      // IB Gateway port conventions:
      //   4001 — Gateway LIVE (or TWS live override port)
      //   4002 — Gateway PAPER (maps to socat relay in Docker: host:4002 → container:4004 → gateway:4002)
      //   7496 — TWS live (direct)
      //   7497 — TWS paper (direct)
      ```
  - Update `configs/config.yaml` (if it exists) to document new `account_id` field with a commented example
  - Update the `.env.example` (if it exists) to add `# IBKR_ACCOUNT_ID=DU1234567  # Paper account ID`

  **Must NOT do**:
  - Do NOT change port selection logic — port is still set by config, this is documentation + warning only
  - Do NOT add required validation for AccountID (it's optional for single-account paper trading)
  - Do NOT change any existing config field names or default values

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Small additions to config struct + adapter warning logic
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 10)
  - **Parallel Group**: Wave 4
  - **Blocks**: None
  - **Blocked By**: Task 1 (adapter struct must exist cleanly)

  **References**:

  **Pattern References**:
  - `backend/internal/config/config.go:31-36` — `IBKRConfig` struct to extend
  - `backend/internal/config/config.go:364-377` — IBKR env var overlay section in `Load()`
  - `backend/internal/adapters/ibkr/adapter.go:34-44` — `NewAdapter` function where PaperMode warning goes
  - `backend/internal/adapters/alpaca/adapter.go` — how Alpaca uses its config fields for reference

  **API/Type References**:
  - `config.IBKRConfig` — extend with `AccountID string`

  **Acceptance Criteria**:
  - [ ] `IBKRConfig` has `AccountID string` field
  - [ ] `IBKR_ACCOUNT_ID` env var is loaded in `Load()`
  - [ ] `NewAdapter` logs a warning for mismatched PaperMode/port combinations
  - [ ] `cd backend && go build ./...` passes
  - [ ] `cd backend && go test ./internal/config/...` passes (config tests unaffected)

  **QA Scenarios**:

  ```
  Scenario: AccountID env var is loaded
    Tool: Bash
    Steps:
      1. Run: cd backend && go test ./internal/config/... -v -run TestLoad
      2. Assert no test failures
    Expected Result: Config tests PASS
    Evidence: .sisyphus/evidence/task-11-config.txt

  Scenario: Build still compiles
    Tool: Bash
    Steps:
      1. Run: cd backend && go build ./...
      2. Assert exit code 0
    Expected Result: Clean build
    Evidence: .sisyphus/evidence/task-11-build.txt
  ```

  **Commit**: YES
  - Message: `fix(ibkr): use PaperMode config field for startup warnings; add AccountID to IBKRConfig`
  - Files: `backend/internal/config/config.go`, `backend/internal/adapters/ibkr/adapter.go`, `backend/internal/adapters/ibkr/connection.go`
  - Pre-commit: `cd backend && go build ./... && go test ./internal/config/...`

---

## Final Verification Wave

- [ ] F1. **Plan Compliance Audit** — `oracle`
  Read plan end-to-end. Verify each Must Have: run `go test ./internal/adapters/ibkr/...` (expect PASS), `go build ./cmd/omo-core` (expect PASS), `go vet ./...` (expect zero warnings). For each Must NOT Have: `grep -r "ibsync.IB" internal/adapters/ibkr/` should only find the interface wrapper (no direct calls in adapter methods). Check evidence files exist in `.sisyphus/evidence/`.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [ ] F2. **Code Quality Review** — `unspecified-high`
  Run `cd backend && go test ./... -count=1` and capture full output. Run `go vet ./...`. Review all changed files for: `as any`/`@ts-ignore` equivalents in Go (`interface{}`), empty catch-all error discards (`_ = err`), `t.Log` in prod code, commented-out code. Check consistency: inline struct mock pattern used (not gomock). Check that no Alpaca adapter files were modified.
  Output: `Build [PASS/FAIL] | Tests [N pass/N fail] | Vet [PASS/FAIL] | Files changed [N] | VERDICT`

- [ ] F3. **Real QA** — `unspecified-high`
  With IB Gateway running at localhost:4002 (paper mode), run: `BROKER=ibkr go test -tags smoke ./internal/adapters/ibkr/ -v -timeout 60s`. Capture full output. Then run `BROKER=ibkr go build ./cmd/omo-core` and confirm binary builds. Check that `BROKER=alpaca go test ./...` still passes (no regressions). Save terminal output to `.sisyphus/evidence/final-qa/smoke-output.txt`.
  Output: `Smoke tests [N/N pass] | BROKER=ibkr build [PASS/FAIL] | BROKER=alpaca tests [PASS/FAIL] | VERDICT`

- [ ] F4. **Scope Fidelity Check** — `deep`
  Run `git diff --name-only HEAD~N HEAD` (or from branch base). Verify: (a) no files in `internal/adapters/alpaca/` changed, (b) no files in `internal/domain/` changed, (c) no files in `internal/ports/` changed, (d) no `go.mod` dependency added, (e) every changed file is in scope. For each task, compare "What to do" in plan against actual diff. Flag any unaccounted changes.
  Output: `In-scope files [N] | Out-of-scope violations [N] | VERDICT`

---

## Commit Strategy

Each task maps to one atomic commit:
- T1: `feat(ibkr): extract ibClient interface for testability`
- T2: `fix(ibkr): PendingCancel event, nil-nil disconnect, SubscribeSymbols type assertion`
- T3: `test(ibkr): contract, mapStatus, mapStatusToEvent unit tests`
- T4: `test(ibkr): BrokerPort method unit tests`
- T5: `test(ibkr): OrderStream polling and event mapping unit tests`
- T6: `test(ibkr): account equity, buying power, quote unit tests`
- T7: `test(ibkr): market data helpers and edge case unit tests`
- T8: `feat(infra): hybrid wiring — Alpaca data + IBKR execution when BROKER=ibkr`
- T9: `fix(services): IBKR-safe service wiring, guards, fallbacks`
- T10: `test(ibkr): smoke tests against IB Gateway paper (//go:build smoke)`
- T11: `fix(ibkr): use PaperMode config field; add AccountID to IBKRConfig`

---

## Success Criteria

### Verification Commands
```bash
cd backend && go test ./internal/adapters/ibkr/... -v    # Expected: PASS (≥7 test functions)
cd backend && go test ./... -count=1                      # Expected: PASS (no regressions)
cd backend && go vet ./...                                # Expected: no warnings
cd backend && go build ./cmd/omo-core                    # Expected: compiles cleanly
BROKER=ibkr cd backend && go test -tags smoke ./internal/adapters/ibkr/ -timeout 60s  # Expected: PASS (requires IB Gateway)
```

### Final Checklist
- [ ] All "Must Have" present and verified
- [ ] All "Must NOT Have" absent (no port changes, no Alpaca changes, no domain changes)
- [ ] All existing tests still pass (BROKER=alpaca path unchanged)
- [ ] Zero `go vet` warnings in ibkr package
- [ ] Smoke tests pass against IB Gateway paper
- [ ] omo-core starts cleanly with BROKER=ibkr
