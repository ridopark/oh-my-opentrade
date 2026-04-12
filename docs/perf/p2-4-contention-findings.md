# P2-4 Contention Findings + GATE-2 Result

Phase 2 restart session (worktree `claude-20260411-190712`) completed P2-1
through P2-4 and ran the GATE-2 benchmark. Summary: **gate missed** —
per-tick fan-out cannot reach 6 s on this workload regardless of
contention tuning. The path to 6 s is an architectural restructure
documented at the bottom of this file.

## Baseline & sweep

30 sym / 1 yr / LOG_LEVEL=warn, all via `omo-replay --backtest`.
Canonical parity: **3386 RTH signals**. All rows below preserve parity
bit-for-bit.

| Stage | Nworkers | Dispatch | Wall | Notes |
|---|---|---|---|---|
| P1 end (pre-Phase 2) | 1 | n/a | **29.73 s** | Phase 1 single-thread baseline |
| P2-2 step 1 | 1 | sync routing | **29.12 s** | ShardedPipeline skeleton (no-op wrapper) |
| P2-2 step 2 | 1 | sync dispatch | **29.18 s** | Replay loop routed through sharded |
| P2-2 step 3a | 1 | sync routing | **28.87 s** | Warmup routed per shard (Nworkers=1) |
| P2-2 step 3b | 24 | sync dispatch | **29.60 s** | Fresh services per slab, Nworkers=GOMAXPROCS |
| P2-2 step 4 naive | 24 | goroutine-per-tick | **38.40 s** | 5.8 M goroutine spawns regressed |
| P2-2 step 4 persistent | 24 | chan+wg | **34.49 s** | Still regressed — 24 workers × tick barriers |
| P2-2 step 4 persistent | 8 | chan+wg | **34.27 s** | GOMAXPROCS=8 env — same |
| P2-2 step 4 + runner in Phase A | 8 | chan+wg | **29.11 s** | Runner defer-publish, best config found |
| P2-2 step 4 + runner in Phase A | 1 | chan+wg | **33.34 s** | Phase split overhead with no parallelism |
| P2-2 step 4 + runner in Phase A | 4 | chan+wg | **30.56 s** | |
| **GATE-2 target** | — | — | **≤ 6 s** | ❌ **missed by 23 s** |

## Parity checks (Step 6)

| Workload | Expected | Measured | Wall |
|---|---|---|---|
| 8 sym / 3 mo | 155 | **155** ✅ | 3.34 s |
| 8 sym / 1 yr | 603 | **603** ✅ | 11.29 s |
| 30 sym / 1 yr | 3386 | **3386** ✅ | 29.11 s |

Every structural change in P2-2 steps 1 through 4 is bit-for-bit parity
with the single-threaded Phase 1 baseline.

## pprof-driven contention analysis (P2-4)

Profile: 30 sym / 1 yr run at Nworkers=8 with runner-in-Phase-A.

```
Type: cpu
Duration: 28.46s, Total samples = 36.60s (128.58%)
```

**Key reading**: total CPU sample time of 36.60 s over 28.46 s wall
means the run averaged **1.3 cores active** despite 8 worker goroutines.
The scheduler is not able to keep workers busy. Top cumulative samples:

| Sample | Cum | Notes |
|---|---|---|
| `ShardedPipeline.startWorkers.func1.1` | 18.27 s | Per-shard worker loop |
| `Pipeline.ProcessBarPhaseA` | 17.05 s | Parallel-safe path |
| `monitor.HandleMarketBar` | 9.09 s | Monitor indicator + HTF work |
| `timescaledb.GetMarketBars` | 6.46 s | **Startup DB load — one-time** |
| `runtime.futex` | **6.13 s flat (16.75%)** | Worker wakeup/sleep cycles |
| `runtime.mcall` / `park_m` / `schedule` / `wakep` / `startm` / `futexwakeup` / `notewakeup` | ~5–6 s each cum | Scheduler churn |
| `strategy.handleBar` | 6.11 s | Runner strategy eval |

**Diagnosis**. With 4.95 M bars across 240 420 ticks the workload is ~20
bars per tick. At Nworkers=8 each shard processes ~2.5 bars per tick,
which is ~15 µs of useful work. Per-tick channel send + `wg.Wait` +
scheduler park/wakeup costs **~25 µs round-trip**. 240 k ticks ×
~25 µs = **~6 s** — exactly the futex sample bucket. Coordination eats
every bit of parallelism.

**This is not a lock-contention problem**. No per-object mutex shows up
meaningfully in `-mutexprofile`: collector and price cache were moved
to Phase B (serial) precisely because their shared mutexes had 8-way
contention at Phase A fan-out. With those moved, the only shared locks
touched on the hot path are the event bus `atomic.Pointer` (lock-free
after FreezeHandlers) and nothing else.

