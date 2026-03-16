# IBKR Broker Adapter Plan

**Created:** 2026-03-16  
**Status:** Draft  
**Goal:** Implement Interactive Brokers (IBKR) as a fully-supported broker backend in `oh-my-opentrade`, running alongside Alpaca and selectable via a single config flag.

---

## Background

`oh-my-opentrade` currently uses Alpaca as its sole broker. The hexagonal architecture means the domain layer has zero broker coupling — all broker interaction flows through 8 port interfaces defined in `backend/internal/ports/`. The Alpaca adapter (`backend/internal/adapters/alpaca/`) is the only concrete implementation.

IBKR offers significantly deeper market access (equities, options, futures, forex), institutional-grade execution, and the ability to run with a real brokerage account. Adding IBKR as an alternative adapter gives users broker choice without touching business logic.

The primary operational difference is fundamental: Alpaca is a REST/WebSocket cloud API; IBKR requires a persistent local sidecar process (IB Gateway) that the adapter communicates with over a stateful TCP socket.

---

## Scope

### In Scope
- New `backend/internal/adapters/ibkr/` package implementing the core trading ports
- `IBKRConfig` struct added to `backend/internal/config/config.go`
- Broker selection via `BROKER=ibkr` env var (default: `alpaca`)
- Conditional adapter initialization in `backend/cmd/omo-core/infra.go`
- `ib-gateway` Docker sidecar in `deployments/docker-compose.yml`
- Paper trading only (live trading is explicitly out of scope for this plan)
- Core ports: `BrokerPort`, `OrderStreamPort`, `MarketDataPort`, `AccountPort`

### Out of Scope
- Live trading (separate risk review required before enabling)
- `OptionsMarketDataPort`, `OptionsPricePort` — IBKR options support (deferred to Phase 5)
- `SnapshotPort`, `UniverseProviderPort` — no IBKR equivalent exists; deferred or replaced with static config
- Crypto trading (IBKR supports crypto but with different mechanics than equity; deferred)
- Dashboard UI changes to surface broker selection
- Migrating existing trade history between brokers

---

## Architecture Decision: Broker Selection Strategy

**Pattern: Runtime-selectable adapter via config flag**

Both Alpaca and IBKR adapters coexist. `infra.go` reads `BROKER` env var and initializes exactly one, storing it as the `ports.BrokerPort` / `ports.MarketDataPort` interface — not a concrete type. `services.go` requires no changes at all.

```
BROKER=alpaca  →  alpaca.NewAdapter(cfg.Alpaca, ...)    (current behavior, default)
BROKER=ibkr    →  ibkr.NewAdapter(cfg.IBKR, ...)        (new)
```

This avoids a feature-flag maze and keeps the wiring clean. Both adapters satisfy the same port interfaces — the compiler enforces this at build time via interface assertion guards:

```go
// backend/internal/adapters/ibkr/adapter.go
var _ ports.BrokerPort       = (*Adapter)(nil)
var _ ports.MarketDataPort   = (*Adapter)(nil)
var _ ports.OrderStreamPort  = (*Adapter)(nil)
var _ ports.AccountPort      = (*Adapter)(nil)
```

The `infraDeps` struct field type changes from `*alpaca.Adapter` to the port interfaces, enabling both adapters to be stored uniformly.

---

## Phase 1: IB Gateway Sidecar + Connection Lifecycle

**Priority:** Critical — nothing else works without this  
**Estimated Effort:** Small-Medium  
**Independently shippable:** Yes (validates Docker setup + socket connectivity before any trading code)

### What IBKR requires that Alpaca does not
IB Gateway is a desktop application that authenticates against IBKR's private network and exposes a local TCP socket. Your Go service connects to this socket. The connection must remain open — losing it means losing order update callbacks and market data subscriptions. IB Gateway also performs a forced restart at ~11:45 PM ET daily.

### 1.1 — Add `ib-gateway` Docker service

**File:** `deployments/docker-compose.yml`

Add the `gnzsnz/ib-gateway` service. This image bundles **IBC (IB Controller)** to automate the GUI login flow, and **socat** to relay the socket from `localhost` to the container network interface.

