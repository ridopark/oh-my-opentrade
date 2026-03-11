# Backtest-Live Parity Plan: omo-replay Overhaul

**Created:** 2026-03-10
**Status:** Draft
**Goal:** Bring `omo-replay` to full pipeline parity with `omo-core` so backtest results accurately reflect production behavior.

## Problem Statement

`omo-replay` is a **re-implementation** of the trading pipeline rather than a **re-configuration** of the same one. This has caused significant drift — the backtest produces optimistic results because it's missing execution guards, position monitoring, multi-strategy support, and AI signal enrichment that filter/reject trades in production.

## Architecture Principle

**One pipeline, swappable adapters.** Extract shared service initialization into reusable functions. Both binaries call the same wiring code — only the broker adapter and data feed differ.

Key references:
- QuantConnect/LEAN: `AlgorithmManager` runs both live and backtest by swapping `ISynchronizer` + `IBrokerage`
- ninjabot (Go): Uses `PaperWallet` (SimBroker) with shared `Exchange` interface
- NautilusTrader: Full event-driven engine with mode-agnostic pipeline

---

## Phase 1: Shared Pipeline Foundation

**Priority:** Critical | **Estimated Effort:** Large
**Why first:** This is the structural change that makes all subsequent phases straightforward. Without it, every future feature requires dual-maintenance.

### 1.1 — Extract Shared Service Wiring

Create `internal/app/pipeline/` package with shared initialization functions.

**Files to create:**
- `internal/app/pipeline/pipeline.go` — Core pipeline struct and builder
- `internal/app/pipeline/options.go` — Functional options for mode-specific configuration

**Design:**
```go
// internal/app/pipeline/pipeline.go
package pipeline

type Mode int
const (
    ModeLive Mode = iota
    ModeBacktest
)

// Deps holds injected adapters — the only things that differ between live and backtest.
type Deps struct {
    Broker        ports.BrokerPort
    QuoteProvider ports.QuoteProviderPort
    EventBus      ports.EventBusPort
    Repo          ports.RepositoryPort
    PnLRepo       ports.PnLPort
    SpecStore     ports.StrategySpecStore
    Clock         ports.Clock
    Logger        zerolog.Logger
}

// Config holds all pipeline configuration (from config.yaml).
type Config struct {
    Trading       config.TradingConfig
    AI            config.AIConfig
    Symbols       config.SymbolsConfig
    InitialEquity float64
    TenantID      string
    EnvMode       domain.EnvMode
}

// Pipeline is the fully-wired trading pipeline.
type Pipeline struct {
    Ingestion      *ingestion.Service
    Monitor        *monitor.Service
    Execution      *execution.Service
    StrategyRunner *strategy.Runner
    RiskSizer      *strategy.RiskSizer
    Enricher       *strategy.SignalDebateEnricher  // nil when AI disabled
    PosMonitor     *positionmonitor.Service
    PriceCache     *positionmonitor.PriceCache
    LedgerWriter   *perf.LedgerWriter
    SignalTracker  *perf.SignalTracker
    Router         *strategy.Router
}

// New builds the complete trading pipeline from injected deps.
func New(mode Mode, cfg Config, deps Deps, opts ...Option) (*Pipeline, error) { ... }

// Start starts all pipeline services in correct order.
func (p *Pipeline) Start(ctx context.Context) error { ... }
```

**Extraction from omo-core:** Refactor `initCoreServices()` and `initStrategyPipeline()` from `services.go` into `pipeline.New()`. The existing `services.go` becomes a thin wrapper:

```go
// cmd/omo-core/services.go — AFTER refactoring
func initCoreServices(cfg *config.Config, infra *infraDeps, log zerolog.Logger) *appServices {
    pipe, err := pipeline.New(pipeline.ModeLive, pipeline.Config{
        Trading:       cfg.Trading,
        AI:            cfg.AI,
        Symbols:       cfg.Symbols,
        InitialEquity: accountEquity,
        TenantID:      "default",
        EnvMode:       domain.EnvModePaper,
    }, pipeline.Deps{
        Broker:        infra.alpacaAdapter,
        QuoteProvider: infra.alpacaAdapter,
        EventBus:      infra.eventBus,
        Repo:          infra.repo,
        PnLRepo:       infra.pnlRepo,
        SpecStore:     specStore,
        Clock:         &wallClock{},
        Logger:        log,
    })
    // ... wire into appServices struct
}
```

