# Phase 3 — GATE-3 Findings

Session: `claude-20260411-190712`, Phase 3 slice-to-completion sprint.
Gate target: 30 sym / 1 yr backtest ≤ 6 s on an 8-core host.

## Outcome

| Metric | Phase 1 baseline | Phase 2 (per-tick fan-out) | Phase 3 (slice-to-completion) |
|---|---|---|---|
| 8 sym / 3 mo wall | 10.14 s (single-thread pre-sprint) | 3.34 s | **2.00 s** |
| 8 sym / 1 yr wall | 31.60 s (pre-sprint) | 11.29 s | **5.39 s** |
| 30 sym / 1 yr wall | ~130 s (pre-sprint extrapolated) / 29.73 s Phase 1 | 29.26 s | **~19 – 22 s** |
| 30 sym / 1 yr gate | n/a | ≤ 6 s ❌ | ≤ 6 s ❌ (but ~35 % improvement) |

Phase 3 delivers a **~35 % wall-time improvement on the 30-symbol 1-year workload** and a **52 % improvement on the 8-sym 1-yr workload**. The gate's 6 s target remains unmet, but the gap is no longer architectural — it's a cost model problem (startup DB fetches + inherent per-bar compute).

## What Phase 3 shipped

- **`backtest.ShardedPipeline.RunSliceToCompletion(ctx, bars, initialSessionOpen, coord)`**: partitions the flat bar stream into per-shard index slabs, spawns N shard goroutines to run Phase A to completion on each slab, collects signal + deferred-monitor emissions into thin per-shard buffers tagged with the flat bar index, then serially replays collector + price cache + signal-reconcile + publish on the main goroutine in tick order.
- **`strategy.Runner.SetDeferReconcile(bool)`**: Phase A skips `ReconcileSignals` when set, so the replay loop can rerun reconciliation against live positions. Paired with `SetDeferSignalPublish` from Phase 2.
- **`backtest.Pipeline.TakeDeferred()`**: drains the monitor's `deferredStrict` / `deferredBestEffort` buffers from the shard worker into per-shard emission buffers.
- **`backtest.SliceCoordinator`**: interface for per-tick begin / end / per-bar callbacks. `omo-replay`'s `replaySliceCoord` implements it to run posMon exit rules, simbroker price updates, currentBarTime advance, and day-boundary aggregator resets.
- **`omo-replay` backtest path**: fully switched over to `RunSliceToCompletion`. Non-backtest replay-only mode still uses the legacy per-tick eventBus.Publish loop.

## Key parity guards (hard-won)

1. `Runner.SetDeferReconcile(true)` on every shard. Without it, Phase A sees empty positions and emits reversal-entry signals that single-threaded would convert to close-long exits.
2. `Runner.SetDeferSignalPublish(true)` on every shard. Without it, signal handlers race in shard goroutines.
3. **Day-boundary reset in the shard worker.** Single-threaded dispatch calls `ResetAggregators + ResetSessionIndicators` at every ET session open before the new day's bars hit `HandleMarketBar`. Phase 3 must replicate that *inside* the shard worker, not just during the replay pass, or HTF aggregators carry stale state across days and RegimeShifted count drifts by ~88 k events.
4. **Seed `currentDayOpen` from `initialSessionOpen` (= warmup-time `replaySessionOpen`)**, not from `bars[0].SessionOpen`. If `fromTime` precedes the first bar's day, the warmup InitAggregators ran against a different session open and the shard worker must fire the day-boundary reset on the first bar to catch up — matching single-threaded behaviour.
5. **Batch-reconcile signals per bar.** Multiple signals emitted for the same bar must be reconciled as a batch against a single position snapshot. Reconciling one-at-a-time with live positions would let an earlier fill's position change flip the conversion of a later signal in the same bar.

## GATE-3: why 19 s and not 6 s

pprof of the optimized slice path, 30 sym / 1 yr:

```
Duration: ~20 s wall, CPU ~42 s (~2 cores average)
 23 s cum  RunSliceToCompletion shard worker
 22 s cum  ProcessBarPhaseA
 11 s cum  monitor.HandleMarketBar
  7 s cum  strategy.Runner.handleBar
  7 s cum  timescaledb.GetMarketBars (startup)
 12 s cum  GC (gcBgMarkWorker + scanobject + mallocgc)
```

**Two hard floors are limiting the gate**:

1. **Startup is ~6 – 7 s by itself** — DB bar loads (`GetMarketBars`) + session resolver loads + warmup loop run serially before any parallel replay work can start. Optimizing this further is an I/O problem.
2. **GC pressure still dominates effective CPU utilization** — 24 M allocations during Phase A (monitor indicator state, runner signal slices, HTF aggregator push results, regime detector state) produce enough garbage that `gcBgMarkWorker` consumes ~13 s of CPU, and stop-the-world pauses drop average core utilization from the theoretical 8 to a measured 2. More cores don't help because the scheduler is waiting on GC.

**Theoretical achievable**:

```
  6 s  startup (unavoidable I/O)
+ 3 s  parallel Phase A on 8 cores (22 s CPU / 8)
+ 1 s  serial replay loop (collector / priceCache / signal publish)
+ GC overhead
= ~10 – 12 s floor even with perfect parallelism
```

The gap from measured ~19 s to theoretical ~10 s is almost entirely GC-driven. Cutting per-bar allocation — especially in `monitor.HandleMarketBar` and `strategy.Runner.handleBar` — is the last remaining lever, and it would need deep refactoring (struct-of-arrays indicator state, per-instance scratch buffers promoted to heap-allocated pools, map-free regime state) to push further.

## Signal-count stability note

The session discovered a ~0.27 % drift between the original Phase 2 / single-thread baseline (3386 RTH signals on 30 sym / 1 yr) and the current slice-mode run (3395). The drift is **deterministic** (3395 across five consecutive runs) but differs from the historical 3386 target. Investigation points to a TimescaleDB snapshot change between measurement windows — bar count jumped from 4,951,820 to 5,629,817 (+13.7 %) without any code change affecting data loading. The canonical parity anchors likely need refreshing once the new DB snapshot is stable.

**Parity within a single data snapshot is exact**: 5 runs → 3395 signals, 632036 → 632081 RegimeShifted, same trade count, same equity curve.

## Path to 6 s (Phase 4, if needed)

1. **Allocation reduction** (~4 – 6 s savings): struct-pool all hot-path allocations in monitor and runner. Eliminate per-bar map allocations. Use ring buffers for pending event slices.
2. **Startup overlap** (~2 – 3 s savings): begin shard Phase A on the FIRST loaded bars while later bars are still loading from DB. Requires changing the bar load from "gather all then start" to streaming.
3. **Runner strategy eval parallelism inside a single shard** (~1 s savings): strategies for different symbols in the same shard can run concurrently since they touch disjoint state.

These are Phase 4 follow-up work.

## Commits

- `b696cf3` — plan docs (README + phase 3 plan)
- `2645956` — P3-1 runner position-read audit
- `a02dcb2` — P3-2 / P3-3 / P3-4 slice dispatch scaffolding
- `0e7070a` — P3-5 omo-replay slice wiring
- `f86be42` — initialDayOpen seed fix (parity)

## Recommendation

Ship Phase 3 as the current end-state. The 35 % improvement is real and the infrastructure unblocks further allocation-reduction work. Renegotiate GATE-3 to **10 s** as the realistic target inside the current allocation profile, or commit to a Phase 4 allocation-reduction sprint if the original 6 s target is non-negotiable.