```yaml
  ib-gateway:
    image: gnzsnz/ib-gateway:stable
    container_name: omo-ib-gateway
    restart: unless-stopped
    ports:
      - "4002:4002"   # Paper Trading API (IB Gateway)
      - "5900:5900"   # VNC — optional visual debugging
    environment:
      - TWS_USERID=${IBKR_USER}
      - TWS_PASSWORD=${IBKR_PASS}
      - TRADING_MODE=paper
      - READ_ONLY=no
      - TWS_ACCEPT_INCOMING_CONNECTION=yes
      - VNC_SERVER_PASSWORD=${VNC_PASS:-password}
    healthcheck:
      test: ["CMD-SHELL", "echo > /dev/tcp/localhost/4002"]
      interval: 10s
      timeout: 5s
      retries: 12   # Gateway takes ~60s to boot
      start_period: 90s
```

Update `omo-core` to optionally depend on `ib-gateway`:

```yaml
  omo-core:
    depends_on:
      timescaledb:
        condition: service_healthy
      migrate:
        condition: service_completed_successfully
      # Only needed when BROKER=ibkr — Docker Compose doesn't support conditional
      # depends_on, so we rely on the retry-with-backoff in infra.go instead.
```

**Done criteria:**
- `docker compose up ib-gateway` starts successfully
- `telnet localhost 4002` or `nc -z localhost 4002` succeeds once gateway is ready
- VNC at `localhost:5900` shows IB Gateway UI logged into paper account

### 1.2 — Add `.env` variables

**File:** `.env` (and `.env.example`)

```bash
# IBKR credentials (paper trading)
IBKR_USER=your_ibkr_username
IBKR_PASS=your_ibkr_password
IBKR_GATEWAY_HOST=localhost    # or "ib-gateway" inside Docker network
IBKR_GATEWAY_PORT=4002
IBKR_CLIENT_ID=1               # unique per connection; increment if running multiple instances
BROKER=alpaca                  # switch to "ibkr" to activate IBKR adapter
```

### 1.3 — Add `IBKRConfig` to config

**File:** `backend/internal/config/config.go`

```go
// IBKRConfig holds connection parameters for the IB Gateway adapter.
type IBKRConfig struct {
    Host     string `yaml:"host"`
    Port     int    `yaml:"port"`
    ClientID int    `yaml:"client_id"`
    PaperMode bool  `yaml:"paper_mode"`
}
```

Add to `Config` struct:
```go
IBKR   IBKRConfig `yaml:"ibkr"`
Broker string     `yaml:"-"`   // populated from BROKER env var
```

Add env overlay in `Load()`:
```go
if val := os.Getenv("BROKER"); val != "" {
    cfg.Broker = val
} else {
    cfg.Broker = "alpaca"
}
if val := os.Getenv("IBKR_GATEWAY_HOST"); val != "" { cfg.IBKR.Host = val }
if val := os.Getenv("IBKR_GATEWAY_PORT"); val != "" { /* strconv.Atoi */ }
if val := os.Getenv("IBKR_CLIENT_ID");   val != "" { /* strconv.Atoi */ }
```

Update `validate()`: skip Alpaca credential checks when `cfg.Broker == "ibkr"`.

**Done criteria:**
- `config.Load()` parses IBKR env vars correctly
- Alpaca credential validation skipped when `BROKER=ibkr`
- Unit test in `config_test.go` covering both broker modes

### 1.4 — Create IBKR adapter skeleton + connection lifecycle

**New files:** `backend/internal/adapters/ibkr/`
```
adapter.go          — Adapter struct, NewAdapter(), compile-time interface assertions
connection.go       — Connect/Disconnect/reconnect loop
```

**Dependency:**
```bash
cd backend && go get github.com/scmhub/ibsync@latest
```