**omo-replay becomes:**
```go
// cmd/omo-replay/main.go — AFTER refactoring
pipe, err := pipeline.New(pipeline.ModeBacktest, pipeline.Config{
    Trading:       cfg.Trading,
    AI:            cfg.AI,
    Symbols:       cfg.Symbols,
    InitialEquity: initialEquity,
    TenantID:      "default",
    EnvMode:       domain.EnvModePaper,
}, pipeline.Deps{
    Broker:        simBrokerInst,
    QuoteProvider: &simQuoteProvider{broker: simBrokerInst},
    EventBus:      eventBus,
    Repo:          repo,           // Real DB for reading bars, noop for writes
    PnLRepo:       &noopPnLRepo{},
    SpecStore:     specStore,
    Clock:         replayClock,
    Logger:        log,
}, pipeline.WithNoAI())  // optional: skip enricher for speed
```

### 1.2 — Clock Abstraction

Create `ports.Clock` interface so time-dependent logic works correctly in both modes.

**File:** `internal/ports/clock.go`

```go
type Clock interface {
    Now() time.Time
}
```

**Implementations:**
- `adapters/clock/wall.go` — Returns `time.Now()` (for live)
- `adapters/clock/replay.go` — Returns the timestamp of the current bar being replayed

**Consumers that need `Clock` injection (currently use `time.Now`):**
- `execution.NewKillSwitch()` — already takes `nowFn func() time.Time` (easy)
- `risk.NewDailyLossBreaker()` — already takes `nowFn` (easy)
- `execution.NewTradingWindowGuard()` — needs `Clock` param added
- `positionmonitor.Service` — exit cooldown timers
- `positionmonitor.Revaluator` — periodic interval

### 1.3 — Noop Adapters Consolidation

Move noop adapters from `cmd/omo-replay/main.go` into `internal/adapters/noop/` for reuse:

- `noop.Repository` — implements `ports.RepositoryPort` (write operations are no-ops, reads delegate to real repo)
- `noop.PnLRepo` — implements `ports.PnLPort`

**Split read/write concern:** The backtest needs to READ bars from the real DB but SKIP writes. Create a `ReadOnlyRepository` wrapper that delegates reads and no-ops writes, rather than a full noop.

### Verification Criteria (Phase 1)
- [ ] `go build ./cmd/omo-core/` succeeds — live binary unchanged behavior
- [ ] `go build ./cmd/omo-replay/` succeeds — uses shared pipeline
- [ ] `go test ./internal/app/pipeline/...` — unit tests for pipeline builder
- [ ] Run existing backtest command — same output as before (no regression)

---

## Phase 2: Multi-Strategy + Full Execution Guards

**Priority:** Critical | **Estimated Effort:** Medium
**Why second:** Depends on Phase 1's shared pipeline. Highest impact on backtest accuracy.

### 2.1 — Multi-Strategy Support in omo-replay

Since Phase 1 extracts shared wiring, this comes nearly for free:

- Pipeline builder loads ALL specs from `specStore.List()`
- Registers all builtins: ORB, AVWAP, AIScalper
- Uses hook-based routing (`spec.Hooks["signals"]` → impl lookup)
- Creates per-symbol instances with `AllowedDirections` from spec
- Wires `SymbolRouter` with `WatchlistMode` from specs

**CLI changes:**
- `--strategy` flag filters which strategies to include (default: all)
- `--symbols` flag overrides symbols (existing behavior preserved)

### 2.2 — Execution Guards via SimBroker Ports

The guards need to query the broker for positions, buying power, and quotes. SimBroker must implement these ports:

**SimBroker interface expansion** (`internal/adapters/simbroker/`):

