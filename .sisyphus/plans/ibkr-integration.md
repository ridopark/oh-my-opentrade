# IBKR Integration — Full Implementation Specification

## TL;DR

> **Quick Summary**: Rewrite the IBKR adapter stack to use `ReqRealTimeBars` for live streaming,
> introduce an `ibClient` interface for testability, build a `CompositeAdapter` routing live
> execution + market data to IBKR and historical + options to Alpaca, and wire it all through
> `infra.go` behind the `BROKER=ibkr` flag.
>
> **Deliverables**:
> - `internal/adapters/ibkr/ib_client.go` — `ibClient` interface (testability shim)
> - `internal/adapters/ibkr/market_data.go` — StreamBars via `ReqRealTimeBars` (replaces polling)
> - `internal/adapters/ibkr/bar_aggregator.go` — correctness-hardened OHLCV aggregator
> - `internal/adapters/ibkr/order_stream.go` — 200ms cache-diff pattern (replaces 500ms poll)
> - `internal/adapters/ibkr/composite.go` — `CompositeAdapter` routing table
> - `internal/adapters/ibkr/connection.go` — `OnReconnect` callback registration
> - `internal/adapters/ibkr/broker.go` — AccountID filter + error wrapping hardening
> - `internal/config/config.go` — `AccountID` added to `IBKRConfig`
> - `internal/adapters/alpaca/adapter.go` — `WithNoStream()` functional option
> - `cmd/omo-core/infra.go` — IBKR split-wiring + `concreteIBKR` field
> - `cmd/omo-core/warmup.go` + `http.go` — IBKR-mode guards + `/health` field
> - `internal/adapters/ibkr/*_test.go` — unit tests alongside implementation
> - `internal/adapters/ibkr/integration_test.go` — paper Gateway integration tests
>
> **Estimated Effort**: Large
> **Parallel Execution**: YES — 4 waves
> **Critical Path**: Task 2 → Task 3 → Task 4 → Task 5 → Task 7 → Task 9 → Task 10 → Task 11

---

## Context

### Original Request
Rewrite the existing `.sisyphus/plans/ibkr-integration.md` into a precise transcription-grade
specification: exact API calls, exact type mappings, exact goroutine patterns, exact test inputs/
outputs. No design decisions left to the implementing agent.

### Updated Data Source Split
```
StreamBars (live)          → IBKR   (ReqRealTimeBars → bar_aggregator → MarketBar)
GetHistoricalBars          → Alpaca (REST only, no WS)
GetSnapshots               → Alpaca (REST, screener use)
SubscribeSymbols           → IBKR   (calls StreamBars with noop handler)
SubmitOrder                → IBKR
CancelOrder                → IBKR
GetPositions               → IBKR
SubscribeOrderUpdates      → IBKR
GetQuote                   → IBKR   (ib.Snapshot)
GetAccountEquity           → IBKR
GetAccountBuyingPower      → IBKR
GetOptionChain             → Alpaca (deferred stub)
GetOptionPrices            → Alpaca (deferred stub)
ListTradeable              → Alpaca (REST)
Close()                    → IBKR.Close() + Alpaca.Close()
```

### Critical ibsync API Facts (verified from source)
- `ReqRealTimeBars(contract, barSize int, whatToShow string, useRTH bool) (chan RealTimeBar, CancelFunc)`
  — returns `chan RealTimeBar` NOT `chan Bar`. `RealTimeBar.Time` is `int64` (Unix seconds).
- `ReqHistoricalData(...) (chan Bar, CancelFunc)` — `Bar.Date` is string (parse with `strconv.ParseInt` or `ibsync.ParseIBTime`).
- `ReqAccountSummary(group, tags string) (AccountSummary, error)` — `AccountSummary = []AccountValue`
- `Snapshot(contract *Contract, regulatorySnapshot ...bool) (*Ticker, error)` — variadic regSnapshot
- `Ticker.Bid() float64`, `Ticker.Ask() float64` — methods, not fields
- `ibsync.IB.IsConnected()` is a direct method on `*ibsync.IB`
- `CancelFunc = func()` — plain function type

### Codebase State (verified)
- `connection.ib` is `*ibsync.IB` — must change to `ibClient` interface
- `market_data.go` StreamBars currently POLLS at 65s via `GetHistoricalBars` — must replace with `ReqRealTimeBars`
- `bar_aggregator.go` already exists with `.add(rtb ibsync.RealTimeBar)` returning `*domain.MarketBar` — needs `Feed` method and correctness fix
- `order_stream.go` polls at 500ms — reduce to 200ms + cache-diff (already has cache-diff logic)
- `deferred.go` stubs: `GetOptionChain`, `ListTradeable`, `GetSnapshots` return `errDeferred` — these move to CompositeAdapter routing Alpaca
- `infra.go` BROKER=ibkr currently assigns raw `*ibkr.Adapter` to `broker` with no Alpaca data provider
- `brokerAdapter` interface in infra.go (lines 25-37): requires `GetQuote`, `GetAccountEquity`, `SubscribeSymbols` in addition to standard ports

---

## Work Objectives

### Core Objective
Replace the polling-based IBKR market data with live `ReqRealTimeBars`, introduce the `ibClient`
interface for unit testability, build `CompositeAdapter` that routes per the data source split,
and wire everything cleanly in `infra.go` so `BROKER=ibkr` fully works with `BROKER=alpaca` zero regression.

### Concrete Deliverables
- `ib_client.go` with `ibClient` interface + compile-time assertion `*ibsync.IB` satisfies it
- `market_data.go` StreamBars using `ReqRealTimeBars` (pacing: max 50 symbols)
- `bar_aggregator.go` with public `Feed(ibsync.RealTimeBar) (*domain.MarketBar)` + period parsing
- Hardened `order_stream.go` at 200ms + status-change cache
- `composite.go` implementing full `brokerAdapter` interface via routing table
- `infra.go` BROKER=ibkr path: Alpaca REST-only + IBKR + CompositeAdapter
- `connection.go` with `OnReconnect(func())` + callback dispatch after reconnect
- `broker.go` AccountID filter in GetPositions + error wrapping
- `config.go` AccountID field + env overlay
- `alpaca/adapter.go` `WithNoStream()` option that skips WS init

### Definition of Done
- [ ] `cd backend && go build -o /dev/null ./cmd/omo-core` → exit 0
- [ ] `cd backend && go test -race ./internal/adapters/ibkr/... -count=1` → all pass
- [ ] `cd backend && go test ./... -count=1` → zero regressions (BROKER=alpaca path)
- [ ] `cd backend && go vet ./...` → zero errors
- [ ] `cd backend && go test -tags=integration -race ./internal/adapters/ibkr/...` → passes with IB Gateway

### Must Have
- `ibClient` interface in `ib_client.go` — `connection.ib` field type changed to `ibClient`
- `ReqRealTimeBars` used in `StreamBars` (not polling)
- `barAggregator.Feed(ibsync.RealTimeBar)` public method
- `CompositeAdapter` satisfies `brokerAdapter` mega-interface
- `BROKER=alpaca` path: zero changes, zero regressions
- `IBKRConfig.AccountID` added, wired into `GetPositions` filter
- `alpaca.WithNoStream()` option prevents WS startup
- `connection.OnReconnect(func())` callback mechanism
- All IBKR adapter errors use `fmt.Errorf("ibkr: ...: %w", err)` wrapping chain

### Must NOT Have
- No changes to `internal/domain/` or `internal/ports/` packages
- No `ReqRealTimeBars` for more than 50 symbols (enforce at runtime)
- No polling loop in the `StreamBars` code path (ReqRealTimeBars is streaming, not polling)
- No TDD RED/GREEN language — implementation and tests written in same pass
- No bare `fmt.Errorf` strings without `%w` where wrapping a sub-error
- No race conditions — all `connection.ib` access through `c.mu` RWMutex
- No hardcoded credentials — env vars only
- No changes to `GetHistoricalBars` routing — stays Alpaca in composite

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed.

### Test Decision
- **Infrastructure exists**: YES (`go test ./...` in `backend/`)
- **Automated tests**: YES — written alongside implementation in same task
- **Framework**: `testing` stdlib + `github.com/stretchr/testify v1.11.1`
- **Style**: Implementation first, tests in same commit. No RED/GREEN phases.

### QA Policy
- **Unit tests**: `cd backend && go test -race ./internal/adapters/ibkr/... -count=1`
- **Integration tests**: `cd backend && go test -tags=integration -race ./internal/adapters/ibkr/... -timeout 120s`
- **Build verification**: `cd backend && go build -o /dev/null ./cmd/omo-core`
- **Regression**: `cd backend && go test ./... -count=1`
- Evidence saved to `.sisyphus/evidence/task-{N}-{slug}.txt`

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — no dependencies):
├── Task 1: IBKRConfig.AccountID + alpaca.WithNoStream() option      [quick]
├── Task 2: ibClient interface + connection.go field change           [unspecified-high]
└── Task 3: CompositeAdapter scaffold + alpacaDataProvider interface  [unspecified-high]

Wave 2 (After Wave 1):
├── Task 4: CompositeAdapter full routing + unit tests                [deep]
├── Task 5: infra.go BROKER=ibkr split-wiring + concreteIBKR field   [deep]
└── Task 6: broker.go AccountID filter + error wrapping + tests       [unspecified-high]

Wave 3 (After Wave 2):
├── Task 7: market_data.go StreamBars via ReqRealTimeBars + bar_aggregator tests  [deep]
├── Task 8: order_stream.go 200ms cache-diff + tests                  [deep]
└── Task 9: connection.go OnReconnect callbacks + tests               [unspecified-high]

Wave 4 (After Wave 3):
├── Task 10: warmup.go + http.go IBKR-mode guards + /health field     [quick]
├── Task 11: Integration test harness (paper IB Gateway)              [deep]
└── Task 12: Final build + full regression verification               [unspecified-high]

