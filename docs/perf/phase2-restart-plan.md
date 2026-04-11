# Phase 2 Restart Plan

This document captures everything a fresh session needs to resume Phase 2 of
the backtest scalability work without context from the original session.

## Where we stopped

**Phase 1 is complete and shipped** (commits in `claude-20260411-131229`):
- `bdb78f7..c185d0e` — the full Phase 1 chain including the direct-dispatch
  pipeline, indicator window shrink, and field layout pass.
- `7ad0202` — P2-1 audit doc (`docs/perf/p2-1-shared-state-inventory.md`).

**Phase 1 gate benchmark** (2025-04-01..2026-04-01, 1-year window):

| Symbols | Wall time |
|---|---|
| 8 | 7.86 s |
| 16 | 16.0 s |
| 30 | 29.73 s |

Per-symbol cost is flat at ~990 ms across 8 → 30 symbols. The super-linear
scaling is gone. The remaining 29.7 s is per-bar compute; Phase 2's job is
parallelising that across cores.

**Phase 2 target:** 30 sym / 1 yr ≤ 6 s on an 8-core host.

---

## What's already in place for Phase 2

1. **P2-1 audit** — `docs/perf/p2-1-shared-state-inventory.md` classifies
   every field on `monitor.Service` and `strategy.Runner` as per-symbol (P),
   read-mostly (R), write-heavy cross-symbol (W), or immutable (I). Read this
   first — it tells you exactly what to shard and what to share.

2. **Direct-dispatch Pipeline** — `backend/internal/app/backtest/pipeline.go`.
   Already bypasses the event bus for the per-bar hot chain. The worker pool
   will dispatch through `Pipeline.ProcessBar` per shard.

3. **Scratch buffers on Runner** — `scratchOneMin`, `scratchHTFNeeded`,
   `scratchInstances`. Currently live on the single Runner instance. Must
   become per-shard.

4. **`directDispatch` flag on monitor.Service** — already exists and makes
   `HandleMarketBar` collect events in `pendingStrict`/`pendingBestEffort`
   instead of publishing. One shard per Service means DrainPending needs no
   synchronisation.

## Architecture target

**Shard-owned services.** Each worker gets its own `monitor.Service` +
`strategy.Runner` instance covering a disjoint slab of symbols. Nested
services (`IndicatorCalculator`, `RegimeDetector`, `ORBTracker`,
`BarAggregator` maps, `AnchoredVWAPCalc`) come along for free because
they live inside the sharded parent.

```
             ┌──────────── shared ─────────────┐
             │ eventBus (frozen, lock-free)    │
             │ backtest.Collector              │
             │ positionmonitor.PriceCache       │
             │ positionmonitor.Service (exit)   │
             │ simbroker / risk / execution    │
             │ IndexTideTracker (own RWMutex)  │
             └──────────────────────────────────┘
                            ▲
                            │ signals/orders/fills via bus
                            │
 ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐
 │ shard 0  │  │ shard 1  │  │ shard 2  │  │ shard 3  │   …
 │──────────│  │──────────│  │──────────│  │──────────│
 │ monitor  │  │ monitor  │  │ monitor  │  │ monitor  │
 │ runner   │  │ runner   │  │ runner   │  │ runner   │
 │ symbols  │  │ symbols  │  │ symbols  │  │ symbols  │
 │ A,B,C…   │  │ D,E,F…   │  │ G,H,I…   │  │ J,K,L…   │
 └──────────┘  └──────────┘  └──────────┘  └──────────┘
```

**Tick-level coordinator** (in the replay main loop):
1. For each tick, `nextMinTime(streams)` returns minTime as today.
2. Gather per-shard bar lists: each shard gets a slice of bars whose symbols
   it owns.
3. Fan out to N worker goroutines via `sync.WaitGroup`. Each worker calls
   `shard.pipeline.ProcessBar(ctx, evt)` for every bar in its slice.
4. Barrier: WaitGroup.Wait() before advancing.
5. Execution pipeline (exit rules, order fills) runs serially on the main
   goroutine after the barrier — shared state preserved.

`IndexTideTracker` already has `sync.RWMutex`; SPY/QQQ bars may be in any
shard and OnBar is a ~20-byte-write critical section. Fine as-is.

## Implementation plan

### Step 1 — introduce `NewPipelineShard` factory (~200 LOC)

New file: `backend/internal/app/backtest/pipeline_shard.go`.

The current `Pipeline` struct owns one set of services. For Phase 2 we need
an abstraction that can construct N parallel shards cheaply:

```go
// ShardedPipeline fans a bar stream out to Nworkers per-shard pipelines,
// each owning its own monitor + runner. Used by the replay binary to
// parallelise the per-bar hot chain across cores.
type ShardedPipeline struct {
    shards        []*Pipeline // one per worker
    symbolToShard map[string]int
    workerCh      []chan domain.Event // per-shard inbox
    done          chan struct{}
    wg            sync.WaitGroup

    // shared services (not sharded)
    priceCache *positionmonitor.PriceCache
    collector  *Collector
    eventBus   ports.EventBusPort
}
```