```go
// SimBroker already implements ports.BrokerPort.
// Add these for guard compatibility:

func (b *Broker) GetPositions(ctx context.Context) ([]domain.Position, error)
func (b *Broker) GetAccountEquity(ctx context.Context) (float64, error)
func (b *Broker) GetBuyingPower(ctx context.Context) (float64, error)
func (b *Broker) GetQuote(ctx context.Context, symbol domain.Symbol) (bid, ask float64, err error)
```

**Guards to wire into backtest pipeline:**

| Guard | What it queries | SimBroker method needed |
|-------|----------------|----------------------|
| `ExposureGuard` | Positions + equity | `GetPositions()` + `GetAccountEquity()` |
| `SpreadGuard` | Bid/ask quote | `GetQuote()` — returns last bar close as both bid/ask (zero spread in backtest) or configurable spread |
| `TradingWindowGuard` | Current time | `Clock.Now()` — checks if within RTH |
| `BuyingPowerGuard` | Buying power | `GetBuyingPower()` — tracks virtual balance |
| `PositionGate` | Existing positions | `GetPositions()` — already partially implemented |

**SimBroker virtual portfolio tracking:**
```go
type virtualPortfolio struct {
    mu            sync.RWMutex
    initialEquity float64
    cash          float64
    positions     map[domain.Symbol]*position
    realizedPnL   float64
}
```

### 2.3 — Config-Driven Risk Params

Replace all hardcoded values in omo-replay:
- `NewRiskEngine(0.02)` → `NewRiskEngine(cfg.Trading.MaxRiskPercent)`
- `NewKillSwitch(3, 30*time.Minute, time.Hour, ...)` → Use `cfg.Trading.KillSwitchMaxStops`, `cfg.Trading.KillSwitchWindow`, `cfg.Trading.KillSwitchHaltDuration`
- Initial equity already configurable via `--initial-equity` flag (keep)

### Verification Criteria (Phase 2)
- [ ] Backtest runs with multiple strategies (ORB + AVWAP on same symbols)
- [ ] ExposureGuard rejects trades when exposure exceeds limit
- [ ] TradingWindowGuard rejects trades outside RTH (using replay clock)
- [ ] PositionGate prevents duplicate entries
- [ ] Per-strategy P&L attribution in backtest results

---

## Phase 3: Position Monitor + Exit Rules

**Priority:** Critical | **Estimated Effort:** Medium
**Why third:** Exit rule evaluation has the biggest impact on P&L accuracy after entry guards.

### 3.1 — Wire PositionMonitor into Backtest

The `positionmonitor.Service` subscribes to events and evaluates exit rules. In backtest mode:

- `PriceCache` subscribes to `EventMarketBarReceived` — same as live
- `PositionMonitor` evaluates exit rules on each bar — same as live
- Exit orders go to SimBroker — fills on next bar

**What changes:**
- `positionmonitor.NewService()` needs `Clock` injection for cooldown timers
- SimBroker must track positions that `PositionMonitor` can query
- `PositionMonitor.SetSpecStore(specStore)` — reads exit rules from strategy specs

**Wire `SetPositionLookup` on runner:**
```go
pipe.StrategyRunner.SetPositionLookup(pipe.PosMonitor.LookupPosition)
```

### 3.2 — Revaluator in Backtest (Optional)

The AI-driven `Revaluator` is expensive. Two options:
- **Default: disabled** in backtest (no LLM calls during large backtests)
- **Flag: `--with-revaluation`** enables it for short, targeted backtests

When disabled, positions still exit via rule-based evaluation (trailing stops, stagnation, etc.).

### Verification Criteria (Phase 3)
- [ ] Trailing stop exits fire during backtest
- [ ] Stagnation exit triggers after configured duration (replay clock)
- [ ] Profit-gated exits trigger correctly
- [ ] No duplicate entry signals when position already open
- [ ] Backtest P&L changes meaningfully vs Phase 2 (exits are working)

---

## Phase 4: Signal Enrichment + Warmup

**Priority:** High | **Estimated Effort:** Small-Medium

### 4.1 — SignalDebateEnricher (Optional AI)

Wire `strategy.NewSignalDebateEnricher()` into backtest pipeline:

