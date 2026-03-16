# IBKR Broker Adapter — Implementation Plan

> **Status**: Draft · **Created**: 2026-03-16 · **Author**: Prometheus (Planning Agent)
> **Target file locations**: `backend/internal/adapters/ibkr/`
> **Estimated total effort**: Large (8–12 developer-days)
> **TDD**: All phases follow RED → GREEN → REFACTOR

---

## TL;DR

Add Interactive Brokers (IBKR) as a second, fully independent broker adapter alongside the existing Alpaca adapter. The adapter connects to IB Gateway via a stateful TCP socket using the `ibsync` Go library, implements **13 interfaces** (8 formal ports + 5 application-level interfaces), and is selected at runtime via a `BROKER=alpaca|ibkr` environment variable. Neither adapter touches the other's code.

**Deliverables**:
- `backend/internal/adapters/ibkr/` — complete adapter package (≈10 files)
- `backend/internal/config/config.go` — `IBKRConfig` struct + env overlay
- `backend/cmd/omo-core/infra.go` — conditional broker wiring
- `deployments/docker-compose.yml` — `ib-gateway` sidecar service
- Full unit test suite (mocked `IBClient` interface); integration tests behind `//go:build integration` tag

**Estimated Effort**: Large (8–12 developer-days across 5 phases)
**Parallel Execution**: NO — phases are sequential (each phase produces foundations the next requires)
**Critical Path**: Phase 0 → Phase 1 → Phase 2 → Phase 3 → Phase 4 → Phase 5 (deferred)

---

## Background

`oh-my-opentrade` uses hexagonal architecture (Ports & Adapters) in Go. The current broker is Alpaca (REST + WebSocket, stateless). Interactive Brokers connects via a persistent TCP socket to IB Gateway, using a fundamentally different API model: stateful callbacks, synchronous-wrapped by `ibsync`, with daily gateway restarts requiring a reconnect watchdog. Both adapters will coexist; broker selection is a runtime config decision, not a compile-time switch.

---

## Scope

### In Scope
- All 13 interfaces the adapter must satisfy (see Architecture Decision section)
- `IBClient` testability interface wrapping ibsync
- Connection lifecycle: connect, disconnect, daily-restart reconnect watchdog
- Qualified contract cache (symbol → `*ibsync.Contract`) populated at startup
- Paper mode: automatic `ReqMarketDataType(4)` for delayed/frozen market data
- Order ID management via `ib.NextID()` (no local persistence needed)
- Broker selection via `BROKER=alpaca|ibkr` env var in `infra.go`
- IB Gateway Docker sidecar in `docker-compose.yml`
- Unit tests with mocked `IBClient` (every public method)
- Integration smoke tests behind `//go:build integration` tag
- Equity-only (STK) asset class support