**Construction**: the replay main.go takes a single-instance `PipelineInfra`
and passes it to `NewShardedPipeline(Nworkers, infra, symbols)`. The
constructor:
1. Partitions `symbols` into `Nworkers` slabs via `hash(symbol) % Nworkers`.
2. For each slab, calls a factory function that returns a fresh
   `(monitor.Service, strategy.Runner)` pair configured for only that slab's
   symbols. The factory reuses `bootstrap.BuildMonitor` and
   `strategy.NewRunner` — all of the existing wiring code.
3. Wraps each pair in a `Pipeline` via `NewPipeline` (already exists).
4. Spawns one goroutine per shard that reads from `workerCh[i]` and calls
   `shard.ProcessBar(evt)`.

**Gotchas**:
- `bootstrap.BuildMonitor` is the single source of truth for monitor
  construction; call it per shard with the slab's symbols. If the function
  isn't currently parametrised by a symbol slab, modify the deps struct to
  take one.
- `strategy.NewRunner` takes a `Router`. Each shard needs its own Router
  with only the instances for that slab's symbols. `Router.Register` is
  already per-instance so we can register only the relevant ones per shard.
- `IndicatorCalculator.RegisterEMAConfig` is called per-symbol — call only
  for the slab's symbols.
- `InitAggregators` — monitor's and runner's aggregator registration both
  take a symbols list; pass the slab.
- `WarmUp` — already serial per-symbol; route each symbol's warmup to its
  owning shard.

### Step 2 — wire ShardedPipeline into omo-replay (~100 LOC)

In `backend/cmd/omo-replay/main.go`:
1. After current `directPipeline` construction, add:
   ```go
   var workerCount int
   if backtestFlag {
       workerCount = runtime.GOMAXPROCS(0)
       if workerCount > len(symbols) {
           workerCount = len(symbols)
       }
   }
   ```
2. Replace `directPipeline = backtest.NewPipeline(...)` with
   `shardedPipeline, err := backtest.NewShardedPipeline(workerCount, infra, symbols)`.
3. In the replay bar loop, replace the per-stream loop body with a single
   `shardedPipeline.Dispatch(ctx, evt)` call. The sharded pipeline routes
   the event to the right shard's goroutine via the inbox channel.
4. After the tick's bars are dispatched, call `shardedPipeline.WaitTick()`
   to barrier-sync all shards before exit-rule evaluation.

### Step 3 — reroute per-symbol setup paths (~150 LOC)

Places that currently call methods on the single `monitorSvc` or
`pipeline.Runner` and need to route per-symbol:
- `monitorSvc.ResetSessionIndicators(sym)` on new-day boundaries — route to
  the shard owning `sym`.
- `monitorSvc.WarmUp(bars)` — one call per slab.
- `monitorSvc.InitAggregators(symbols, ...)` — call with each slab.
- `monitorSvc.MarkReady(sym)` — route per-symbol.
- `pipeline.Runner.WarmUp(sym, ...)` — route per-symbol.
- `pipeline.Runner.InitAggregators(sessionOpen)` — call on each shard.
- `pipeline.Runner.SetAIAnchorResolver(...)` — call on each shard. The
  resolver itself can be shared (has own lock).
- `pipeline.Runner.SetSuppressProgressEvents(true)` — per shard.
- Everything touching `r.anchorRegimes` via external setters — per shard.

### Step 4 — worker pool dispatch (P2-3) (~100 LOC)

The actual goroutine fan-out. Options for the inbox mechanic:

**Option A: blocking channels** (simplest).
```go
type ShardedPipeline struct {
    shardCh  []chan barJob // one per shard, buffered
    shardDone sync.WaitGroup
}
type barJob struct {
    ctx context.Context
    evt domain.Event
}
```

Per tick:
1. For each bar in the tick, `sp.shardCh[sp.symbolToShard[sym]] <- barJob{ctx, evt}`.
2. Each shard goroutine loops `for job := range sp.shardCh[i]`.
3. After dispatching a tick's bars, send a `tickBarrier{}` sentinel to each
   shard. Each shard receives it, calls `sp.shardDone.Done()` to unblock the
   coordinator.

**Option B: batch per tick** (lower overhead).
Pre-group the tick's bars by shard into per-shard slices, then start one
goroutine per shard with `sync.WaitGroup`. Each goroutine processes its
slice and calls `wg.Done()`. Coordinator calls `wg.Wait()`.

**Recommendation: Option B.** Fewer channel ops per bar, more predictable
GC behaviour. Shard goroutines aren't long-lived — they're spawned per tick.
Alternatively keep them pinned and use `sync.Cond` for wakeup; measure.

### Step 5 — contention measurement (P2-4)

Run with `-blockprofile=block.prof -mutexprofile=mutex.prof`.

Expected contention points:
- `eventBus.Publish` for SignalCreated — shared bus, lock-free Publish after
  FreezeHandlers, so this should be fine unless signal emission is hot.
- `IndexTideTracker.mu` — ~3-4 workers can have SPY/QQQ bars in the same
  tick; uncontended most of the time.
