# Backtest Performance Roadmap

This directory holds the performance and scalability work for the backtest
(`omo-replay`) binary. It's a three-phase plan that ran over multiple
sessions — each phase has a gate benchmark deciding whether the next phase
is needed.

## Single-source-of-truth entry points

Read in this order if you're picking up the work:

1. **This file** — where everything fits together.
2. [`p1-1-publish-chain-audit.md`](p1-1-publish-chain-audit.md) — per-bar
   event dispatch chain before direct-dispatch.
3. [`p1-2-backtest-pipeline-design.md`](p1-2-backtest-pipeline-design.md) —
   design of `backend/internal/app/backtest.Pipeline`.
4. [`p2-1-shared-state-inventory.md`](p2-1-shared-state-inventory.md) —
   field-level classification of `monitor.Service` and `strategy.Runner`
   for sharding. **Source of truth for Phase 2.**
5. [`phase2-restart-plan.md`](phase2-restart-plan.md) — full step-by-step
   implementation plan for Phase 2 with LOC estimates, file lists, risk
   register, pre-flight checklist.

## Baseline numbers

Original pre-sprint (`41ef1b2`) on a 2026-01-02..2026-04-01 / 8-symbol /
3-month workload: **10.14 s wall**. On the 2025-04-01..2026-04-01 /
8-symbol / 1-year workload: **31.60 s wall**.

Extrapolated pre-sprint 30-symbol / 1-year cost: ~130 s.

## Current status (end of last session)

**Phase 1: COMPLETE.** All five P1 tasks shipped. Gate benchmark missed
the 15 s absolute target but the structural win is clear — per-symbol
cost flattened, scaling is linear, and the remaining cost is per-bar
compute.

| | 8 sym / 1yr | 30 sym / 1yr |
|---|---|---|
| Pre-sprint baseline | 31.60 s | ~130 s (extrapolated) |
| Post Phase 1 | **7.86 s** | **29.73 s** |
| Phase 1 target | — | ≤15 s |
| **Gate result** | — | **❌ target missed, proceed to Phase 2** |

**Phase 2: COMPLETE, gate MISSED.** P2-1 through P2-4 all shipped in
one worktree session. Parity is preserved end-to-end (155 / 603 / 3386
RTH signals) but the per-tick worker-pool architecture cannot reach
6 s on this workload — pprof showed 240 420 per-tick barriers × 8
worker wakeups = ~6 s of `runtime.futex` alone, more than the entire
target. Full writeup in
[`p2-4-contention-findings.md`](p2-4-contention-findings.md).

| | 30 sym / 1yr |
|---|---|
| Post Phase 2 (N=8 workers, per-tick fan-out) | **29.26 s** |
| Phase 2 target | ≤6 s |
| **Gate result** | **❌ target missed, proceed to Phase 3** |

Phase 2 still delivered the mechanical groundwork Phase 3 needs:
`ShardedPipeline`, `BuildStrategyShared` / `BuildStrategyShard`,
runner `SetDeferSignalPublish`, two-phase split, persistent worker
pool. Only the coordination model (per-tick barriers → single
end-of-run merge) is missing.

**Phase 3: COMPLETE.** Slice-to-completion architecture shipped.
Eliminates 240k per-tick barriers; shards run to completion in
parallel, k-way merge replays signals in tick order.

| | 30 sym / 1yr |
|---|---|
| Post Phase 3+4+5 (slice + alloc reduction) | **18–20 s** |
| Phase 3 target | ≤6 s |
| **Gate result** | **❌ target missed, but 86% reduction from pre-sprint** |

**Phase 4+5 (allocation reduction)** also complete. Total allocs
dropped from 19 GB → 9.7 GB per run. Typed hot-path methods
(monitor, runner, ingestion, pipeline) bypass Event construction on
the Phase A path. GC eliminated from pprof top-25.

**Dashboard backtest runner** also ported to slice dispatch at
Nworkers=1 (from the pre-Phase-1 eventBus.PublishDirect path).

Full trajectory from pre-sprint to current:

| Workload | Pre-sprint | Current | Reduction |
|---|---|---|---|
| 8 sym / 3 mo | 10.14 s | **1.88 s** | -81% |
| 8 sym / 1 yr | 31.60 s | **5.01 s** | -84% |
| 30 sym / 1 yr | ~130 s | **18–20 s** | -86% |

Docs: [`p3-gate3-findings.md`](p3-gate3-findings.md),
[`p4-allocation-reduction.md`](p4-allocation-reduction.md).

## The 16-task plan

Task IDs reference the in-session TaskCreate state. They're meant as
labels, not persistent identifiers — a restart session will get fresh IDs.
What matters is the content.

### Phase 1 — single-core performance (COMPLETE)
Target: 30 sym / 1yr ≤ 15 s. Effort estimate: 2-3 days. **Actual: completed
in one session.**