### Out of Scope
- Crypto trading via IBKR (IB's crypto model incompatible with current domain patterns)
- Forex or futures support
- Live trading mode (paper only in this plan)
- Full options parity in Phase 5 (stubbed interfaces acceptable initially)
- Refactoring `infraDeps` to use interface-typed fields (deferred follow-up PR)
- Alpaca-equivalent observability hooks (`WSClient().FeedHealth()`, `SetMetrics()`, circuit breaker callbacks) — no-op in initial release
- Complex subscription rotation for IB's 100 simultaneous market data line limit
- Symbol normalization library

---

## Architecture Decision: Interface Surface Area

### The 13 Interfaces the IBKR Adapter Must Satisfy

The Alpaca adapter satisfies **13 interfaces** (not just 8 port interfaces). The IBKR adapter must implement all of them to be a drop-in replacement in the wiring layer.

**Formal Port Interfaces** (defined in `backend/internal/ports/`):

| # | Interface | File | Key Methods |
|---|-----------|------|-------------|
| 1 | `ports.BrokerPort` | `ports/broker.go` | `SubmitOrder, CancelOrder, CancelOpenOrders, GetOrderStatus, GetPositions, GetPosition, ClosePosition, GetOrderDetails, CancelAllOpenOrders` |
| 2 | `ports.OrderStreamPort` | `ports/broker.go` | `SubscribeOrderUpdates` |
| 3 | `ports.MarketDataPort` | `ports/market_data.go` | `StreamBars, GetHistoricalBars, Close` |
| 4 | `ports.AccountPort` | `ports/account.go` | `GetAccountBuyingPower` |
| 5 | `ports.SnapshotPort` | `ports/screener.go` | `GetSnapshots` |
| 6 | `ports.UniverseProviderPort` | `ports/universe.go` | `ListTradeable` |
| 7 | `ports.OptionsMarketDataPort` | `ports/options_market_data.go` | `GetOptionChain` |
| 8 | `ports.OptionsPricePort` | `ports/options_price.go` | `GetOptionPrices` |

**Application-Level Interfaces** (defined in `internal/app/` services, NOT in `ports/`):

| # | Interface | Defined In | Method Signature |
|---|-----------|-----------|------------------|
| 9 | `execution.QuoteProvider` | `app/execution/` | `GetQuote(ctx context.Context, symbol domain.Symbol) (bid, ask float64, err error)` |
| 10 | `activation.HistoricalDataProvider` | `app/activation/` | `GetHistoricalBars(ctx, symbol, timeframe, from, to time.Time) ([]domain.MarketBar, error)` |
| 11 | `activation.SymbolSubscriber` | `app/activation/` | `SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error` |
| 12 | `screener.MarketDataProvider` | `app/screener/` | `GetHistoricalBars(ctx, symbol, timeframe, from, to time.Time) ([]domain.MarketBar, error)` |
| 13 | `orchestrator.EquitySource` | `app/orchestrator/` | `GetAccountEquity(ctx context.Context) (float64, error)` |

**Compile-time assertions** (in `adapter.go`, following Alpaca pattern):
```go
var _ ports.BrokerPort              = (*Adapter)(nil)
var _ ports.OrderStreamPort         = (*Adapter)(nil)
var _ ports.MarketDataPort          = (*Adapter)(nil)
var _ ports.AccountPort             = (*Adapter)(nil)
var _ ports.SnapshotPort            = (*Adapter)(nil)
var _ ports.UniverseProviderPort    = (*Adapter)(nil)
var _ ports.OptionsMarketDataPort   = (*Adapter)(nil)
var _ ports.OptionsPricePort        = (*Adapter)(nil)
// app-level interfaces verified in adapter_test.go
```

### Architecture Decision: Broker Selection Strategy (Option A — Additive)

**Decision**: Keep `alpacaAdapter *alpaca.Adapter` as-is in `infraDeps`. Add `ibkrAdapter *ibkr.Adapter` alongside. Use `BROKER=alpaca|ibkr` env var to select which adapter's methods are wired to each service.

**Rationale**: Minimises regression risk. Alpaca adapter and all its concrete-type observability hooks remain completely untouched. IBKR conditional paths are purely additive. A clean Option B refactor (interface-typed `infraDeps` fields) is deferred to a follow-up PR.

**Wiring sketch**:
```go
// infra.go
type infraDeps struct {
    alpacaAdapter *alpaca.Adapter  // unchanged
    ibkrAdapter   *ibkr.Adapter    // NEW — nil when BROKER=alpaca
    activeBroker  string            // "alpaca" | "ibkr"
    // ... all other fields unchanged
}

// services.go — conditional port assignment at wiring time
var broker ports.BrokerPort
if infra.activeBroker == "ibkr" {
    broker = infra.ibkrAdapter
} else {
    broker = infra.alpacaAdapter  // default, unchanged
}
```

---

## Phase Overview

```
Phase 0: Config + Dependency          [~0.5 day]  — Unblocks all subsequent phases
Phase 1: Foundation                   [~2 days]   — IBClient interface, connect, contract cache
Phase 2: Core Trading                 [~3 days]   — BrokerPort, AccountPort, OrderStreamPort + QuoteProvider
Phase 3: Market Data                  [~2 days]   — MarketDataPort, historical + streaming bars
Phase 4: Config Wiring & Docker       [~1 day]    — Wire into app, docker-compose sidecar
Phase 5: Advanced Features            [~3+ days]  — Options, Snapshot, Universe (deferred)
```

Sequential dependency chain: `Phase 0 → Phase 1 → Phase 2 → Phase 3 → Phase 4 → Phase 5`

---

## TODOs

---

### Phase 0: Config + Dependency

- [ ] P0.1 — Add `IBKRConfig` struct to `backend/internal/config/config.go`

  **What to do**:
  - Add `IBKRConfig` struct with fields: `Host string`, `Port int`, `ClientID int`, `AccountID string`, `PaperMode bool`
  - Add yaml struct tags matching the Alpaca pattern: `yaml:"host"`, `yaml:"port"`, etc.
  - Add `IBKR IBKRConfig` field to the top-level `Config` struct (alongside `Alpaca AlpacaConfig`)
  - Add env overlay block in the env-overlay section of `config.go`:
    ```go
    if val := os.Getenv("IBKR_HOST"); val != "" { cfg.IBKR.Host = val }
    if val := os.Getenv("IBKR_PORT"); val != "" { cfg.IBKR.Port, _ = strconv.Atoi(val) }
    if val := os.Getenv("IBKR_CLIENT_ID"); val != "" { cfg.IBKR.ClientID, _ = strconv.Atoi(val) }
    if val := os.Getenv("IBKR_ACCOUNT_ID"); val != "" { cfg.IBKR.AccountID = val }
    if val := os.Getenv("IBKR_PAPER_MODE"); val != "" { cfg.IBKR.PaperMode = val == "true" }
    ```
  - Add default values: `Host: "localhost"`, `Port: 4002`, `ClientID: 1`, `PaperMode: true`
  - Add `BROKER string` field to `Config` with env overlay: `if val := os.Getenv("BROKER"); val != "" { cfg.Broker = val }`
  - Add to `configs/config.yaml`: new `ibkr:` section with default values
  - Add to `.env.example` (if it exists): `IBKR_HOST=localhost`, `IBKR_PORT=4002`, `IBKR_CLIENT_ID=1`, `IBKR_ACCOUNT_ID=`, `BROKER=alpaca`

  **Must NOT do**:
  - Do NOT modify `AlpacaConfig` or any existing config fields
  - Do NOT add validation logic beyond existing patterns in config.go
  - Do NOT add a BrokerFactory or interface — just the plain struct

  **References**:
  - Pattern: `backend/internal/config/config.go:28-38` — `AlpacaConfig` struct with yaml tags
  - Pattern: `backend/internal/config/config.go` env overlay block — follow exactly for `APCA_*` vars
  - Pattern: `configs/config.yaml` — follow existing indentation and section structure

  **Acceptance Criteria**:
  - [ ] `cd backend && go build ./...` — zero errors after adding IBKRConfig
  - [ ] `cd backend && go test ./internal/config/...` — all config tests pass
  - [ ] `grep -n "IBKRConfig" backend/internal/config/config.go` — struct visible
  - [ ] `IBKR_HOST=myhost go run ./cmd/omo-core/ 2>&1 | grep ibkr` — config loads without panic (can early-exit on missing deps)

  **QA Scenarios**:
  ```
  Scenario: IBKRConfig loads from environment variables
    Tool: Bash
    Steps:
      1. Set env: IBKR_HOST=test-gateway IBKR_PORT=4002 IBKR_CLIENT_ID=42
      2. Write a small Go test that calls config.Load() and asserts IBKR.Host == "test-gateway"
      3. Run: cd backend && go test -run TestIBKRConfigEnvOverlay ./internal/config/...
    Expected: Test passes, IBKR fields populated correctly
    Evidence: .sisyphus/evidence/P0.1-config-env-override.txt

  Scenario: Default values applied when no env vars set
    Tool: Bash
    Steps:
      1. Unset all IBKR_* env vars
      2. Run config.Load() — assert IBKR.Host == "localhost", IBKR.Port == 4002, IBKR.PaperMode == true
    Expected: Defaults applied correctly
    Evidence: .sisyphus/evidence/P0.1-config-defaults.txt
  ```

  **Commit**: `feat(config): add IBKRConfig struct with yaml tags and env overlay`

---

- [ ] P0.2 — Add `github.com/scmhub/ibsync` dependency to `backend/go.mod`

  **What to do**:
  - Run: `cd backend && go get github.com/scmhub/ibsync@latest`
  - Pin the resolved version (do NOT use floating `@latest` in go.mod — pin to exact version)
  - Run: `cd backend && go mod tidy`
  - Verify `go.sum` is updated with ibsync and its transitive dependencies
  - Do NOT import ibsync in any Go file yet (that happens in Phase 1)

  **Must NOT do**:
  - Do NOT add `github.com/scmhub/ibapi` (low-level raw API) — ibsync is sufficient
  - Do NOT import ibsync in any application code yet

  **References**:
  - `backend/go.mod` — existing dependency format
  - ibsync GitHub: `github.com/scmhub/ibsync` — verify latest stable tag before pinning

  **Acceptance Criteria**:
  - [ ] `grep "scmhub/ibsync" backend/go.mod` — dependency visible at pinned version
  - [ ] `cd backend && go mod verify` — all module checksums valid
  - [ ] `cd backend && go build ./...` — full build succeeds with new dependency

  **QA Scenarios**:
  ```
  Scenario: ibsync dependency is importable
    Tool: Bash
    Steps:
      1. Create a temporary file: /tmp/ibsync_check.go with `import _ "github.com/scmhub/ibsync"`
      2. Run: cd backend && go build /tmp/ibsync_check.go
    Expected: No import errors
    Evidence: .sisyphus/evidence/P0.2-dep-importable.txt
  ```

  **Commit**: `chore(deps): add github.com/scmhub/ibsync dependency to go.mod`

---

### Phase 1: Foundation — Adapter Skeleton, IBClient Interface, Connection, Contract Cache

- [ ] P1.1 — Create adapter package skeleton with 13 compile-time interface assertions

  **What to do**:
  - Create directory: `backend/internal/adapters/ibkr/`
  - Create `backend/internal/adapters/ibkr/adapter.go`:
    ```go
    package ibkr

    import (
        "errors"
        "github.com/oh-my-opentrade/backend/internal/config"
        "github.com/oh-my-opentrade/backend/internal/ports"
        "github.com/rs/zerolog"
    )

    var ErrNotImplemented = errors.New("ibkr: not implemented")

    // Compile-time interface assertions — all 13 interfaces
    var _ ports.BrokerPort            = (*Adapter)(nil)
    var _ ports.OrderStreamPort       = (*Adapter)(nil)
    var _ ports.MarketDataPort        = (*Adapter)(nil)
    var _ ports.AccountPort           = (*Adapter)(nil)
    var _ ports.SnapshotPort          = (*Adapter)(nil)
    var _ ports.UniverseProviderPort  = (*Adapter)(nil)
    var _ ports.OptionsMarketDataPort = (*Adapter)(nil)
    var _ ports.OptionsPricePort      = (*Adapter)(nil)

    type Adapter struct {
        cfg config.IBKRConfig
        log zerolog.Logger
        // ib IBClient — added in P1.2
    }

    func NewAdapter(cfg config.IBKRConfig, log zerolog.Logger) (*Adapter, error) {
        if cfg.Host == "" {
            return nil, errors.New("ibkr: host is required")
        }
        if cfg.Port == 0 {
            return nil, errors.New("ibkr: port is required")
        }
        return &Adapter{cfg: cfg, log: log}, nil
    }
    ```
  - Create stub implementations for ALL methods of all 8 formal port interfaces — each returns `ErrNotImplemented` or zero values
  - Create `backend/internal/adapters/ibkr/adapter_test.go` with compile-time assertion tests:
    ```go
    package ibkr_test
    // Verify app-level interfaces (tested via concrete type check)
    ```
  - The 5 app-level interfaces (QuoteProvider, HistoricalDataProvider, SymbolSubscriber, MarketDataProvider, EquitySource) are verified by matching method signatures — read each app service file to get exact signatures before writing stubs

  **Must NOT do**:
  - Do NOT import ibsync yet — Adapter struct uses only `config.IBKRConfig` and `zerolog.Logger` in this task
  - Do NOT implement any real logic — stubs only
  - Do NOT modify any file outside `backend/internal/adapters/ibkr/`

  **References**:
  - Pattern: `backend/internal/adapters/alpaca/adapter.go:1-80` — struct definition, var assertions, NewAdapter constructor with validation
  - Pattern: `backend/internal/ports/broker.go` — exact BrokerPort/OrderStreamPort signatures
  - Pattern: `backend/internal/ports/market_data.go` — exact MarketDataPort signatures
  - Pattern: `backend/internal/ports/account.go` — exact AccountPort signatures
  - App-level interfaces: read `backend/internal/app/execution/`, `backend/internal/app/activation/`, `backend/internal/app/screener/`, `backend/internal/app/orchestrator/` to find exact interface method signatures for interfaces 9–13

  **Acceptance Criteria**:
  - [ ] `cd backend && go build ./internal/adapters/ibkr/...` — compiles with zero errors
  - [ ] `cd backend && go vet ./internal/adapters/ibkr/...` — zero vet issues
  - [ ] All 8 compile-time `var _ ports.X = (*Adapter)(nil)` assertions pass at compile time
  - [ ] `cd backend && go test ./internal/adapters/ibkr/...` — package tests compile and run (even if 0 test cases pass yet)

  **QA Scenarios**:
  ```
  Scenario: All interface assertions compile
    Tool: Bash
    Steps:
      1. cd backend && go build ./internal/adapters/ibkr/...
    Expected: Zero errors — all 8 compile-time assertions satisfied
    Evidence: .sisyphus/evidence/P1.1-compile-assertions.txt

  Scenario: NewAdapter rejects empty host
    Tool: Bash
    Steps:
      1. Write test: NewAdapter(IBKRConfig{Host: "", Port: 4002}, logger) → expect non-nil error
      2. cd backend && go test -run TestNewAdapterValidation ./internal/adapters/ibkr/...
    Expected: Error returned, no panic
    Evidence: .sisyphus/evidence/P1.1-adapter-validation.txt
  ```

  **Commit**: `feat(ibkr): add package skeleton with 13 compile-time interface assertions (all return ErrNotImplemented)`

---

- [ ] P1.2 — Extract `IBClient` interface over ibsync for testability, add mock

  **What to do**:
  - Create `backend/internal/adapters/ibkr/ibclient.go`:
    - Define `IBClient` interface with ALL ibsync methods the adapter will use:
      ```go
      type IBClient interface {
          Connect(cfg *ibsync.Config) error
          Disconnect()
          IsConnected() bool
          NextID() int64
          QualifyContracts(contracts ...*ibsync.Contract) error
          ReqMarketDataType(marketDataType int)
          PlaceOrder(contract *ibsync.Contract, order *ibsync.Order) *ibsync.Trade
          CancelOrder(order *ibsync.Order, cancel ibsync.OrderCancel)
          ReqOpenOrders() ([]*ibsync.Trade, error)
          Positions() []ibsync.Position
          AccountSummary() []ibsync.AccountValue
          ReqHistoricalData(contract *ibsync.Contract, endDateTime, duration, barSize, whatToShow string, useRTH bool, formatDate int) (chan ibsync.Bar, error)
          ReqRealTimeBars(contract *ibsync.Contract, barSize int, whatToShow string, useRTH bool) (chan ibsync.RealTimeBar, context.CancelFunc, error)
          ReqMktData(contract *ibsync.Contract, genericTickList string, snapshot bool, regulatorySnapshot bool) *ibsync.Ticker
          ReqSecDefOptParams(underlyingSymbol, futFopExchange, underlyingSecType string, underlyingConID int64) ([]ibsync.OptionChain, error)
          // add any additional methods discovered when reading ibsync source
      }
      ```
    - Wrap `*ibsync.IB` as the production implementation: `type ibClientImpl struct { ib *ibsync.IB }`
    - Implement all `IBClient` methods on `ibClientImpl` as thin pass-throughs
  - Create `backend/internal/adapters/ibkr/mock_ibclient.go`:
    - `MockIBClient` struct with `sync.Mutex` for thread safety
    - Each method stores call args and returns configurable `ReturnError`/`ReturnValue` fields
    - Helper `AssertCalled(t, methodName)` for tests
  - Wire `IBClient` into `Adapter` struct: `ib IBClient`
  - Update `NewAdapter` to accept optional `IBClient` or use a functional option: `type Option func(*Adapter)`; `WithIBClient(c IBClient) Option`

  **Must NOT do**:
  - Do NOT couple `Adapter` directly to `*ibsync.IB` — always go through `IBClient` interface
  - Do NOT add methods to `IBClient` that ibsync doesn't actually have — verify each method exists in ibsync source
  - Do NOT use `testify/mock` — hand-written mock is sufficient and avoids extra dependency

  **References**:
  - ibsync source: `github.com/scmhub/ibsync/ib.go` — method signatures on `*IB` type
  - Pattern for mock: any `backend/internal/adapters/alpaca/*_test.go` mock patterns

  **Acceptance Criteria**:
  - [ ] `cd backend && go build ./internal/adapters/ibkr/...` — compiles zero errors
  - [ ] `cd backend && go vet ./internal/adapters/ibkr/...` — zero vet issues
  - [ ] `MockIBClient` implements `IBClient` interface (compile-time assertion in mock file)
  - [ ] `ibClientImpl` implements `IBClient` interface (compile-time assertion)

  **QA Scenarios**:
  ```
  Scenario: MockIBClient satisfies IBClient interface
    Tool: Bash
    Steps:
      1. cd backend && go build ./internal/adapters/ibkr/...
      2. Verify compile-time assertion: var _ IBClient = (*MockIBClient)(nil) passes
    Expected: Zero compile errors
    Evidence: .sisyphus/evidence/P1.2-ibclient-interface.txt
  ```

  **Commit**: `feat(ibkr): extract IBClient interface over ibsync for testability + mock implementation`

---

- [ ] P1.3 — Implement Connect/Disconnect lifecycle with reconnect watchdog

  **What to do**:
  - In `backend/internal/adapters/ibkr/adapter.go`:
    - Add `Connect(ctx context.Context) error` method:
      - Build `ibsync.Config` from `a.cfg` (host, port, clientID, timeout=30s)
      - Call `a.ib.Connect(cfg)` with timeout
      - If `cfg.PaperMode == true`, call `a.ib.ReqMarketDataType(4)` immediately after connect
      - Log successful connection at INFO level (zerolog pattern: `a.log.Info().Str("host", cfg.Host).Int("port", cfg.Port).Msg("connected to IB Gateway")`)
    - Add `Disconnect()` method: call `a.ib.Disconnect()`, log INFO
    - Add `startReconnectWatchdog(ctx context.Context)` goroutine method:
      - Tick every 5 seconds
      - If `!a.ib.IsConnected()`, attempt reconnect with exponential backoff (start 1s, double each attempt, max 30s, max 60 attempts before logging FATAL and stopping)
      - On successful reconnect: call `a.ib.ReqMarketDataType(4)` if paper mode, log INFO "reconnected to IB Gateway"
      - On max-attempts exceeded: log `a.log.Error().Msg("ibkr: max reconnect attempts exceeded")` and return (do NOT call os.Exit — let supervisor handle it)
    - Call `startReconnectWatchdog` at the end of `Connect()` in a new goroutine, passing the context
  - Create `backend/internal/adapters/ibkr/adapter_test.go`:
    - Test `Connect()` success path (mock returns nil)
    - Test `Connect()` failure path (mock returns error)
    - Test reconnect watchdog triggers when `IsConnected()` returns false
    - Test watchdog stops when context is cancelled
    - Test that `ReqMarketDataType(4)` is called when `PaperMode=true`

  **Must NOT do**:
  - Do NOT use `os.Exit` in the watchdog — return/log only
  - Do NOT reconnect more than once per 5-second tick
  - Do NOT call `ReqMarketDataType(4)` when `PaperMode=false`
  - Do NOT add complex subscription re-registration in this task (that belongs to market data Phase 3)

  **References**:
  - Pattern: `backend/internal/adapters/alpaca/trade_stream.go` — exponential backoff reconnect loop pattern
  - ibsync: `ib.Connect(cfg)`, `ib.IsConnected()`, `ib.Disconnect()`, `ib.ReqMarketDataType(4)`
  - Zerolog pattern: `backend/internal/adapters/alpaca/adapter.go` — log field patterns

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestConnect ./internal/adapters/ibkr/...` — all tests pass
  - [ ] `cd backend && go test -race -run TestReconnect ./internal/adapters/ibkr/...` — all tests pass
  - [ ] `cd backend && go vet ./internal/adapters/ibkr/...` — zero issues
  - [ ] Watchdog goroutine stops cleanly on context cancellation (no goroutine leak — verify with `-race`)

  **QA Scenarios**:
  ```
  Scenario: Connect succeeds and calls ReqMarketDataType(4) in paper mode
    Tool: Bash (unit test)
    Steps:
      1. Create MockIBClient with Connect() returning nil, IsConnected() returning true
      2. Call adapter.Connect(ctx) with PaperMode=true
      3. Assert MockIBClient.ReqMarketDataTypeCalled == true, arg == 4
      4. cd backend && go test -race -run TestConnectPaperMode ./internal/adapters/ibkr/...
    Expected: Test PASS
    Evidence: .sisyphus/evidence/P1.3-connect-paper-mode.txt

  Scenario: Reconnect watchdog retries on disconnect
    Tool: Bash (unit test)
    Steps:
      1. Mock IsConnected() returns false for first 3 calls, then true
      2. Mock Connect() returns nil
      3. Start watchdog with 100ms tick interval (injectable for test speed)
      4. After 500ms, assert Connect() was called at least once
    Expected: Watchdog attempted reconnect
    Evidence: .sisyphus/evidence/P1.3-reconnect-watchdog.txt

  Scenario: Watchdog stops on context cancellation
    Tool: Bash (unit test with goroutine leak detection)
    Steps:
      1. Start adapter with watchdog
      2. Cancel context after 200ms
      3. Sleep 500ms, verify no additional calls to IsConnected()
    Expected: Watchdog exits cleanly, no goroutine leak
    Evidence: .sisyphus/evidence/P1.3-watchdog-cancel.txt
  ```

  **Commit**: `feat(ibkr): implement Connect/Disconnect with reconnect watchdog + unit tests`

---

- [ ] P1.4 — Implement qualified contract cache

  **What to do**:
  - Create `backend/internal/adapters/ibkr/contract_cache.go`:
    ```go
    type contractCache struct {
        mu      sync.RWMutex
        entries map[domain.Symbol]*ibsync.Contract
    }
    func newContractCache() *contractCache
    func (c *contractCache) Get(sym domain.Symbol) (*ibsync.Contract, bool)
    func (c *contractCache) Set(sym domain.Symbol, contract *ibsync.Contract)
    func (c *contractCache) Clear()
    ```
  - Add `contractCache *contractCache` field to `Adapter` struct
  - Implement `qualifySymbol(ctx context.Context, sym domain.Symbol) (*ibsync.Contract, error)`:
    - Check cache first (read lock)
    - If miss: build `ibsync.Contract{Symbol: string(sym), SecType: "STK", Exchange: "SMART", Currency: "USD"}`
    - Call `a.ib.QualifyContracts(contract)` — this fills in ConID, PrimaryExch, etc.
    - Store in cache (write lock)
    - Return qualified contract
  - Add `PopulateContractCache(ctx context.Context, symbols []domain.Symbol) error` method on Adapter:
    - Iterate symbols, call `qualifySymbol` for each
    - Log WARN for each symbol that fails to qualify (don't fail all on one error)
    - Called during startup (Phase 4 wiring)
  - Create `backend/internal/adapters/ibkr/contract_cache_test.go`:
    - Test cache hit (no IB call on second lookup)
    - Test cache miss calls QualifyContracts
    - Test cache cleared on Clear()
    - Test concurrent access (race detector)
    - Test PopulateContractCache with mixed success/failure symbols

  **Must NOT do**:
  - Do NOT add TTL expiry — cache is refreshed on reconnect only
  - Do NOT support crypto or options contracts in this task (STK only)
  - Do NOT make QualifyContracts call blocking beyond 5 second context timeout

  **References**:
  - Pattern: `backend/internal/adapters/alpaca/position_cache.go` — similar cache pattern with mutex
  - ibsync: `QualifyContracts(*Contract) error` — mutates the passed contract in-place
  - Domain: `backend/internal/domain/value.go` — `domain.Symbol` type

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestContractCache ./internal/adapters/ibkr/...` — all tests pass
  - [ ] Race detector finds no data races on concurrent cache access
  - [ ] Cache hit avoids calling `IBClient.QualifyContracts` (verified by mock call count)

  **QA Scenarios**:
  ```
  Scenario: Cache hit avoids duplicate QualifyContracts call
    Tool: Bash (unit test)
    Steps:
      1. MockIBClient.QualifyContracts call counter starts at 0
      2. Call qualifySymbol(ctx, "AAPL") twice
      3. Assert MockIBClient.QualifyContractsCalled == 1 (second call served from cache)
    Expected: Test PASS — only 1 IB API call for 2 symbol lookups
    Evidence: .sisyphus/evidence/P1.4-cache-hit.txt

  Scenario: PopulateContractCache skips failed symbols without aborting
    Tool: Bash (unit test)
    Steps:
      1. MockIBClient.QualifyContracts returns error for "INVALID_SYM", success for "AAPL"
      2. Call PopulateContractCache(ctx, ["AAPL", "INVALID_SYM"])
      3. Assert: returns nil (no error), cache has "AAPL" entry, log contains WARN for "INVALID_SYM"
    Expected: Partial success, no abort
    Evidence: .sisyphus/evidence/P1.4-populate-partial.txt
  ```

  **Commit**: `feat(ibkr): implement qualified contract cache with startup population + unit tests`

---

### Phase 2: Core Trading — BrokerPort, AccountPort, OrderStreamPort, QuoteProvider

- [ ] P2.1 — Implement `SubmitOrder` with domain-to-IB type mapping

  **What to do**:
  - Create `backend/internal/adapters/ibkr/orders.go`:
    - Implement `domainIntentToIBOrder(intent domain.OrderIntent) (*ibsync.Order, error)`:
      - Map `intent.Direction` → IB `Action`: `LONG/CLOSE_SHORT` → `"BUY"`, `SHORT/CLOSE_LONG` → `"SELL"`
      - Map `intent.OrderType`: `domain.Market` → `"MKT"`, `domain.Limit` → `"LMT"`, `domain.Stop` → `"STP"`
      - Map `intent.TimeInForce`: `"DAY"` → `"DAY"`, `"GTC"` → `"GTC"`
      - Set `TotalQuantity` from `intent.Quantity` (use `ibsync.StringToDecimal`)
      - Set `LmtPrice` if `intent.LimitPrice > 0`
      - Set `OrderRef` to `intent.ID.String()` (for round-trip correlation)
    - Implement `(a *Adapter) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error)`:
      - Call `a.qualifySymbol(ctx, intent.Symbol)` — fail fast on unqualified symbol
      - Build ibsync order via `domainIntentToIBOrder(intent)`
      - Call `a.ib.PlaceOrder(contract, order)` — returns `*ibsync.Trade`
      - Store mapping: `a.orderMap[trade.Order.OrderID] = intent.ID` (thread-safe map)
      - Return `strconv.FormatInt(trade.Order.OrderID, 10)` as string broker order ID
  - Create `backend/internal/adapters/ibkr/orders_test.go`:
    - Table-driven tests for `domainIntentToIBOrder`:
      - LONG limit order → Action="BUY", OrderType="LMT", LmtPrice set
      - SHORT market order → Action="SELL", OrderType="MKT"
      - CLOSE_LONG → Action="SELL"
      - Unknown direction → error returned
    - Test `SubmitOrder` happy path: mock PlaceOrder returns Trade, broker order ID returned
    - Test `SubmitOrder` when qualifySymbol fails: error propagated
    - Test `SubmitOrder` when PlaceOrder fails: error propagated

  **Must NOT do**:
  - Do NOT await fill in SubmitOrder — fire and return order ID only (fills come via OrderStreamPort)
  - Do NOT support crypto or options in this task
  - Do NOT hardcode `AccountID` in orders — IB gateway uses the logged-in account automatically

  **References**:
  - Domain: `backend/internal/domain/entity.go` — `OrderIntent` struct fields
  - Domain: `backend/internal/domain/value.go` — `Direction`, `OrderType`, `TimeInForce` values
  - Alpaca pattern: `backend/internal/adapters/alpaca/rest.go` — `SubmitOrder` for structural reference
  - ibsync: `ibsync.LimitOrder(action, qty, lmtPrice)`, `ibsync.MarketOrder(action, qty)`, `ibsync.NewStock(sym, exch, currency)`

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestSubmitOrder ./internal/adapters/ibkr/...` — all tests pass
  - [ ] `cd backend && go test -race -run TestDomainIntentToIBOrder ./internal/adapters/ibkr/...` — table tests all green
  - [ ] All direction/ordertype combinations covered by table tests

  **QA Scenarios**:
  ```
  Scenario: LONG limit order mapped correctly to IB order
    Tool: Bash (unit test)
    Steps:
      1. Create OrderIntent{Direction: LONG, OrderType: Limit, LimitPrice: 150.00, Quantity: 10}
      2. Call domainIntentToIBOrder(intent)
      3. Assert: Action=="BUY", OrderType=="LMT", LmtPrice==150.00, TotalQuantity==10
    Expected: All fields mapped correctly
    Evidence: .sisyphus/evidence/P2.1-order-mapping.txt

  Scenario: SubmitOrder returns broker order ID
    Tool: Bash (unit test)
    Steps:
      1. Mock QualifyContracts success, PlaceOrder returns Trade with OrderID=12345
      2. Call adapter.SubmitOrder(ctx, validIntent)
      3. Assert: returned orderID == "12345", no error
    Expected: PASS
    Evidence: .sisyphus/evidence/P2.1-submit-order.txt
  ```

  **Commit**: `feat(ibkr): implement SubmitOrder with domain-to-IB type mapping + unit tests`

---

- [ ] P2.2 — Implement `CancelOrder`, `GetOrderStatus`, `GetOrderDetails`

  **What to do**:
  - In `backend/internal/adapters/ibkr/orders.go`, add:
    - `CancelOrder(ctx context.Context, orderID string) error`:
      - Parse orderID string → int64
      - Look up order in `a.orderMap` (reverse lookup by orderID string)
      - Call `a.ib.CancelOrder(order, ibsync.NewOrderCancel())` — requires `*ibsync.Order`, not just ID
      - If order not found in local map: return `fmt.Errorf("ibkr: order %s not tracked", orderID)`
    - `GetOrderStatus(ctx context.Context, orderID string) (string, error)`:
      - Call `a.ib.ReqOpenOrders()` to get current open orders
      - Search for matching order by ID
      - Map IB status strings to OMO status strings: `"Filled"→"filled"`, `"Cancelled"→"cancelled"`, `"Submitted"→"pending"`, `"PreSubmitted"→"pending_new"`
      - If not in open orders: return `"filled"` (IB doesn't retain filled order state without dedicated query)
    - `GetOrderDetails(ctx context.Context, orderID string) (ports.OrderDetails, error)`:
      - Call `a.ib.ReqOpenOrders()`, find matching order
      - Map to `ports.OrderDetails{BrokerOrderID, Status, Symbol, Side, FilledQty, FilledAvgPrice, Qty, FilledAt}`
    - `CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error)`:
      - Get open orders via `a.ib.ReqOpenOrders()`
      - Filter by symbol (contract.Symbol) and side (Action)
      - Cancel each matching order; count cancellations
    - `CancelAllOpenOrders(ctx context.Context) (int, error)`:
      - Get all open orders, cancel each; return count
  - Tests:
    - `CancelOrder` with known order ID
    - `CancelOrder` with unknown order ID → error
    - `GetOrderStatus` for open order → "pending"
    - `GetOrderStatus` for non-existent order (assumed filled) → "filled"
    - `CancelOpenOrders` counts and cancels correctly filtered subset

  **Must NOT do**:
  - Do NOT call any sleeping or polling in these methods — synchronous based on ibsync API
  - Do NOT add order persistence to database — in-memory map only

  **References**:
  - Port signatures: `backend/internal/ports/broker.go` — exact method signatures for `CancelOrder`, `GetOrderStatus`, `GetOrderDetails`, `CancelOpenOrders`, `CancelAllOpenOrders`
  - ibsync: `ReqOpenOrders() ([]*Trade, error)`, `CancelOrder(order *Order, cancel OrderCancel)` 
  - Alpaca pattern: `backend/internal/adapters/alpaca/rest.go` — status mapping patterns

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestCancelOrder ./internal/adapters/ibkr/...` — all pass
  - [ ] `cd backend && go test -race -run TestGetOrderStatus ./internal/adapters/ibkr/...` — all pass
  - [ ] All IB status strings mapped to OMO status strings in table-driven test

  **QA Scenarios**:
  ```
  Scenario: CancelOrder for tracked order succeeds
    Tool: Bash (unit test)
    Steps:
      1. Add order to adapter orderMap with key "12345"
      2. Call adapter.CancelOrder(ctx, "12345")
      3. Assert: MockIBClient.CancelOrderCalled == true
    Expected: PASS
    Evidence: .sisyphus/evidence/P2.2-cancel-order.txt

  Scenario: CancelOrder for unknown order returns error
    Tool: Bash (unit test)
    Steps:
      1. Empty orderMap
      2. Call adapter.CancelOrder(ctx, "99999")
      3. Assert: error != nil, error contains "not tracked"
    Expected: PASS — not panic
    Evidence: .sisyphus/evidence/P2.2-cancel-unknown.txt
  ```

  **Commit**: `feat(ibkr): implement CancelOrder, GetOrderStatus, GetOrderDetails + unit tests`

