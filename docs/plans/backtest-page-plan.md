# Backtest Page Plan: Live Replay UI

**Created:** 2026-03-16
**Status:** Draft
**Goal:** Expose `omo-replay --backtest` as a web feature in the dashboard — users configure, launch, and watch a backtest replay with candles building in real time, entry/exit markers appearing as the strategy fires, and live-updating metrics.

**Depends on:** [backtest-parity-plan.md](./backtest-parity-plan.md) (shared pipeline extraction)

---

## Problem Statement

`omo-replay` is a powerful CLI tool that replays historical candles through the full trading pipeline with SimBroker execution. But it's **CLI-only** — no web access, no visual replay, no interactive speed control. Users must SSH in, run the binary, and parse terminal output to evaluate strategies.

The dashboard already has all the charting primitives (lightweight-charts v5 candlestick chart with signal markers, SSE event streaming, real-time bar updates). We need to wire these together behind a backtest-specific flow.

---

## Existing Infrastructure Audit

### Already Built — Reuse Directly

| Component | File | What It Does |
|---|---|---|
| SimBroker | `adapters/simbroker/broker.go` | Fills at close ± slippage BPS, position/cash tracking |
| Backtest Collector | `app/backtest/collector.go` | FIFO trade matching, equity curve, Sharpe/drawdown/profit factor, JSON export |
| SSE Handler | `adapters/sse/handler.go` | Event bus → browser SSE fan-out with keepalive, CORS, slow-client protection |
| Bootstrap Builders | `app/bootstrap/` | `BuildIngestion`, `BuildExecutionService`, `BuildStrategyPipeline`, `BuildPositionMonitor` — shared between omo-core and omo-replay |
| SSE Proxy (FE) | `app/api/events/route.ts` | Next.js → backend SSE proxy |
| `useEventStream` | `lib/event-stream.ts` | EventSource hook with typed listeners, reconnection |
| `TradingSignalChart` | `components/trading-signal-chart.tsx` | 847-line chart: candles, volume, EMA, Bollinger, RSI, crosshair, infinite scroll |
| `SignalMarkerOverlay` | `lib/signal-markers.ts` | Custom ISeriesPrimitive — 4 marker types (Long/Short entry/exit) with arrows + labels |
| `useChartData` | `lib/use-chart-data.ts` | Multi-TF bar fetching, live SSE merge, loadMore pagination |

### Needs Adaptation

| Component | Change Required |
|---|---|
| `TradingSignalChart` | Add **progressive mode** — `update()` per bar instead of `setData()` bulk. Support "replay" prop that disables live SSE merge and instead accepts bars one-at-a-time. |
| `useEventStream` | Create backtest-specific variant that connects to `/api/backtest/events/{id}` instead of `/api/events`. |
| SSE Handler (BE) | Create a per-backtest handler instance with its own isolated event bus. |
| omo-replay main.go | Extract the setup + replay loop into a reusable `backtest.Runner` struct (aligns with backtest-parity-plan Phase 1). |

### Needs to Be Built

| Component | Description |
|---|---|
| `backtest.Runner` | Reusable Go struct wrapping omo-replay's setup + replay loop with SSE event emission |
| HTTP endpoints | `POST /backtest/run`, `GET /backtest/events/{id}`, `POST /backtest/control/{id}`, `GET /backtest/results/{id}` |
| Backtest page | `/backtest` — config form, replay chart, playback controls, trade log, metrics sidebar |
| Equity curve chart | Secondary lightweight-charts line chart tracking equity over time |
| Playback controls | Speed selector (1x/2x/5x/10x/max), pause/resume, progress bar |

---

## Architecture

### Data Flow