Critical Path: Task 2 → Task 3 → Task 4 → Task 5 → Task 7 → Task 9 → Task 10 → Task 11
Parallel Speedup: ~55% faster than sequential
Max Concurrent: 3 (each wave)
```

### Task Dependency Graph

| Task | Depends On | Blocks | Reason |
|------|------------|--------|--------|
| 1 | None | 4, 5, 6 | Config struct standalone; alpaca option standalone |
| 2 | None | 4, 6, 7, 8, 9 | ibClient interface needed by all adapter tests |
| 3 | None | 4, 5 | CompositeAdapter scaffold needed before routing impl |
| 4 | 1, 2, 3 | 5, 10, 11 | Full routing needs config field + mock + scaffold |
| 5 | 1, 3, 4 | 10, 11 | infra.go wiring needs CompositeAdapter to be real |
| 6 | 1, 2 | 11 | AccountID filter needs config field + mock |
| 7 | 2 | 11 | StreamBars needs ibClient for mock testing |
| 8 | 2 | 11 | Order stream needs ibClient mock |
| 9 | 2 | 7, 8 | OnReconnect needed by StreamBars + order stream |
| 10 | 5 | 11 | warmup/http guards depend on infra.go wiring |
| 11 | 4, 5, 6, 7, 8, 9, 10 | 12 | Integration tests need everything wired |
| 12 | 11 | — | Final check after all tasks |

### Agent Dispatch Summary
- **Wave 1**: 3 parallel — T1 `quick`, T2 `unspecified-high`, T3 `unspecified-high`
- **Wave 2**: 3 parallel — T4 `deep`, T5 `deep`, T6 `unspecified-high`
- **Wave 3**: 3 parallel — T7 `deep`, T8 `deep`, T9 `unspecified-high`
- **Wave 4**: 3 parallel — T10 `quick`, T11 `deep`, T12 `unspecified-high`

---

## TODOs

> Implementation and tests written in the same pass. One commit per task.
> Pre-commit for ALL tasks: `cd backend && go build -o /dev/null ./cmd/omo-core && go test ./internal/adapters/ibkr/... && go vet ./...`

---

- [ ] 1. Add `AccountID` to `IBKRConfig` + add `alpaca.WithNoStream()` functional option

  **Files**: `internal/config/config.go`, `internal/adapters/alpaca/adapter.go`

  **Exact code for `config.go`**:
  ```go
  // IBKRConfig holds connection parameters for the IB Gateway adapter.
  type IBKRConfig struct {
      Host      string `yaml:"host"`
      Port      int    `yaml:"port"`
      ClientID  int    `yaml:"client_id"`
      PaperMode bool   `yaml:"paper_mode"`
      AccountID string `yaml:"account_id"` // ADD THIS — optional, empty = all accounts
  }
  ```

  In the `Load()` function env overlay block (after existing IBKR overlays), add:
  ```go
  if val := os.Getenv("IBKR_ACCOUNT_ID"); val != "" {
      cfg.IBKR.AccountID = val
  }
  ```

  **Exact code for `adapter.go` (alpaca)**:
  ```go
  // adapterOptions holds functional options for NewAdapter.
  type adapterOptions struct {
      noStream bool // skip WebSocket subscription startup
  }

  // Option is a functional option for NewAdapter.
  type Option func(*adapterOptions)

  // WithNoStream skips initializing the WebSocket market data streams.
  // Use when the adapter is only needed for REST (historical bars, snapshots, options).
  func WithNoStream() Option {
      return func(o *adapterOptions) { o.noStream = true }
  }
  ```

  Change `NewAdapter` signature:
  ```go
  func NewAdapter(cfg config.AlpacaConfig, log zerolog.Logger, opts ...Option) (*Adapter, error) {
      o := &adapterOptions{}
      for _, opt := range opts { opt(o) }
      // ... existing setup ...
      if o.noStream {
          // Return adapter without starting any WS clients
          return &Adapter{
              rest:    rest,
              ws:      ws,      // constructed but NOT started
              cryptoWs: cryptoWs,
              tradeStream: tradeStream,
              dataURL: dataURL,
              posC:    newPositionCache(2 * time.Second),
              log:     log,
          }, nil
      }
      return &Adapter{ /* ... existing return ... */ }, nil
  }
  ```
  Note: `ws`, `cryptoWs`, `tradeStream` are constructed but `StreamBars` will be called later
  only if BROKER=alpaca. In BROKER=ibkr mode, `StreamBars` on the alpaca adapter is never called.

  **Tests** (`internal/config/config_test.go`):
  Add test `TestIBKRConfig_AccountIDFromEnv`:
  ```go
  func TestIBKRConfig_AccountIDFromEnv(t *testing.T) {
      t.Setenv("IBKR_ACCOUNT_ID", "DU123456")
      cfg := &Config{}
      applyEnvOverlays(cfg) // or call Load() with minimal yaml
      assert.Equal(t, "DU123456", cfg.IBKR.AccountID)
  }
  func TestIBKRConfig_AccountIDDefault(t *testing.T) {
      t.Setenv("IBKR_ACCOUNT_ID", "")
      cfg := &Config{}
      assert.Equal(t, "", cfg.IBKR.AccountID)
  }
  ```
  Add test `TestAlpacaWithNoStream` in `internal/adapters/alpaca/adapter_test.go` (or new file):
  ```go
  func TestAlpacaWithNoStream_DoesNotPanic(t *testing.T) {
      cfg := config.AlpacaConfig{APIKeyID: "k", APISecretKey: "s"}
      a, err := alpaca.NewAdapter(cfg, zerolog.Nop(), alpaca.WithNoStream())
      require.NoError(t, err)
      require.NotNil(t, a)
  }
  ```

  **Must NOT do**:
  - Do NOT add validation requiring AccountID to be non-empty (paper mode works without it)
  - Do NOT change existing `NewAdapter(cfg, log)` call sites — opts are variadic, all existing calls work unchanged

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`senior-backend`]
    - `senior-backend`: Go config patterns, functional options pattern

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3)
  - **Blocks**: Tasks 4, 5, 6
  - **Blocked By**: None

  **References**:
  - `internal/config/config.go:31-36` — IBKRConfig struct (add AccountID after PaperMode)
  - `internal/config/config.go:364-390` — env overlay section (add IBKR_ACCOUNT_ID block)
  - `internal/adapters/alpaca/adapter.go:32-76` — NewAdapter to add opts variadic
  - `deployments/docker-compose.yml` — add `IBKR_ACCOUNT_ID: ""` to omo-core env block

  **Acceptance Criteria**:
  - [ ] `IBKRConfig.AccountID` field exists in `config.go`
  - [ ] `IBKR_ACCOUNT_ID` env var sets `cfg.IBKR.AccountID`
  - [ ] `alpaca.NewAdapter(cfg, log)` still compiles (no args changed)
  - [ ] `alpaca.NewAdapter(cfg, log, alpaca.WithNoStream())` compiles and returns non-nil
  - [ ] `cd backend && go test ./internal/config/... -count=1` → PASS
  - [ ] `cd backend && go build -o /dev/null ./cmd/omo-core` → PASS

  **QA Scenarios**:
  ```
  Scenario: AccountID loaded from env var
    Tool: Bash
    Steps:
      1. cd backend && IBKR_ACCOUNT_ID=DU123456 go test ./internal/config/... -run TestIBKRConfig_AccountIDFromEnv -v
    Expected Result: PASS — cfg.IBKR.AccountID == "DU123456"
    Evidence: .sisyphus/evidence/task-1-config-accountid.txt

  Scenario: AccountID defaults empty, WithNoStream compiles
    Tool: Bash
    Steps:
      1. cd backend && go build -o /dev/null ./cmd/omo-core
    Expected Result: exit code 0
    Evidence: .sisyphus/evidence/task-1-build.txt
  ```

  **Commit**: `feat(config): add AccountID to IBKRConfig + alpaca WithNoStream option`
  Files: `internal/config/config.go`, `internal/adapters/alpaca/adapter.go`, `deployments/docker-compose.yml`

---

- [ ] 2. Create `ibClient` interface + change `connection.ib` field type + mock test helper

  **Files**:
  - `internal/adapters/ibkr/ib_client.go` (new)
  - `internal/adapters/ibkr/connection.go` (change field type)
  - `internal/adapters/ibkr/mock_ib_test.go` (new, test-only)

  **Exact `ib_client.go`**:
  ```go
  package ibkr

  import "github.com/scmhub/ibsync"

  // ibClient is a subset of *ibsync.IB methods used by this adapter.
  // Defined as an interface to enable unit testing with mock implementations.
  type ibClient interface {
      IsConnected() bool
      PlaceOrder(contract *ibsync.Contract, order *ibsync.Order) *ibsync.Trade
      CancelOrder(order *ibsync.Order, orderCancel ibsync.OrderCancel) *ibsync.Trade
      ReqGlobalCancel()
      OpenTrades() []*ibsync.Trade
      Trades() []*ibsync.Trade
      Positions(account ...string) []ibsync.Position
      ReqAccountSummary(groupName string, tags string) (ibsync.AccountSummary, error)
      Snapshot(contract *ibsync.Contract, regulatorySnapshot ...bool) (*ibsync.Ticker, error)
      ReqRealTimeBars(contract *ibsync.Contract, barSize int, whatToShow string, useRTH bool, realTimeBarsOptions ...ibsync.TagValue) (chan ibsync.RealTimeBar, ibsync.CancelFunc)
      ReqHistoricalData(contract *ibsync.Contract, endDateTime string, duration string, barSize string, whatToShow string, useRTH bool, formatDate int, chartOptions ...ibsync.TagValue) (chan ibsync.Bar, ibsync.CancelFunc)
  }

  // Compile-time assertion: *ibsync.IB satisfies ibClient.
  var _ ibClient = (*ibsync.IB)(nil)
  ```

  **Change to `connection.go`** (line 34: change field type):
  ```go
  type connection struct {
      ib      ibClient   // was *ibsync.IB — changed for testability
      cfg     config.IBKRConfig
      log     zerolog.Logger
      symHook *symbolHook
      ctx     context.Context
      cancel  context.CancelFunc
      mu      sync.RWMutex
  }
  ```

  In `connect()` (line 62), the assignment `c.ib = ib` now assigns `*ibsync.IB` to `ibClient` — this works because of the interface satisfaction. **No other changes needed to connect()**.

  In `isConnected()` (line 116): `c.ib.IsConnected()` still works — `IsConnected()` is in the interface.

  In `disconnect()` (line 119): `c.ib.Disconnect()` — ADD `Disconnect()` to the interface OR cast:
  ```go
  // In ib_client.go interface, add:
  Disconnect() error
  ```

  In `IB()` (line 129): Return type changes from `*ibsync.IB` to `ibClient`:
  ```go
  func (c *connection) IB() ibClient {
      c.mu.RLock()
      defer c.mu.RUnlock()
      return c.ib
  }
  ```

  **All call sites** of `a.conn.IB()` currently check `ib == nil` — this still works since a nil interface is nil.

  **Fix in broker.go**: `ib.IsConnected()` calls — some methods do `ib := a.conn.IB(); if ib == nil`. After this change `IB()` returns `ibClient` interface, which can be nil only if `c.ib` was set to nil. The nil check `if ib == nil` still works for nil interface. BUT: `ib.IsConnected()` in `GetHistoricalBars` (market_data.go line 111) does `ib.IsConnected()` directly — still valid since it's in the interface.

  **Exact `mock_ib_test.go`**:
  ```go
  package ibkr_test  // or package ibkr — use package ibkr for white-box tests

  import (
      "sync"
      "github.com/scmhub/ibsync"
  )

  type mockIB struct {
      mu          sync.Mutex
      connected   bool
      trades      []*ibsync.Trade
      openTrades  []*ibsync.Trade
      positions   []ibsync.Position
      accountSummary ibsync.AccountSummary
      accountErr  error
      snapTicker  *ibsync.Ticker
      snapErr     error
      placedOrders []*ibsync.Order
      cancelledOrders []int64
      globalCancelled bool
      rtBarChans  map[*ibsync.Contract]chan ibsync.RealTimeBar
      rtCancelCalled map[*ibsync.Contract]bool
  }

  func (m *mockIB) IsConnected() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.connected }
  func (m *mockIB) PlaceOrder(contract *ibsync.Contract, order *ibsync.Order) *ibsync.Trade {
      m.mu.Lock(); defer m.mu.Unlock()
      m.placedOrders = append(m.placedOrders, order)
      t := &ibsync.Trade{Order: order}
      m.trades = append(m.trades, t)
      return t
  }
  func (m *mockIB) CancelOrder(order *ibsync.Order, _ ibsync.OrderCancel) *ibsync.Trade {
      m.mu.Lock(); defer m.mu.Unlock()
      m.cancelledOrders = append(m.cancelledOrders, order.OrderID)
      return nil
  }
  func (m *mockIB) ReqGlobalCancel() { m.mu.Lock(); m.globalCancelled = true; m.mu.Unlock() }
  func (m *mockIB) OpenTrades() []*ibsync.Trade { m.mu.Lock(); defer m.mu.Unlock(); return m.openTrades }
  func (m *mockIB) Trades() []*ibsync.Trade { m.mu.Lock(); defer m.mu.Unlock(); return m.trades }
  func (m *mockIB) Positions(account ...string) []ibsync.Position { m.mu.Lock(); defer m.mu.Unlock(); return m.positions }
  func (m *mockIB) ReqAccountSummary(_ string, _ string) (ibsync.AccountSummary, error) {
      m.mu.Lock(); defer m.mu.Unlock(); return m.accountSummary, m.accountErr
  }
  func (m *mockIB) Snapshot(_ *ibsync.Contract, _ ...bool) (*ibsync.Ticker, error) {
      m.mu.Lock(); defer m.mu.Unlock(); return m.snapTicker, m.snapErr
  }
  func (m *mockIB) ReqRealTimeBars(contract *ibsync.Contract, _ int, _ string, _ bool, _ ...ibsync.TagValue) (chan ibsync.RealTimeBar, ibsync.CancelFunc) {
      m.mu.Lock(); defer m.mu.Unlock()
      ch := make(chan ibsync.RealTimeBar, 16)
      if m.rtBarChans == nil { m.rtBarChans = make(map[*ibsync.Contract]chan ibsync.RealTimeBar) }
      m.rtBarChans[contract] = ch
      cancelled := false
      return ch, func() { m.mu.Lock(); cancelled = true; m.mu.Unlock(); close(ch) }
  }
  func (m *mockIB) ReqHistoricalData(_ *ibsync.Contract, _, _, _, _, _ string, _ bool, _ int, _ ...ibsync.TagValue) (chan ibsync.Bar, ibsync.CancelFunc) {
      ch := make(chan ibsync.Bar)
      close(ch)
      return ch, func() {}
  }
  func (m *mockIB) Disconnect() error { return nil }

  // makeTrade creates a *ibsync.Trade with given orderID and status for tests.
  func makeTrade(orderID int64, status ibsync.Status, filled float64) *ibsync.Trade {
      order := &ibsync.Order{}
      order.OrderID = orderID
      t := &ibsync.Trade{Order: order}
      t.OrderStatus.Status = status
      t.OrderStatus.Filled = ibsync.StringToDecimal(fmt.Sprintf("%.6f", filled))
      return t
  }
  ```

  **Tests** (`internal/adapters/ibkr/ib_client_test.go`):
  ```go
  func TestIBClient_InterfaceSatisfied(t *testing.T) {
      // Compile-time: var _ ibClient = (*ibsync.IB)(nil) in ib_client.go
      // Runtime: just verify mockIB also satisfies ibClient
      var _ ibClient = (*mockIB)(nil) // compile-time assertion in test file
  }
  func TestConnection_IB_ReturnsIbClient(t *testing.T) {
      // Verify IB() return type is ibClient (not *ibsync.IB)
      // This is verified by compilation — just build the package
  }
  ```

  **Must NOT do**:
  - Do NOT expose `*ibsync.IB` publicly — callers use `ibClient` interface
  - Do NOT break existing broker.go / account.go / market_data.go call patterns — `ib := a.conn.IB()` still works

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: [`senior-backend`, `testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3)
  - **Blocks**: Tasks 4, 6, 7, 8, 9
  - **Blocked By**: None

  **References**:
  - `internal/adapters/ibkr/connection.go:33-41` — `connection` struct with `ib *ibsync.IB`
  - `internal/adapters/ibkr/connection.go:129-133` — `IB()` method return type
  - `/home/ridopark/go/pkg/mod/github.com/scmhub/ibsync@v0.10.44/ib.go:23` — `CancelFunc` type
  - `/home/ridopark/go/pkg/mod/github.com/scmhub/ibsync@v0.10.44/ib.go:2155` — `ReqRealTimeBars` returns `(chan RealTimeBar, CancelFunc)`
  - `/home/ridopark/go/pkg/mod/github.com/scmhub/ibsync@v0.10.44/ib.go:1735` — `ReqHistoricalData` returns `(chan Bar, CancelFunc)`
  - `/home/ridopark/go/pkg/mod/github.com/scmhub/ibsync@v0.10.44/ib.go:1230` — `ReqAccountSummary` returns `(AccountSummary, error)`
  - `/home/ridopark/go/pkg/mod/github.com/scmhub/ibsync@v0.10.44/ib.go:1029` — `CancelOrder` returns `*Trade`
  - `/home/ridopark/go/pkg/mod/github.com/scmhub/ibsync@v0.10.44/ib.go:286` — `Positions(account ...string)` is variadic

  **Acceptance Criteria**:
  - [ ] `ib_client.go` exists with `ibClient` interface + `var _ ibClient = (*ibsync.IB)(nil)` compile assertion
  - [ ] `connection.ib` is type `ibClient`
  - [ ] `connection.IB()` returns `ibClient`
  - [ ] `cd backend && go build ./internal/adapters/ibkr/...` → exit 0
  - [ ] `cd backend && go test -run ^$ ./internal/adapters/ibkr/...` → compiles (no panic)

  **QA Scenarios**:
  ```
  Scenario: ibClient interface satisfies *ibsync.IB
    Tool: Bash
    Steps:
      1. cd backend && go build ./internal/adapters/ibkr/...
    Expected Result: exit code 0 — compile-time assertion passes
    Evidence: .sisyphus/evidence/task-2-build.txt

  Scenario: Race detector passes on connection tests
    Tool: Bash
    Steps:
      1. cd backend && go test -race -run TestConnection ./internal/adapters/ibkr/... 2>&1
    Expected Result: exit 0, no DATA RACE output
    Evidence: .sisyphus/evidence/task-2-race.txt
  ```

  **Commit**: `feat(ibkr): ibClient interface + connection.go ibClient field`
  Files: `internal/adapters/ibkr/ib_client.go`, `internal/adapters/ibkr/connection.go`, `internal/adapters/ibkr/mock_ib_test.go`

