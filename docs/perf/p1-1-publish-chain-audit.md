# P1-1 Per-Bar Publish Chain Audit (backtest mode)

One `MarketBarReceived` from the replay loop triggers this cascade in serial
(omo-replay uses `memory.NewSyncBus()` + `FreezeHandlers`, so every Publish
dispatches handlers inline on the calling goroutine).

## Event dispatch graph

```
omo-replay main loop
 └─ Publish(MarketBarReceived)                       [1 publish]
     ├─ ingestion.Service.HandleMarketBar
     │   └─ Publish(MarketBarSanitized)              [2 publishes]
     │       ├─ monitor.Service.HandleMarketBar
     │       │   ├─ calculator.Update (1m)
     │       │   ├─ for each anchor TF:
     │       │   │   ├─ agg.Push(bar)
     │       │   │   ├─ Publish(MarketBarSanitized HTF)   [0-3 per bar]
     │       │   │   └─ Publish(RegimeShifted HTF)        [rare]
     │       │   ├─ regimeDetector.Detect(snap)
     │       │   ├─ Publish(StateUpdated)                 [3 publishes]
     │       │   │   ├─ strategy.Runner.handleStateUpdated
     │       │   │   │   (caches indicators in r.indicators[sym])
     │       │   │   ├─ positionmonitor.Revaluator.handleStateUpdated
     │       │   │   └─ sse.Handler (not wired in replay)
     │       │   ├─ Publish(RegimeShifted 1m)             [rare]
     │       │   ├─ feedORBBar → ORBTracker.OnBar
     │       │   └─ Publish(SetupDetected)                [rare; only when ORB fires]
     │       ├─ monitor.Service.publishEnrichedBar
     │       │   └─ Publish(EnrichedBar)                  [4 publishes]
     │       │       └─ (no backtest subscribers — SSE only)
     │       ├─ strategy.Runner.handleBar
     │       │   ├─ UpdateAVWAPCalc
     │       │   ├─ for each 1m instance: safeOnBar → strategy.OnBar
     │       │   ├─ for each HTF instance: aggregator.Push + safeOnBar
     │       │   └─ emits SignalCreated [rare]
     │       └─ positionmonitor.PriceCache.handleBar
     │           (cache only — no publish)
     └─ backtest.Collector.onBar
         (metric collection — no publish)

omo-replay main loop (per-tick, after all bars published)
 └─ eventBus.WaitPending()
 └─ posMonSvc.EvalExitRules(minTime)   [publishes exit intents if triggered]
 └─ eventBus.WaitPending()
```

## Per-bar publish count (common case, no signals / no regime change)

| # | Event | Subscribers (hot-path) | Can bypass bus? |
|---|---|---|---|
| 1 | `MarketBarReceived` | ingestion, backtest.Collector | **Yes** — 2 inline calls |
| 2 | `MarketBarSanitized` (1m) | monitor.HandleMarketBar, monitor.publishEnrichedBar, strategy.Runner.handleBar, positionmonitor.PriceCache.handleBar | **Yes** — 4 inline calls |
| 3 | `StateUpdated` | strategy.Runner.handleStateUpdated, positionmonitor.Revaluator.handleStateUpdated | **Yes** — 2 inline calls |
| 4 | `EnrichedBar` | *(no backtest subscribers; SSE wires in live only)* | **Yes** — drop entirely in backtest |

Plus conditional publishes:
- 0-3× `MarketBarSanitized` (HTF) when a 5m/15m/1h aggregator closes
- Very rare `RegimeShifted`, `SetupDetected`, `SignalCreated`

**Total publishes per typical bar: 4 (MarketBarReceived → Sanitized → StateUpdated → EnrichedBar).**

Each publish pays for:
- `frozen.Load()` atomic (cheap)
- map lookup by event.Type
- slice iteration over handlers
- interface dispatch per handler (domain.Event passed by value — 120-byte copy)
- per-handler defer / error wrapping

pprof attributes ~40-50% of per-bar CPU to the chain ingestion.HandleMarketBar → Bus.Publish → handler functions.

## Classification for direct-dispatch

### 1. Serial-chain (inline-able) — always in backtest
All five handlers above run on the same goroutine, in the same order, every bar. None of them need the pub/sub semantics. They're only on the bus because it was the simplest wiring in the original live-trading design.