- `sync.Pool` inside `instanceContextPool` — already per-runner in current
  code; shard-local after Step 1.
- `r.mu` / `s.mu` — gone after sharding.
- `strategyEmitSeq atomic.Uint64` — atomic, no contention.

Fix candidates if contention shows up:
- Per-shard `sync.Pool` for instanceContext.
- Per-shard atomic counter + coalescing for metrics.

### Step 6 — behaviour parity check

Non-negotiable. Before declaring victory:
1. Run 8 sym / 3 mo in single-threaded mode (current) and sharded mode.
   Signal counts MUST match exactly (expected 155 RTH signals).
2. Same for 30 sym / 1 yr (expected 3386 RTH signals).
3. Trade counts and equity curve points must match for backtest mode.

If they don't match, the problem is almost certainly:
- A shared resource being updated concurrently (look for shared maps without
  per-shard wrapping).
- Event ordering — if execution service sees fills in a different order,
  P&L could differ. The tick-level barrier should prevent this.
- A counter/atomic that was single-writer and is now multi-writer.

### Step 7 — gate benchmark (P2 gate)

Target: 30-sym / 1-yr ≤ 6 s on 8 cores. If met, ship Phase 2. If not, run
P2-4 contention profiling and iterate. If workers spend more than ~20% of
wall time blocked or contending, Phase 3 (full sharded architecture including
execution pipeline) is needed.

## Files to touch

**New**:
- `backend/internal/app/backtest/pipeline_shard.go`

**Modified**:
- `backend/cmd/omo-replay/main.go` — construct ShardedPipeline, replace
  loop body.
- `backend/internal/app/bootstrap/ingestion.go` — maybe make `BuildMonitor`
  accept a symbol slab so we can call it N times.
- `backend/internal/app/bootstrap/strategy.go` — same, parametrise the
  runner factory on a symbol slab.
- `backend/internal/app/strategy/runner.go` — only if the scratch buffers
  don't already live per-instance (they do; should be fine).
- `backend/internal/app/monitor/service.go` — only if symbol set is
  hard-coded anywhere (spot-check).

**Tests that will break and need updating**:
- `backend/internal/app/bootstrap/pipeline_integration_test.go`
- Probably nothing else — strategy/runner/monitor internal tests don't use
  bootstrap.

## Pre-flight checklist for the restart session

Before writing any code:

- [ ] Re-read `docs/perf/p2-1-shared-state-inventory.md` — it's the source
  of truth for what's shardable vs shared.
- [ ] Re-read this document.
- [ ] `git log --oneline 7ad0202..HEAD` — confirm no new work landed between
  sessions that changes the starting state.
- [ ] Run the current 30-sym / 1-yr benchmark to confirm the Phase 1 number
  is still ~29.7 s. Regressions here indicate drift that must be addressed
  first.
- [ ] Decide on `Nworkers` default. Recommendation: `runtime.GOMAXPROCS(0)`
  capped by `len(symbols)`.

## Estimated effort

- Step 1 (ShardedPipeline factory): **4-6 hours** — most of the complexity
  is in re-running bootstrap per shard safely and ensuring each has its own
  router/aggregators without racing on shared services.
- Step 2 (omo-replay wiring): **1-2 hours**.
- Step 3 (per-symbol setup routing): **2-3 hours** — scattered one-off calls
  that need to learn about shards.
- Step 4 (dispatch mechanics): **1 hour**.
- Step 5 (contention profiling + fixes): **2-4 hours** — open-ended.
- Step 6 (parity): **1 hour** if nothing is wrong, **many hours** if
  something is.
- Total: **11-17 hours**, or roughly a focused half-week.

**Bring snacks.**

## Risk register

| Risk | Mitigation |
|---|---|
| Tests break on bootstrap change | Start with an additive path: new `BuildMonitorShard` that wraps existing `BuildMonitor`; leave old callers untouched. |
| Behaviour diverges due to ordering | Add an assertion/debug mode that compares sharded vs single-threaded output bit-for-bit on a small window. |
| Contention on eventBus for signals | Very rare in replay (~3000 signals total); measure before worrying. |
| Shard imbalance (one shard much heavier) | Partition by bar count not symbol count. Inspect after first run. |
| Tide tracker race on a SPY/QQQ update | Already protected by RWMutex; verify in profile. |
| Scratch buffer aliasing across shards | Buffers live on Runner; each shard has its own Runner → fine. |

## What Phase 2 does NOT do

- Does NOT change the live `omo-core` pipeline. Sharding is a backtest-only
  transport.
- Does NOT parallelise execution (orders, fills, risk). Post-tick work
  stays serial.
- Does NOT touch Strategy.OnBar implementations — strategies stay
  single-threaded per symbol by construction.
- Does NOT change behaviour semantics. Strict parity is non-negotiable.

## What Phase 3 would add (if Phase 2 isn't enough)

- Full sharded architecture incl. execution pipeline.
- Cross-symbol coordinator actor for features that genuinely need it.
- Unified live+backtest scheduler where the only difference is the clock
  source.

See the top-level plan in tasks #28-#32 and the plan message in the original
chat.