```go
// In pipeline builder
if mode != ModeBacktest || !opts.NoAI {
    enricher = strategy.NewSignalDebateEnricher(deps.EventBus, aiAdvisor, stratLog,
        strategy.WithRepository(deps.Repo),
        strategy.WithMarketDataProvider(monitor.GetLastSnapshot),
        strategy.WithPositionLookup(posMonitor.LookupPosition),
        strategy.WithDebateTimeout(30*time.Second),
    )
}
```

**CLI flags:**
- `--no-ai` (default for backtest) — skips enricher, direct runner → riskSizer
- `--with-ai` — enables enricher for realistic signal filtering

**NoOp AI advisor for fast backtests:**
- When `--no-ai`, use `llm.NewNoOpAdvisor()` which approves all signals
- When `--with-ai`, use real LLM advisor (requires API keys, slow)

### 4.2 — Strategy Runner Warmup

Port the warmup logic from `cmd/omo-core/warmup.go`:

```go
// Warm up strategy runner with indicator snapshots
warmupCalc := monitor.NewIndicatorCalculator()
snapshotFn := func(bar domain.MarketBar) start.IndicatorData {
    snap := warmupCalc.Update(bar)
    return start.IndicatorData{
        RSI: snap.RSI, StochK: snap.StochK, StochD: snap.StochD,
        EMA9: snap.EMA9, EMA21: snap.EMA21, VWAP: snap.VWAP,
        Volume: snap.Volume, VolumeSMA: snap.VolumeSMA,
    }
}
for _, sym := range symbols {
    bars := warmupBars[sym]
    pipe.StrategyRunner.WarmUp(sym, bars, snapshotFn)
}
```