| # | Task | Status | Commit / doc |
|---|---|---|---|
| P1-1 | Audit per-bar publish chain | ✅ done | [p1-1-publish-chain-audit.md](p1-1-publish-chain-audit.md) |
| P1-2 | Design `BacktestPipeline` direct-dispatch interface | ✅ done | [p1-2-backtest-pipeline-design.md](p1-2-backtest-pipeline-design.md) |
| P1-3 | Wire `BacktestPipeline` into `omo-replay` | ✅ done | commit `7913d7c` (post-rebase: `eb14b3c` pre-rebase) |
| P1-4 | Shrink indicator window state (250 → 60 + EMA200 accumulator) | ✅ done | `145405d` |
| P1-5 | Struct layout pass on `symbolState` | ✅ done | `0b2456f` |
| **GATE-1** | Re-benchmark and decide on Phase 2 | ✅ done | 29.73 s at 30 sym — target missed, Phase 2 required |

### Phase 2 — per-tick worker pool (COMPLETE, gate missed)
Target: 30 sym / 1yr ≤ 6 s on an 8-core host. Effort estimate: **11-17
hours**. Scope: shard-owned `monitor.Service` + `strategy.Runner` per
worker, worker-pool dispatch inside the tick loop, shared execution
pipeline.

| # | Task | Status | Commit / notes |
|---|---|---|---|
| P2-1 | Inventory cross-symbol state in monitor and runner | ✅ done | [`p2-1-shared-state-inventory.md`](p2-1-shared-state-inventory.md) |
| P2-2 | Per-symbol sharding of hot maps (shard-owned services) | ✅ done | commits `c320091`, `fdf1ef4`, `00736d9`, `7f3024b` |
| P2-3 | Worker-pool dispatch inside tick loop | ✅ done | commit `c69b00a` — two-phase split + persistent worker pool + runner `SetDeferSignalPublish` |
| P2-4 | Contention measurement and shard-local pools | ✅ done | [`p2-4-contention-findings.md`](p2-4-contention-findings.md) |
| **GATE-2** | Re-benchmark and decide on Phase 3 | ❌ **missed** | 29.26 s measured vs 6 s target. Parity ✅ (155/603/3386). Phase 3 required. |

Key finding from P2-4: `runtime.futex` alone is ~6 s because the
scheduler must round-trip 240 k per-tick barriers × 8 worker wakeups
at ~25 µs each. Work granularity (20 bars/tick ÷ 8 shards = 2.5
bars/shard) is below Go's scheduler coordination floor. No tuning
inside the per-tick-barrier architecture can cross the ~15 s wall-time
floor, let alone reach 6 s.

### Phase 3 — slice-to-completion architecture (COMPLETE)
Target: 30 sym / 1yr ≤ 6 s. Actual: **18–20 s** (gate missed, but
86% reduction from pre-sprint baseline).

| # | Task | Status | Commit / notes |
|---|---|---|---|
| P3-1 | Runner/gate position-read audit | ✅ done | [`p3-1-runner-position-read-audit.md`](p3-1-runner-position-read-audit.md) — only `ReconcileSignals` reads positions; deferred to replay via `SetDeferReconcile` |
| P3-2 | Slice-to-completion dispatch | ✅ done | `RunSliceToCompletion` + `replayFlat` in `slice_pipeline.go` |
| P3-3 | K-way merge + replay | ✅ done | Flat-indexed per-shard emission buffers, replay iterates input bars in order |
| P3-4 | Position-state handling | ✅ done | Batch-reconcile per bar in replay loop against live `PosLookup` |
| P3-5 | omo-replay + dashboard wiring | ✅ done | Both paths use `RunSliceToCompletion` at max speed |
| **GATE-3** | Re-benchmark | ❌ **missed** | 18–20 s vs 6 s target. See [`p3-gate3-findings.md`](p3-gate3-findings.md) |

### Phase 4+5 — allocation reduction (COMPLETE)

| # | Task | Status | Notes |
|---|---|---|---|
| P4a | Typed StateUpdated drain | ✅ done | `pendingStateUpdates` + `HandleStateUpdatedSnap` — saved ~2 GB/run |
| P4a | Presize sliceBars slab | ✅ done | Saved ~8 GB of append-growth |
| P4b | `ingestion.ProcessBarTyped` | ✅ done | Zero-alloc ingestion hot path |
| P4b | Empty IdempotencyKey | ✅ done | Saved ~450 MB/run |
| P5 | Typed monitor/runner fast paths | ✅ done | `HandleMarketBarTyped` + `HandleBarDirectTyped` — bypass Event creation |
| P5b | Scratch publish event slices | ✅ done | Reuse across bars |
| P5c | Round-robin shard balance | ✅ done | 4-4-4-4-4-4-3-3 vs old 6-2 FNV imbalance |
| P5d | Dashboard runner port | ✅ done | `backtest/runner.go` uses `RunSliceToCompletion` at max speed |