---

- [ ] P2.3 — Implement `GetPositions`, `GetPosition`, `ClosePosition`

  **What to do**:
  - Create `backend/internal/adapters/ibkr/positions.go`:
    - `GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)`:
      - Call `a.ib.Positions()` → `[]ibsync.Position`
      - Map each position to `domain.Trade`:
        - `Symbol` ← `pos.Contract.Symbol`
        - `Side` ← "BUY" if pos.Position > 0, "SELL" if < 0
        - `Quantity` ← `abs(pos.Position)` (as float64 from Decimal)
        - `Price` ← `pos.AvgCost`
        - `TenantID` ← tenantID param
        - `EnvMode` ← envMode param
        - `Status` ← "filled"
        - `Time` ← `time.Now()` (IB doesn't return open time for positions)
        - `TradeID` ← `uuid.New().String()`
      - Return slice of mapped domain.Trade
    - `GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error)`:
      - Call `a.ib.Positions()`, filter by symbol, return net quantity (positive = long, negative = short)
    - `ClosePosition(ctx context.Context, symbol domain.Symbol) (string, error)`:
      - Get current position via GetPosition
      - If net qty == 0: return `"", nil` (already flat)
      - Build close order: if qty > 0 → SELL qty shares; if qty < 0 → BUY abs(qty) shares
      - SubmitOrder with a constructed OrderIntent (use `Direction: domain.CloseLong` or `domain.CloseShort`)
  - Tests:
    - GetPositions maps all fields correctly for a long position
    - GetPositions maps short position (negative qty → SELL side)
    - GetPosition returns correct net qty
    - GetPosition returns 0 for unknown symbol
    - ClosePosition calls SubmitOrder with correct direction
    - ClosePosition returns no error and empty orderID for already-flat position

  **References**:
  - Port signature: `backend/internal/ports/broker.go` — `GetPositions`, `GetPosition`, `ClosePosition`
  - Domain: `backend/internal/domain/entity.go` — `Trade` struct fields
  - ibsync: `Positions() []Position`, `Position.Contract.Symbol`, `Position.Position (Decimal)`, `Position.AvgCost`

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestGetPositions ./internal/adapters/ibkr/...` — all pass
  - [ ] `cd backend && go test -race -run TestClosePosition ./internal/adapters/ibkr/...` — all pass
  - [ ] Long position (pos > 0) → Side "BUY" domain.Trade
  - [ ] Short position (pos < 0) → Side "SELL" domain.Trade

  **QA Scenarios**:
  ```
  Scenario: Long AAPL position mapped to domain.Trade correctly
    Tool: Bash (unit test)
    Steps:
      1. Mock Positions() returns [{Contract:{Symbol:"AAPL"}, Position: 100, AvgCost: 150.00}]
      2. Call adapter.GetPositions(ctx, "tenant1", "Paper")
      3. Assert: len(trades)==1, trades[0].Symbol=="AAPL", trades[0].Side=="BUY", trades[0].Quantity==100, trades[0].Price==150.00
    Expected: PASS
    Evidence: .sisyphus/evidence/P2.3-get-positions.txt
  ```

  **Commit**: `feat(ibkr): implement GetPositions, GetPosition, ClosePosition, Cancel*Orders + unit tests`

---

- [ ] P2.4 — Implement `SubscribeOrderUpdates` (OrderStreamPort)

  **What to do**:
  - Create `backend/internal/adapters/ibkr/trade_stream.go`:
    - Implement `SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error)`:
      - Create buffered channel: `ch := make(chan ports.OrderUpdate, 100)`
      - Subscribe to ibsync order fill events — investigate ibsync's approach:
        - If ibsync exposes a `trade.Fills` channel or callback mechanism, use that
        - If not, poll `ReqOpenOrders()` on a ticker and emit updates for status changes
        - **Preferred**: ibsync's `IB` struct should expose a way to watch trade events — look for `OnOrderStatus`, `WaitForState`, or equivalent channels in ibsync source before implementing
      - Start a goroutine that reads IB order events and converts to `ports.OrderUpdate`:
        ```go
        ports.OrderUpdate{
            BrokerOrderID: strconv.FormatInt(ibTrade.Order.OrderID, 10),
            ExecutionID:   execution.ExecID,
            Event:         mapIBStatusToEvent(ibTrade.OrderStatus.Status),
            Qty:           ibTrade.OrderStatus.Remaining,
            Price:         ibTrade.OrderStatus.LastFillPrice,
            FilledQty:     ibTrade.OrderStatus.Filled,
            FilledAvgPrice: ibTrade.OrderStatus.AvgFillPrice,
            FilledAt:      time.Now(),
        }
        ```
      - Close channel on context cancel
      - Map IB event types to OMO event strings: `"Filled"→"fill"`, `"PartiallyFilled"→"partial_fill"`, `"Cancelled"→"canceled"`
  - Tests:
    - SubscribeOrderUpdates returns a channel
    - Order fill event maps correctly to OrderUpdate
    - Channel closes when context is cancelled
    - Partial fill event produces correct OrderUpdate with correct FilledQty

  **Must NOT do**:
  - Do NOT block the main thread waiting for fills — all async via goroutine + channel
  - Do NOT poll more frequently than every 500ms if polling approach is needed

  **References**:
  - Port signature: `backend/internal/ports/broker.go` — `OrderStreamPort.SubscribeOrderUpdates` return type `(<-chan OrderUpdate, error)`
  - Port type: `backend/internal/ports/broker.go` — `OrderUpdate` struct fields
  - Alpaca pattern: `backend/internal/adapters/alpaca/trade_stream.go` — goroutine + channel pattern
  - ibsync: investigate `IB.WaitForState()`, `Trade.Done()`, `Trade.Fills`, `PubSub` subscription for order events

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestSubscribeOrderUpdates ./internal/adapters/ibkr/...` — all pass
  - [ ] Fill event produces OrderUpdate with Event=="fill" and correct FilledQty/Price
  - [ ] Channel closes cleanly on context cancel (no goroutine leak in race test)

  **QA Scenarios**:
  ```
  Scenario: Order fill produces OrderUpdate on channel
    Tool: Bash (unit test)
    Steps:
      1. Create adapter with MockIBClient
      2. Call SubscribeOrderUpdates(ctx) → receive channel
      3. Trigger a fill event via mock (inject into the mock's fill channel)
      4. Read from channel with 1s timeout
      5. Assert: OrderUpdate.Event=="fill", FilledQty==100, Price==150.00
    Expected: PASS
    Evidence: .sisyphus/evidence/P2.4-order-update.txt

  Scenario: Channel closes on context cancellation
    Tool: Bash (unit test)
    Steps:
      1. Subscribe to order updates with cancellable context
      2. Cancel context after 100ms
      3. Wait for channel close (select with 500ms timeout)
    Expected: Channel closed cleanly, no goroutine leak
    Evidence: .sisyphus/evidence/P2.4-channel-cancel.txt
  ```

  **Commit**: `feat(ibkr): implement SubscribeOrderUpdates (OrderStreamPort) + unit tests`