**Warmup window for backtest:** Use bars from `--from` minus 120 bars (same logic as omo-core's previous RTH session warmup).

### 4.3 — SignalTracker

Wire `perf.NewSignalTracker()` to capture signal quality metrics during backtest. Include in final report output alongside the existing backtest collector results.

### Verification Criteria (Phase 4)
- [ ] `--with-ai` flag runs enricher, `--no-ai` skips it
- [ ] Strategy runner warmup produces indicator state before first trade signal
- [ ] Signal tracker metrics appear in backtest report
- [ ] Cold start vs warm start produces different signal counts (warmup working)

---

## Phase 5: SimBroker Enhancements (Realism)

**Priority:** Medium | **Estimated Effort:** Medium
**Why last:** Improves accuracy but not correctness. The pipeline is already at parity from Phases 1-4.

### 5.1 — Next-Bar Fill Model

Current: SimBroker fills at current bar's close price.
Better: Fill at next bar's **open** + slippage.

```go
type FillModel interface {
    Fill(order Order, currentBar, nextBar MarketBar) (price float64, qty float64, err error)
}

type ImmediateFillModel struct{ SlippageBPS int64 }  // current behavior
type NextBarFillModel struct{ SlippageBPS int64 }     // realistic
```

**CLI flag:** `--fill-model=immediate|next-bar` (default: `immediate` for backward compat)

### 5.2 — Volume-Aware Partial Fills

If `order.Quantity > bar.Volume * participationRate`, fill only what's available:

```go
maxFillQty := bar.Volume * participationRate  // e.g., 2% of bar volume
actualFillQty := math.Min(order.Quantity, maxFillQty)
```

**CLI flag:** `--participation-rate=0.02` (default: 1.0 = no volume limit)

### 5.3 — Configurable Simulated Spread

Instead of zero spread (`bid == ask == close`), simulate realistic spread:

```go
func (b *Broker) GetQuote(ctx context.Context, sym domain.Symbol) (float64, float64, error) {
    price, ok := b.GetPrice(sym)
    if !ok { return 0, 0, err }
    halfSpread := price * b.spreadBPS / 10000.0 / 2.0
    return price - halfSpread, price + halfSpread, nil
}
```

**CLI flag:** `--spread-bps=2` (default: 0 = zero spread)

### Verification Criteria (Phase 5)
- [ ] Next-bar fill model produces different (more conservative) results than immediate
- [ ] Volume-limited fills produce partial fills on low-volume bars
- [ ] Spread simulation causes SpreadGuard to reject trades on configured threshold

---

## Phase 6: Testing + Report Enhancements

**Priority:** Medium | **Estimated Effort:** Small

### 6.1 — Integration Test

Create `backend/internal/app/pipeline/pipeline_integration_test.go`:

```go
func TestBacktestPipeline_ProducesExpectedEvents(t *testing.T) {
    // 1. Seed TimescaleDB with known bar data (or use in-memory bar store)
    // 2. Build pipeline in ModeBacktest with SimBroker
    // 3. Replay bars through pipeline
    // 4. Assert: signals generated, guards triggered, fills executed
    // 5. Assert: position monitor exits fired
    // 6. Assert: per-strategy P&L attribution correct
}
```

### 6.2 — Enhanced Backtest Report

Extend `backtest.Collector` result to include:
- Per-strategy breakdown (trades, P&L, win rate, Sharpe)
- Guard rejection counts (how many trades each guard blocked)
- Signal tracker metrics (signal → fill conversion rate)
- Comparison mode: run with/without guards and diff results

### 6.3 — Regression Test

Compare backtest results before and after the overhaul to document the impact:
- Run old omo-replay on known date range, save results
- Run new omo-replay on same range, save results
- Document differences (should show fewer trades, more realistic P&L)

---

## Implementation Order Summary

```
Phase 1: Shared Pipeline Foundation        [CRITICAL - structural]
  1.1 Extract shared service wiring        → internal/app/pipeline/
  1.2 Clock abstraction                    → ports.Clock + adapters
  1.3 Noop adapters consolidation          → internal/adapters/noop/

Phase 2: Multi-Strategy + Execution Guards [CRITICAL - accuracy]
  2.1 Multi-strategy support               → all specs + hook routing
  2.2 Execution guards via SimBroker ports  → SimBroker expansion
  2.3 Config-driven risk params            → replace hardcoded values

Phase 3: Position Monitor + Exit Rules     [CRITICAL - P&L accuracy]
  3.1 Wire PositionMonitor                 → exit rules in backtest
  3.2 Revaluator (optional)                → --with-revaluation flag

Phase 4: Signal Enrichment + Warmup        [HIGH - signal accuracy]
  4.1 SignalDebateEnricher (optional AI)    → --no-ai / --with-ai
  4.2 Strategy runner warmup               → indicator state
  4.3 SignalTracker                         → quality metrics

Phase 5: SimBroker Enhancements            [MEDIUM - realism]
  5.1 Next-bar fill model                  → --fill-model flag
  5.2 Volume-aware partial fills           → --participation-rate
  5.3 Configurable spread                  → --spread-bps

Phase 6: Testing + Report                  [MEDIUM - quality]
  6.1 Integration test                     → pipeline_integration_test.go
  6.2 Enhanced backtest report             → per-strategy breakdown
  6.3 Regression comparison                → before/after documentation
```

## Risk Mitigation

1. **omo-core regression:** Every phase includes "build + test omo-core" as verification. Shared code extraction must not change live behavior.
2. **Performance:** Large backtests with AI enrichment will be slow. Default to `--no-ai` for speed, `--with-ai` for accuracy.
3. **SimBroker complexity:** Keep virtual portfolio simple. Don't simulate margin, options, or complex order types in v1.
4. **Incremental delivery:** Each phase is independently valuable. Phase 1 alone eliminates future drift. Phase 2 fixes the biggest accuracy gap.

## Files Changed (Estimated)

| Phase | New Files | Modified Files |
|-------|-----------|----------------|
| 1 | `internal/app/pipeline/{pipeline,options}.go`, `ports/clock.go`, `adapters/clock/{wall,replay}.go`, `adapters/noop/{repo,pnl}.go` | `cmd/omo-core/services.go`, `cmd/omo-replay/main.go` |
| 2 | — | `internal/adapters/simbroker/broker.go`, `cmd/omo-replay/main.go` |
| 3 | — | `internal/app/positionmonitor/service.go`, `cmd/omo-replay/main.go` |
| 4 | — | `cmd/omo-replay/main.go` |
| 5 | `internal/adapters/simbroker/fillmodel.go` | `internal/adapters/simbroker/broker.go` |
| 6 | `internal/app/pipeline/pipeline_integration_test.go` | `internal/app/backtest/collector.go` |