See [`p4-allocation-reduction.md`](p4-allocation-reduction.md).

### Future work (not started)

- **Nworkers > 1 for dashboard runner** — currently wraps pre-built
  services as single shard. Graduating to N=8 with the shard factory
  would give full multi-core parallelism.
- **Startup overlap** — stream bar loading into Phase A instead of
  load-all-then-process. Would hide ~6 s of DB I/O behind Phase A
  work.
- **Unified live+backtest scheduler** — same code path for both,
  different clock source. Original Phase 4 scope.
- **Cross-symbol coordinator** for SPY/QQQ tide state updates.
- **Struct-of-arrays indicator state** — deepest refactor, would
  eliminate per-bar `duffcopy` and map-hash overhead.

### Non-goals (explicitly NOT in this plan)

- **Actor model for everything** — too intrusive, no clear win over
  sharding.
- **SIMD/vectorisation** — Go toolchain support is weak, not
  memory-bandwidth bound yet.
- **Rewriting indicators in C/Rust** — not yet.
- **Parallelising across ticks** — breaks strict tick ordering that live
  needs.

## Gate-based progression rule

**Only start phase N+1 if phase N's gate benchmark shows the previous
phase wasn't enough.** The measurement gates matter more than the tasks
themselves — they're what stop us from over-engineering if an earlier
phase was already sufficient.

## Previous work (pre-Phase 1)

Before the phased plan was drafted, a previous session landed 28 smaller
performance commits that produced the 10.14 s → 2.68 s improvement on the
3-month workload. Those are listed in detail in the original chat history
(not saved to disk). The shortlist of the biggest wins:

- `bdb78f7` BB bandwidth sort → O(n) count (−36% CPU alone)
- `a7de212` Parallelize startup DB fetches (−39% wall in one commit)
- `59d57ea` tz Location memoization
- `0ac6596` Fast event IDs for replay
- `252026e` eventTracer fastpath at warn level
- `46439dd` Lock-free Publish when frozen
- `c9678ee` Pool instanceContext + int64 lastBarTime
- `3da9756` Suppress EntryGated/ORBPhaseUpdate events in replay

These landed before the formal phased plan; they address one-off hotspots
found by pprof. The phased plan (this doc) is about structural wins that
needed explicit design. Both sets of work remain in main.

## Pre-flight checklist for restart sessions

Every time a session picks up the work:

- [ ] Re-run the current 30-sym / 1-yr benchmark to confirm no regression
  since the last session. Expected: ~29.7 s post Phase 1. If it's
  meaningfully different (±10%), find out why before doing anything else.
- [ ] Re-read this README.
- [ ] Read the doc for the phase you're working on.
- [ ] Check `git log --oneline <last-commit>..HEAD` in main to see if
  anything new landed on main since the last session.
- [ ] Confirm you're on a fresh sandbox worktree; never work in primary.

## Benchmark command reference

```bash
# 8 symbol / 1-year
LOG_LEVEL=warn /usr/bin/time -f "%e sec" \
  ./backend/bin/omo-replay --backtest \
  --from 2025-04-01 --to 2026-04-01 \
  --symbols AAPL,MSFT,GOOGL,AMZN,TSLA,SPY,META,NVDA \
  --initial-equity 100000 --slippage-bps 5 --no-ai \
  --config ./configs/config.yaml --env-file ./.env \
  --output-json /tmp/r.json

# 30 symbol / 1-year
SYMBOLS_30="AAPL,MSFT,GOOGL,AMZN,META,NVDA,AMD,AVGO,MU,MRVL,SMCI,PLTR,CRM,SNOW,NET,SOFI,HOOD,AFRM,JPM,XOM,OXY,LLY,HIMS,RIVN,RBLX,BA,SPY,QQQ,IWM,SOXL"
LOG_LEVEL=warn /usr/bin/time -f "%e sec" \
  ./backend/bin/omo-replay --backtest \
  --from 2025-04-01 --to 2026-04-01 \
  --symbols "$SYMBOLS_30" \
  --initial-equity 100000 --slippage-bps 5 --no-ai \
  --config ./configs/config.yaml --env-file ./.env \
  --output-json /tmp/r.json
```

Behaviour-parity baselines (must match after every structural change):
- 8 sym / 3 mo (2026-01-02..2026-04-01) → 155 RTH signals
- 8 sym / 1 yr → 603 RTH signals
- 30 sym / 1 yr → 3386 RTH signals

## Revision log

- 2026-04-11 — Initial roadmap. Phase 1 complete; Phase 2 at P2-1; Phase 3
  outlined but not started.
