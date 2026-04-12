# Phase 3 — Slice-to-Completion Plan

This document captures everything a fresh session needs to execute
Phase 3 of the backtest-scalability sprint. It assumes Phase 2 is
complete and its contention findings
([`p2-4-contention-findings.md`](p2-4-contention-findings.md)) have
been read.

## Why this plan exists

Phase 2 shipped a shard-owned monitor + runner + ingestion architecture
and a per-tick worker-pool dispatch, but GATE-2 missed **29.26 s vs
6 s target** because `runtime.futex` alone eats ~6 s on 240 k per-tick
barriers. The per-tick coordination overhead is a hard floor that no
amount of tuning can cross inside that architecture.

Phase 3 replaces the coordination model: **one barrier per run, not
one per tick.** All the per-shard service plumbing shipped in Phase 2
(`ShardedPipeline`, `BuildStrategyShared`/`BuildStrategyShard`,
`Runner.SetDeferSignalPublish`, `Pipeline.ProcessBarPhaseA`/`B`) is
reusable as-is.

## Architecture target

```
┌── replay start ────────────────────────────────────────────┐
│  Partition bars by FNV-1a(symbol) → slab[0..N-1]           │
│  Each slab is a flat chronological slice for N workers     │
└────────────────┬───────────────────────────────────────────┘
                 │
     ┌───────────┼───────────┬───────────┬───────────┐
     ▼           ▼           ▼           ▼           ▼
 ┌───────┐  ┌───────┐   ┌───────┐   ┌───────┐   ┌───────┐
 │shard 0│  │shard 1│   │shard 2│   │shard 3│   │…      │
 │bars A │  │bars B │   │bars C │   │bars D │   │       │
 │→ run  │  │→ run  │   │→ run  │   │→ run  │   │       │
 │  to   │  │  to   │   │  to   │   │  to   │   │       │
 │  end  │  │  end  │   │  end  │   │  end  │   │       │
 │→ emit │  │→ emit │   │→ emit │   │→ emit │   │       │
 │ Events│  │ Events│   │ Events│   │ Events│   │       │
 └───┬───┘  └───┬───┘   └───┬───┘   └───┬───┘   └───┬───┘
     │          │           │           │           │
     └──────────┴─────┬─────┴───────────┴───────────┘
                     ▼
         ┌───────────────────────┐
         │ k-way merge by        │
         │ (tick, shardIdx, seq) │
         └───────────┬───────────┘
                     ▼
     ┌─────────────────────────────────────┐
     │ replay signals + exits in tick      │
     │ order on main goroutine via event   │
     │ bus (risk sizer, execution, fills,  │
     │ pos monitor — UNCHANGED)            │
     └─────────────────────────────────────┘
```

Each shard goroutine runs the full bar stream for its slab. Phase A
logic from Phase 2 (ingestion + monitor + runner.handleBar +
StateUpdated drain, with `deferSignalPublish` on the runner) remains
the hot-path body — only the dispatch shell changes from "main loop
calls Dispatch+WaitTick per tick" to "main spawns N goroutines then
joins once."

Per-shard output is a **chronologically ordered stream of events**
the main goroutine will replay:

```go
type shardEvent struct {
    tickTime  time.Time   // minTime of the replay tick
    shardIdx  int         // for stable ordering on ties
    seq       uint64      // append order within the shard
    kind      shardEventKind
    payload   any
}
```

`kind` covers: `kindMarketBar` (for collector/priceCache replay),
`kindSignalCreated`, `kindRegimeShifted`, `kindSetupDetected`, plus
any other stashed `deferredStrict` / `deferredBestEffort` events.

## Replay invariants that must hold

1. **Tick ordering.** Merged events for tick T must replay before any
   event for tick T+1. Exit-rule evaluation runs between ticks on the
   main goroutine, so T's fills settle before T+1's gate checks.

2. **Within-tick ordering.** For two events with the same `tickTime`,
   the merge must use `(shardIdx, seq)` as tiebreaker consistently —
   this is deterministic and the same across runs, so parity can be
   asserted.

3. **Single-threaded cross-symbol ordering.** Within a tick, the
   single-threaded path processes bars in a fixed order (sorted
   streams). The parallel path cannot replicate that order exactly
   because shards run independently, but it CAN produce a
   deterministic alternative order. Downstream handlers must be
   order-insensitive within a tick for parity to hold — see P3-1
   audit.

