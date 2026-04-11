# P2-1 Shared State Inventory (monitor + strategy runner)

Every field touched during per-bar processing, classified by shardability.

**Classification:**
- **(P)** Per-symbol — keyed by symbol, safe to shard with one worker per
  symbol. Each worker touches only its own keys.
- **(R)** Read-mostly cross-symbol — written once at startup, read from
  every worker. Safe under `sync.RWMutex` or an atomic pointer.
- **(W)** Write-heavy cross-symbol — multiple workers want to mutate.
  Needs design work (channel, single-writer goroutine, atomic ops).
- **(I)** Immutable after init — safe for any worker to read without
  synchronisation.
- **(S)** Startup/warmup only — not touched during the bar hot loop, so
  irrelevant for parallelism.

---

## monitor.Service

| Field | Type | Class | Notes |
|---|---|---|---|
| eventBus | ports.EventBusPort | I | immutable reference after Start |
| repo | ports.RepositoryPort | I | same |
| calculator | *IndicatorCalculator | **W** | per-symbol state inside, but one instance shared |
| regimeDetector | *RegimeDetector | **W** | per-symbol state inside, one instance shared |
| orbTracker | *ORBTracker | **W** | per-symbol sessions map, one instance shared |
| orbCfg | ORBConfig | I | config value |
| mu | sync.Mutex | — | global lock; removed by sharding |
| baseSymbols | map[string]struct{} | R | written once in InitAggregators / setter |
| effectiveSymbols | map[string]struct{} | R | written once at startup; read per bar in isAllowedSymbolLocked |
| lastSnaps | map[string]IndicatorSnapshot | **P** | keyed by symbol; written per bar |
| liveBars | map[string]int | **P** | keyed by symbol; ++ per bar |
| aggregators | map[string]*BarAggregator | **P** | keyed by "sym:tf"; push per bar |
| aggKeysBySym | map[string][]string | R | written in InitAggregators, read-only after |
| anchorRegimeMaps | map[string]map[Timeframe]MarketRegime | **P** | per-symbol output map, cleared+refilled per bar |
| htfDataMaps | map[string]map[Timeframe]HTFData | **P** | same pattern |
| orbAggregators | map[string]*BarAggregator | **P** | per-symbol ORB 5m aggregator |
| orbTimeframe | Timeframe | I | config |
| anchorRegimes | map[string]MarketRegime | **P** | keyed by "sym:tf"; written per HTF bar |
| lastHTFSnaps | map[string]IndicatorSnapshot | **P** | keyed by "sym:tf"; written per HTF close |
| htfStatic | map[string]HTFData | R | static daily HTF data written at startup |
| readySymbols | map[string]struct{} | R | written at warmup completion |
| log | zerolog.Logger | I | |
| dnaGate | DNAGateChecker | I | config |
| strategyKey | string | I | |
| vixLevel / vixSkipAbove / vixWidenAbove | float64 | R | occasional writes via setters |
| orbAllowedRegimes / orbHTFBiasEnabled / orbMinATRPct | — | I | config |
| avwapFn | func | I | set once |
| monitorGateChain | *MonitorGateChain | **W** | internal state, need audit |
| tideTracker | *IndexTideTracker | **W** | **cross-symbol** — SPY/QQQ feeds inform OTHER symbols |
| avwapCalcs | map[string]*AnchoredVWAPCalc | **P** | per-symbol AVWAP calculator |
| avwapAnchors | []string | I | config |
| avwapLastSession / avwapLastSessionInt | map[string]...  | **P** | per-symbol session tracking |
| directDispatch / pendingStrict / pendingBestEffort | — | **W→P** | currently single-writer; move per-worker in P2 |
| anchorResolverFn / prevDayBarsFn | func | I | set once |
| nyLoc | *time.Location | I | |

### Nested: `IndicatorCalculator`
| Field | Class |
|---|---|
| states map[stateKey]*symbolState | **P** |
| emaConfigs map[stateKey]emaConfig | R |

### Nested: `RegimeDetector`
| Field | Class |
|---|---|
| states map[regimeKey]*regimeState | **P** |
| thresholds map[regimeKey]float64 | R |