---

- [ ] P2.5 — Implement `AccountPort`, `QuoteProvider`, `GetAccountEquity`

  **What to do**:
  - Create `backend/internal/adapters/ibkr/account.go`:
    - `GetAccountBuyingPower(ctx context.Context) (ports.BuyingPower, error)`:
      - Call `a.ib.AccountSummary()` → `[]ibsync.AccountValue`
      - Filter for key fields:
        - `"BuyingPower"` → `EffectiveBuyingPower`
        - `"DayTradingBuyingPower"` → `DayTradingBuyingPower`
        - `"NonMarginableBuyingPower"` → `NonMarginableBuyingPower`
        - `"PatternDayTrader"` → `PatternDayTrader` (string "1"/"0" → bool)
      - Return `ports.BuyingPower` struct
    - `GetAccountEquity(ctx context.Context) (float64, error)`:
      - Call `a.ib.AccountSummary()`, extract `"NetLiquidation"` value
      - Parse as float64, return
      - (This satisfies the `orchestrator.EquitySource` interface)
    - `GetQuote(ctx context.Context, symbol domain.Symbol) (bid, ask float64, err error)`:
      - Call `a.ib.ReqMktData(contract, "", true, false)` — snapshot mode
      - Wait for `ticker.Bid` and `ticker.Ask` to be populated (with 5s timeout)
      - Return bid, ask
      - (This satisfies the `execution.QuoteProvider` interface)
    - `SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error`:
      - For each symbol: qualify contract, start `ReqMktData` subscription (non-snapshot, persistent)
      - Track subscriptions in `a.mktDataSubs map[domain.Symbol]int` (map to reqID for cancellation)
      - (This satisfies the `activation.SymbolSubscriber` interface)
  - Tests for each method with mock IBClient

  **References**:
  - Port signature: `backend/internal/ports/account.go` — `BuyingPower` struct
  - ibsync: `AccountSummary() []AccountValue`, `AccountValue.Tag`, `AccountValue.Value`
  - ibsync: `ReqMktData(contract, genericTickList, snapshot, regulatory) *Ticker`, `Ticker.Bid`, `Ticker.Ask`

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestGetAccountBuyingPower ./internal/adapters/ibkr/...` — pass
  - [ ] `cd backend && go test -race -run TestGetAccountEquity ./internal/adapters/ibkr/...` — pass
  - [ ] `cd backend && go test -race -run TestGetQuote ./internal/adapters/ibkr/...` — pass

  **QA Scenarios**:
  ```
  Scenario: GetAccountBuyingPower maps IB AccountValues correctly
    Tool: Bash (unit test)
    Steps:
      1. Mock AccountSummary returns [{Tag:"BuyingPower", Value:"50000"}, {Tag:"DayTradingBuyingPower", Value:"200000"}]
      2. Call adapter.GetAccountBuyingPower(ctx)
      3. Assert: bp.EffectiveBuyingPower==50000.0, bp.DayTradingBuyingPower==200000.0
    Expected: PASS
    Evidence: .sisyphus/evidence/P2.5-buying-power.txt
  ```

  **Commit**: `feat(ibkr): implement AccountPort, QuoteProvider, GetAccountEquity + unit tests`

---

### Phase 3: Market Data

- [ ] P3.1 — Implement `GetHistoricalBars`

  **What to do**:
  - Create `backend/internal/adapters/ibkr/market_data.go`:
    - Implement `GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)`:
      - Map `domain.Timeframe` to IB bar size string:
        - `"1m"` → `"1 min"`, `"5m"` → `"5 mins"`, `"15m"` → `"15 mins"`, `"1h"` → `"1 hour"`, `"1d"` → `"1 day"`
      - Calculate IB `duration` string from `to - from`:
        - < 7 days → `"N D"` (days), < 365 days → `"N W"` (weeks), else `"N Y"` (years)
      - Format `endDateTime`: `to.Format("20060102 15:04:05 EST")`
      - Call `a.ib.ReqHistoricalData(contract, endDateTime, duration, barSize, "TRADES", true, 1)`
      - Read from returned channel until closed
      - Map each `ibsync.Bar` to `domain.MarketBar`:
        - `Time` ← parse IB time string, `Symbol` ← symbol, `Timeframe` ← timeframe
        - `Open/High/Low/Close/Volume` ← direct mapping
        - `TradeCount` ← `bar.BarCount`
      - Return sorted slice (IB returns chronological; verify ordering)
  - Create `backend/internal/adapters/ibkr/market_data_test.go`:
    - Table-driven timeframe → barSize mapping test
    - Table-driven from/to → duration string test
    - GetHistoricalBars happy path: mock returns 3 bars, all mapped correctly
    - GetHistoricalBars empty result (no bars in range) → returns empty slice, no error
    - GetHistoricalBars context cancelled mid-stream → returns partial results with context error

  **Must NOT do**:
  - Do NOT fetch more than 365 days in a single call (IB limit) — return error if range exceeds this
  - Do NOT mix `"TRADES"` and `"MIDPOINT"` whatToShow — always use `"TRADES"` for equity

  **References**:
  - Port signature: `backend/internal/ports/market_data.go` — `GetHistoricalBars` signature
  - Domain: `backend/internal/domain/entity.go` — `MarketBar` struct, `Timeframe` values
  - Alpaca pattern: `backend/internal/adapters/alpaca/rest.go` — historical bar mapping for reference
  - ibsync: `ReqHistoricalData(contract, endDateTime, duration, barSize, whatToShow string, useRTH bool, formatDate int) (chan Bar, error)`

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestGetHistoricalBars ./internal/adapters/ibkr/...` — all pass
  - [ ] All timeframe mappings covered by table test
  - [ ] Context cancellation handled without panic or goroutine leak

  **QA Scenarios**:
  ```
  Scenario: 1-minute bars for 1-day range mapped correctly
    Tool: Bash (unit test)
    Steps:
      1. Mock ReqHistoricalData returns channel with 3 bars (Open:100, High:105, Low:99, Close:103, Volume:5000)
      2. Call GetHistoricalBars(ctx, "AAPL", "1m", yesterday, today)
      3. Assert: 3 bars returned, first bar fields match mock values, Timeframe=="1m"
    Expected: PASS
    Evidence: .sisyphus/evidence/P3.1-historical-bars.txt

  Scenario: Unsupported timeframe returns error
    Tool: Bash (unit test)
    Steps:
      1. Call GetHistoricalBars with timeframe "3m" (not a supported IB bar size)
      2. Assert: error returned containing "unsupported timeframe"
    Expected: PASS — not panic
    Evidence: .sisyphus/evidence/P3.1-unsupported-timeframe.txt
  ```

  **Commit**: `feat(ibkr): implement GetHistoricalBars with IB bar format mapping + unit tests`