4. **No position reads during shard execution.** Shard execution
   sees no fills (they're deferred to replay). Strategy gates that
   read `posLookup` would see stale (empty) position state. P3-1
   audits whether this changes observed signal counts.

## Step 1 — P3-1 audit (no code)

Grep for every `posLookup` / position read on the per-bar hot path.
The question is: does removing mid-bar position visibility change
the 3386 RTH signal count?

### Call sites to audit

1. `backend/internal/app/strategy/runner.go` `handleBar` — does it
   read `r.posLookup(sym)` to suppress duplicate entries? If yes, how
   many signals get suppressed in a typical run?

2. Strategy implementations under
   `backend/internal/app/strategy/builtin/` — do any of the 5
   built-in strategies (ORB, AVWAP, AIScalper, BreakRetest, MACD)
   call `ctx.Position()` or similar to gate entries?

3. Gate chain (if any is wired into the runner) — `gate.*` packages.

4. Risk sizer subscribes to `SignalEnriched`, runs on the main
   goroutine in the replay pass, and has its own state and cooldowns
   — it's already downstream of the merge, so its position reads are
   fine.

### Classification & resolution

Each position-read site lands in one of three buckets:

- **(a) Pure indicator-based.** No position reads. Phase 3 works
  unchanged. Most of the runner body is expected to fall here.

- **(b) Reads positions but tolerates staleness.** E.g., a gate that
  says "don't emit if an entry for this symbol already exists" — if
  we emit a duplicate, downstream (risk sizer / execution / pos
  monitor) rejects it and parity still holds after dedup at the
  fill level. Measure: does the replay signal count match after
  removing the read?

- **(c) Requires fresh position state.** E.g., stop-loss adjustment
  that reads current position size. Can't run before merge. Options:
    - Move evaluation into the replay pass (runs on main, serial).
    - Snapshot positions per epoch; shards use last-epoch snapshot.
    - Disable the gate in backtest slice mode and document.

### Deliverable

A short `p3-1-runner-position-read-audit.md` file listing every call
site, its classification, and the resolution picked for Phase 3.
Block Step 2 on this — writing the dispatch code without knowing the
classification risks parity breaks.

## Step 2 — `ShardedPipeline.RunSliceToCompletion`

New method on `backtest.ShardedPipeline`:

```go
// RunSliceToCompletion executes every bar in streams across Nworkers
// shards in parallel, collects deferred events per shard, then merges
// and replays them through the event bus in tick order on the main
// goroutine. Replaces per-tick Dispatch/WaitTick for offline replay.
func (sp *ShardedPipeline) RunSliceToCompletion(
    ctx context.Context,
    streams []*barStream,      // one per symbol, bars already loaded
    coord SliceCoordinator,    // exit rule + mark-to-market hook
) error
```

`SliceCoordinator` is a callback interface main provides so the
merged replay loop can run exit rules and simbroker updates without
the sharded pipeline knowing about those services.

```go
type SliceCoordinator interface {
    // OnTick is called once per merged tick, with events already
    // replayed into the bus for that tick. Implementations run exit
    // rules, price cache updates, and drain pending handlers.
    OnTick(ctx context.Context, tickTime time.Time) error
}
```

### Worker body (one goroutine per shard)

```go
func (sp *ShardedPipeline) shardSliceWorker(
    idx int,
    bars []barWithTick,
    out *[]shardEvent,
) error {
    shard := sp.shards[idx]
    var seq uint64
    for _, bt := range bars {
        sanitized, dropped, err := shard.ProcessBarPhaseA(bt.ctx, bt.evt)
        if err != nil { return err }
        if dropped { continue }

        // Runner has SetDeferSignalPublish(true); drain its buffer.
        if r := shard.Runner(); r != nil {
            for _, sig := range r.DrainPendingSignals() {
                *out = append(*out, shardEvent{
                    tickTime: bt.tickTime,
                    shardIdx: idx,
                    seq:      seq, kind: kindSignal, payload: sig,
                })
                seq++
            }
        }
        // Also stash the raw bar + sanitized for Phase B's collector
        // + priceCache replay.
        *out = append(*out, shardEvent{
            tickTime: bt.tickTime,
            shardIdx: idx,
            seq:      seq, kind: kindBar,
            payload: barPayload{raw: bt.evt, sanitized: sanitized},
        })
        seq++

        // Drain stashed deferred monitor events (regime/setup).
        for _, ev := range shard.TakeDeferred() {
            *out = append(*out, shardEvent{
                tickTime: bt.tickTime,
                shardIdx: idx,
                seq:      seq, kind: kindRaw, payload: ev,
            })
            seq++
        }
    }
    return nil
}
```

Requires a small `Pipeline.TakeDeferred()` accessor to drain the
`deferredStrict` / `deferredBestEffort` buffers the Phase A path
already populates.

### Slab partitioning

Reuse the existing `sp.symbolToShard` map from Phase 2. Walk every
stream, append each bar to the shard's slab with the bar's
`tickTime` (= the `nextMinTime` that would have grouped it in the
single-threaded path). Slabs are chronological because input streams
are sorted by time.

```go
for _, s := range streams {
    for _, bar := range s.bars {
        idx := sp.symbolToShard[bar.Symbol.String()]
        slabs[idx] = append(slabs[idx], barWithTick{
            tickTime: bar.Time, // 1-min bars are tick-aligned
            ctx:      ctx,
            evt:      domain.NewBacktestEvent(domain.EventMarketBarReceived, ...),
        })
    }
}
```

Memory budget: 4.95 M bars × ~160 bytes/entry = ~800 MB for the full
slabs. That's large but OK for a one-shot backtest binary.

**Optimization if memory matters**: stream bars to shards via bounded
channels instead of flat slices. Parallelism still works; shards read
from a small queue and process continuously. Skip this on first pass
— flat slices keep the code simple.

## Step 3 — k-way merge + replay

Main goroutine after the barrier:

```go
// Merge per-shard buffers. All shards are already sorted by
// (tickTime, shardIdx, seq) because each worker appends in order.
// k-way merge is a min-heap over shard head pointers.
heads := make([]int, sp.nworkers)
totalEvents := 0
for i := range perShardOut { totalEvents += len(perShardOut[i]) }

for done := 0; done < totalEvents; {
    // Find the shard with the smallest (tickTime, shardIdx, seq).
    var bestIdx = -1
    for i, h := range heads {
        if h >= len(perShardOut[i]) { continue }
        if bestIdx < 0 || lessEvent(perShardOut[i][h], perShardOut[bestIdx][heads[bestIdx]]) {
            bestIdx = i
        }
    }
    ev := perShardOut[bestIdx][heads[bestIdx]]
    heads[bestIdx]++
    done++

    // If we crossed a tick boundary, let the coordinator run exit
    // rules for the previous tick.
    if !ev.tickTime.Equal(currentTick) {
        if !currentTick.IsZero() {
            coord.OnTick(ctx, currentTick)
        }
        currentTick = ev.tickTime
    }

    // Replay event through bus (publishes to shared risk sizer,
    // execution, sim broker, pos monitor).
    sp.replayEvent(ctx, ev)
}
// Final tick flush.
if !currentTick.IsZero() { coord.OnTick(ctx, currentTick) }
```

`replayEvent` dispatches by kind:
- `kindBar` → `collector.OnBarDirect(raw)` + `priceCache.HandleBarDirect(sanitized)`
- `kindSignal` → `eventBus.Publish(SignalCreated)`
- `kindRaw` → `eventBus.Publish(ev)` (regime, setup, etc.)

For N=8 and 30 sym / 1 yr, the linear scan to find the smallest
head is O(N) per step — fine at N=8. A min-heap isn't needed but is
easy to swap in if N grows.

## Step 4 — position-state handling (P3-4)

Decided by the P3-1 audit. Likely outcomes:

### Most likely: (b) stale reads are fine

Runner `handleBar` likely checks "do I already have an open entry"
via `posLookup(sym)`. In slice-to-completion mode, the first bar
that would have triggered an entry sees `posLookup = nothing`, emits
a signal. The second bar that would have been suppressed ALSO sees
`posLookup = nothing` (because fills haven't replayed yet), emits a
duplicate signal. Downstream risk sizer has a cooldown timer
(`SetExitCooldown(3*time.Minute)`); it'll reject the duplicate.

Parity holds at the SignalEnriched level but MAY drift at the
SignalCreated level. The canonical parity baseline counts RTH
`SignalCreated` events. If that number drifts, we have two options:

1. Relax parity to `SignalEnriched` (downstream of risk sizer) —
   already the production-relevant number.
2. Move the dedup gate from `handleBar` to replay pass.

### If (c) fresh positions are required

Defer the entry-suppression gate to the replay pass. Concretely:
- Runner emits every raw signal into the per-shard buffer.
- During replay, main applies a position-aware filter before
  publishing to the bus.

This is ~30 lines of code in the replay loop and preserves the
single-threaded signal count exactly.

### Fallback: (a) snapshot positions per epoch

Partition replay into E epochs (say, one epoch per trading day).
Between epochs, barrier-sync, reconstruct position state, then kick
off the next epoch. Retains most of the parallelism win (1 barrier
per day = ~250 per 1-year run vs 240 k per-tick) while giving
strategies fresh-ish positions. Implement only if (b) parity drifts
unacceptably and (c) is too invasive.

## Step 5 — wiring + parity + GATE-3

1. Add an `omo-replay --slice-to-completion` flag (default on in
   backtest mode once proven).
2. When the flag is set, skip the per-tick `for ctx.Err() == nil`
   loop and call `shardedPipeline.RunSliceToCompletion(ctx, streams,
   coord)` instead. The coordinator is a small adapter that runs the
   existing `posMonSvc.EvalExitRules(minTime)` logic in `OnTick`.
3. Parity check: 155 / 603 / 3386 RTH signals on 8sym/3mo,
   8sym/1yr, 30sym/1yr. If parity drifts, go back to P3-4.
4. GATE-3: 30 sym / 1 yr ≤ 6-8 s on 8 cores. If met, ship Phase 3.
5. Clean up dead per-tick-dispatch code. `ShardedPipeline.Dispatch`
   and `WaitTick` can either stay (live path may reuse them later)
   or be removed.

## Effort estimate

| Step | Hours | Notes |
|---|---|---|
| P3-1 audit | 1–2 | Grep + classify. Short writeup. |
| P3-2 dispatch | 2–3 | New `RunSliceToCompletion` + slab builder + worker body. |
| P3-3 merge + replay | 2 | k-way merge, event replay switch, coordinator interface. |
| P3-4 position handling | 1–4 | Cheap if (b) holds; more if (c) needed. |
| P3-5 wiring + parity + gate | 2 | omo-replay flag + parity runs + benchmark. |
| **Total** | **8–13 hours** | One focused day. |

## Risk register

| Risk | Mitigation |
|---|---|
| Position reads cause parity drift | P3-1 audit first; defer gate eval to replay pass if needed (option c). |
| Memory pressure from 800 MB slabs | Stream bars via bounded channels (Step 2 alt path). Only needed if RSS hits a limit. |
| k-way merge hot loop too slow | Linear scan is O(N=8) per event; if profile shows it dominant, swap in min-heap. Not expected at N=8. |
| Cross-symbol ordering changes from single-threaded baseline | Within-tick event order IS deterministic (by shardIdx+seq) but differs from single-threaded sort order. Only matters if downstream handlers are order-sensitive within a tick — risk sizer / execution / sim broker / pos monitor are all mutex-protected single-writer so they aren't. Audit confirmed in P2-4. |
| Shard imbalance — SPY/QQQ heavier | FNV hash may put both in the same shard. 30 symbols / 8 shards = 3.75 avg; straggler shard becomes the wall-time floor. Mitigate by partitioning on bar count, not symbol count, after a dry run. |

## What Phase 3 does NOT do

- Does NOT change the live `omo-core` pipeline. Backtest-only again.
- Does NOT implement the unified live+backtest scheduler. That's
  Phase 4.
- Does NOT parallelize exit rules or the execution pipeline. They
  stay on the main goroutine after the merge.

## Phase 4 preview (not in scope)

Once Phase 3 ships and backtest performance is acceptable, the
remaining work is unifying the live path on the same sharded model:

- Tick scheduler abstraction (bar-clock vs wall-clock)
- Cross-symbol coordinator for SPY/QQQ tide state
- Shard-owned execution pipeline
- Live-replay integration test

Captured as future work in the main roadmap.
