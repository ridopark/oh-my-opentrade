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

**Phase 1: COMPLETE.** All five P1 tasks shipped. Gate benchmark met parity
targets but not absolute target.

| | 8 sym / 1yr | 30 sym / 1yr |
|---|---|---|
| Pre-sprint baseline | 31.60 s | ~130 s (extrapolated) |
| Post Phase 1 | **7.86 s** | **29.73 s** |
| Phase 1 target | — | ≤15 s |
| **Gate result** | — | **❌ target missed, proceed to Phase 2** |

Per-symbol cost is now flat at ~990 ms across 8→30 symbols. The remaining
29.7 s is per-bar compute, ideal for parallelisation.

**Phase 2: in progress.** Only P2-1 (audit) is done. P2-2 through the
gate are pending, captured in [`phase2-restart-plan.md`](phase2-restart-plan.md).

**Phase 3: not started.** Conditional on Phase 2 not meeting its gate.

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

### Phase 2 — per-tick worker pool (IN PROGRESS)
Target: 30 sym / 1yr ≤ 6 s on an 8-core host. Effort estimate: **11-17
hours** (see [`phase2-restart-plan.md`](phase2-restart-plan.md) for
breakdown). Scope: shard-owned `monitor.Service` + `strategy.Runner` per
worker, worker-pool dispatch inside the tick loop, shared execution
pipeline.

| # | Task | Status | Notes |
|---|---|---|---|
| P2-1 | Inventory cross-symbol state in monitor and runner | ✅ done | [p2-1-shared-state-inventory.md](p2-1-shared-state-inventory.md) |
| P2-2 | Per-symbol sharding of hot maps (shard-owned services) | ⏳ pending | Restart plan Step 1-3 |
| P2-3 | Worker-pool dispatch inside tick loop | ⏳ pending | Restart plan Step 4 — use Option B (batch per tick + sync.WaitGroup) |
| P2-4 | Contention measurement and shard-local pools | ⏳ pending | Restart plan Step 5 — `-blockprofile` + `-mutexprofile` |
| **GATE-2** | Re-benchmark and decide on Phase 3 | ⏳ pending | Target: 30 sym ≤ 6 s on 8 cores. Parity check first (603 @ 8sym, 3386 @ 30sym). |

### Phase 3 — full sharded architecture (NOT STARTED)
Target: near-linear scaling to ~100+ symbols; unified live + backtest
scheduler. Effort estimate: **2-4 weeks.** Scope: shard-owned service
instances including execution pipeline, explicit tick scheduler,
cross-symbol coordinator actor for features that genuinely need it.

**Only start if Phase 2's gate benchmark shows the worker pool is bottlenecked
on a shared service** (execution, position monitor) rather than on per-bar
work. If Phase 2 hits its target, Phase 3 is unnecessary.

| # | Task | Status | Notes |
|---|---|---|---|
| P3-1 | Cross-symbol feature audit | ⏳ pending | Inventory features reading state from other symbols: `tideTracker`, anchor resolver caches, cross-symbol gates, portfolio-level risk, position monitor. Classify as (a) shardable with duplication, (b) needs coordinator actor, or (c) move to post-barrier execution. |
| P3-2 | Shard-owned service instances | ⏳ pending | Each worker owns `monitor.Service`, `strategy.Runner`, local event bus. Shared services (simbroker, position monitor, risk, execution) behind a single-writer channel or single lock, serialized by tick barrier. |
| P3-3 | Tick scheduler | ⏳ pending | Explicit scheduler coordinates ticks: broadcast start → wait for all → run execution → broadcast next. Backtest and live share the scheduler; backtest replaces wall-clock advance with bar-time advance. |
| P3-4 | Cross-symbol message bus | ⏳ pending | Low-latency channel for SPY/QQQ state updates that other shards subscribe to. Only touched when needed — doesn't affect per-symbol hot path. Used for tide tracker and similar. |
| P3-5 | Migrate live runner to sharded architecture | ⏳ pending | Same code path as backtest, different clock source. Prove symmetry with a replay-matches-live integration test. |

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
