# Phase 4 — Allocation Reduction

Session: `claude-20260411-190712`, continuation of the Phase 3
slice-to-completion sprint. Goal: push wall-time closer to GATE-3's
6 s target by eliminating the GC pressure `docs/perf/p3-gate3-findings.md`
identified as the remaining ceiling.

## Trajectory

| Phase | 30 sym / 1 yr | 8 sym / 1 yr | 8 sym / 3 mo |
|---|---|---|---|
| Pre-sprint baseline | ~130 s (extrapolated) | 31.60 s | 10.14 s |
| Phase 1 direct-dispatch | 29.73 s | 7.86 s | 2.68 s |
| Phase 2 per-tick fan-out | 29.26 s | 11.29 s | 3.34 s |
| Phase 3 slice-to-completion | ~19 s | 5.39 s | 2.00 s |
| **Phase 4 allocation reduction** | **18 – 20 s** | **5.01 s** | **1.88 s** |
| GATE-3 target | 6 s | — | — |
| **Total reduction from pre-sprint** | **~85 %** | **~84 %** | **~81 %** |

## What Phase 4 shipped

All commits on `main` via the `claude-20260411-190712` sandbox:

| Commit | Content |
|---|---|
| `16eb503` | Typed StateUpdated drain + presized `sliceBars` slab |
| `817c484` | `ingestion.ProcessBarTyped` + empty IdempotencyKey |

### P4a — typed StateUpdated drain

`monitor.Service` direct-dispatch mode now pushes `IndicatorSnapshot`
values directly onto a new `pendingStateUpdates []IndicatorSnapshot`
slice instead of wrapping each one in a `domain.Event` with a
concatenated idempotency key. `Pipeline.ProcessBarPhaseA` drains via
`DrainPendingStateUpdates` and hands them straight to
`runner.HandleStateUpdatedSnap`, a new typed entry point that shares
its body (`applyStateUpdate`) with the legacy `handleStateUpdated`.

Allocation win: **~2 GB/run** removed from `monitor.HandleMarketBar`
(Event struct + `IdempotencyKey+"-state-updated"` concat + interface
boxing of the snap payload). Profile confirmed: monitor flat
allocations dropped from 3.26 GB → 676 MB.

### P4a — presize sliceBars slab

`omo-replay` now computes `totalBars := sum(len(s.bars))` once at
assembly time and pre-allocates the flat slab via `make(...,
0, totalBars)`. Appending from nil was growing the backing array via
doubling, costing ~8 GB of cumulative allocation in copies. Profile
confirmed: `main.main` flat allocations dropped from 8.13 GB →
1.37 GB.

### P4b — `ingestion.ProcessBarTyped`

New typed entry point on `ingestion.Service` that accepts a raw
`MarketBar` and returns `(sanitizedBar, ok, err)` without ever
constructing a `domain.Event`. Passthrough-mode ingestion (backtest
default) can now run zero-allocation on the hot path.

Saves ~450 MB/run of sanitized-Event allocations.

### P4b — empty IdempotencyKey

`SliceBar.toEvent` now passes `""` for the idempotency key instead of
`strconv.FormatInt(Bar.Time.UnixNano(), 36) + string(Bar.Symbol)`.
Direct-dispatch has no handler that deduplicates on the key, so the
40-byte string concat × 11 M calls was pure waste. Saves another
~450 MB/run.

## Cumulative allocation reduction

From alloc_space profile over the 30 sym / 1 yr run:

| Source | Phase 3 | Phase 4 | Δ |
|---|---|---|---|
| Total alloc_space | 19.06 GB | 12.04 GB | **-37 %** |
| `main.main` (sliceBars slab) | 8.13 GB flat | 1.37 GB flat | -83 % |
| `monitor.HandleMarketBar` | 3.26 GB flat | 0.68 GB flat | -79 % |
| `SliceBar.toEvent` | 2.08 GB flat | 1.87 GB flat | -10 % |
| `timescaledb.GetMarketBars` (startup) | 3.21 GB flat | 3.32 GB flat | unchanged |

**GC cumulative CPU** dropped from ~12 s → ~8 s, freeing ~2 cores on
average for actual Phase A work.

## Where the remaining ~14 s goes

Per pprof on the Phase 4 binary, 30 sym / 1 yr:

```
 6 s  startup — GetMarketBars + session resolver + warmup (serial I/O)
 8 s  Phase A parallel work (~22 s CPU / 2-3 effective cores after GC pauses)
 1 s  serial replay loop (collector, priceCache, signal publish)
 2 s  GC + scheduler overhead
```

**Theoretical floor inside the current architecture: ~10 s**
(startup 6 s + ideal 8 cores × 22 s / 8 = 2.75 s + replay 1 s).
Phase 4 is at 18 – 20 s, leaving another ~8 s on the table before
hitting that floor.

## Why we stopped short of the 6 s gate

Three remaining allocation hotspots each needs a larger refactor than
what fit in this session:

1. **`SliceBar.toEvent` 1.87 GB** (Phase A + replay = 11 M calls):
   eliminating requires **typed `monitor.HandleBarTyped`** + **typed
   `runner.HandleBarDirectTyped`** fast paths that accept
   `(bar, tenantID, envMode)` directly without ever constructing a
   `domain.Event`. Both functions are ~200-line hot-path bodies
   that would need careful refactoring. Estimated ~3 – 5 h of
   work + parity re-verification.

2. **`monitor.HandleMarketBar` per-bar map allocations**: regime map,
   anchor regime map, HTF data map refills. Pre-allocating and
   reusing per-symbol would save another ~500 MB and improve
   cache locality. Estimated ~2 h.

3. **`timescaledb.GetMarketBars` startup 3.3 GB**: 5.6 M bar rows
   scanned from DB. Reusing a byte buffer for the scan + pre-sizing
   the per-symbol result slices would help. Estimated ~2 h.

Together these would likely land at 10 – 12 s, still short of 6 s.

**The 6 s gate is not achievable inside the current hot-path
architecture without:**
- Moving DB loading to overlap Phase A (pipeline startup with work)
- Eliminating interface boxing of `MarketBar` in `Event.Payload`
  (requires API break)
- Struct-of-arrays indicator state in `monitor.calculator`
  (single biggest per-bar alloc source left)

Those are Phase 5 scope — multi-day rewrites.

## Parity

All three canonical workloads preserve deterministic parity across
five consecutive runs:

| Workload | RTH signals | Regime events | Wall |
|---|---|---|---|
| 8 sym / 3 mo | 155 | ~81 k | 1.88 s |
| 8 sym / 1 yr | 603 | ~160 k | 5.01 s |
| 30 sym / 1 yr | 3395 | 632 036 | 18 – 20 s |

(The 30 sym / 1 yr signal count is 3395 on the current DB snapshot,
not the historical 3386 baseline — TimescaleDB gained ~680 k rows
between measurement sessions, documented in
`p3-gate3-findings.md`.)

## Recommendation

**Ship Phase 4 as the current end state and renegotiate GATE-3 to
10 s.** The trajectory from pre-sprint to now is an 85 % reduction;
further gains require multi-day architectural work that crosses the
risk/reward threshold for this workload.

If 6 s is non-negotiable, the follow-up sprint must:

1. Add typed fast paths to `monitor.HandleMarketBar` +
   `runner.HandleBarDirect` (3 – 5 h).
2. Pre-allocate + reuse per-symbol indicator state maps (2 h).
3. Overlap DB startup with Phase A dispatch via streaming bar
   loading (3 – 4 h).
4. Re-audit interface boxing in `domain.Event.Payload` (1 day).

Expected outcome: **6 – 8 s** with those changes landed.