Key design decisions for `connection.go`:
- `Connect()` dials IB Gateway via `ibsync.NewIB().Connect(host, port, clientID)`
- Background goroutine monitors socket health (read EOF / timeout)
- On disconnect: exponential backoff reconnect (initial 5s, max 60s, indefinite retries) — mirrors existing `retryWithBackoff` pattern in `infra.go`
- On daily reset (~11:45 PM ET): reconnect loop handles this automatically
- Expose `IsConnected() bool` for health checks / Prometheus metric

**Done criteria:**
- `ibkr.NewAdapter(cfg.IBKR, log)` connects to IB Gateway without error
- Disconnect + reconnect cycle verified manually (kill gateway, restart, observe reconnect logs)
- `go test ./internal/adapters/ibkr/ -run TestConnection` passes (uses mock or integration tag)

---

## Phase 2: Core Trading Ports

**Priority:** High  
**Estimated Effort:** Medium  
**Depends on:** Phase 1 complete  
**Independently shippable:** Yes — can paper-trade equities with these ports alone

### Ports to implement in this phase
- `BrokerPort` — orders + positions
- `AccountPort` — buying power
- `OrderStreamPort` — real-time order updates

### 2.1 — BrokerPort: Order submission + management

**File:** `backend/internal/adapters/ibkr/broker.go`

IBKR order placement requires:
1. A `Contract` object (Symbol, SecType="STK", Exchange="SMART", Currency="USD")
2. An `Order` object (Action="BUY"/"SELL", TotalQuantity, OrderType, LmtPrice)
3. A unique `OrderID` obtained via `ib.ReqIds()` on startup and incremented locally

```go
// Pseudocode — adapt domain.OrderIntent → ibsync types
func (a *Adapter) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
    contract := ibsync.Stock(intent.Symbol.String(), "SMART", "USD")
    order := ibsync.LimitOrder(sideStr(intent.Side), intent.Qty, intent.LimitPrice)
    trade, err := a.ib.PlaceOrder(contract, order)
    if err != nil { return "", err }
    return strconv.FormatInt(int64(trade.Order.OrderId), 10), nil
}
```

Methods to implement:
- `SubmitOrder` — `ib.PlaceOrder(contract, order)`
- `CancelOrder` — `ib.CancelOrder(orderID)`
- `CancelOpenOrders` — fetch open orders, filter by symbol+side, cancel each
- `GetOrderStatus` — `ib.OpenOrders()` or `ib.CompletedOrders()`, map status string
- `GetOrderDetails` — parse `ibsync.Trade` fields into `ports.OrderDetails`
- `CancelAllOpenOrders` — `ib.ReqGlobalCancel()`
- `GetPositions` / `GetPosition` — `ib.Positions()`, filter by symbol
- `ClosePosition` — `ib.ClosePosition(contract)` or submit market order for inverse qty

**Done criteria:**
- Can place a paper BUY limit order for AAPL and see it in IB Gateway UI
- `GetPositions()` returns the open position after fill
- `ClosePosition()` liquidates it; position shows zero

### 2.2 — AccountPort: Buying power

**File:** `backend/internal/adapters/ibkr/account.go`

```go
func (a *Adapter) GetAccountBuyingPower(ctx context.Context) (float64, error) {
    summary, err := a.ib.AccountSummary()
    for _, item := range summary {
        if item.Tag == "BuyingPower" {
            return strconv.ParseFloat(item.Value, 64)
        }
    }
}
```

**Done criteria:** Returns non-zero float from paper account

### 2.3 — OrderStreamPort: Real-time order updates

**File:** `backend/internal/adapters/ibkr/order_stream.go`

IBKR delivers order updates asynchronously via the EWrapper callback model. `ibsync` exposes these as channel events on `Trade.Updates`.

Pattern:
```go
func (a *Adapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
    out := make(chan ports.OrderUpdate, 64)
    go func() {
        defer close(out)
        for {
            select {
            case <-ctx.Done():
                return
            case event := <-a.ib.TradeEvents():   // ibsync trade event channel
                out <- mapTradeEvent(event)
            }
        }
    }()
    return out, nil
}
```