---

- [ ] P3.2 — Implement `StreamBars` and `Close` (real-time bar streaming)

  **What to do**:
  - In `backend/internal/adapters/ibkr/market_data.go`, add:
    - Internal tracking struct: `activeSubs map[domain.Symbol]context.CancelFunc`
    - Implement `StreamBars(ctx context.Context, symbols []domain.Symbol, timeframe domain.Timeframe, handler ports.BarHandler) error`:
      - For each symbol:
        - Qualify contract
        - Call `a.ib.ReqRealTimeBars(contract, 5, "TRADES", true)` → returns `chan ibsync.RealTimeBar` + `cancelFunc`
        - Store `cancelFunc` in `a.activeSubs[symbol]`
        - Start goroutine: read from RealTimeBar channel, aggregate 5-second bars into the requested timeframe
        - On complete bar (based on timeframe boundary): call `handler(ctx, domain.MarketBar{...})`
      - Note: IB's `ReqRealTimeBars` always returns 5-second bars. Must aggregate to 1m, 5m, etc.:
        - Maintain per-symbol `barAggregator` struct that accumulates 5s bars and emits complete bars
      - Return nil (errors reported via handler returning error)
    - Implement `Close() error`:
      - Cancel all active subscriptions by calling each stored `cancelFunc`
      - Clear `activeSubs` map
      - Return nil
    - Also implement `SubscribeSymbols(ctx, symbols)` if not done in P2.5 — just calls StreamBars setup without handler (for warm-up)
  - Tests:
    - `StreamBars` with 1 symbol: mock RealTimeBars channel, emit 12 5s bars (1 minute), verify handler called once with aggregated 1m bar
    - `StreamBars` handler error → streaming stops for that symbol
    - `Close()` cancels all active subscriptions
    - Context cancel stops streaming goroutines

  **Must NOT do**:
  - Do NOT use polling for real-time bars — IB push via `ReqRealTimeBars` channel
  - Do NOT support crypto symbols in StreamBars
  - Do NOT exceed 100 simultaneous market data subscriptions — track count, return error if exceeded

  **References**:
  - Port signature: `backend/internal/ports/market_data.go` — `StreamBars(ctx, symbols, timeframe, handler) error`, `BarHandler` type
  - Alpaca pattern: `backend/internal/adapters/alpaca/websocket.go` — bar aggregation and handler invocation patterns
  - ibsync: `ReqRealTimeBars(contract *Contract, barSize int, whatToShow string, useRTH bool) (chan RealTimeBar, context.CancelFunc, error)`
  - Domain: `domain.MarketBar` — fields `OHLCV`, `TradeCount`, `Time`, `Symbol`, `Timeframe`

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestStreamBars ./internal/adapters/ibkr/...` — all pass
  - [ ] `cd backend && go test -race -run TestClose ./internal/adapters/ibkr/...` — all pass
  - [ ] 12 × 5s bars correctly aggregated into 1 × 1m bar
  - [ ] No goroutine leak on Close() (verified with race detector + goroutine count check)

  **QA Scenarios**:
  ```
  Scenario: 12 five-second bars aggregate into 1 one-minute bar
    Tool: Bash (unit test)
    Steps:
      1. Mock ReqRealTimeBars channel emits 12 bars with timestamps t, t+5s, t+10s, ..., t+60s
      2. Call StreamBars(ctx, ["AAPL"], "1m", handler)
      3. After 12 bars, assert handler called exactly once
      4. Assert aggregated bar: Open=first bar Open, Close=last bar Close, High=max, Low=min, Volume=sum
    Expected: PASS
    Evidence: .sisyphus/evidence/P3.2-bar-aggregation.txt

  Scenario: Close() stops all active subscriptions
    Tool: Bash (unit test)
    Steps:
      1. Start StreamBars for 3 symbols
      2. Call adapter.Close()
      3. Assert: all 3 cancelFuncs called, activeSubs map empty
    Expected: PASS, no goroutine leak
    Evidence: .sisyphus/evidence/P3.2-close-subs.txt
  ```

  **Commit**: `feat(ibkr): implement StreamBars via ReqRealTimeBars + bar aggregation + unit tests`

---

### Phase 4: Config Wiring & Docker

- [ ] P4.1 — Add `BROKER` env var and active broker selection in config

  **What to do**:
  - In `backend/internal/config/config.go`:
    - Ensure `Broker string` field is on top-level `Config` struct (added in P0.1)
    - Add validation: if `cfg.Broker == ""`, default to `"alpaca"`
    - Add validation: if `cfg.Broker != "alpaca" && cfg.Broker != "ibkr"`, return error `"BROKER must be 'alpaca' or 'ibkr'"`
    - When `cfg.Broker == "ibkr"`, validate IBKRConfig fields: Host must not be empty, Port must be > 0
  - Add to `configs/config.yaml`: `broker: alpaca`  (default)
  - Write config unit test: `TestBrokerSelection` — validates both "alpaca" and "ibkr" pass validation, unknown value fails

  **Must NOT do**:
  - Do NOT add broker-specific business logic to config package
  - Do NOT validate Alpaca credentials when `BROKER=ibkr` (and vice versa)

  **Acceptance Criteria**:
  - [ ] `BROKER=ibkr go build ./cmd/omo-core/` — compiles
  - [ ] `BROKER=alpaca go build ./cmd/omo-core/` — compiles
  - [ ] `cd backend && go test -run TestBrokerSelection ./internal/config/...` — pass

  **QA Scenarios**:
  ```
  Scenario: BROKER=ibkr with valid IBKRConfig passes validation
    Tool: Bash (unit test)
    Steps:
      1. Set BROKER=ibkr, IBKR_HOST=localhost, IBKR_PORT=4002
      2. Call config.Load() — assert no error
    Expected: PASS
    Evidence: .sisyphus/evidence/P4.1-broker-ibkr-valid.txt

  Scenario: Unknown BROKER value returns descriptive error
    Tool: Bash (unit test)
    Steps:
      1. Set BROKER=tradovate
      2. Call config.Load() — assert error contains "must be 'alpaca' or 'ibkr'"
    Expected: PASS
    Evidence: .sisyphus/evidence/P4.1-broker-unknown.txt
  ```

  **Commit**: `feat(config): add BROKER env var validation and active broker selection logic`

---

- [ ] P4.2 — Conditional broker wiring in `infra.go` and `services.go`

  **What to do**:
  - **BEFORE MAKING ANY CHANGES**: Use `lsp_find_references` on `alpacaAdapter` and `ast_grep_search` for `infra.alpacaAdapter` to get the complete callsite map.
  - In `backend/cmd/omo-core/infra.go`:
    - Add `ibkrAdapter *ibkr.Adapter` field to `infraDeps` struct
    - Add `activeBroker string` field to `infraDeps`
    - In `initInfra()` (or equivalent init function): conditional adapter init:
      ```go
      cfg.Broker = "alpaca" // or "ibkr" from config
      if cfg.Broker == "ibkr" {
          var a *ibkr.Adapter
          if err := retryWithBackoff(log, "ibkr_adapter", 5, 2*time.Second, 30*time.Second, func() error {
              var err error
              a, err = ibkr.NewAdapter(cfg.IBKR, log.With().Str("component", "ibkr").Logger())
              if err != nil { return err }
              return a.Connect(ctx)
          }); err != nil {
              log.Fatal().Err(err).Msg("failed to create IBKR adapter")
          }
          deps.ibkrAdapter = a
      } else {
          // existing Alpaca init block — UNCHANGED
      }
      deps.activeBroker = cfg.Broker
      ```
    - Import `"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"` (add to imports)
  - In `backend/cmd/omo-core/services.go`:
    - For every service that takes a port (BrokerPort, MarketDataPort, AccountPort, etc.):
      ```go
      var broker ports.BrokerPort
      var marketData ports.MarketDataPort
      var account ports.AccountPort
      // ... etc
      if infra.activeBroker == "ibkr" {
          broker = infra.ibkrAdapter
          marketData = infra.ibkrAdapter
          account = infra.ibkrAdapter
          // QuoteProvider, EquitySource, etc.
      } else {
          broker = infra.alpacaAdapter
          marketData = infra.alpacaAdapter
          account = infra.alpacaAdapter
          // ... existing Alpaca wiring — UNCHANGED
      }
      // Then pass `broker`, `marketData`, etc. to services
      ```
    - Observability hooks (SetMetrics, FeedHealth, SetCircuitBreakerCallback) remain Alpaca-only (behind `if infra.activeBroker == "alpaca"` guard)
  - Add `defer infra.ibkrAdapter.Close()` in main cleanup path when `activeBroker == "ibkr"`
  - **REGRESSION CHECK** after every change: `cd backend && go test ./...` and `BROKER=alpaca go build ./cmd/omo-core/`

  **Must NOT do**:
  - Do NOT remove or modify any Alpaca wiring
  - Do NOT add observability hooks for IBKR in Phase 4
  - Do NOT refactor `infraDeps` to use interface types — additive only

  **References**:
  - `backend/cmd/omo-core/infra.go` — existing `infraDeps` struct and `retryWithBackoff` pattern (lines 59-114)
  - `backend/cmd/omo-core/services.go` — existing service wiring for all ports
  - Pattern: `backend/internal/adapters/alpaca/adapter.go` — `NewAdapter` signature

  **Acceptance Criteria**:
  - [ ] `BROKER=alpaca go build ./cmd/omo-core/` — zero errors (regression check)
  - [ ] `BROKER=ibkr go build ./cmd/omo-core/` — zero errors
  - [ ] `cd backend && go test ./...` — all tests pass (Alpaca path unbroken)
  - [ ] `cd backend && go vet ./cmd/omo-core/...` — zero issues

  **QA Scenarios**:
  ```
  Scenario: BROKER=alpaca path compiles and tests pass (regression)
    Tool: Bash
    Steps:
      1. BROKER=alpaca go build ./cmd/omo-core/ 2>&1
      2. go test ./... 2>&1 | tail -5
    Expected: zero build errors, all tests pass
    Evidence: .sisyphus/evidence/P4.2-alpaca-regression.txt

  Scenario: BROKER=ibkr path compiles
    Tool: Bash
    Steps:
      1. BROKER=ibkr IBKR_HOST=localhost IBKR_PORT=4002 go build ./cmd/omo-core/ 2>&1
    Expected: zero build errors
    Evidence: .sisyphus/evidence/P4.2-ibkr-compile.txt
  ```

  **Commit**: `feat(infra): conditional broker wiring in initInfra + services.go port assignment`

---

- [ ] P4.3 — Add `ib-gateway` sidecar to `docker-compose.yml`

  **What to do**:
  - In `deployments/docker-compose.yml`, add the `ib-gateway` service:
    ```yaml
    ib-gateway:
      image: ghcr.io/gnzsnz/ib-gateway:stable
      container_name: omo-ib-gateway
      restart: unless-stopped
      environment:
        - TWS_USERID=${IBKR_USER}
        - TWS_PASSWORD=${IBKR_PASS}
        - TRADING_MODE=paper
        - TWS_ACCEPT_EULA=yes
        - TWS_ACCEPT_INCOMING_CONNECTION=yes
        - VNC_SERVER_PASSWORD=${IBKR_VNC_PASS:-changeme}
      ports:
        - "4002:4002"   # Paper Trading API
        - "5900:5900"   # VNC debugging (optional)
      healthcheck:
        test: ["CMD", "nc", "-z", "localhost", "4002"]
        interval: 30s
        timeout: 10s
        retries: 10
        start_period: 60s   # IB Gateway takes ~60s to start
    ```
  - Add dependency: `omo-core` should have `depends_on: {ib-gateway: {condition: service_healthy}}` — but only conditionally. Since the current `omo-core` service is not broker-conditional in docker-compose, add a comment:
    ```yaml
    # Uncomment when BROKER=ibkr:
    # depends_on:
    #   ib-gateway:
    #     condition: service_healthy
    ```
  - Add to `.env` (or `.env.example`): `IBKR_USER=`, `IBKR_PASS=`, `IBKR_VNC_PASS=changeme`
  - Update `README` or `docs/` with IBKR setup instructions (brief: set env vars, docker compose up)
  - In `backend/internal/config/config.go` — update IBKR host default when running in Docker: `Host: "ib-gateway"` (matches docker-compose service name)

  **Must NOT do**:
  - Do NOT change the Alpaca service config or any existing docker-compose services
  - Do NOT set `TRADING_MODE=live` — paper only in this plan
  - Do NOT commit real IBKR credentials to any file

  **References**:
  - `deployments/docker-compose.yml` — existing service format, network config, volume mounts
  - gnzsnz/ib-gateway image: `ghcr.io/gnzsnz/ib-gateway:stable` — env var names: `TWS_USERID`, `TWS_PASSWORD`, `TRADING_MODE`, `TWS_ACCEPT_EULA`

  **Acceptance Criteria**:
  - [ ] `docker compose -f deployments/docker-compose.yml config` — valid YAML, no errors
  - [ ] `docker compose -f deployments/docker-compose.yml pull ib-gateway` — image pulls successfully
  - [ ] `IBKR_USER=test IBKR_PASS=test docker compose -f deployments/docker-compose.yml up ib-gateway --no-start` — no config errors

  **QA Scenarios**:
  ```
  Scenario: docker-compose config is valid YAML
    Tool: Bash
    Steps:
      1. docker compose -f deployments/docker-compose.yml config 2>&1
    Expected: Valid config output, no YAML parse errors
    Evidence: .sisyphus/evidence/P4.3-compose-valid.txt

  Scenario: Existing services unaffected (Alpaca path regression)
    Tool: Bash
    Steps:
      1. docker compose -f deployments/docker-compose.yml up timescaledb --no-start
      2. docker compose -f deployments/docker-compose.yml up omo-core --no-start
    Expected: Both services configured without error, Alpaca path unbroken
    Evidence: .sisyphus/evidence/P4.3-existing-services.txt
  ```

  **Commit**: `feat(deploy): add ib-gateway sidecar to docker-compose with paper trading config`

---

### Phase 5: Advanced Features (Deferred)

- [ ] P5.1 — Implement `SnapshotPort` (`GetSnapshots`)

  **What to do**:
  - In `backend/internal/adapters/ibkr/snapshot.go`:
    - Implement `GetSnapshots(ctx context.Context, symbols []string, asOf time.Time) (map[string]ports.Snapshot, error)`:
      - For each symbol: qualify contract, call `a.ib.ReqMktData(contract, "", true, false)` (snapshot=true)
      - Wait for `ticker.Last`, `ticker.Bid`, `ticker.Ask`, `ticker.Volume` with 5s timeout per symbol
      - Map to `ports.Snapshot` struct
      - Collect all results into map, return
    - Note: IB snapshot data for paper accounts may be delayed — document this in code comment

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestGetSnapshots ./internal/adapters/ibkr/...` — pass

  **Commit**: `feat(ibkr): implement SnapshotPort (GetSnapshots via ReqMktData snapshot)`