---

- [ ] 3. Scaffold `CompositeAdapter` + `alpacaDataProvider` interface

  **Files**:
  - `internal/adapters/ibkr/composite.go` (new)

  **Exact `composite.go` scaffold**:
  ```go
  package ibkr

  import (
      "context"
      "time"

      "github.com/oh-my-opentrade/backend/internal/domain"
      "github.com/oh-my-opentrade/backend/internal/ports"
      "github.com/rs/zerolog"
  )

  // alpacaDataProvider lists the Alpaca adapter methods used by CompositeAdapter.
  // Only REST-capable methods: historical bars, snapshots, options, universe.
  // WebSocket/streaming methods (StreamBars, SubscribeOrderUpdates) are IBKR-only.
  type alpacaDataProvider interface {
      GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
      GetSnapshots(ctx context.Context, symbols []string, asOf time.Time) (map[string]ports.Snapshot, error)
      GetOptionChain(ctx context.Context, symbol domain.Symbol, expiry time.Time, right domain.OptionRight) ([]domain.OptionContractSnapshot, error)
      GetOptionPrices(ctx context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error)
      ListTradeable(ctx context.Context, assetClass domain.AssetClass) ([]ports.Asset, error)
      Close() error
  }

  // CompositeAdapter satisfies the brokerAdapter mega-interface by routing:
  //   - Live execution + market data + account  → IBKR
  //   - Historical bars + snapshots + options   → Alpaca
  type CompositeAdapter struct {
      ibkr   *Adapter
      alpaca alpacaDataProvider
      log    zerolog.Logger
  }

  // NewCompositeAdapter creates a CompositeAdapter.
  func NewCompositeAdapter(ibkrAdapter *Adapter, alpacaAdapter alpacaDataProvider, log zerolog.Logger) *CompositeAdapter {
      return &CompositeAdapter{
          ibkr:   ibkrAdapter,
          alpaca: alpacaAdapter,
          log:    log.With().Str("component", "ibkr_composite").Logger(),
      }
  }

  // Stub methods — implemented in Task 4.
  // All execution methods → IBKR:
  func (c *CompositeAdapter) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
      panic("CompositeAdapter.SubmitOrder: not implemented — implement in Task 4")
  }
  // ... (add all brokerAdapter methods as panicking stubs)
  ```

  Add compile-time assertions in `composite.go` for each sub-interface:
  ```go
  // Verify CompositeAdapter satisfies every sub-interface of brokerAdapter.
  // (Cannot assert against brokerAdapter directly since it's in package main.)
  var (
      _ ports.BrokerPort            = (*CompositeAdapter)(nil)
      _ ports.OrderStreamPort       = (*CompositeAdapter)(nil)
      _ ports.MarketDataPort        = (*CompositeAdapter)(nil)
      _ ports.AccountPort           = (*CompositeAdapter)(nil)
      _ ports.SnapshotPort          = (*CompositeAdapter)(nil)
      _ ports.OptionsMarketDataPort = (*CompositeAdapter)(nil)
      _ ports.OptionsPricePort      = (*CompositeAdapter)(nil)
      _ ports.UniverseProviderPort  = (*CompositeAdapter)(nil)
  )
  ```
  Note: `GetQuote`, `GetAccountEquity`, `SubscribeSymbols` are NOT in standard ports — they are
  extra methods on `brokerAdapter` in infra.go. Add them as stubs with no interface assertion needed here.

  **Must NOT do**:
  - Do NOT implement real logic in this task — panicking stubs only
  - Do NOT import from `cmd/omo-core` package (circular dependency)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: [`senior-backend`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2)
  - **Blocks**: Tasks 4, 5
  - **Blocked By**: None

  **References**:
  - `cmd/omo-core/infra.go:25-37` — full `brokerAdapter` interface (all methods needed)
  - `internal/ports/broker.go` — BrokerPort methods
  - `internal/ports/market_data.go` — MarketDataPort (StreamBars, GetHistoricalBars, Close)
  - `internal/ports/account.go` — AccountPort (GetAccountBuyingPower)
  - `internal/ports/screener.go:20-21` — SnapshotPort (GetSnapshots)
  - `internal/ports/options_market_data.go` — OptionsMarketDataPort (GetOptionChain)
  - `internal/ports/options_price.go` — OptionsPricePort (GetOptionPrices)
  - `internal/ports/universe.go` — UniverseProviderPort (ListTradeable)
  - `internal/adapters/ibkr/adapter.go:12-20` — existing interface assertions on `*Adapter`

  **Acceptance Criteria**:
  - [ ] `composite.go` exists with `CompositeAdapter` struct + `NewCompositeAdapter` constructor
  - [ ] `alpacaDataProvider` interface defined with correct method signatures
  - [ ] All sub-interface compile assertions pass
  - [ ] `cd backend && go build ./internal/adapters/ibkr/...` → exit 0

  **QA Scenarios**:
  ```
  Scenario: CompositeAdapter compiles with all port interface assertions
    Tool: Bash
    Steps:
      1. cd backend && go build ./internal/adapters/ibkr/...
    Expected Result: exit code 0
    Evidence: .sisyphus/evidence/task-3-build.txt
  ```

  **Commit**: `feat(ibkr): CompositeAdapter scaffold + alpacaDataProvider interface`
  Files: `internal/adapters/ibkr/composite.go`

---