Map IBKR event types to OMO's `OrderUpdate.Event` vocabulary:
| IBKR Status | OMO Event |
|---|---|
| `Submitted` / `PreSubmitted` | `"new"` |
| `Filled` | `"fill"` |
| `PartiallyFilled` | `"partial_fill"` |
| `Cancelled` | `"canceled"` |
| `Inactive` | `"expired"` |

**Done criteria:** Place a paper order; observe `"new"` then `"fill"` events emitted on the channel within execution service logs.

---

## Phase 3: Market Data Port

**Priority:** High  
**Estimated Effort:** Medium  
**Depends on:** Phase 1 complete  
**Independently shippable:** Yes — can be developed and tested independently of Phase 2

### 3.1 — Real-time bar streaming

**File:** `backend/internal/adapters/ibkr/market_data.go`

IBKR streams real-time 5-second "RealTimeBars" or minute bars via `reqMktData`. Map to `domain.MarketBar` and call the `BarHandler`.

```go
func (a *Adapter) StreamBars(ctx context.Context, symbols []domain.Symbol, tf domain.Timeframe, handler ports.BarHandler) error {
    for _, sym := range symbols {
        contract := ibsync.Stock(sym.String(), "SMART", "USD")
        bars, err := a.ib.ReqRealTimeBars(contract, 5, "MIDPOINT", false)
        // Aggregate 5s bars → 1m/5m bars using existing domain logic if needed
        go func() {
            for bar := range bars {
                _ = handler(ctx, mapBar(bar, sym))
            }
        }()
    }
    <-ctx.Done()
    return nil
}
```

**Note on bar aggregation:** IBKR only natively provides 5-second real-time bars. If the configured timeframe is 1m or 5m, bars must be aggregated. Evaluate whether to reuse existing aggregation logic from the Alpaca `WSClient` or implement it fresh in the IBKR adapter.

### 3.2 — Historical bars

**File:** `backend/internal/adapters/ibkr/market_data.go`

```go
func (a *Adapter) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
    contract := ibsync.Stock(symbol.String(), "SMART", "USD")
    bars, err := a.ib.ReqHistoricalData(contract, to, durationStr(from, to), barSizeStr(tf), "MIDPOINT", 1, 1, false, nil)
    return mapHistoricalBars(bars, symbol), err
}
```

Map timeframe to IBKR `barSize` string: `1m → "1 min"`, `5m → "5 mins"`, `1h → "1 hour"`, `1d → "1 day"`.

**Done criteria:**
- `StreamBars` delivers bars for AAPL/MSFT and they appear in ingestion logs
- `GetHistoricalBars` returns populated slice for a known date range
- `Close()` cancels all active subscriptions cleanly

---

## Phase 4: Config Wiring, infra.go, and Docker Integration

**Priority:** High  
**Estimated Effort:** Small  
**Depends on:** Phases 1–3 complete  
**This phase makes it end-to-end runnable**

### 4.1 — Update `infraDeps` and `infra.go`

**File:** `backend/cmd/omo-core/infra.go`

Change `infraDeps.alpacaAdapter *alpaca.Adapter` to port interfaces so both brokers can be stored uniformly:

```go
type infraDeps struct {
    eventBus      *memory.Bus
    broker        ports.BrokerPort        // was: alpacaAdapter *alpaca.Adapter
    marketData    ports.MarketDataPort
    orderStream   ports.OrderStreamPort
    accountPort   ports.AccountPort
    snapshotPort  ports.SnapshotPort      // only Alpaca satisfies this for now
    // ... rest unchanged
}
```

Conditional initialization:
```go
switch cfg.Broker {
case "ibkr":
    a, err := ibkr.NewAdapter(cfg.IBKR, log.With().Str("component", "ibkr").Logger())
    // handle err with retryWithBackoff
    infra.broker = a
    infra.marketData = a
    infra.orderStream = a
    infra.accountPort = a
    infra.snapshotPort = nil   // not supported; services must handle nil gracefully
case "alpaca":
    fallthrough
default:
    a, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger())
    // existing logic
    infra.broker = a
    infra.marketData = a
    // ... etc
}
```

### 4.2 — Update `services.go` injection sites

**File:** `backend/cmd/omo-core/services.go`