---

- [ ] P5.2 — Implement `UniverseProviderPort` (`ListTradeable`) with hardcoded fallback

  **What to do**:
  - In `backend/internal/adapters/ibkr/universe.go`:
    - IBKR has no "list all tradeable assets" API equivalent to Alpaca's `/v2/assets`
    - Implement `ListTradeable(ctx context.Context, assetClass domain.AssetClass) ([]ports.Asset, error)`:
      - Return a hardcoded list of SP500 constituents (sourced from a static Go slice in the file, or loaded from a bundled JSON file)
      - For `assetClass == domain.Equity`: return the hardcoded equity list
      - For other asset classes: return empty list (no error)
    - Include at minimum the 50 most liquid US equities as a stub list
    - Document clearly in code: `// IBKR has no list-all-assets endpoint. Using curated static list. Update periodically.`

  **Must NOT do**:
  - Do NOT attempt to query IB API for a universe list — no such endpoint exists
  - Do NOT fail with error — return best-effort static list

  **Acceptance Criteria**:
  - [ ] `ListTradeable(ctx, domain.Equity)` returns non-empty slice
  - [ ] `ListTradeable(ctx, domain.Crypto)` returns empty slice, nil error

  **Commit**: `feat(ibkr): implement UniverseProviderPort with hardcoded equity list fallback`