**Plan:** `BacktestPipeline.ProcessBar(bar)` calls each directly:
```go
func (p *BacktestPipeline) ProcessBar(bar domain.MarketBar) error {
    // was: Publish(MarketBarReceived)
    sanitized, ok := p.ingestion.Process(bar)
    if !ok { return nil }

    // was: Publish(MarketBarSanitized)
    snap, htfPublishes := p.monitor.Process(sanitized)
    for _, htfBar := range htfPublishes {
        p.runner.HandleHTFBar(htfBar)  // HTF path
    }
    p.runner.HandleStateUpdate(snap)        // was: Publish(StateUpdated) hop
    p.runner.HandleBar(sanitized, snap)     // was: Publish(MarketBarSanitized) hop
    p.priceCache.HandleBar(sanitized)       // was: same hop
    p.collector.OnBar(sanitized)            // was: Publish(MarketBarReceived) hop
    // EnrichedBar dropped entirely (no subscribers in backtest)
    return nil
}
```

Savings per bar:
- 4 `eventBus.Publish` calls → 0
- 4 × 120-byte `domain.Event` struct copies → 0
- 4 × handler slice allocation (already zero since `FreezeHandlers`, but lookup cost remains) → 0
- 1 `NewBacktestEvent` per publish → 0

Expected per-bar savings: **1.5-2.5 μs** (from 6.7 μs total at 30 sym → projected 4.5-5.5 μs).

### 2. Conditional events — still use the bus
- `SignalCreated` / `SignalEnriched` / `OrderIntentCreated` / `OrderIntentValidated` / `OrderIntentRejected` / `FillReceived` / `ExitOrderTerminal` — multiple subscribers (risk sizer, execution, collector, strategy runner, position monitor). These fan out to many consumers and happen far less often than bar events. Keep on the bus.
- `RegimeShifted`, `SetupDetected` — rare, preserve the bus hop.
- `EnrichedBar` — no consumers in backtest. **Drop the emission entirely when `isBacktest`** instead of publishing to a dead handler.
- HTF `MarketBarSanitized` (from anchor aggregators) — subscribers are monitor's own HandleMarketBar recursively? Let me verify before designing.

### 3. Position monitor & execution — keep async-style semantics
- `EvalExitRules` runs once per tick after the bar fan-out. It may publish exit intents which flow through the execution pipeline. This boundary is the right place to keep the bus.
- `EventOrderIntentCreated`, `EventFillReceived` etc. — multi-subscriber, relatively rare. Keep on the bus.

## What DirectDispatch does NOT change

- Execution pipeline (simbroker, risk engine, position monitor exit rules) — unchanged.
- Live trading path (`omo-core`) — unchanged.
- Signal/order events — still go through the bus (rare, multi-consumer).
- Test suites that rely on bus subscription (Collector uses `onBar` callback on bus).

## Answered open questions

1. **HTF MarketBarSanitized publishes from monitor** — `monitor.HandleMarketBar` at
   service.go:578 filters non-1m bars early (`if bar.Timeframe != "1m" { return nil }`).
   `strategy.Runner.handleBar` accepts all timeframes BUT already does its own
   per-strategy HTF aggregation from the 1m stream via `r.aggregators` (see
   runner.go:1134+). The HTF bars emitted by monitor.HandleMarketBar are
   effectively duplicative — strategy.Runner's HTF instances get their HTF bars
   from the runner's own aggregator push, not from monitor's published HTF events.

   **Conclusion:** monitor's HTF Publishes can be dropped in the direct-dispatch
   path. They produce no additional work that strategy.Runner hasn't already
   done via its internal aggregation. Confirm with a no-signal-count-regression
   test when wiring P1-3.

2. **publishEnrichedBar** — its only subscriber is the SSE adapter
   (`sse.Handler`), which is NOT wired in `omo-replay`. The publish fires, the
   bus iterates an empty handler slice, and returns. Pure overhead.

   **Conclusion:** don't call it in the BacktestPipeline. The 4th publish per
   bar disappears completely.

3. **positionmonitor.PriceCache.handleBar** — reads the bar, updates an
   internal `map[symbol]lastPrice`. No cross-bar dependency; it's consumed at
   tick boundary by `posMonSvc.EvalExitRules`. Can be called as a direct
   function on `BacktestPipeline.ProcessBar`.

## Final dispatch count comparison

| mode | publishes/bar | handler calls/bar | notes |
|---|---|---|---|
| current (bus) | 4 | ~7-9 | MarketBarReceived → Sanitized(+HTF) → StateUpdated → EnrichedBar |
| direct dispatch | 0 | ~5 | ingestion → monitor → handleStateUpdate → handleBar → priceCache |

Per-bar framing savings (from pprof attribution of Bus.Publish + handler
invoke overhead):
- **Event struct copies saved:** 4 × 120 B = 480 B/bar
- **Map lookups saved:** 4 per bar
- **NewBacktestEvent allocs saved:** 4 per bar (already cheap but not free)
- **Indirect call + interface dispatch:** ~7 per bar

Projected wall-clock improvement: **-20% to -35%** on the 30-sym/1yr workload
(33.5s → 22-27s). Real numbers come from P1-3 A/B run.