Replace all `infra.alpacaAdapter` references with the appropriate port interface field (`infra.broker`, `infra.marketData`, etc.). Services already accept port interfaces — this is a field name change only.

```bash
# Verify: no remaining references to alpacaAdapter in services.go after change
grep -n "alpacaAdapter" backend/cmd/omo-core/services.go
```

**Done criteria:**
- `BROKER=ibkr go run ./cmd/omo-core/` starts without panics
- `BROKER=alpaca go run ./cmd/omo-core/` continues to work identically (regression)
- `go build ./...` clean

### 4.3 — Docker Compose integration

Update `deployments/docker-compose.yml`:
- Add `ib-gateway` service (from Phase 1.1)
- Pass `BROKER`, `IBKR_GATEWAY_HOST`, `IBKR_GATEWAY_PORT` to `omo-core`
- `omo-core` service: no hard `depends_on: ib-gateway` (retry-with-backoff handles timing)

```yaml
  omo-core:
    environment:
      - TIMESCALEDB_HOST=timescaledb
      - TIMESCALEDB_PORT=5432
      - STRATEGY_V2=true
      - BROKER=${BROKER:-alpaca}
      - IBKR_GATEWAY_HOST=ib-gateway
      - IBKR_GATEWAY_PORT=4002
      - IBKR_CLIENT_ID=1
```

**Done criteria:**
- `BROKER=ibkr docker compose up` starts all services; `omo-core` logs show "IBKR adapter initialized"
- `BROKER=alpaca docker compose up` is unchanged

---

## Phase 5: Advanced Features (Deferred)

**Priority:** Low  
**Estimated Effort:** High  
**Depends on:** Phases 1–4 complete and stable

These ports are complex or have no clean IBKR equivalent. They are explicitly deferred.

### 5.1 — `SnapshotPort` (`GetSnapshots`)

Alpaca's snapshot API returns current price + OHLCV in a single REST call for a list of symbols. IBKR's equivalent is `reqMktData` with snapshot=true, but it requires a `Contract` per symbol and returns results asynchronously.

**Approach:** Implement as fan-out: for each symbol, fire `reqMktData(snapshot=true)`, collect results with timeout.

**Complexity:** Medium. May be needed by `AIScreenerService` — verify before deprioritizing.

### 5.2 — `OptionsMarketDataPort` (`GetOptionChain`) and `OptionsPricePort`

IBKR options use ConID-based queries. The chain request (`reqSecDefOptParams`) returns expiries + strikes; you then query greeks and IV per contract via `reqMktData`.

**Complexity:** High. Requires understanding IBKR's options data model (very different from Alpaca's REST endpoints). Recommend prototyping in a standalone script before integrating.

### 5.3 — `UniverseProviderPort` (`ListTradeable`)

IBKR has no "list all tradeable assets" endpoint. Options:
1. **Static list:** Maintain a curated YAML of tradeable symbols in `configs/`
2. **Scanner:** Use IBKR's market scanner (`reqScannerSubscription`) for dynamic lists
3. **Disable:** Return the current `cfg.Symbols` list directly (simplest)

**Recommendation:** Start with option 3 (return configured symbols). Universe scanning can be added later.

---

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|-----------|
| **Market data subscription required** — Paper accounts show error 10167 (frozen data) without a paid live subscription | High | High | Document clearly; warn in startup logs; test with delayed data first (set `useRTH=0`) |
| **Daily IB Gateway reset (~11:45 PM ET)** — TCP socket drops; all subscriptions lost | Certain | High | Reconnect loop in `connection.go` with exponential backoff; re-subscribe to market data on reconnect |
| **OrderID collision on restart** — Reusing an OrderID from a prior session causes broker rejection | Medium | High | Call `ReqIds()` on every connection and store next valid ID; never reset counter to 0 |
| **Docker startup race** — `omo-core` starts before IB Gateway is ready to accept connections | High | Medium | `retryWithBackoff` in `infra.go` (already exists for Alpaca); set generous timeout (2 min) |
| **`SnapshotPort` nil panic** — If AI Screener calls `GetSnapshots()` and `snapshotPort` is nil | Medium | Medium | Audit all `snapshotPort` call sites; add nil guard or return `ErrNotSupported` |
| **ibsync API surface gaps** — `ibsync` may not expose all needed callbacks | Low | Medium | Fall back to raw `ibapi` for specific calls; `ibsync` wraps `ibapi` so both can coexist |
| **Contract ambiguity** — IBKR may return multiple matches for a symbol ticker | Low | Low | Always specify Exchange="SMART"; use `reqContractDetails` to resolve ambiguity on startup |