---

- [ ] P5.3 — Implement `OptionsMarketDataPort` (`GetOptionChain`)

  **What to do**:
  - In `backend/internal/adapters/ibkr/options.go`:
    - Implement `GetOptionChain(ctx context.Context, underlying domain.Symbol, expiry time.Time, right domain.OptionRight) ([]domain.OptionContractSnapshot, error)`:
      - First qualify the underlying stock contract to get its ConID
      - Call `a.ib.ReqSecDefOptParams(string(underlying), "", "STK", conID)` → returns `[]ibsync.OptionChain`
      - Filter chains by expiry date matching `expiry.Format("20060102")`
      - For each strike in the matching chain: build `domain.OptionContractSnapshot` with symbol, strike, expiry, right
      - For pricing: optionally call `ReqMktData` for each option contract (expensive — only if required by caller)

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestGetOptionChain ./internal/adapters/ibkr/...` — pass with mock

  **Commit**: `feat(ibkr): implement OptionsMarketDataPort (GetOptionChain via ReqSecDefOptParams)`

---

- [ ] P5.4 — Implement `OptionsPricePort` (`GetOptionPrices`)

  **What to do**:
  - In `backend/internal/adapters/ibkr/options.go`, add:
    - `GetOptionPrices(ctx context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error)`:
      - For each option symbol: parse OCC symbol format → build `ibsync.Contract{SecType:"OPT", Symbol, Strike, Right, LastTradeDateOrContractMonth, Multiplier:"100", Exchange:"SMART", Currency:"USD"}`
      - Call `QualifyContracts` then `ReqMktData(contract, "", true, false)` (snapshot)
      - Map ticker Bid/Ask/Last to `domain.OptionQuote`
      - Collect results with 5s timeout

  **Acceptance Criteria**:
  - [ ] `cd backend && go test -race -run TestGetOptionPrices ./internal/adapters/ibkr/...` — pass with mock

  **Commit**: `feat(ibkr): implement OptionsPricePort (GetOptionPrices via ReqMktData snapshot)`

---

## Risk Register

### R1 — ibsync Library Maturity (HIGH)
**Risk**: `ibsync` is a community library (v0.x). Edge cases with partial fills, error propagation, or reconnect behaviour may require workarounds.
**Mitigation**: Extract `IBClient` interface over ibsync in Task 1.2. Pin exact version in `go.mod`. Write integration tests against real paper gateway in Phase 1 (not deferred). If ibsync has critical gaps, the interface boundary makes it replaceable with raw `ibapi`.

### R2 — Daily Gateway Restart During Open Positions (HIGH)
**Risk**: IB Gateway restarts ~23:45 ET. Open positions and pending orders during the 30–60 second outage window could cause missed fills or duplicate orders.
**Mitigation**: Reconnect watchdog with exponential backoff (1s → 30s, max 60 attempts). On successful reconnect: re-subscribe to market data, reconcile positions, re-fetch `NextID` via IB (never use stale cached ID).

### R3 — Wiring Layer Regression (HIGH)
**Risk**: `infra.go` and `services.go` have 40+ references to `alpacaAdapter`. Incorrect conditional wiring could silently break the Alpaca path.
**Mitigation**: Phase 4 wiring is last. Run `go test ./...` (full suite) after every wiring change. Explicit regression check: `BROKER=alpaca go build ./cmd/omo-core/` must compile and all tests pass.

### R4 — Order ID Management Across Reconnects (MEDIUM)
**Risk**: After reconnect, IB provides a new `NextID` base. Stale local order-to-intent mapping causes lookup failures.
**Mitigation**: Always use `ib.NextID()` fresh on reconnect — never persist or predict. Store intent→orderID in an in-memory map keyed by IBKR order ID.

### R5 — Market Data Lines Limit (MEDIUM)
**Risk**: IB standard accounts have a 100 simultaneous market data subscription limit. Exceeding it silently degrades data.
**Mitigation**: Track active subscription count in adapter. Return explicit error when limit reached. Document limit in `IBKRConfig` comments.

### R6 — Contract Qualification Latency (MEDIUM)
**Risk**: `QualifyContract()` is a synchronous IB API call (~100ms per symbol). Called before every order on a cold cache.
**Mitigation**: Populate contract cache at startup for all configured symbols. Refresh on reconnect. Cold-path latency is acceptable for paper trading.

### R7 — Paper Account Market Data (LOW — mitigated by design)
**Risk**: Paper accounts return error 10167 without `ReqMarketDataType(4)`.
**Mitigation**: Adapter constructor calls `ib.ReqMarketDataType(4)` when `cfg.PaperMode == true`. Handled in Phase 1, not deferred.

### R8 — trade.Done() Partial Fill Semantics (LOW)
**Risk**: Unclear if ibsync's `trade.Done()` fires on partial fills or only terminal states (FILLED, CANCELLED).
**Mitigation**: Verified in Phase 1 integration test. If partial fills don't trigger `Done()`, switch to watching `trade.Fills` channel directly in OrderStreamPort implementation.

---

## Testing Strategy

### Unit Tests (All Phases, Mandatory)
- All unit tests use a mocked `IBClient` interface (Task 1.2)
- Run: `cd backend && go test -race ./internal/adapters/ibkr/...`
- Coverage target: ≥80% of adapter code
- Every public method has at minimum: one happy-path test + one error-path test
- Table-driven tests for domain type conversions (symbol→Contract, OrderIntent→ibsync.Order, Position→domain.Trade, etc.)

### Integration Tests (Opt-In, Phase 1+)
- Tagged `//go:build integration` — excluded from CI unless explicitly opted in
- Require real IB Gateway on `localhost:4002` (paper mode)
- Run: `cd backend && go test -tags=integration -timeout=120s -run TestIBKR ./internal/adapters/ibkr/...`
- Phase 1: connect/disconnect, reconnect after simulated gateway restart
- Phase 2: submit market order → wait for fill → verify position
- Phase 3: stream 1-minute bars for AAPL for 60 seconds → verify bar structure