### Nested: `ORBTracker`
| Field | Class |
|---|---|
| sessions map[string]*ORBSession | **P** |
| others (logger, etc.) | I |

---

## strategy.Runner

| Field | Type | Class | Notes |
|---|---|---|---|
| mu | sync.Mutex | — | global lock; removed by sharding |
| eventBus | ports.EventBusPort | I | |
| router | *Router | **R** | symbolMap + instances — **written at startup only** |
| swapManager | *SwapManager | **W** | internal state; need audit |
| posLookup | func | I | |
| logger | *slog.Logger | I | |
| tenantID / envMode | — | I | |
| indicators | map[string]IndicatorData | **P** | per-symbol cached indicators |
| indLogOnce | map[string]bool | **P** | per-symbol log-once flag |
| metrics | *Metrics | **R** | atomic counters inside — goroutine-safe on their own |
| aggregators | map[string]*BarAggregator | **P** | per-"sym:tf" HTF aggregator for strategy |
| htfCalcs | map[string]*IndicatorCalculator | **P** | per-"sym:tf" HTF indicator calc |
| regimeDetector | *monitor.RegimeDetector | **W** | nested per-symbol state under its own map |
| anchorRegimes | map[string]map[string]MarketRegime | **P** | sym → tf → regime, now nested |
| collectedAnchorRegimes | map[string]map[string]AnchorRegime | **P** | per-symbol output cache |
| signalsRTHSuppressed | atomic.Int64 | **A** | atomic, goroutine-safe as-is |
| anchorResolver / prevDayBarsFn / keyLevelPricesFn | func | I | |
| keyLevelsBySymbol | map[string]map[string]float64 | **P** | per-symbol level data |
| aiAnchorResolver | *AIAnchorResolver | **W** | **shared** — has its own lock |
| lastSessionDate | map[string]int | **P** | per-symbol |
| lastResolvedRegime | map[string]RegimeType | **P** | per-symbol |
| dpLookup | map[DPLookupKey]DarkPoolBar | R | written at startup, read per bar |
| dpRolling | map[string]*dpRollingStats | **P** | per-symbol rolling stats |
| whaleLookup | map[string]WhaleAccumulation | R | written at startup |
| signalProgressMu + signalProgressCache | — | — | suppressed in replay, ignore |
| lastBarTime | atomic.Int64 | **A** | atomic |
| tideTracker | *IndexTideTracker | **W** | same cross-symbol concern as monitor |
| notifier | NotifierPort | I | |
| suppressProgressEvents | bool | I | set once |
| noInstancesLogged | map[string]struct{} | **P** | per-symbol flag |
| scratchOneMin / scratchHTFNeeded / scratchInstances | — | **per-worker** | scratch buffers must become per-worker |

---

## Cross-symbol hotspots (the hard parts)

These are the fields where simple per-symbol sharding won't work:

### 1. `tideTracker *IndexTideTracker` (both monitor + runner)
**Issue:** SPY and QQQ 1m bars feed the tide tracker; its running VWAP then
influences OTHER symbols' AVWAP decisions. So a worker handling SPY writes
state that a worker handling AAPL reads.

**Options:**
- (a) Run tideTracker updates on a dedicated single-writer goroutine that
  receives SPY/QQQ bars via a channel from whichever worker owns them.
  Other workers read the latest value via an atomic snapshot (sync/atomic
  Value or sync.Map with pointer).
- (b) Since tide state only advances when a SPY/QQQ bar arrives, and there's
  only ONE SPY and ONE QQQ, pin SPY+QQQ to a single worker and expose the
  result via an atomic pointer published on each update.
- (c) Compute tide state in a pre-pass before fan-out so all workers see a
  consistent read-only snapshot for that tick.

Option (c) is cleanest for the tick-barrier model — SPY/QQQ bars at the
current timestamp run BEFORE the worker fan-out, populating a read-only
snapshot that all workers use for the rest of the tick.

### 2. `regimeDetector` + `orbTracker` + `calculator` (monitor)
**Issue:** These are single instances with internal `map[string]state` —
when two workers call `calculator.Update(barA)` and `calculator.Update(barB)`
concurrently, they race on the map.