- [ ] 4. Implement `CompositeAdapter` full routing + unit tests

  **Files**:
  - `internal/adapters/ibkr/composite.go` (replace stubs with real implementations)
  - `internal/adapters/ibkr/composite_test.go` (new)

  **Exact routing implementation** — replace all `panic(...)` stubs in `composite.go`:

  ```go
  // ── Execution → IBKR ──────────────────────────────────────────────────────

  func (c *CompositeAdapter) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
      return c.ibkr.SubmitOrder(ctx, intent)
  }
  func (c *CompositeAdapter) CancelOrder(ctx context.Context, orderID string) error {
      return c.ibkr.CancelOrder(ctx, orderID)
  }
  func (c *CompositeAdapter) CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error) {
      return c.ibkr.CancelOpenOrders(ctx, symbol, side)
  }
  func (c *CompositeAdapter) CancelAllOpenOrders(ctx context.Context) (int, error) {
      return c.ibkr.CancelAllOpenOrders(ctx)
  }
  func (c *CompositeAdapter) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
      return c.ibkr.GetOrderStatus(ctx, orderID)
  }
  func (c *CompositeAdapter) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
      return c.ibkr.GetPositions(ctx, tenantID, envMode)
  }
  func (c *CompositeAdapter) GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error) {
      return c.ibkr.GetPosition(ctx, symbol)
  }
  func (c *CompositeAdapter) ClosePosition(ctx context.Context, symbol domain.Symbol) (string, error) {
      return c.ibkr.ClosePosition(ctx, symbol)
  }
  func (c *CompositeAdapter) GetOrderDetails(ctx context.Context, orderID string) (ports.OrderDetails, error) {
      return c.ibkr.GetOrderDetails(ctx, orderID)
  }
  func (c *CompositeAdapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
      return c.ibkr.SubscribeOrderUpdates(ctx)
  }
  func (c *CompositeAdapter) GetAccountBuyingPower(ctx context.Context) (ports.BuyingPower, error) {
      return c.ibkr.GetAccountBuyingPower(ctx)
  }
  func (c *CompositeAdapter) GetAccountEquity(ctx context.Context) (float64, error) {
      return c.ibkr.GetAccountEquity(ctx)
  }
  func (c *CompositeAdapter) GetQuote(ctx context.Context, symbol domain.Symbol) (float64, float64, error) {
      return c.ibkr.GetQuote(ctx, symbol)
  }

  // SubscribeSymbols starts streaming without a handler (used by screener for price subscriptions).
  // Uses a noop BarHandler so IBKR ticks flow through bar_aggregator but results are discarded.
  func (c *CompositeAdapter) SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error {
      return c.ibkr.SubscribeSymbols(ctx, symbols)
  }

  // ── Live Streaming → IBKR ────────────────────────────────────────────────

  func (c *CompositeAdapter) StreamBars(ctx context.Context, symbols []domain.Symbol, tf domain.Timeframe, handler ports.BarHandler) error {
      return c.ibkr.StreamBars(ctx, symbols, tf, handler)
  }

  // ── Historical / Snapshot / Options → Alpaca ─────────────────────────────

  func (c *CompositeAdapter) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
      return c.alpaca.GetHistoricalBars(ctx, symbol, tf, from, to)
  }
  func (c *CompositeAdapter) GetSnapshots(ctx context.Context, symbols []string, asOf time.Time) (map[string]ports.Snapshot, error) {
      return c.alpaca.GetSnapshots(ctx, symbols, asOf)
  }
  func (c *CompositeAdapter) GetOptionChain(ctx context.Context, symbol domain.Symbol, expiry time.Time, right domain.OptionRight) ([]domain.OptionContractSnapshot, error) {
      return c.alpaca.GetOptionChain(ctx, symbol, expiry, right)
  }
  func (c *CompositeAdapter) GetOptionPrices(ctx context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error) {
      return c.alpaca.GetOptionPrices(ctx, symbols)
  }
  func (c *CompositeAdapter) ListTradeable(ctx context.Context, assetClass domain.AssetClass) ([]ports.Asset, error) {
      return c.alpaca.ListTradeable(ctx, assetClass)
  }

  // ── Lifecycle ─────────────────────────────────────────────────────────────

  func (c *CompositeAdapter) Close() error {
      ibErr := c.ibkr.Close()
      alpErr := c.alpaca.Close()
      if ibErr != nil {
          return fmt.Errorf("ibkr composite close ibkr: %w", ibErr)
      }
      return alpErr
  }
  ```

  **Exact `composite_test.go`**:
  ```go
  package ibkr_test

  import (
      "context"
      "testing"
      "time"

      "github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
      "github.com/oh-my-opentrade/backend/internal/domain"
      "github.com/oh-my-opentrade/backend/internal/ports"
      "github.com/rs/zerolog"
      "github.com/stretchr/testify/assert"
      "github.com/stretchr/testify/require"
  )

  // mockAlpacaProvider is a test double for alpacaDataProvider.
  type mockAlpacaProvider struct {
      getHistoricalBarsCalled bool
      getSnapshotsCalled      bool
      getOptionChainCalled    bool
      listTradeableCalled     bool
      closeCalled             bool
  }

  func (m *mockAlpacaProvider) GetHistoricalBars(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
      m.getHistoricalBarsCalled = true
      return nil, nil
  }
  func (m *mockAlpacaProvider) GetSnapshots(_ context.Context, _ []string, _ time.Time) (map[string]ports.Snapshot, error) {
      m.getSnapshotsCalled = true
      return nil, nil
  }
  func (m *mockAlpacaProvider) GetOptionChain(_ context.Context, _ domain.Symbol, _ time.Time, _ domain.OptionRight) ([]domain.OptionContractSnapshot, error) {
      m.getOptionChainCalled = true
      return nil, nil
  }
  func (m *mockAlpacaProvider) GetOptionPrices(_ context.Context, _ []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error) {
      return nil, nil
  }
  func (m *mockAlpacaProvider) ListTradeable(_ context.Context, _ domain.AssetClass) ([]ports.Asset, error) {
      m.listTradeableCalled = true
      return nil, nil
  }
  func (m *mockAlpacaProvider) Close() error { m.closeCalled = true; return nil }

  func makeTestComposite(t *testing.T, mock *mockIB) (*ibkr.CompositeAdapter, *mockAlpacaProvider) {
      t.Helper()
      // Build a minimal Adapter with the mock ibClient injected
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop()) // see Task 4 note below
      alpMock := &mockAlpacaProvider{}
      c := ibkr.NewCompositeAdapter(a, alpMock, zerolog.Nop())
      return c, alpMock
  }

  func TestComposite_SubmitOrder_RoutesToIBKR(t *testing.T) {
      mock := &mockIB{connected: true}
      mock.trades = []*ibsync.Trade{makeTrade(42, ibsync.Submitted, 0)}
      c, alpMock := makeTestComposite(t, mock)

      orderID, err := c.SubmitOrder(context.Background(), domain.OrderIntent{
          Symbol: "AAPL", Direction: domain.DirectionLong,
          Quantity: 1, OrderType: "market",
      })
      require.NoError(t, err)
      assert.NotEmpty(t, orderID)
      assert.Len(t, mock.placedOrders, 1, "IBKR PlaceOrder must be called")
      _ = alpMock // Alpaca NOT called
  }

  func TestComposite_GetHistoricalBars_RoutesToAlpaca(t *testing.T) {
      mock := &mockIB{connected: true}
      c, alpMock := makeTestComposite(t, mock)

      _, _ = c.GetHistoricalBars(context.Background(), "AAPL", "1m", time.Now().Add(-time.Hour), time.Now())
      assert.True(t, alpMock.getHistoricalBarsCalled, "Alpaca GetHistoricalBars must be called")
      assert.Empty(t, mock.placedOrders, "IBKR must NOT be called for historical bars")
  }

  func TestComposite_GetSnapshots_RoutesToAlpaca(t *testing.T) {
      mock := &mockIB{connected: true}
      c, alpMock := makeTestComposite(t, mock)

      _, _ = c.GetSnapshots(context.Background(), []string{"AAPL"}, time.Now())
      assert.True(t, alpMock.getSnapshotsCalled, "Alpaca GetSnapshots must be called")
  }

  func TestComposite_GetOptionChain_RoutesToAlpaca(t *testing.T) {
      mock := &mockIB{connected: true}
      c, alpMock := makeTestComposite(t, mock)

      _, _ = c.GetOptionChain(context.Background(), "AAPL", time.Now(), domain.OptionRight("C"))
      assert.True(t, alpMock.getOptionChainCalled)
  }

  func TestComposite_Close_ClosesBoth(t *testing.T) {
      mock := &mockIB{connected: true}
      c, alpMock := makeTestComposite(t, mock)

      err := c.Close()
      assert.NoError(t, err)
      assert.True(t, alpMock.closeCalled, "Alpaca Close must be called")
  }

  func TestComposite_SubscribeOrderUpdates_RoutesToIBKR(t *testing.T) {
      mock := &mockIB{connected: true}
      c, _ := makeTestComposite(t, mock)

      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()
      ch, err := c.SubscribeOrderUpdates(ctx)
      require.NoError(t, err)
      assert.NotNil(t, ch)
  }
  ```

  **Note on `NewAdapterWithClient`**: To enable testing without a real IB Gateway connection,
  add a test helper constructor to `adapter.go`:
  ```go
  // NewAdapterWithClient creates an Adapter using an already-connected ibClient.
  // For use in tests only — production code uses NewAdapter.
  func NewAdapterWithClient(client ibClient, log zerolog.Logger) *Adapter {
      conn := &connection{ib: client, log: log}
      return &Adapter{
          conn:      conn,
          log:       log,
          streaming: make(map[domain.Symbol]struct{}),
      }
  }
  ```
  (Add this to `adapter.go` — it is NOT test-only since it uses exported types.)

  **Must NOT do**:
  - Do NOT add business logic in the composite — pure delegation only
  - Do NOT import `cmd/omo-core` from adapter package

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: [`senior-backend`, `testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 5, 6)
  - **Parallel Group**: Wave 2
  - **Blocks**: Tasks 5, 10, 11
  - **Blocked By**: Tasks 1, 2, 3

  **References**:
  - `internal/adapters/ibkr/composite.go` — scaffold from Task 3
  - `cmd/omo-core/infra.go:25-37` — full `brokerAdapter` interface (all method signatures)
  - `internal/adapters/ibkr/mock_ib_test.go` — mockIB from Task 2
  - `internal/adapters/ibkr/broker.go` — IBKR method implementations to delegate to
  - `internal/adapters/ibkr/account.go` — GetAccountEquity, GetAccountBuyingPower, GetQuote

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race ./internal/adapters/ibkr/... -run TestComposite` → all PASS
  - [ ] Every `brokerAdapter` method is non-panicking in composite
  - [ ] `cd backend && go vet ./internal/adapters/ibkr/...` → PASS

  **QA Scenarios**:
  ```
  Scenario: SubmitOrder routes to IBKR
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestComposite_SubmitOrder_RoutesToIBKR
    Expected Result: PASS — mock.placedOrders len == 1
    Evidence: .sisyphus/evidence/task-4-submit-routing.txt

  Scenario: GetHistoricalBars routes to Alpaca
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestComposite_GetHistoricalBars_RoutesToAlpaca
    Expected Result: PASS — alpMock.getHistoricalBarsCalled == true
    Evidence: .sisyphus/evidence/task-4-historical-routing.txt
  ```

  **Commit**: `feat(ibkr): CompositeAdapter full routing IBKR=live Alpaca=historical+stubs`
  Files: `internal/adapters/ibkr/composite.go`, `internal/adapters/ibkr/composite_test.go`, `internal/adapters/ibkr/adapter.go`

---

- [ ] 5. Update `infra.go` BROKER=ibkr split-wiring + `concreteIBKR` field

  **Files**: `cmd/omo-core/infra.go`

  **Exact changes**:

  1. Add `concreteIBKR *ibkr.Adapter` to `infraDeps`:
  ```go
  type infraDeps struct {
      eventBus        *memory.Bus
      broker          brokerAdapter
      concreteAlpaca  *alpaca.Adapter
      concreteIBKR    *ibkr.Adapter   // ADD — non-nil only when BROKER=ibkr
      sqlDB           *sql.DB
      // ... rest unchanged
  }
  ```

  2. Replace the `case "ibkr":` block (lines 91-102) with:
  ```go
  case "ibkr":
      // Step 1: Init Alpaca in REST-only mode (no WebSocket streams).
      // Used for historical bars, snapshots, options, universe — not live streaming.
      var alpacaAdapter *alpaca.Adapter
      if err := retryWithBackoff(log, "alpaca_adapter_rest", 5, 2*time.Second, 30*time.Second, func() error {
          a, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger(), alpaca.WithNoStream())
          if err != nil {
              return err
          }
          alpacaAdapter = a
          return nil
      }); err != nil {
          log.Fatal().Err(err).Msg("failed to create Alpaca adapter (REST mode) after retries")
      }
      concreteAlpaca = alpacaAdapter

      // Step 2: Init IBKR adapter with retry (IB Gateway may not be ready immediately).
      var ibkrAdapter *ibkr.Adapter
      if err := retryWithBackoff(log, "ibkr_adapter", 10, 5*time.Second, 60*time.Second, func() error {
          a, err := ibkr.NewAdapter(cfg.IBKR, log.With().Str("component", "ibkr").Logger())
          if err != nil {
              return err
          }
          ibkrAdapter = a
          return nil
      }); err != nil {
          log.Fatal().Err(err).Msg("failed to connect to IB Gateway after retries")
      }

      // Step 3: Create composite — routes live ops to IBKR, historical to Alpaca.
      broker = ibkr.NewCompositeAdapter(ibkrAdapter, alpacaAdapter, log)
      log.Info().
          Str("broker", "ibkr").
          Str("market_data", "ibkr").
          Str("historical", "alpaca").
          Str("host", cfg.IBKR.Host).
          Int("port", cfg.IBKR.Port).
          Msg("broker initialized: IBKR live + Alpaca historical")

      // concreteAlpaca and concreteIBKR are captured below in the return struct
  ```

  3. In the return struct (line 139), add `concreteIBKR`:
  ```go
  return &infraDeps{
      eventBus:        eventBus,
      broker:          broker,
      concreteAlpaca:  concreteAlpaca,
      concreteIBKR:    ibkrAdapter,  // ADD — may be nil if BROKER=alpaca
      sqlDB:           sqlDB,
      // ... rest unchanged
  }
  ```

  Note: `ibkrAdapter` will be `nil` for `BROKER=alpaca` path, which is fine — `concreteIBKR`
  is only used in Task 10 for the health endpoint.

  **Must NOT do**:
  - Do NOT remove `concreteAlpaca` — `warmup.go` and `http.go` use it
  - Do NOT change the `default:` (Alpaca) code path
  - Do NOT skip Alpaca credentials check — `alpaca.NewAdapter` still validates them

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: [`senior-backend`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 4, 6)
  - **Parallel Group**: Wave 2
  - **Blocks**: Tasks 10, 11
  - **Blocked By**: Tasks 1, 3, 4

  **References**:
  - `cmd/omo-core/infra.go:25-50` — brokerAdapter interface + infraDeps struct
  - `cmd/omo-core/infra.go:87-116` — existing broker switch statement
  - `cmd/omo-core/warmup.go:359-382` — concreteAlpaca usage (must remain non-nil when BROKER=ibkr)
  - `cmd/omo-core/http.go:44-53` — concreteAlpaca metrics wiring
  - `internal/adapters/ibkr/composite.go` — NewCompositeAdapter constructor (Task 3/4 output)
  - `internal/adapters/alpaca/adapter.go` — WithNoStream() option (Task 1 output)

  **Acceptance Criteria**:
  - [ ] `cd backend && go build -o /dev/null ./cmd/omo-core` → PASS
  - [ ] `infraDeps.concreteIBKR` field exists
  - [ ] `BROKER=alpaca` code path: `concreteIBKR` is nil, behavior unchanged
  - [ ] `BROKER=ibkr` code path: `broker` is `*ibkr.CompositeAdapter`, `concreteAlpaca` non-nil

  **QA Scenarios**:
  ```
  Scenario: Binary builds with ibkr wiring
    Tool: Bash
    Steps:
      1. cd backend && go build -o /dev/null ./cmd/omo-core
    Expected Result: exit code 0
    Evidence: .sisyphus/evidence/task-5-build.txt

  Scenario: Full test suite still passes (Alpaca path unbroken)
    Tool: Bash
    Steps:
      1. cd backend && go test ./... -count=1 2>&1 | tail -30
    Expected Result: all tests pass, no new failures
    Evidence: .sisyphus/evidence/task-5-regression.txt
  ```

  **Commit**: `feat(infra): BROKER=ibkr split-wires IBKR+Alpaca REST + concreteIBKR field`
  Files: `cmd/omo-core/infra.go`

---

- [ ] 6. Harden `broker.go`: AccountID position filter + error wrapping + nil safety + tests

  **Files**:
  - `internal/adapters/ibkr/broker.go`
  - `internal/adapters/ibkr/broker_test.go` (new)

  **Exact changes to `broker.go`**:

  **GetPositions** — add AccountID filter after `positions := ib.Positions()`:
  ```go
  func (a *Adapter) GetPositions(_ context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
      ib := a.conn.IB()
      if ib == nil {
          return nil, fmt.Errorf("ibkr: not connected")
      }
      positions := ib.Positions()
      trades := make([]domain.Trade, 0, len(positions))
      for _, p := range positions {
          // Filter by AccountID if configured (multi-account IB setups)
          if a.cfg.AccountID != "" && p.Account != a.cfg.AccountID {
              a.log.Debug().
                  Str("account", p.Account).
                  Str("wanted", a.cfg.AccountID).
                  Msg("ibkr: skipping position from different account")
              continue
          }
          qty := p.Position.Float()
          if qty == 0 {
              continue
          }
          // ... rest of conversion unchanged
      }
      return trades, nil
  }
  ```

  **SubmitOrder** — add Quantity validation before PlaceOrder:
  ```go
  if intent.Quantity <= 0 {
      return "", fmt.Errorf("ibkr: SubmitOrder: quantity must be positive, got %f", intent.Quantity)
  }
  ```

  **ClosePosition** — handle nil trade gracefully:
  ```go
  trade := ib.PlaceOrder(contract, order)
  if trade == nil {
      a.log.Warn().Str("symbol", string(symbol)).Msg("ibkr: ClosePosition: PlaceOrder returned nil trade")
      return "", fmt.Errorf("ibkr: ClosePosition: PlaceOrder returned nil")
  }
  ```

  **Error wrapping** — all errors that wrap sub-errors must use `%w`:
  Scan broker.go for `fmt.Errorf("ibkr: ...")` — already mostly correct. Verify:
  - Line 60: `fmt.Errorf("ibkr: invalid orderID %q: %w", orderID, err)` ✓
  - Any new errors added must use `%w` when wrapping

  **Zerolog debug logging** — add entry log to SubmitOrder, CancelOrder:
  ```go
  // In SubmitOrder, before PlaceOrder:
  a.log.Debug().Str("symbol", string(intent.Symbol)).Float64("qty", intent.Quantity).Str("direction", string(intent.Direction)).Msg("ibkr: SubmitOrder called")
  // In CancelOrder, before loop:
  a.log.Debug().Str("order_id", orderID).Msg("ibkr: CancelOrder called")
  ```

  **Exact `broker_test.go`**:
  ```go
  package ibkr_test

  func TestSubmitOrder_NotConnected(t *testing.T) {
      mock := &mockIB{connected: false}
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      _, err := a.SubmitOrder(context.Background(), domain.OrderIntent{Symbol: "AAPL", Quantity: 1, Direction: domain.DirectionLong, OrderType: "market"})
      require.Error(t, err)
      assert.Contains(t, err.Error(), "ibkr: not connected")
  }

  func TestSubmitOrder_ZeroQuantity_ReturnsError(t *testing.T) {
      mock := &mockIB{connected: true}
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      _, err := a.SubmitOrder(context.Background(), domain.OrderIntent{Symbol: "AAPL", Quantity: 0})
      require.Error(t, err)
      assert.Contains(t, err.Error(), "quantity must be positive")
  }

  func TestSubmitOrder_Success_ReturnsOrderID(t *testing.T) {
      mock := &mockIB{connected: true}
      // PlaceOrder mock returns trade with OrderID 42
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      orderID, err := a.SubmitOrder(context.Background(), domain.OrderIntent{
          Symbol: "AAPL", Quantity: 10, Direction: domain.DirectionLong, OrderType: "market",
      })
      require.NoError(t, err)
      assert.NotEmpty(t, orderID)
      assert.Len(t, mock.placedOrders, 1)
  }

  func TestCancelOrder_NotFound_ReturnsError(t *testing.T) {
      mock := &mockIB{connected: true, openTrades: []*ibsync.Trade{}}
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      err := a.CancelOrder(context.Background(), "999")
      require.Error(t, err)
      assert.Contains(t, err.Error(), "open order 999 not found")
  }

  func TestGetPositions_FiltersAccountID(t *testing.T) {
      mock := &mockIB{
          connected: true,
          positions: []ibsync.Position{
              {Account: "DU111111", Contract: ibsync.Contract{Symbol: "AAPL"}, Position: ibsync.StringToDecimal("10")},
              {Account: "DU999999", Contract: ibsync.Contract{Symbol: "MSFT"}, Position: ibsync.StringToDecimal("5")},
          },
      }
      // Create adapter with AccountID = "DU111111"
      a := ibkr.NewAdapterWithClientAndCfg(mock, config.IBKRConfig{AccountID: "DU111111"}, zerolog.Nop())
      trades, err := a.GetPositions(context.Background(), "tenant", domain.Paper)
      require.NoError(t, err)
      require.Len(t, trades, 1, "only DU111111 positions returned")
      assert.Equal(t, domain.Symbol("AAPL"), trades[0].Symbol)
  }

  func TestGetPositions_EmptyAccountID_ReturnsAll(t *testing.T) {
      mock := &mockIB{
          connected: true,
          positions: []ibsync.Position{
              {Account: "DU111111", Contract: ibsync.Contract{Symbol: "AAPL"}, Position: ibsync.StringToDecimal("10")},
              {Account: "DU999999", Contract: ibsync.Contract{Symbol: "MSFT"}, Position: ibsync.StringToDecimal("5")},
          },
      }
      a := ibkr.NewAdapterWithClientAndCfg(mock, config.IBKRConfig{AccountID: ""}, zerolog.Nop())
      trades, err := a.GetPositions(context.Background(), "tenant", domain.Paper)
      require.NoError(t, err)
      assert.Len(t, trades, 2, "all accounts returned when AccountID empty")
  }

  func TestGetPosition_ZeroWhenMissing(t *testing.T) {
      mock := &mockIB{connected: true, positions: []ibsync.Position{}}
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      qty, err := a.GetPosition(context.Background(), "UNKNOWN")
      require.NoError(t, err)
      assert.Equal(t, float64(0), qty)
  }
  ```

  **Add `NewAdapterWithClientAndCfg`** to `adapter.go`:
  ```go
  func NewAdapterWithClientAndCfg(client ibClient, cfg config.IBKRConfig, log zerolog.Logger) *Adapter {
      conn := &connection{ib: client, log: log}
      return &Adapter{conn: conn, cfg: cfg, log: log, streaming: make(map[domain.Symbol]struct{})}
  }
  ```

  **Must NOT do**:
  - Do NOT change BrokerPort interface signatures
  - Do NOT add options/crypto logic (deferred)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: [`senior-backend`, `testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 4, 5)
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 11
  - **Blocked By**: Tasks 1, 2

  **References**:
  - `internal/adapters/ibkr/broker.go` — full file (298 lines)
  - `internal/adapters/ibkr/mock_ib_test.go` — mockIB from Task 2
  - `internal/config/config.go:31-36` — IBKRConfig (after Task 1 adds AccountID)
  - `ibsync.Position` struct: has `Account string`, `Contract`, `Position Decimal`, `AvgCost float64`

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestSubmitOrder` → PASS
  - [ ] `cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestGetPositions` → PASS
  - [ ] `cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestCancelOrder` → PASS
  - [ ] AccountID filter verified in `TestGetPositions_FiltersAccountID`

  **QA Scenarios**:
  ```
  Scenario: AccountID filter in GetPositions
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestGetPositions_FiltersAccountID
    Expected Result: PASS — only DU111111 position returned
    Evidence: .sisyphus/evidence/task-6-accountid-filter.txt

  Scenario: SubmitOrder returns error when not connected
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestSubmitOrder_NotConnected
    Expected Result: PASS — error contains "ibkr: not connected"
    Evidence: .sisyphus/evidence/task-6-not-connected.txt
  ```

  **Commit**: `fix(ibkr): broker.go AccountID filter + error wrapping + nil safety`
  Files: `internal/adapters/ibkr/broker.go`, `internal/adapters/ibkr/broker_test.go`, `internal/adapters/ibkr/adapter.go`

---

- [ ] 7. Rewrite `StreamBars` via `ReqRealTimeBars` + harden `bar_aggregator.go` + tests

  **Files**:
  - `internal/adapters/ibkr/market_data.go` (rewrite StreamBars only; keep GetHistoricalBars, Close, SubscribeSymbols)
  - `internal/adapters/ibkr/bar_aggregator.go` (add public `Feed` method, keep existing `add`)
  - `internal/adapters/ibkr/market_data_test.go` (new)
  - `internal/adapters/ibkr/bar_aggregator_test.go` (new)

  **CRITICAL ibsync fact**: `ReqRealTimeBars` returns `(chan RealTimeBar, CancelFunc)` where
  `ibsync.RealTimeBar` is `ibapi.RealTimeBar` with `Time int64` (Unix seconds), NOT `time.Time`.
  The existing `bar_aggregator.go` already handles this correctly via `time.Unix(rtb.Time, 0).UTC()`.

  **Exact new `StreamBars` in `market_data.go`**:
  ```go
  const maxRealTimeBarsSymbols = 50 // IBKR pacing limit

  func (a *Adapter) StreamBars(ctx context.Context, symbols []domain.Symbol, tf domain.Timeframe, handler ports.BarHandler) error {
      if len(symbols) > maxRealTimeBarsSymbols {
          return fmt.Errorf("ibkr: StreamBars: pacing limit exceeded: max %d symbols, got %d", maxRealTimeBarsSymbols, len(symbols))
      }
      ib := a.conn.IB()
      if ib == nil {
          return fmt.Errorf("ibkr: StreamBars: not connected")
      }

      a.streamMu.Lock()
      a.barCtx = ctx
      a.barTF = tf
      a.barHdl = handler
      a.streamMu.Unlock()

      var wg sync.WaitGroup
      var cancelMu sync.Mutex
      cancelFuncs := make([]ibsync.CancelFunc, 0, len(symbols))

      for _, sym := range symbols {
          a.streamMu.Lock()
          a.streaming[sym] = struct{}{}
          a.streamMu.Unlock()

          contract := newContract(sym)
          // barSize=5: IBKR only supports barSize=5 for ReqRealTimeBars
          barCh, cancel := ib.ReqRealTimeBars(contract, 5, "TRADES", false)

          cancelMu.Lock()
          cancelFuncs = append(cancelFuncs, cancel)
          cancelMu.Unlock()

          wg.Add(1)
          go func(symbol domain.Symbol, ch <-chan ibsync.RealTimeBar) {
              defer wg.Done()
              agg := newBarAggregator(symbol, tf)
              a.log.Debug().Str("symbol", string(symbol)).Str("tf", string(tf)).Msg("ibkr: real-time bar stream started")
              for {
                  select {
                  case rtb, ok := <-ch:
                      if !ok {
                          a.log.Debug().Str("symbol", string(symbol)).Msg("ibkr: real-time bar channel closed")
                          return
                      }
                      if mb := agg.add(rtb); mb != nil {
                          if err := handler(ctx, *mb); err != nil {
                              a.log.Error().Err(err).Str("symbol", string(symbol)).Msg("ibkr: bar handler error")
                          }
                      }
                  case <-ctx.Done():
                      return
                  }
              }
          }(sym, barCh)
      }

      // Register reconnect callback: when connection drops and recovers,
      // re-subscribe all streaming symbols.
      a.conn.OnReconnect(func() {
          a.streamMu.RLock()
          hdl := a.barHdl
          tf := a.barTF
          streamCtx := a.barCtx
          syms := make([]domain.Symbol, 0, len(a.streaming))
          for s := range a.streaming {
              syms = append(syms, s)
          }
          a.streamMu.RUnlock()
          if hdl == nil || streamCtx == nil {
              return
          }
          // Re-subscribe (ignore errors — keepAlive handles reconnect)
          _ = a.StreamBars(streamCtx.(context.Context), syms, tf, hdl)
      })

      // Cancel all subscriptions when context is done.
      go func() {
          <-ctx.Done()
          cancelMu.Lock()
          for _, cancel := range cancelFuncs {
              cancel()
          }
          cancelMu.Unlock()
          wg.Wait()
          a.log.Debug().Msg("ibkr: StreamBars: all subscriptions cancelled")
      }()

      return nil // Non-blocking: returns immediately after starting goroutines
  }
  ```

  **Note on `barCtx`**: The existing `Adapter` struct stores `barCtx interface{ Done() <-chan struct{} }`.
  This is an interface, so passing `context.Context` satisfies it. The cast `streamCtx.(context.Context)`
  is safe because only `context.Context` values are stored there.

  **`SubscribeSymbols`** — update to use `StreamBars` with noop handler:
  ```go
  func (a *Adapter) SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error {
      a.streamMu.RLock()
      hdl := a.barHdl
      tf := a.barTF
      streamCtx := a.barCtx
      a.streamMu.RUnlock()

      // If StreamBars has been called already, add new symbols to the existing stream.
      if hdl != nil && streamCtx != nil {
          newSyms := make([]domain.Symbol, 0)
          a.streamMu.Lock()
          for _, s := range symbols {
              if _, exists := a.streaming[s]; !exists {
                  newSyms = append(newSyms, s)
                  a.streaming[s] = struct{}{}
              }
          }
          a.streamMu.Unlock()
          if len(newSyms) == 0 {
              return nil
          }
          return a.StreamBars(streamCtx.(context.Context), newSyms, tf, hdl)
      }
      // No active stream — start with noop handler
      noop := func(_ context.Context, _ domain.MarketBar) error { return nil }
      return a.StreamBars(ctx, symbols, "1m", noop)
  }
  ```

  **Harden `bar_aggregator.go`**:
  Keep existing `add(rtb ibsync.RealTimeBar) *domain.MarketBar` method unchanged.
  Add public alias for use in tests:
  ```go
  // Feed is the public equivalent of add — adds a 5s real-time bar and returns
  // a completed MarketBar when a period boundary is crossed, or nil otherwise.
  func (a *barAggregator) Feed(rtb ibsync.RealTimeBar) *domain.MarketBar {
      return a.add(rtb)
  }
  ```

  Fix VWAP population (currently missing in `completed` bar):
  ```go
  completed := &domain.MarketBar{
      Symbol:    a.symbol,
      Timeframe: a.timeframe,
      Time:      a.barStart,
      Open:      a.open,
      High:      a.high,
      Low:       a.low,
      Close:     a.close,
      Volume:    a.volume,
      VWAP:      0, // IBKR ReqRealTimeBars provides WAP — store in reset and accumulate
  }
  ```
  Actually add WAP tracking to `barAggregator`:
  ```go
  type barAggregator struct {
      // ... existing fields ...
      vwapNumer float64 // sum(price * vol)
      vwapDenom float64 // sum(vol)
  }

  func (a *barAggregator) reset(barStart time.Time, rtb ibsync.RealTimeBar) {
      a.barStart = barStart
      a.open = rtb.Open
      a.high = rtb.High
      a.low = rtb.Low
      a.close = rtb.Close
      vol := rtb.Volume.Float()
      a.volume = vol
      wap := rtb.Wap.Float()
      a.vwapNumer = wap * vol
      a.vwapDenom = vol
      a.hasData = true
  }

  // In add(), accumulate:
  wap := rtb.Wap.Float()
  vol := rtb.Volume.Float()
  a.vwapNumer += wap * vol
  a.vwapDenom += vol
  a.volume += vol

  // In emitted completed bar:
  var vwap float64
  if a.vwapDenom > 0 { vwap = a.vwapNumer / a.vwapDenom }
  completed := &domain.MarketBar{..., VWAP: vwap}
  ```

  **Exact `bar_aggregator_test.go`**:
  ```go
  package ibkr_test

  func makeRTB(unixTs int64, open, high, low, close, vol, wap float64) ibsync.RealTimeBar {
      rtb := ibsync.NewRealTimeBar()
      rtb.Time = unixTs
      rtb.Open = open
      rtb.High = high
      rtb.Low = low
      rtb.Close = close
      rtb.Volume = ibsync.StringToDecimal(fmt.Sprintf("%.6f", vol))
      rtb.Wap = ibsync.StringToDecimal(fmt.Sprintf("%.6f", wap))
      return rtb
  }

  // minute boundary Unix timestamps for testing (truncated to minute)
  var (
      min0 = time.Date(2025, 1, 2, 9, 30, 0, 0, time.UTC).Unix()
      min1 = time.Date(2025, 1, 2, 9, 31, 0, 0, time.UTC).Unix()
  )

  func TestBarAggregator_1Min_12BarsYieldOneBar(t *testing.T) {
      agg := newBarAggregatorExported("AAPL", "1m") // use package-level func if exported
      // Feed 12 five-second bars all within the same minute
      for i := 0; i < 12; i++ {
          mb := agg.Feed(makeRTB(min0+int64(i*5), 100, 101, 99, 100, 100, 100))
          assert.Nil(t, mb, "bar %d: should not emit until minute boundary", i)
      }
      // 13th bar crosses minute boundary → emit previous minute
      mb := agg.Feed(makeRTB(min1, 102, 103, 101, 102, 50, 102))
      require.NotNil(t, mb, "crossing minute boundary must emit bar")
  }

  func TestBarAggregator_OHLCV_Correct(t *testing.T) {
      agg := newBarAggregatorExported("AAPL", "1m")
      // First bar in minute 0
      agg.Feed(makeRTB(min0+0,  100, 105, 98, 102, 500, 101)) // open=100, high=105, low=98
      agg.Feed(makeRTB(min0+5,  102, 103, 99, 101, 300, 102)) // high stays 105, low stays 98
      agg.Feed(makeRTB(min0+10, 101, 102, 97, 100, 200, 100)) // low drops to 97, close=100

      // Trigger emission with first bar of next minute
      mb := agg.Feed(makeRTB(min1, 99, 99, 99, 99, 10, 99))
      require.NotNil(t, mb)
      assert.Equal(t, float64(100), mb.Open,   "open must be first bar open")
      assert.Equal(t, float64(105), mb.High,   "high must be max across all bars")
      assert.Equal(t, float64(97),  mb.Low,    "low must be min across all bars")
      assert.Equal(t, float64(100), mb.Close,  "close must be last bar close")
      assert.Equal(t, float64(1000), mb.Volume, "volume must be sum: 500+300+200")
  }

  func TestBarAggregator_Symbol_Timeframe_Propagated(t *testing.T) {
      agg := newBarAggregatorExported("MSFT", "5m")
      agg.Feed(makeRTB(min0, 200, 201, 199, 200, 100, 200))
      mb := agg.Feed(makeRTB(min0+int64(5*60), 201, 202, 200, 201, 50, 201))
      require.NotNil(t, mb)
      assert.Equal(t, domain.Symbol("MSFT"), mb.Symbol)
      assert.Equal(t, domain.Timeframe("5m"), mb.Timeframe)
  }

  func TestStreamBars_PacingLimit_ReturnsError(t *testing.T) {
      mock := &mockIB{connected: true}
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      syms := make([]domain.Symbol, 51)
      for i := range syms { syms[i] = domain.Symbol(fmt.Sprintf("SYM%d", i)) }
      err := a.StreamBars(context.Background(), syms, "1m", func(_ context.Context, _ domain.MarketBar) error { return nil })
      require.Error(t, err)
      assert.Contains(t, err.Error(), "pacing limit exceeded: max 50")
  }

  func TestStreamBars_ContextCancel_ClosesCancelFuncs(t *testing.T) {
      mock := &mockIB{connected: true}
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      ctx, cancel := context.WithCancel(context.Background())

      err := a.StreamBars(ctx, []domain.Symbol{"AAPL"}, "1m", func(_ context.Context, _ domain.MarketBar) error { return nil })
      require.NoError(t, err)

      cancel() // triggers cleanup goroutine
      time.Sleep(100 * time.Millisecond) // allow goroutine to run

      // Verify the channel created by mockIB was closed (cancel func called)
      mock.mu.Lock()
      var ch chan ibsync.RealTimeBar
      for _, c := range mock.rtBarChans { ch = c; break }
      mock.mu.Unlock()
      require.NotNil(t, ch)
      select {
      case _, ok := <-ch:
          assert.False(t, ok, "channel must be closed after cancel")
      default:
          t.Fatal("expected channel to be closed")
      }
  }
  ```

  Note: `newBarAggregatorExported` — if `newBarAggregator` is unexported, use `package ibkr` (white-box) instead of `package ibkr_test`, or export it temporarily for testing. Use `package ibkr` for bar aggregator tests.

  **Must NOT do**:
  - Do NOT change `GetHistoricalBars` — still uses IBKR historical API (will be routed via Alpaca in composite, but GetHistoricalBars on ibkr.Adapter itself stays functional)
  - Do NOT start goroutines that outlive the context

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: [`senior-backend`, `testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 8, 9)
  - **Parallel Group**: Wave 3
  - **Blocks**: Task 11
  - **Blocked By**: Task 2

  **References**:
  - `internal/adapters/ibkr/market_data.go` — current StreamBars polling (full 252 lines to replace)
  - `internal/adapters/ibkr/bar_aggregator.go` — existing aggregator (93 lines, extend not replace)
  - `/home/ridopark/go/pkg/mod/github.com/scmhub/ibsync@v0.10.44/ib.go:2155` — `ReqRealTimeBars(contract, barSize int, whatToShow string, useRTH bool) (chan RealTimeBar, CancelFunc)`
  - `/home/ridopark/go/pkg/mod/github.com/scmhub/ibapi@v0.10.44/bar.go:30-40` — `RealTimeBar` struct: `Time int64`, `Open/High/Low/Close float64`, `Volume Decimal`, `Wap Decimal`
  - `internal/adapters/ibkr/connection.go` — `OnReconnect` method (Task 9 output — add stub first, implement in Task 9)

  **Acceptance Criteria**:
  - [ ] `StreamBars` returns error for >50 symbols
  - [ ] `StreamBars` uses `ReqRealTimeBars` not polling loop
  - [ ] `cd backend && go test -race ./internal/adapters/ibkr/... -run TestStreamBars` → PASS
  - [ ] `cd backend && go test -race ./internal/adapters/ibkr/... -run TestBarAggregator` → PASS

  **QA Scenarios**:
  ```
  Scenario: Pacing limit enforced at 50 symbols
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestStreamBars_PacingLimit
    Expected Result: PASS — error "pacing limit exceeded: max 50"
    Evidence: .sisyphus/evidence/task-7-pacing-limit.txt

  Scenario: OHLCV values correct across accumulated 5s bars
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestBarAggregator_OHLCV_Correct
    Expected Result: PASS — open=100, high=105, low=97, close=100, vol=1000
    Evidence: .sisyphus/evidence/task-7-bar-aggregator.txt
  ```

  **Commit**: `feat(ibkr): StreamBars via ReqRealTimeBars + bar_aggregator WAP + tests`
  Files: `internal/adapters/ibkr/market_data.go`, `internal/adapters/ibkr/bar_aggregator.go`, `internal/adapters/ibkr/market_data_test.go`, `internal/adapters/ibkr/bar_aggregator_test.go`

---

- [ ] 8. Harden `order_stream.go`: reduce to 200ms + improve cache-diff + tests

  **Files**:
  - `internal/adapters/ibkr/order_stream.go` (change poll interval, keep architecture)
  - `internal/adapters/ibkr/order_stream_test.go` (new)

  **What to change** (minimal — existing architecture is sound):

  Change `orderPollInterval` from 500ms to 200ms:
  ```go
  const orderPollInterval = 200 * time.Millisecond
  ```

  The existing `pollOrderUpdates` already has cache-diff logic. The spec's improvement:
  also handle the case where `ib` becomes nil during reconnect — re-fetch each poll:

  Change `pollOrderUpdates` to NOT capture `ib` at subscription time, re-fetch each tick:
  ```go
  func (a *Adapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
      ib := a.conn.IB()
      if ib == nil {
          return nil, fmt.Errorf("ibkr: not connected")
      }
      out := make(chan ports.OrderUpdate, 64)
      go a.pollOrderUpdates(ctx, out) // no longer passes ib — fetches it each tick
      return out, nil
  }

  func (a *Adapter) pollOrderUpdates(ctx context.Context, out chan<- ports.OrderUpdate) {
      defer close(out)

      type tradeState struct {
          status ibsync.Status
          filled float64
      }
      seen := make(map[int64]tradeState)

      ticker := time.NewTicker(orderPollInterval)
      defer ticker.Stop()

      for {
          select {
          case <-ctx.Done():
              return
          case <-ticker.C:
              ib := a.conn.IB() // re-fetch each tick — survives reconnect
              if ib == nil {
                  continue // reconnecting — skip this poll
              }
              trades := ib.Trades()
              for _, t := range trades {
                  if t.Order == nil {
                      continue
                  }
                  id := t.Order.OrderID
                  cur := tradeState{
                      status: t.OrderStatus.Status,
                      filled: t.OrderStatus.Filled.Float(),
                  }
                  prev, existed := seen[id]
                  seen[id] = cur

                  shouldEmit := !existed ||
                      cur.status != prev.status ||
                      (cur.status == ibsync.Submitted && cur.filled > prev.filled)

                  if shouldEmit {
                      update := tradeToOrderUpdate(t)
                      select {
                      case out <- update:
                      case <-ctx.Done():
                          return
                      }
                  }
              }
          }
      }
  }
  ```

  Note: Removed the `mu sync.Mutex` inside the goroutine — `seen` map is local to the goroutine
  and only accessed from that single goroutine, so no mutex needed.

  **Exact `order_stream_test.go`**:
  ```go
  package ibkr_test

  func TestOrderStream_EmitsOnStatusChange(t *testing.T) {
      mock := &mockIB{connected: true}
      // Start with Submitted trade
      t42 := makeTrade(42, ibsync.Submitted, 0)
      mock.trades = []*ibsync.Trade{t42}

      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()

      ch, err := a.SubscribeOrderUpdates(ctx)
      require.NoError(t, err)

      // First event: "new" (Submitted)
      var firstUpdate ports.OrderUpdate
      select {
      case firstUpdate = <-ch:
      case <-time.After(500 * time.Millisecond):
          t.Fatal("timeout waiting for first order update (Submitted)")
      }
      assert.Equal(t, "new", firstUpdate.Event)
      assert.Equal(t, "42", firstUpdate.BrokerOrderID)

      // Change status to Filled
      mock.mu.Lock()
      t42.OrderStatus.Status = ibsync.Filled
      t42.OrderStatus.Filled = ibsync.StringToDecimal("10")
      t42.OrderStatus.AvgFillPrice = 150.50
      mock.mu.Unlock()

      // Second event: "fill"
      var fillUpdate ports.OrderUpdate
      select {
      case fillUpdate = <-ch:
      case <-time.After(500 * time.Millisecond):
          t.Fatal("timeout waiting for fill order update")
      }
      assert.Equal(t, "fill", fillUpdate.Event)
      assert.Equal(t, float64(10), fillUpdate.FilledQty)
      assert.Equal(t, float64(150.50), fillUpdate.FilledAvgPrice)
  }

  func TestOrderStream_NoDuplicateEvents(t *testing.T) {
      mock := &mockIB{connected: true}
      t42 := makeTrade(42, ibsync.Submitted, 0)
      mock.trades = []*ibsync.Trade{t42}

      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()

      ch, err := a.SubscribeOrderUpdates(ctx)
      require.NoError(t, err)

      // Read first event
      <-ch

      // Wait 3 polls (3 * 200ms = 600ms) — status unchanged
      time.Sleep(600 * time.Millisecond)

      // Channel should have nothing new
      select {
      case update := <-ch:
          t.Fatalf("unexpected duplicate event: %+v", update)
      default:
          // correct — no duplicates
      }
  }

  func TestOrderStream_ChannelClosedOnCancel(t *testing.T) {
      mock := &mockIB{connected: true}
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      ctx, cancel := context.WithCancel(context.Background())

      ch, err := a.SubscribeOrderUpdates(ctx)
      require.NoError(t, err)

      cancel()

      select {
      case _, ok := <-ch:
          assert.False(t, ok, "channel must be closed after context cancel")
      case <-time.After(500 * time.Millisecond):
          t.Fatal("channel not closed within 500ms of context cancel")
      }
  }

  func TestOrderStream_FilledEventFields(t *testing.T) {
      mock := &mockIB{connected: true}
      t99 := makeTrade(99, ibsync.Filled, 5)
      t99.OrderStatus.AvgFillPrice = 200.25
      mock.trades = []*ibsync.Trade{t99}

      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()

      ch, err := a.SubscribeOrderUpdates(ctx)
      require.NoError(t, err)

      var update ports.OrderUpdate
      select {
      case update = <-ch:
      case <-time.After(500 * time.Millisecond):
          t.Fatal("timeout waiting for fill event")
      }
      assert.Equal(t, "fill", update.Event)
      assert.Equal(t, "99", update.BrokerOrderID)
      assert.Equal(t, float64(5), update.FilledQty)
      assert.Equal(t, float64(200.25), update.FilledAvgPrice)
  }

  func TestOrderStream_ReconnectFetchesNewIB(t *testing.T) {
      // Verify that after nil IB (simulating disconnect), updates resume
      mock := &mockIB{connected: true}
      a := ibkr.NewAdapterWithClient(mock, zerolog.Nop())
      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()

      ch, err := a.SubscribeOrderUpdates(ctx)
      require.NoError(t, err)

      // Simulate disconnect: set IB to nil on mock (connection lost)
      // Then re-add a trade after "reconnect"
      // The poller must pick it up without restarting SubscribeOrderUpdates
      mock.mu.Lock()
      mock.connected = false
      mock.mu.Unlock()
      time.Sleep(300 * time.Millisecond) // let a few polls see nil IB

      // "Reconnect": add a new trade
      t77 := makeTrade(77, ibsync.Submitted, 0)
      mock.mu.Lock()
      mock.connected = true
      mock.trades = []*ibsync.Trade{t77}
      mock.mu.Unlock()

      select {
      case update := <-ch:
          assert.Equal(t, "77", update.BrokerOrderID)
      case <-time.After(time.Second):
          t.Fatal("timeout waiting for update after reconnect simulation")
      }
  }
  ```

  Note: `TestOrderStream_ReconnectFetchesNewIB` works because `pollOrderUpdates` now calls
  `a.conn.IB()` each tick. The `mockIB.IsConnected()` check in `SubscribeOrderUpdates` initial
  call ensures we start. The `nil` IB simulation requires the mock connection to return nil — but
  `NewAdapterWithClient` sets `conn.ib` directly. The test simulates by having the mock's
  `Trades()` return empty while `connected=false`. Adjust test if needed.

  **Must NOT do**:
  - Do NOT remove the channel buffer (keep `make(chan ports.OrderUpdate, 64)`)
  - Do NOT remove `tradeToOrderUpdate` and `mapStatusToEvent` helper functions

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: [`senior-backend`, `testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 7, 9)
  - **Parallel Group**: Wave 3
  - **Blocks**: Task 11
  - **Blocked By**: Task 2

  **References**:
  - `internal/adapters/ibkr/order_stream.go` — current implementation (129 lines)
  - `internal/adapters/ibkr/mock_ib_test.go` — mockIB with Trades() method
  - `internal/ports/broker.go:44-67` — OrderUpdate struct fields

  **Acceptance Criteria**:
  - [ ] `orderPollInterval == 200 * time.Millisecond`
  - [ ] `pollOrderUpdates` calls `a.conn.IB()` each tick (not captured at subscribe time)
  - [ ] `cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestOrderStream` → all PASS
  - [ ] No data races with `-race`

  **QA Scenarios**:
  ```
  Scenario: Submitted→Filled emits two distinct events
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestOrderStream_EmitsOnStatusChange -timeout 10s
    Expected Result: PASS — "new" then "fill" events received
    Evidence: .sisyphus/evidence/task-8-status-change.txt

  Scenario: Channel closed within 500ms of context cancel
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestOrderStream_ChannelClosedOnCancel -timeout 5s
    Expected Result: PASS — channel closed, no timeout
    Evidence: .sisyphus/evidence/task-8-ctx-cancel.txt
  ```

  **Commit**: `fix(ibkr): order stream 200ms cache-diff + reconnect-safe + tests`
  Files: `internal/adapters/ibkr/order_stream.go`, `internal/adapters/ibkr/order_stream_test.go`

---

- [ ] 9. Add `OnReconnect` callback mechanism to `connection.go` + tests

  **Files**:
  - `internal/adapters/ibkr/connection.go`
  - `internal/adapters/ibkr/connection_test.go` (new)

  **Exact changes to `connection.go`**:

  Add fields to `connection` struct:
  ```go
  type connection struct {
      ib      ibClient
      cfg     config.IBKRConfig
      log     zerolog.Logger
      symHook *symbolHook
      ctx     context.Context
      cancel  context.CancelFunc
      mu      sync.RWMutex

      // Reconnect callback support
      reconnectSubs []func()   // registered callbacks
      subsMu        sync.Mutex // protects reconnectSubs
  }
  ```

  Add `OnReconnect` method:
  ```go
  // OnReconnect registers a function to be called (in a new goroutine) after
  // each successful reconnect. Safe to call concurrently.
  func (c *connection) OnReconnect(fn func()) {
      c.subsMu.Lock()
      defer c.subsMu.Unlock()
      c.reconnectSubs = append(c.reconnectSubs, fn)
  }

  // fireReconnectCallbacks dispatches all registered callbacks in separate goroutines.
  func (c *connection) fireReconnectCallbacks() {
      c.subsMu.Lock()
      fns := make([]func(), len(c.reconnectSubs))
      copy(fns, c.reconnectSubs)
      c.subsMu.Unlock()
      for _, fn := range fns {
          go fn()
      }
  }
  ```

  Update `keepAlive` to call `fireReconnectCallbacks` after successful reconnect:
  ```go
  func (c *connection) keepAlive() {
      delay := reconnectInitialDelay
      ticker := time.NewTicker(delay)
      defer ticker.Stop()

      for {
          select {
          case <-c.ctx.Done():
              return
          case <-ticker.C:
              if !c.isConnected() {
                  c.log.Warn().Dur("retry_in", delay).Msg("ibkr: connection lost, reconnecting")
                  if err := c.connect(); err != nil {
                      c.log.Error().Err(err).Msg("ibkr: reconnect failed")
                      delay *= 2
                      if delay > reconnectMaxDelay {
                          delay = reconnectMaxDelay
                      }
                  } else {
                      delay = reconnectInitialDelay
                      c.fireReconnectCallbacks() // ADD THIS LINE
                  }
                  ticker.Reset(delay)
              }
          }
      }
  }
  ```

  **Exact `connection_test.go`**:
  ```go
  package ibkr_test

  func TestOnReconnect_CallbackFiredAfterReconnect(t *testing.T) {
      // We can't easily test keepAlive without a real IB connection.
      // Test the fireReconnectCallbacks mechanism directly via an exported test helper
      // OR test OnReconnect registration via white-box test in package ibkr.
      // Use package ibkr (white-box) for this test.
  }
  ```

  Since `fireReconnectCallbacks` is unexported, test it via a package-level test in `package ibkr`:

  Create `connection_test.go` in `package ibkr` (white-box):
  ```go
  package ibkr

  import (
      "sync"
      "sync/atomic"
      "testing"
      "time"

      "github.com/rs/zerolog"
      "github.com/stretchr/testify/assert"
  )

  func TestOnReconnect_SingleCallback(t *testing.T) {
      conn := &connection{log: zerolog.Nop()}
      var called atomic.Bool
      conn.OnReconnect(func() { called.Store(true) })
      conn.fireReconnectCallbacks()
      time.Sleep(50 * time.Millisecond)
      assert.True(t, called.Load(), "callback must be called")
  }

  func TestOnReconnect_MultipleCallbacks(t *testing.T) {
      conn := &connection{log: zerolog.Nop()}
      var count atomic.Int32
      conn.OnReconnect(func() { count.Add(1) })
      conn.OnReconnect(func() { count.Add(1) })
      conn.OnReconnect(func() { count.Add(1) })
      conn.fireReconnectCallbacks()
      time.Sleep(50 * time.Millisecond)
      assert.Equal(t, int32(3), count.Load())
  }

  func TestOnReconnect_ConcurrentSafe(t *testing.T) {
      conn := &connection{log: zerolog.Nop()}
      var wg sync.WaitGroup
      for i := 0; i < 100; i++ {
          wg.Add(1)
          go func() {
              defer wg.Done()
              conn.OnReconnect(func() {})
          }()
      }
      wg.Wait()
      // Fire callbacks while registrations may still be happening
      conn.fireReconnectCallbacks()
      // No panic = success (race detector will catch data races)
  }

  func TestConnection_IsConnected_ThreadSafe(t *testing.T) {
      mock := &mockIB{connected: true}
      conn := &connection{ib: mock, log: zerolog.Nop()}
      var wg sync.WaitGroup
      for i := 0; i < 100; i++ {
          wg.Add(1)
          go func() {
              defer wg.Done()
              _ = conn.isConnected()
          }()
      }
      wg.Wait()
  }
  ```

  Note: `mockIB` used in `package ibkr` white-box test requires it to be in the same package
  or defined in a `_test.go` file with `package ibkr`. Move `mock_ib_test.go` to `package ibkr`
  (not `package ibkr_test`) or duplicate the minimal mock needed.

  **Must NOT do**:
  - Do NOT change the `keepAlive` backoff constants (5s initial, 60s max)
  - Do NOT fire callbacks synchronously — always `go fn()`

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: [`senior-backend`, `testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 7, 8)
  - **Parallel Group**: Wave 3
  - **Blocks**: Tasks 7, 11 (Task 7 uses OnReconnect — add a stub if Task 9 not done)
  - **Blocked By**: Task 2

  **References**:
  - `internal/adapters/ibkr/connection.go` — full file (133 lines)
  - `internal/adapters/ibkr/connection.go:86-111` — keepAlive function (add fireReconnectCallbacks call)

  **Acceptance Criteria**:
  - [ ] `OnReconnect` method exists on `*connection`
  - [ ] `fireReconnectCallbacks` dispatches all registered callbacks asynchronously
  - [ ] `cd backend && go test -race ./internal/adapters/ibkr/... -run TestOnReconnect` → PASS
  - [ ] `cd backend && go test -race ./internal/adapters/ibkr/... -run TestConnection_IsConnected_ThreadSafe` → PASS

  **QA Scenarios**:
  ```
  Scenario: All 3 registered callbacks fired
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestOnReconnect_MultipleCallbacks -timeout 5s
    Expected Result: PASS — count.Load() == 3
    Evidence: .sisyphus/evidence/task-9-multi-callback.txt

  Scenario: IsConnected thread-safe under 100 goroutines
    Tool: Bash
    Steps:
      1. cd backend && go test -race -v ./internal/adapters/ibkr/... -run TestConnection_IsConnected_ThreadSafe -timeout 5s
    Expected Result: PASS, no DATA RACE in output
    Evidence: .sisyphus/evidence/task-9-thread-safe.txt
  ```

  **Commit**: `fix(ibkr): connection OnReconnect callbacks + re-subscription + tests`
  Files: `internal/adapters/ibkr/connection.go`, `internal/adapters/ibkr/connection_test.go`

---

- [ ] 10. Update `warmup.go` + `http.go`: IBKR-mode guards + `/health` `ibkr_connected` field

  **Files**:
  - `cmd/omo-core/warmup.go`
  - `cmd/omo-core/http.go`
  - `internal/adapters/ibkr/adapter.go`

  **Add `IsConnected()` to `adapter.go`**:
  ```go
  // IsConnected returns true if the IBKR adapter is currently connected to IB Gateway.
  func (a *Adapter) IsConnected() bool {
      return a.conn.isConnected()
  }
  ```
  Note: `isConnected()` is unexported on `connection`. Either export it (`IsConnected`) or use
  a delegating method. The simplest: add this public method to `adapter.go` that delegates to the
  connection. No connection export needed.

  **Changes to `http.go`**:

  In the health handler (find the `/health` or `/healthz` endpoint), add IBKR status:
  ```go
  // After existing health fields, add IBKR connection status:
  if infra.concreteIBKR != nil {
      healthData["ibkr_connected"] = infra.concreteIBKR.IsConnected()
  }
  ```

  The exact location: find where `/health` JSON response is built. Check `http.go` for the health
  handler. Add `ibkr_connected` to the response map only when `concreteIBKR != nil`.

  **Changes to `warmup.go`**:

  In `startStreaming` (around lines 359-382), add IBKR startup log:
  ```go
  // Before the existing concreteAlpaca nil check, add:
  if infra.concreteIBKR != nil {
      log.Info().
          Bool("ibkr_connected", infra.concreteIBKR.IsConnected()).
          Str("market_data", "ibkr_realtime_bars").
          Str("historical", "alpaca_rest").
          Msg("ibkr: live market data mode")
  }
  ```

  The existing `if infra.concreteAlpaca != nil { ... }` block at lines 359-382 still runs even
  when BROKER=ibkr (because Task 5 sets concreteAlpaca). This means:
  - `infra.concreteAlpaca.SetTradeHandler(...)` — still called ✓
  - `infra.concreteAlpaca.CryptoWSClient().SetDegradedCallback(...)` — still called ✓
  - `infra.concreteAlpaca.WSClient().SetPipelineHealth(...)` — still called ✓

  These are all safe with `WithNoStream()` because the WS clients are constructed but not started.
  The `SetPipelineHealth` and `SetDegradedCallback` are setters — they don't start connections.
  Verify `alpaca.WSClient()` and `alpaca.CryptoWSClient()` return non-nil even with `WithNoStream()`.
  If they do, no warmup changes are needed beyond the IBKR log line.

  **Must NOT do**:
  - Do NOT change `/health` JSON structure for BROKER=alpaca (no regression)
  - Do NOT start IBKR-specific WebSocket subscriptions in warmup — StreamBars handles this

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`senior-backend`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 11 can start in parallel but logically Wave 4)
  - **Parallel Group**: Wave 4 (with Tasks 11, 12)
  - **Blocks**: Task 11
  - **Blocked By**: Task 5

  **References**:
  - `cmd/omo-core/warmup.go:351-420` — startStreaming + concreteAlpaca nil checks
  - `cmd/omo-core/http.go:44-53` — concreteAlpaca metrics wiring
  - `cmd/omo-core/http.go:153-156` — feed health (concreteAlpaca nil check)
  - `internal/adapters/ibkr/adapter.go:34-45` — Adapter struct + constructor
  - `internal/adapters/ibkr/connection.go:113-117` — `isConnected()` unexported method

  **Acceptance Criteria**:
  - [ ] `Adapter.IsConnected()` public method exists
  - [ ] `cd backend && go build -o /dev/null ./cmd/omo-core` → PASS
  - [ ] `/health` response includes `ibkr_connected` field when BROKER=ibkr

  **QA Scenarios**:
  ```
  Scenario: Binary builds with all new code
    Tool: Bash
    Steps:
      1. cd backend && go build -o /dev/null ./cmd/omo-core
    Expected Result: exit code 0
    Evidence: .sisyphus/evidence/task-10-build.txt

  Scenario: IsConnected method accessible
    Tool: Bash
    Steps:
      1. cd backend && go vet ./internal/adapters/ibkr/...
    Expected Result: exit code 0
    Evidence: .sisyphus/evidence/task-10-vet.txt
  ```

  **Commit**: `fix(infra): warmup/http IBKR-mode guards + /health ibkr_connected field`
  Files: `cmd/omo-core/warmup.go`, `cmd/omo-core/http.go`, `internal/adapters/ibkr/adapter.go`

---

- [ ] 11. Integration test harness against paper IB Gateway

  **Files**:
  - `internal/adapters/ibkr/integration_test.go` (new)
  - `Makefile` (add `test-integration-ibkr` target)

  **Build tag**: `//go:build integration` — tests only run with `-tags=integration`.

  **Exact `integration_test.go`**:
  ```go
  //go:build integration

  package ibkr_test

  import (
      "context"
      "os"
      "testing"
      "time"

      "github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
      "github.com/oh-my-opentrade/backend/internal/config"
      "github.com/oh-my-opentrade/backend/internal/domain"
      "github.com/rs/zerolog"
      "github.com/stretchr/testify/assert"
      "github.com/stretchr/testify/require"
  )

  func integrationConfig() config.IBKRConfig {
      host := os.Getenv("IB_GATEWAY_HOST")
      if host == "" { host = "localhost" }
      port := 4002 // paper mode default
      return config.IBKRConfig{Host: host, Port: port, ClientID: 99, PaperMode: true}
  }

  func TestIntegration_Connect(t *testing.T) {
      cfg := integrationConfig()
      a, err := ibkr.NewAdapter(cfg, zerolog.New(os.Stdout).With().Timestamp().Logger())
      require.NoError(t, err, "NewAdapter must succeed when IB Gateway is running")
      assert.True(t, a.IsConnected(), "IsConnected must return true after connect")
      require.NoError(t, a.Close())
  }

  func TestIntegration_GetAccountEquity(t *testing.T) {
      a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
      require.NoError(t, err)
      defer a.Close()

      equity, err := a.GetAccountEquity(context.Background())
      require.NoError(t, err)
      assert.Greater(t, equity, float64(0), "paper account equity must be positive")
  }

  func TestIntegration_GetPositions_ReturnsSlice(t *testing.T) {
      a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
      require.NoError(t, err)
      defer a.Close()

      positions, err := a.GetPositions(context.Background(), "test", domain.Paper)
      require.NoError(t, err)
      // positions may be empty (no open positions) — that's OK
      assert.NotNil(t, positions)
  }

  func TestIntegration_GetQuote_AAPL(t *testing.T) {
      a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
      require.NoError(t, err)
      defer a.Close()

      bid, ask, err := a.GetQuote(context.Background(), "AAPL")
      require.NoError(t, err)
      assert.Greater(t, bid, float64(0), "bid must be positive during market hours")
      assert.Greater(t, ask, float64(0), "ask must be positive")
      assert.LessOrEqual(t, bid, ask, "bid must be <= ask")
  }

  func TestIntegration_SubmitAndCancelOrder(t *testing.T) {
      a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
      require.NoError(t, err)
      defer a.Close()

      ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
      defer cancel()

      // Subscribe to order updates BEFORE submitting
      updateCh, err := a.SubscribeOrderUpdates(ctx)
      require.NoError(t, err)

      // Submit limit order far from market (won't fill)
      orderID, err := a.SubmitOrder(ctx, domain.OrderIntent{
          Symbol:     "AAPL",
          Direction:  domain.DirectionLong,
          Quantity:   1,
          OrderType:  "limit",
          LimitPrice: 1.00, // $1 — will not fill
      })
      require.NoError(t, err)
      require.NotEmpty(t, orderID, "orderID must be non-empty")
      t.Logf("submitted order: %s", orderID)

      // Wait for "new" or "accepted" event
      var newEvent domain.OrderUpdate
      select {
      case update := <-updateCh:
          assert.Contains(t, []string{"new", "accepted"}, update.Event)
          newEvent = domain.OrderUpdate{} // silence unused variable
          _ = newEvent
          t.Logf("received event: %s for order %s", update.Event, update.BrokerOrderID)
      case <-time.After(10 * time.Second):
          t.Fatal("timeout waiting for 'new' order event")
      }

      // Cancel the order
      err = a.CancelOrder(ctx, orderID)
      require.NoError(t, err)

      // Wait for "canceled" event
      deadline := time.After(15 * time.Second)
      for {
          select {
          case update := <-updateCh:
              if update.Event == "canceled" && update.BrokerOrderID == orderID {
                  t.Logf("cancel confirmed for order %s", orderID)
                  return // test passed
              }
          case <-deadline:
              t.Fatal("timeout waiting for 'canceled' event")
          case <-ctx.Done():
              t.Fatal("context canceled before cancel event received")
          }
      }
  }

  func TestIntegration_StreamBars_ReceivesBars(t *testing.T) {
      a, err := ibkr.NewAdapter(integrationConfig(), zerolog.Nop())
      require.NoError(t, err)
      defer a.Close()

      ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
      defer cancel()

      barCh := make(chan domain.MarketBar, 10)
      err = a.StreamBars(ctx, []domain.Symbol{"AAPL"}, "1m",
          func(_ context.Context, bar domain.MarketBar) error {
              barCh <- bar
              return nil
          })
      require.NoError(t, err)

      // Note: bars only emit at 1-minute boundaries via bar_aggregator.
      // During market hours with active trading, expect a bar within 60s.
      // Outside market hours, this test is expected to timeout (no bars).
      // Skip if outside market hours or add a market-hours guard.
      t.Log("waiting for bar (requires active market or extended hours trading)...")
      select {
      case bar := <-barCh:
          assert.Equal(t, domain.Symbol("AAPL"), bar.Symbol)
          assert.Greater(t, bar.High, float64(0))
          t.Logf("received bar: %+v", bar)
      case <-ctx.Done():
          t.Skip("no bars received (possibly outside market hours) — skipping")
      }
  }
  ```

  **Makefile target** (add to existing `Makefile`):
  ```makefile
  test-integration-ibkr:
  	cd backend && go test -tags=integration -race -v \
  		./internal/adapters/ibkr/... -timeout 120s
  ```

  **Must NOT do**:
  - Do NOT submit market orders (might fill!)
  - Do NOT hardcode credentials
  - Do NOT run without build tag (gate with `//go:build integration`)

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: [`senior-backend`, `testing-patterns`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 10, 12)
  - **Parallel Group**: Wave 4
  - **Blocks**: FV4
  - **Blocked By**: Tasks 4, 5, 6, 7, 8, 9, 10

  **References**:
  - `internal/adapters/ibkr/adapter.go` — `NewAdapter` constructor
  - `internal/adapters/ibkr/broker.go` — `SubmitOrder`, `CancelOrder`
  - `internal/adapters/ibkr/order_stream.go` — `SubscribeOrderUpdates`
  - `deployments/docker-compose.yml` — IB Gateway paper on port 4002
  - `AGENTS.md` — project setup and Loki debugging patterns

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -run ^$ ./internal/adapters/ibkr/... -count=1` — integration tests NOT run
  - [ ] `cd backend && go test -tags=integration ./internal/adapters/ibkr/... -run TestIntegration_Connect -timeout 30s` → PASS (with IB Gateway)
  - [ ] `cd backend && make test-integration-ibkr` → exists as Makefile target

  **QA Scenarios**:
  ```
  Scenario: Integration tests not run without build tag
    Tool: Bash
    Steps:
      1. cd backend && go test -v ./internal/adapters/ibkr/... -run TestIntegration 2>&1 | head -5
    Expected Result: "no test files" or no TestIntegration tests listed
    Evidence: .sisyphus/evidence/task-11-no-tag.txt

  Scenario: Connect + GetAccountEquity with paper IB Gateway
    Tool: Bash
    Preconditions: docker compose up ib-gateway (healthy, port 4002)
    Steps:
      1. cd backend && go test -tags=integration -v ./internal/adapters/ibkr/... -run TestIntegration_Connect -timeout 30s
    Expected Result: PASS — IsConnected() == true
    Evidence: .sisyphus/evidence/task-11-connect.txt
  ```

  **Commit**: `test(ibkr): integration test harness against paper IB Gateway`
  Files: `internal/adapters/ibkr/integration_test.go`, `Makefile`

---

- [ ] 12. Final build + full regression verification

  **Files**: No new files — verification only.

  **What to do**:
  Run the complete verification suite and fix any issues found:

  ```bash
  # 1. Full build
  cd backend && go build -o /dev/null ./cmd/omo-core

  # 2. Unit tests with race detector
  cd backend && go test -race ./internal/adapters/ibkr/... -count=1 -v 2>&1 | tee /tmp/ibkr-tests.txt

  # 3. Full regression suite
  cd backend && go test ./... -count=1 2>&1 | tee /tmp/full-tests.txt

  # 4. Vet
  cd backend && go vet ./...

  # 5. Verify evidence files exist
  ls .sisyphus/evidence/task-*.txt 2>/dev/null | wc -l
  ```

  Fix any test failures before committing. If race conditions exist, fix them in the relevant
  task files before this commit.

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: [`senior-backend`]

  **Parallelization**:
  - **Can Run In Parallel**: NO (sequential — must run after all tasks complete)
  - **Parallel Group**: Wave 4 (last)
  - **Blocks**: FV1, FV2, FV3, FV4
  - **Blocked By**: Tasks 1-11

  **References**:
  - All modified files

  **Acceptance Criteria**:
  - [ ] `go build -o /dev/null ./cmd/omo-core` → exit 0
  - [ ] `go test -race ./internal/adapters/ibkr/... -count=1` → all tests pass
  - [ ] `go test ./... -count=1` → zero regressions
  - [ ] `go vet ./...` → zero errors

  **QA Scenarios**:
  ```
  Scenario: Full test suite passes
    Tool: Bash
    Steps:
      1. cd backend && go test ./... -count=1 2>&1 | grep -E "FAIL|ok" | tail -30
    Expected Result: All packages show "ok", none show "FAIL"
    Evidence: .sisyphus/evidence/task-12-full-suite.txt

  Scenario: Race detector clean
    Tool: Bash
    Steps:
      1. cd backend && go test -race ./internal/adapters/ibkr/... -count=1 2>&1 | grep -i "DATA RACE\|PASS\|FAIL"
    Expected Result: "PASS" lines only, no "DATA RACE"
    Evidence: .sisyphus/evidence/task-12-race-clean.txt
  ```

  **Commit**: `chore(ibkr): final build + full regression suite`
  Files: Any fixes needed from verification

---

## Final Verification Wave

> 4 review agents run in PARALLEL. ALL must APPROVE. Rejection → fix → re-run.

- [ ] FV1. **Plan Compliance Audit** — `oracle`
  Read plan end-to-end. For each "Must Have": verify implementation exists (read file, run command). For each "Must NOT Have": search codebase — reject with file:line if found. Check evidence files in `.sisyphus/evidence/`. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [ ] FV2. **Code Quality Review** — `unspecified-high`
  Run `cd backend && go vet ./... && go test -race ./internal/adapters/ibkr/... -count=1`. Review all changed files: bare `fmt.Errorf` without `%w`, race conditions on `connection.mu`, nil pointer dereferences, unchecked errors. Check AI slop: over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Vet [PASS/FAIL] | Tests [N pass/N fail] | VERDICT`

- [ ] FV3. **Regression Check** — `unspecified-high`
  Run `cd backend && go test ./... -count=1` with default `BROKER=alpaca`. Verify zero new failures. Run `cd backend && go build -o /dev/null ./cmd/omo-core`.
  Output: `Alpaca Tests [N pass/N fail] | Build [PASS/FAIL] | VERDICT`

- [ ] FV4. **Integration QA** — `deep` (requires IB Gateway paper)
  Run `cd backend && go test -tags=integration -race ./internal/adapters/ibkr/... -v -timeout 120s`. Submit a paper order. Verify order update on channel. Cancel. Verify cancel event.
  Output: `Integration [N/N pass] | Race [CLEAN/issues] | VERDICT`

---

## Commit Strategy

```
feat(config): add AccountID to IBKRConfig + alpaca WithNoStream option      — Task 1
feat(ibkr): ibClient interface + connection.go ibClient field                — Task 2
feat(ibkr): CompositeAdapter scaffold + alpacaDataProvider interface         — Task 3
feat(ibkr): CompositeAdapter full routing IBKR=live Alpaca=historical+stubs  — Task 4
feat(infra): BROKER=ibkr split-wires IBKR+Alpaca REST + concreteIBKR field  — Task 5
fix(ibkr): broker.go AccountID filter + error wrapping + nil safety          — Task 6
feat(ibkr): StreamBars via ReqRealTimeBars + bar_aggregator hardening        — Task 7
fix(ibkr): order stream 200ms cache-diff + tests                             — Task 8
fix(ibkr): connection OnReconnect callbacks + re-subscription                — Task 9
fix(infra): warmup/http IBKR-mode guards + /health ibkr_connected field      — Task 10
test(ibkr): integration test harness against paper IB Gateway                — Task 11
chore(ibkr): final build + full regression suite                             — Task 12
```

Pre-commit all: `cd backend && go build -o /dev/null ./cmd/omo-core && go test ./internal/adapters/ibkr/... && go vet ./...`

---

## Success Criteria

```bash
# Build succeeds (both broker modes)
cd backend && go build -o /dev/null ./cmd/omo-core

# Unit tests pass with race detector
cd backend && go test -race ./internal/adapters/ibkr/... -count=1

# Full regression (Alpaca path unbroken)
cd backend && go test ./... -count=1

# Vet clean
cd backend && go vet ./...

# Integration (requires Docker IB Gateway paper on port 4002)
cd backend && go test -tags=integration -race ./internal/adapters/ibkr/... -v -timeout 120s
```

### Final Checklist
- [ ] All "Must Have" present in codebase
- [ ] All "Must NOT Have" absent from codebase
- [ ] `go test ./... -count=1` — PASS (Alpaca path zero regressions)
- [ ] `go build -o /dev/null ./cmd/omo-core` — PASS
- [ ] `go test -race ./internal/adapters/ibkr/...` — PASS
- [ ] IB Gateway paper integration tests pass