### Regression Tests (Phase 4, Mandatory)
- Before and after every wiring change: `cd backend && go test ./...` (full suite)
- Compilation check both paths: `BROKER=alpaca go build ./cmd/omo-core/` and `BROKER=ibkr go build ./cmd/omo-core/`

### TDD Workflow Per Task
1. **RED**: Write failing test asserting desired behaviour (compile error or assertion failure)
2. **GREEN**: Implement minimal code to pass the test
3. **REFACTOR**: Clean up while keeping tests green
4. Before every commit: `cd backend && go vet ./internal/adapters/ibkr/... && go test -race ./internal/adapters/ibkr/...`

---

## Open Questions

| # | Question | Decision |
|---|----------|---------|
| 1 | Wiring approach (Option A vs B)? | **Option A** (additive `ibkrAdapter` field) confirmed |
| 2 | Crypto support? | **Out of scope**. IBKR adapter handles equity (STK) only |
| 3 | Market data line limit handling? | Simple counter + explicit error on exceed. No rotation |
| 4 | Options interfaces in Phase 1–4? | Return `ErrNotImplemented`. Services.go conditionals skip options when `BROKER=ibkr` |
| 5 | Forming bar feed (SetTradeHandler)? | Skipped Phase 1–4. IBKR adapter `Close()` returns nil. Phase 5 enhancement |
| 6 | `trade.Done()` partial fill behaviour? | **Must verify** in Phase 1 integration test before Phase 2 order stream |

---

## Commit Strategy

```
Phase 0:
  [P0.1] feat(config): add IBKRConfig struct with yaml tags and env overlay
  [P0.2] chore(deps): add github.com/scmhub/ibsync dependency to go.mod

Phase 1:
  [P1.1] feat(ibkr): add package skeleton with 13 compile-time interface assertions (all return ErrNotImplemented)
  [P1.2] feat(ibkr): extract IBClient interface over ibsync for testability + mock implementation
  [P1.3] feat(ibkr): implement Connect/Disconnect with reconnect watchdog + unit tests
  [P1.4] feat(ibkr): implement qualified contract cache with startup population + unit tests

Phase 2:
  [P2.1] feat(ibkr): implement SubmitOrder with domain-to-IB type mapping + unit tests
  [P2.2] feat(ibkr): implement CancelOrder, GetOrderStatus, GetOrderDetails + unit tests
  [P2.3] feat(ibkr): implement GetPositions, GetPosition, ClosePosition, Cancel*Orders + unit tests
  [P2.4] feat(ibkr): implement SubscribeOrderUpdates (OrderStreamPort) + unit tests
  [P2.5] feat(ibkr): implement AccountPort, QuoteProvider, GetAccountEquity + unit tests

Phase 3:
  [P3.1] feat(ibkr): implement GetHistoricalBars with IB bar format mapping + unit tests
  [P3.2] feat(ibkr): implement StreamBars via ReqRealTimeBars + bar aggregation + unit tests

Phase 4:
  [P4.1] feat(config): add BROKER env var and active broker selection logic in config
  [P4.2] feat(infra): conditional broker wiring in initInfra + services.go port assignment
  [P4.3] feat(deploy): add ib-gateway sidecar to docker-compose with paper config

Phase 5 (deferred):
  [P5.1] feat(ibkr): implement SnapshotPort (GetSnapshots via ReqMktData)
  [P5.2] feat(ibkr): implement UniverseProviderPort (hardcoded equity list fallback + SP500 query)
  [P5.3] feat(ibkr): implement OptionsMarketDataPort (GetOptionChain via ReqSecDefOptParams)
  [P5.4] feat(ibkr): implement OptionsPricePort (GetOptionPrices via ReqMktData)
```

---

## Success Criteria

```bash
# Unit tests pass with race detector
cd backend && go test -race ./internal/adapters/ibkr/...
# Expected: ok  github.com/oh-my-opentrade/backend/internal/adapters/ibkr

# Both broker paths compile without errors
BROKER=alpaca go build ./cmd/omo-core/
BROKER=ibkr   go build ./cmd/omo-core/

# Full suite regression (Alpaca path unbroken)
cd backend && go test ./...
# Expected: all packages pass, zero failures

# Integration smoke test (requires ib-gateway on localhost:4002)
cd backend && go test -tags=integration -timeout=120s -run TestIBKR ./internal/adapters/ibkr/...
# Expected: connect → submit order → receive fill → disconnect — all assertions pass
```