**Options:**
- (a) Make each of these "thread-safe by using a lock per inner map entry"
  — too fine-grained, lock overhead kills the win.
- (b) Shard the inner maps: `map[shardID]map[key]state` and have each worker
  only touch its own sub-map.
- (c) Give each worker its own `IndicatorCalculator` / `RegimeDetector` /
  `ORBTracker` instance. One-time cost to construct, zero contention.

Option (c) is the simplest and is what the plan called for. "Shard-owned
service instances".

### 3. `aiAnchorResolver *AIAnchorResolver`
Already uses its own lock. If it's called once per bar per symbol, that
lock becomes contended under parallelism. Safe because replay's direct
pipeline mostly skips the AI path in no-ai mode (`--no-ai`). Leave as-is
for now; contention check (P2-4) will tell us if it matters.

### 4. `router *Router`
Read-only after startup. Safe for any worker to read under RLock or via
an immutable atomic snapshot. The `FreezeHandlers`-style approach works
here.

### 5. `swapManager *SwapManager`
Used for strategy hot-swaps — not touched in the bar hot loop except via
`OnBarProcessed` which is a single cross-cutting hook. Check contention;
may need to move outside the hot loop.

### 6. `aggregators` (runner and monitor, two separate maps)
Per-"sym:tf". Writes happen when 1m bars close higher TFs. Safe to shard
by sym because each aggregator only receives bars for its own symbol.

---

## Plan for P2-2 (per-symbol sharding of hot maps)

### Phase A: make the services "shard-owned"
Each worker gets its own `monitor.Service` and `strategy.Runner` instance,
each wired to the same shared `eventBus`. This gives us **zero contention**
on the monitor/runner internal maps at the cost of duplicating a few
bytes of config per worker.

**Pros:**
- Simplest path; no map-sharding per se
- Each worker's internal maps only have its own symbols, so they're
  SMALLER, which helps cache behaviour too
- Nested services (calculator, regime, orb tracker, aggregator) get sharded
  for free since they live inside the sharded parent

**Cons:**
- `tideTracker` still needs cross-shard reads — fix via option (c) above
- `lastSnaps` / `anchorRegimes` — `publishEnrichedBar` and SSE consumers
  outside replay assume one shared map; safe in replay because we drop
  `publishEnrichedBar` and SSE isn't wired. Document the constraint.
- Shared counters (signalsRTHSuppressed, notifier) still global — atomic
  so fine.

### Phase B: worker-pool dispatch (P2-3)
Construct `Nworkers = runtime.GOMAXPROCS(0)` shards at startup. Each shard
owns an (N/Nworkers) slice of symbols. A dispatcher loop:
1. Compute `tide` pre-pass for the current tick (SPY/QQQ from shard that
   owns them — may need a small routing table)
2. Fan out per-symbol bars to shard-owned goroutines via a WaitGroup
3. Wait for all workers to finish the tick
4. Run the execution pipeline serially (position monitor, risk, simbroker)
   — shared state, outside the hot loop
5. Advance to next tick

### Phase C: contention measurement (P2-4)
`go tool pprof -block` + `-mutex` on the run. Fix anything hotter than
1 ms total.

---

## Risks / unknowns

- **Behaviour parity**: Each worker having its own `monitor.Service`
  changes the `lastSnaps` and `anchorRegimes` maps from global to
  per-shard. The replay path doesn't consume these cross-shard, but test
  code might. Need a comprehensive behaviour-parity run against the
  single-goroutine path.
- **Startup cost**: Constructing N copies of services isn't free. Measure
  and cache where possible.
- **Shared `eventBus`**: Signal/fill/order events still go through the
  shared bus — it already has `FreezeHandlers` lock-free Publish, so
  contention should be minimal. But worker N publishing a signal while
  worker M is in a handler that also publishes could serialize. Measure.
- **`pipeline.Runner` aggregation of HTF bars**: The runner does its own
  1m → HTF aggregation via `r.aggregators`. When we shard the runner,
  each shard has its own aggregator, so this is fine.