```
┌──────────────────────────────────────────────────────────────────┐
│  Dashboard  /backtest                                            │
│                                                                  │
│  ┌─────────────┐  ┌──────────────────┐  ┌──────────────────────┐│
│  │ Config Form │  │  Replay Chart    │  │  Metrics Sidebar     ││
│  │             │  │  (lwc v5)        │  │  Equity  | Sharpe    ││
│  │ symbols     │  │  candles build   │  │  P&L     | Drawdown  ││
│  │ date range  │  │  progressively   │  │  Trades  | Win Rate  ││
│  │ strategy    │  │  ▼ ▼ ▼ ▼ ▼      │  │  Profit Factor       ││
│  │ speed       │  │  markers appear  │  │                      ││
│  │ equity      │  │  on signals      │  │  ┌────────────────┐  ││
│  │             │  │                  │  │  │ Equity Curve   │  ││
│  │ [▶ Run]     │  │  ┌────────────┐  │  │  │ (line chart)   │  ││
│  │             │  │  │ Playback   │  │  │  └────────────────┘  ││
│  │             │  │  │⏸ ▶ 1x 5x 10x│ │  │                      ││
│  └──────┬──────┘  │  └────────────┘  │  │  ┌────────────────┐  ││
│         │         └────────▲─────────┘  │  │ Trade Log      │  ││
│         │                  │            │  │ (scrollable)   │  ││
│         │ POST /run        │ SSE        │  └────────▲───────┘  ││
└─────────┼──────────────────┼────────────┼───────────┼──────────┘│
          │                  │            │           │
┌─────────▼──────────────────┼────────────┼───────────┼───────────┐
│  omo-core  HTTP server     │            │           │           │
│                            │            │           │           │
│  POST /backtest/run ───────┼────────────┼───────────┤           │
│    → spawns Runner goroutine            │           │           │
│    → returns { backtest_id }            │           │           │
│                            │            │           │           │
│  GET /backtest/events/{id} ─────────────┴───────────┘           │
│    → SSE stream from isolated event bus                         │
│    → event types: candle, signal, trade, metrics, complete      │
│                                                                 │
│  POST /backtest/control/{id}                                    │
│    → { action: "pause"|"resume"|"set_speed", speed: 5 }        │
│                                                                 │
│  GET /backtest/results/{id}                                     │
│    → final Result JSON (after completion)                       │
│                                                                 │
│  ┌──────────────────────────────────────────────┐               │
│  │  backtest.Runner (one per active backtest)   │               │
│  │                                              │               │
│  │  memory.EventBus (isolated, NOT live bus)    │               │
│  │  ├── Ingestion                               │               │
│  │  ├── Monitor (indicators, regime, MTFA)      │               │
│  │  ├── Strategy Runner                         │               │
│  │  ├── RiskSizer → OrderIntent                 │               │
│  │  ├── Execution → SimBroker                   │               │
│  │  ├── Position Monitor                        │               │
│  │  ├── Backtest Collector                      │               │
│  │  └── SSE Emitter (→ browser)                 │               │
│  │                                              │               │
│  │  TimescaleDB ──► GetMarketBars() ──► replay  │               │
│  └──────────────────────────────────────────────┘               │
└─────────────────────────────────────────────────────────────────┘
```

### Key Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Where backtest runs | In-process goroutine in omo-core | Reuses SSE infrastructure, shared DB pool, single deployment. No subprocess management. |
| Event isolation | Separate `memory.NewBus()` per backtest | Backtest events MUST NOT leak into the live trading event bus. |
| Streaming protocol | SSE (server → client) + REST (client → server) | SSE for unidirectional data stream (candles, signals, trades). REST POST for control commands (pause/resume/speed). Simpler than WebSocket for this pattern. |
| Chart update strategy | `candleSeries.update()` per bar | Progressive append, not bulk `setData()`. Allows smooth real-time animation. |
| Speed control | Atomic `time.Duration` in replay loop | omo-replay already has `perBarDelay`. Make it dynamically adjustable via `atomic.Value`. |
| Concurrent backtests | Max 1 active at a time (MVP) | Simplifies resource management. Can lift later. |
| Result persistence | In-memory during run, optional DB save | Collector already produces `Result` struct. Add optional `SaveResult()` to write to a `backtest_runs` table for history. |

---

## SSE Event Protocol

The backtest SSE stream emits typed events that mirror the domain event bus:

```
event: backtest:candle
data: {"time":1710000060,"symbol":"AAPL","open":171.50,"high":172.10,"low":171.30,"close":171.90,"volume":12500,"timeframe":"1m"}

event: backtest:signal
data: {"time":1710000060,"symbol":"AAPL","side":"buy","kind":"entry","strategy":"avwap_v1","strength":0.82,"confidence":0.75}

event: backtest:trade
data: {"time":1710000120,"symbol":"AAPL","side":"buy","qty":50,"price":171.95,"strategy":"avwap_v1"}

event: backtest:trade_closed
data: {"time":1710003600,"symbol":"AAPL","side":"sell","qty":50,"price":173.20,"pnl":62.50,"strategy":"avwap_v1"}

event: backtest:metrics
data: {"equity":100062.50,"cash":98402.50,"drawdown_pct":0,"open_positions":1,"closed_trades":0,"win_rate":0,"sharpe":0,"profit_factor":0}

event: backtest:progress
data: {"bars_processed":120,"total_bars":390,"pct":30.8,"current_time":"2026-03-10T10:30:00Z","replay_speed":"5x"}

event: backtest:complete
data: {"total_trades":8,"final_equity":101240.50,"total_return_pct":1.24,"sharpe":1.67,"max_drawdown_pct":0.85,"win_rate_pct":62.5,"profit_factor":2.1}
```

**Prefix rationale:** `backtest:` prefix distinguishes from live events, prevents collision if both streams are open.

---

## Implementation Phases

### Phase 1: Backend — Backtest Runner + HTTP Endpoints

**Effort:** ~2 days | **Priority:** Critical (gates everything else)

#### 1.1 — Extract `backtest.Runner` from omo-replay

Create `backend/internal/app/backtest/runner.go`:

```go
package backtest

type RunConfig struct {
    Symbols       []domain.Symbol
    From          time.Time
    To            time.Time
    Timeframe     domain.Timeframe
    InitialEquity float64
    SlippageBPS   int64
    Speed         string // "max", "1x", "5x", etc.
    NoAI          bool
    StrategyDir   string
}

type Runner struct {
    cfg       RunConfig
    db        *sql.DB
    appCfg    *config.Config
    log       zerolog.Logger
    eventBus  *memory.Bus
    collector *Collector
    
    // Playback control
    speed     atomic.Value // time.Duration — per-bar delay
    paused    atomic.Bool
    pauseCh   chan struct{}
    
    // State
    status      atomic.Value // "running", "paused", "completed", "cancelled"
    progress    atomic.Value // ProgressInfo
    result      atomic.Value // *Result (set on completion)
    cancelFn    context.CancelFunc
}

type ProgressInfo struct {
    BarsProcessed int    `json:"bars_processed"`
    TotalBars     int    `json:"total_bars"`
    Pct           float64 `json:"pct"`
    CurrentTime   string  `json:"current_time"`
    Speed         string  `json:"replay_speed"`
}
```

**Extraction from omo-replay/main.go:**

The core logic to extract (lines 167-668 of main.go):
1. Bootstrap pipeline setup (ingestion, monitor, execution, strategy, position monitor)
2. Warmup phase (indicator seeding, spike filter, MTFA aggregators)
3. Bar loading from TimescaleDB
4. Time-synchronized replay loop with speed control
5. Per-bar event publishing + WaitPending synchronization

The `Runner.Run(ctx)` method does all of this, but emits SSE events along the way.

#### 1.2 — SSE Emitter Subscriber

Subscribe to the isolated event bus and emit SSE-formatted events:

```go
// Subscribe to relevant events on the backtest's isolated bus
func (r *Runner) subscribeSSEEmitter(ctx context.Context) {
    // Candle events → backtest:candle
    r.eventBus.Subscribe(ctx, domain.EventMarketBarSanitized, func(_ context.Context, ev domain.Event) error {
        bar := ev.Payload.(domain.MarketBar)
        r.emitSSE("backtest:candle", bar)
        return nil
    })
    
    // Signal events → backtest:signal
    r.eventBus.Subscribe(ctx, domain.EventSignalCreated, func(_ context.Context, ev domain.Event) error {
        sig := ev.Payload.(strategy.Signal)
        r.emitSSE("backtest:signal", mapSignalToWire(sig))
        return nil
    })
    
    // Fill events → backtest:trade
    r.eventBus.Subscribe(ctx, domain.EventFillReceived, func(_ context.Context, ev domain.Event) error {
        fill := ev.Payload.(map[string]any)
        r.emitSSE("backtest:trade", fill)
        return nil
    })
    
    // Periodic metrics from collector → backtest:metrics
    r.eventBus.Subscribe(ctx, domain.EventMarketBarReceived, func(_ context.Context, ev domain.Event) error {
        // Emit metrics every N bars (not every bar — too noisy)
        if r.barsProcessed % 10 == 0 {
            r.emitSSE("backtest:metrics", r.currentMetrics())
        }
        return nil
    })
}
```

#### 1.3 — HTTP Endpoints