**The problem is work granularity**. Go's scheduler round-trip floor
is ~10–30 µs on Linux futex; sub-100 µs per-worker work units cannot
amortize that cost at 8+ way fan-out. Per-tick barriers are
architecturally wrong for this workload.

## What was tried and why none of it hit the gate

1. Shared services moved to Phase B to kill mutex contention on
   `positionmonitor.PriceCache` and `backtest.Collector`. Helped — the
   goroutines are at least _able_ to run in parallel now — but can't
   overcome the coordination floor.
2. `strategy.Runner.SetDeferSignalPublish` so `emitSignal` buffers
   `SignalCreated` events into a per-runner slice, letting runner work
   run in Phase A. The alternative (serial Phase B runner) would put a
   40 %+ of per-bar cost on the serial path and make parallelism
   pointless.
3. Persistent worker pool (per-shard buffered channels) to eliminate
   the goroutine-spawn tax the naive implementation paid. Drops 9.6 M
   spawns to 0; still bound by futex wakeups per tick.
4. Nworkers sweep (1, 4, 8, 24) — 8 is the best empirical setting.
   More workers means more futex traffic with less work per wakeup.

## Why per-tick fan-out has a floor

```
per-tick coordination overhead × ticks/run ≈ 25 µs × 240 420 ≈ 6.0 s
```

```
achievable floor with per-tick fan-out:
  startup 6.5 s  (DB load, unavoidable)
+ futex  6.0 s  (scheduler coordination)
+ serial Phase B  ~2 s  (collector/priceCache/signal publish)
+ parallel Phase A / 8  ~2.5 s
≈ 17 s wall
```

Measured 29 s suggests cache thrash and allocator pressure on top. No
tuning inside this architecture can cross the ~15 s floor, let alone
reach 6 s.

## Path to the 6 s gate (recommended Phase 3 approach)

**Slice-to-completion partitioning**, per the P2 go-architect
consultation. Sketch:

1. Partition the full bar stream by FNV-1a hash on symbol into
   Nworkers disjoint slices **at replay start**, not per tick.
2. Spawn Nworkers goroutines, each running every bar in its slice to
   completion (ingestion + monitor + runner). Each shard writes its
   emitted signals and regime/setup events into a per-shard ordered
   buffer tagged with the original tick timestamp.
3. Main goroutine performs a serial k-way merge of the per-shard
   buffers in (tick, dispatch-order) order and replays the merged
   stream through the event bus. Downstream handlers (risk sizer,
   execution, sim broker, position monitor) see signals in identical
   order to the single-threaded path.
4. Exit rule evaluation moves out of the bar loop entirely. Since the
   merge replays signals/fills in tick order, the position snapshot
   needed for exit rules is reconstructable from the merged event log
   alone.

**Eliminates** per-tick barriers — N workers run continuously for the
full ~6 s of parallel work instead of sleeping 240 k times. Futex cost
drops from 6 s to near zero.

**Soundness requirement**. `strategy.Runner.handleBar` must not read
mutable position state mid-bar — or if it does, the position reads
must be snapshotted at shard boundaries (coarser snapshots trade
fidelity for throughput). The existing runner has position checks
inside strategy gate logic that would need to be audited and possibly
moved to the merge pass.

**Expected wall time**. 6–8 s realistic. 8 s would beat the current
architecture by 3.6× and make the Phase 2 gate.

### Alternative: batch-N-ticks

Phase A runs in parallel across K (say 32) ticks of bars at once.
Phase B still runs per-tick serially. Amortizes coordination cost over
K ticks. Cheaper surgery than slice-to-completion but hits ~10–12 s
floor because Phase B is still per-tick serial.

Preserves parity if and only if `handleBar` does not read mutable
position state within the K-tick window. Same audit required.

## Outcome

Phase 2's **6 s gate is missed** by the current per-tick fan-out
architecture. The parity infrastructure shipped in P2-2 steps 1–4
(ShardedPipeline, BuildStrategyShared/Shard, runner defer-publish,
phase split, persistent worker pool) is all the mechanical groundwork
future Phase 3 work needs — the partitioning, the shard-owned service
construction, the signal deferral, and the dispatch-order replay are
already in place.

What's missing is the **coordination model**. Phase 3's
slice-to-completion approach replaces `WaitTick()` per-tick barriers
with a single merge pass at replay end and should unblock the gate.

## Honest recommendation

**Renegotiate the Phase 2 gate to 10 s** (achievable by batch-N-ticks
inside the current architecture with a position-read audit) or commit
to the slice-to-completion refactor as Phase 3. Do not keep iterating
inside the current architecture — the contention profile says the
floor is ~15 s no matter how aggressively we tune.