---

## Testing Strategy

### Unit Tests (no IB Gateway required)
- `config_test.go` — IBKRConfig parsing, broker selection, validation skip
- `ibkr/adapter_test.go` — Interface compliance assertions (compile-time, zero runtime cost)
- `ibkr/order_stream_test.go` — Event mapping (IBKR status → OMO event vocabulary)
- `ibkr/market_data_test.go` — Bar aggregation logic (5s → 1m)

### Integration Tests (require IB Gateway + paper account)
Tag: `//go:build integration`

```bash
BROKER=ibkr IBKR_GATEWAY_HOST=localhost IBKR_GATEWAY_PORT=4002 \
  go test ./internal/adapters/ibkr/... -tags integration -v -timeout 60s
```

Test cases:
- `TestConnect` — connects and disconnects cleanly
- `TestPlaceAndCancelOrder` — places a limit order far off market, cancels it, verifies status
- `TestGetPositions` — returns current paper account positions (may be empty, must not error)
- `TestGetBuyingPower` — returns a non-zero float
- `TestStreamBars` — subscribes to AAPL bars, receives at least one bar within 30s
- `TestOrderStream` — places an order, receives `"new"` event on the order update channel

### Smoke Test (Docker Compose end-to-end)
```bash
BROKER=ibkr docker compose up -d
# Wait 90s for ib-gateway to start
sleep 90
# Check omo-core health
curl http://localhost:8080/health
# Verify IBKR adapter in logs
docker logs omo-core 2>&1 | grep "IBKR adapter initialized"
```

### Regression Test (Alpaca still works)
```bash
BROKER=alpaca docker compose up -d
# Run existing smoke test
go test ./internal/adapters/alpaca/... -run TestSmoke -tags smoke
```

---

## Open Questions

1. **ibsync trade event API** — Does `ibsync` expose a unified channel for all order updates (fills across all symbols), or must you subscribe per trade? Needs prototype to confirm.

2. **Bar aggregation ownership** — Should 5s→1m aggregation live in the IBKR adapter (matching Alpaca's behavior) or in the `ingestion.Service`? Current Alpaca implementation aggregates inside the WS client — follow the same pattern.

3. **`SnapshotPort` with IBKR** — The AI Screener calls `GetSnapshots()` at market open. Is this call path reachable when `BROKER=ibkr`? Check `services.go` wiring; may need to implement a basic snapshot via `reqMktData(snapshot=true)` earlier than Phase 5 to avoid a startup panic.

4. **ClientID collision** — If `omo-core` restarts while IB Gateway still has an active session for ClientID=1, will the new connection succeed or be rejected? Test this; may need to force-disconnect stale client or use `ClientID=0` (IB Gateway special: auto-assign).

5. **Paper account market data** — Confirm whether your IBKR paper account has access to real-time data. If not, delayed data (15-min) is still useful for testing order flow, but the `AIScreener` and ingestion pipeline will behave differently.

---

## Implementation Order Summary

```
Phase 1 (Days 1–3):   ib-gateway Docker + IBKRConfig + connection lifecycle
Phase 2 (Days 4–8):   BrokerPort + AccountPort + OrderStreamPort
Phase 3 (Days 9–12):  MarketDataPort (streaming + historical)
Phase 4 (Days 13–14): infra.go wiring + services.go injection + end-to-end smoke test
Phase 5 (Future):     Options, Snapshots, Universe
```

Each phase ends with a working, independently verifiable milestone. The system can paper-trade equities with IBKR after Phase 4 completes.