Add to `backend/internal/adapters/http/handler.go` (or new `backtest_handler.go`):

```go
// POST /backtest/run
// Body: { "symbols": ["AAPL","SPY"], "from": "2026-03-01", "to": "2026-03-10",
//         "initial_equity": 100000, "slippage_bps": 5, "speed": "5x" }
// Response: { "backtest_id": "bt-abc123" }

// GET /backtest/events/{id}
// → SSE stream (text/event-stream)

// POST /backtest/control/{id}
// Body: { "action": "pause" | "resume" | "set_speed", "speed": 10 }
// Response: { "status": "paused" | "running", "speed": "10x" }

// GET /backtest/results/{id}
// Response: backtest.Result JSON (available after completion)

// DELETE /backtest/{id}
// → Cancel running backtest
```

#### 1.4 — Playback Control

Modify the replay loop to check pause/speed atomically:

```go
// In the replay loop (extracted from omo-replay main.go line 659-668):
if r.paused.Load() {
    <-r.pauseCh // Block until resumed
}

delay := r.speed.Load().(time.Duration)
if delay > 0 {
    t := time.NewTimer(delay)
    select {
    case <-ctx.Done():
        t.Stop()
        return
    case <-t.C:
    }
}
```

Control endpoint updates:
```go
func (r *Runner) Pause()              { r.paused.Store(true); r.status.Store("paused") }
func (r *Runner) Resume()             { r.paused.Store(false); close(r.pauseCh); r.pauseCh = make(chan struct{}); r.status.Store("running") }
func (r *Runner) SetSpeed(factor string) { /* parse + update atomic perBarDelay */ }
```

---

### Phase 2: Frontend — Backtest Page Shell + Configuration

**Effort:** ~1.5 days | **Priority:** Critical

#### 2.1 — Create `/backtest` Route

`apps/dashboard/app/backtest/page.tsx` — Main backtest page with three-column layout:

```
┌──────────────────────────────────────────────────────────┐
│  Backtest                                    [Run ▶]     │
├────────────┬─────────────────────────┬───────────────────┤
│            │                         │                   │
│  Config    │   Replay Chart          │   Metrics         │
│  Panel     │   (candlestick)         │   Equity: $100K   │
│            │                         │   P&L: +$1,240    │
│  Symbols   │   ┌─────────────────┐   │   Return: +1.24%  │
│  [AAPL ×]  │   │                 │   │   Sharpe: 1.67    │
│  [SPY  ×]  │   │   Candles grow  │   │   Drawdown: 0.85% │
│            │   │   ← over time   │   │   Win Rate: 62.5% │
│  From:     │   │                 │   │   Trades: 8       │
│  To:       │   │   ▲ BUY markers │   │                   │
│  Speed: 5x │   │   ▼ SELL        │   │   ┌─────────────┐ │
│  Equity:   │   │                 │   │   │Equity Curve │ │
│  $100,000  │   └─────────────────┘   │   └─────────────┘ │
│            │   [⏸] [▶] [1x 5x 10x]  │                   │
│  Strategy: │                         │   ┌─────────────┐ │
│  [all  ▼]  │   Progress: ████░ 73%   │   │ Trade Log   │ │
│            │                         │   │ BUY AAPL @  │ │
│  Slippage: │                         │   │ SELL AAPL @ │ │
│  5 bps     │                         │   │ ...         │ │
└────────────┴─────────────────────────┴───────────────────┘
```

#### 2.2 — Configuration Form Component

`apps/dashboard/components/backtest/config-panel.tsx`:

```typescript
interface BacktestConfig {
  symbols: string[];
  from: string;          // YYYY-MM-DD
  to: string;            // YYYY-MM-DD
  initialEquity: number; // default 100000
  slippageBps: number;   // default 5
  speed: string;         // "1x" | "2x" | "5x" | "10x" | "max"
  noAi: boolean;         // default true
}
```

Form uses existing dashboard styling (shadcn/ui components if present, or Tailwind).

#### 2.3 — API Client Hook

`apps/dashboard/lib/use-backtest.ts`:

```typescript
interface UseBacktestReturn {
  // State
  status: "idle" | "running" | "paused" | "completed" | "error";
  backtestId: string | null;
  progress: ProgressInfo | null;
  
  // Accumulated data (grows during replay)
  bars: Map<string, OHLCBar[]>;      // symbol → bars received so far
  signals: ChartSignal[];            // all signals fired
  trades: TradeRecord[];             // all fills
  metrics: MetricsSnapshot | null;   // latest metrics
  result: BacktestResult | null;     // final result (on complete)
  equityCurve: { time: number; equity: number }[];
  
  // Actions
  run: (config: BacktestConfig) => Promise<void>;
  pause: () => Promise<void>;
  resume: () => Promise<void>;
  setSpeed: (speed: string) => Promise<void>;
  cancel: () => Promise<void>;
}
```

This hook:
1. `run()` → POST /api/backtest/run → stores backtestId → opens SSE connection
2. SSE listener accumulates bars, signals, trades, metrics into state
3. Batches rapid updates with `requestAnimationFrame` to prevent React re-render storm
4. On `backtest:complete` event → fetches final result from GET /api/backtest/results/{id}
5. Cleanup on unmount → DELETE /api/backtest/{id} if still running

---

### Phase 3: Frontend — Replay Chart + Playback Controls

**Effort:** ~1.5 days | **Priority:** Critical

#### 3.1 — Progressive Chart Mode

Adapt `TradingSignalChart` or create `BacktestReplayChart` that:

1. **Starts empty** — no initial data
2. **Grows bar by bar** — each SSE `backtest:candle` event calls `candleSeries.update(bar)`
3. **Auto-scrolls** — keeps the latest bar visible (rightmost), unless user has scrolled left
4. **Signal markers appear in real time** — accumulate signals, call `signalOverlay.setSignals(allSignals)` on each new signal
5. **Forming candle pulse** — reuse existing pulsation logic for the latest candle during replay

Key difference from live chart: data source is the backtest SSE stream, not `/api/bars` + live SSE.

```typescript
// Progressive update handler (inside useBacktest or the chart component)
function handleCandleEvent(bar: OHLCBar) {
  // Lightweight-charts update() appends new bar or updates last bar
  candleSeriesRef.current?.update({
    time: bar.time as Time,
    open: bar.open,
    high: bar.high,
    low: bar.low,
    close: bar.close,
  });
  
  volumeSeriesRef.current?.update({
    time: bar.time as Time,
    value: bar.volume,
    color: bar.close >= bar.open
      ? 'rgba(16, 185, 129, 0.15)'
      : 'rgba(239, 68, 68, 0.15)',
  });
}
```

#### 3.2 — Playback Controls Component

`apps/dashboard/components/backtest/playback-controls.tsx`:

- **Pause/Resume** button (⏸/▶)
- **Speed selector**: `1x` `2x` `5x` `10x` `max` — pill buttons, active state highlighted
- **Progress bar**: `bars_processed / total_bars` with percentage + current simulated time display
- **Cancel** button (stops backtest, keeps results so far)

Controls call REST endpoints:
```typescript
await fetch(`/api/backtest/control/${backtestId}`, {
  method: 'POST',
  body: JSON.stringify({ action: 'set_speed', speed: '10x' }),
});
```

#### 3.3 — Symbol Tab Selector

When multiple symbols are being backtested, a tab bar above the chart lets the user switch which symbol's candles are displayed. All symbols' data is accumulated in state; switching tabs just changes which symbol feeds the chart.

---

### Phase 4: Frontend — Metrics Sidebar + Trade Log + Equity Curve

**Effort:** ~1 day | **Priority:** High

#### 4.1 — Metrics Sidebar

`apps/dashboard/components/backtest/metrics-panel.tsx`:

Live-updating stats panel (updates on each `backtest:metrics` event):

| Metric | Source |
|---|---|
| Equity | `metrics.equity` |
| P&L | `metrics.equity - initialEquity` |
| Return % | `(equity - initial) / initial * 100` |
| Open Positions | `metrics.open_positions` |
| Closed Trades | `metrics.closed_trades` |
| Win Rate | `metrics.win_rate` |
| Sharpe Ratio | `metrics.sharpe` |
| Max Drawdown | `metrics.max_drawdown_pct` |
| Profit Factor | `metrics.profit_factor` |

On `backtest:complete`, swap to final result metrics with full precision.

#### 4.2 — Trade Log Table

`apps/dashboard/components/backtest/trade-log.tsx`:

Scrollable table that grows as trades are filled:

| Time | Symbol | Side | Qty | Price | Strategy | P&L |
|---|---|---|---|---|---|---|
| 10:32 | AAPL | BUY | 50 | $171.95 | avwap_v1 | — |
| 11:45 | AAPL | SELL | 50 | $173.20 | avwap_v1 | +$62.50 |

- Entry trades show no P&L
- Exit trades show realized P&L (green/red)
- Auto-scrolls to latest trade
- Click to jump chart to that trade's timestamp

#### 4.3 — Equity Curve

Small lightweight-charts `LineSeries` below the metrics:

```typescript
const equityChart = createChart(container, { height: 120, ... });
const equitySeries = equityChart.addSeries(LineSeries, {
  color: '#10b981',
  lineWidth: 2,
});

// On each metrics event:
equitySeries.update({
  time: currentSimulatedTime as Time,
  value: metrics.equity,
});
```

Shows equity progression over the backtest period. Drawdown periods can be shaded red.

---

### Phase 5: Next.js API Proxy Routes

**Effort:** ~0.5 day | **Priority:** Critical (needed by Phase 2)

#### 5.1 — Proxy Routes

Following the existing pattern in `app/api/events/route.ts`:

```
apps/dashboard/app/api/backtest/
  run/route.ts          → POST proxy to backend POST /backtest/run
  [id]/events/route.ts  → GET SSE proxy to backend GET /backtest/events/{id}
  [id]/control/route.ts → POST proxy to backend POST /backtest/control/{id}
  [id]/results/route.ts → GET proxy to backend GET /backtest/results/{id}
  [id]/route.ts         → DELETE proxy to backend DELETE /backtest/{id}
```

The SSE proxy follows the same pattern as the existing `/api/events/route.ts` — forward the backend's `ReadableStream` response directly.

---

## Implementation Order

```
Phase 5 (proxy routes)  ──┐
                          ├──► Phase 2 (page shell + config) ──► Phase 3 (chart + playback) ──► Phase 4 (metrics + trades)
Phase 1 (backend runner) ─┘
```

Phase 1 (backend) and Phase 5 (proxy routes) can be done in parallel. Phase 2 depends on both. Phases 3 and 4 are sequential after Phase 2.

| Phase | Effort | Dependencies |
|---|---|---|
| Phase 1: Backend Runner + HTTP | ~2 days | backtest-parity-plan (partial — shared bootstrap) |
| Phase 2: Page Shell + Config | ~1.5 days | Phase 1, Phase 5 |
| Phase 3: Replay Chart + Playback | ~1.5 days | Phase 2 |
| Phase 4: Metrics + Trade Log + Equity | ~1 day | Phase 3 |
| Phase 5: Next.js Proxy Routes | ~0.5 day | None |
| **Total** | **~6.5 days** | |

---

## Anti-Patterns to Avoid

| Anti-Pattern | Prevention |
|---|---|
| **Lookahead bias** | omo-replay's mutable clock + `WaitPending()` already prevents this. Runner inherits the same pattern. |
| **Event bus contamination** | Each backtest gets `memory.NewBus()` — completely isolated from the live bus. Never share. |
| **React re-render storm** | Batch SSE events with `requestAnimationFrame`. Don't call `setState` on every single bar — accumulate in a ref, flush on rAF. |
| **Memory leak on unmount** | `useBacktest` hook cleanup: close EventSource, cancel backtest via DELETE, clear accumulated state. |
| **Unrealistic fills** | SimBroker limitation (instant fills at close ± slippage) is documented. Not addressed in this plan — see backtest-parity-plan for fill model improvements. |
| **Chart performance with 10K+ bars** | lightweight-charts handles this natively. But avoid re-calling `setSignals()` with the full array on every bar — only call when a new signal arrives. |
| **Concurrent backtests** | MVP: max 1 active backtest. Return 409 Conflict if one is already running. |

---

## Future Enhancements (Not in MVP)

- **Result persistence** — `backtest_runs` table to save and compare historical backtest results
- **Parameter sweep** — run multiple backtests with varying parameters, compare results in a grid
- **Walk-forward analysis** — rolling in-sample/out-of-sample optimization
- **Multi-strategy comparison** — overlay equity curves from different strategy configs
- **Shareable backtest links** — permalink to a completed backtest result
- **Export** — CSV/PDF report generation from backtest results
- **Fill model improvements** — next-bar fills, volume-aware sizing, partial fills (see backtest-parity-plan)
