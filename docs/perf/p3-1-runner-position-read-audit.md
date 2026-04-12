# P3-1 Runner / Gate Position-Read Audit

Goal: enumerate every call site on the per-bar Phase A hot path that
reads mutable position state, and classify each as safe/unsafe for
slice-to-completion execution.

## Method

```
grep -RIn 'posLookup|PositionLookup|\.Position\(|LookupPosition' \
  backend/internal/app/strategy/ \
  backend/internal/app/monitor/   \
  backend/internal/app/gate/      \
  backend/internal/app/backtest/
```

Filtered to call sites reachable from `Pipeline.ProcessBarPhaseA`.

## Findings

### 1. `strategy.Runner.handleBar` → `ReconcileSignals` (the only on-path reader)

**Location**: `backend/internal/app/strategy/runner.go:1281`

```go
allSignals = r.filterByAllowedDirections(allSignals)
allSignals = ReconcileSignals(allSignals, r.posLookup, r.logger)
```

**What it does** (`strategy/signal_reconciler.go`):

- Entry **sell** signal + existing **LONG**  → converts to CLOSE_LONG exit
- Entry **buy**  signal + existing **SHORT** → converts to CLOSE_SHORT exit
- Same-direction entry on existing position → passthrough
- Non-entry signals (exit / adjust / flat)  → passthrough

It transforms **reversal entries** into **close-position exits** so the
risk sizer produces a flat-position order instead of a double-down.

**What goes wrong in slice-to-completion**. Each shard runs to
completion with its own deferred signal buffer. No fills have
replayed yet — `posLookup` returns "no position" for every bar. The
reconciler passes through every reversal entry as an ENTRY instead of
converting it to an EXIT. The downstream risk sizer then opens a new
position (possibly in the opposite direction) instead of closing the
existing one. Parity breaks at both the signal-type level and the
trade-outcome level.

**Classification**: **(b) reads positions, needs deferred eval**.

**Resolution for P3-2/3/4**. Move the reconciliation pass from
`runner.handleBar` to the replay loop. Runner emits the raw
(un-reconciled) signal into the per-shard buffer; the replay pass
calls `ReconcileSignals` just before publishing each signal, using
the real `posLookup` that reflects every fill replayed so far during
the merge.

Implementation shape:

1. Add `deferReconcile bool` flag on `Runner` (parallel to
   `deferSignalPublish`). When set, `handleBar` skips the
   `ReconcileSignals` call and emits raw signals.
2. The replay loop in `ShardedPipeline.RunSliceToCompletion` holds
   the original `PositionLookupFunc` from the caller. Before
   publishing each signal, it runs `ReconcileSignals([]{sig},
   lookup, logger)` and publishes the (possibly-transformed)
   result.

This matches the single-threaded call sequence exactly — signals get
reconciled in the same order, with the same position snapshots —
because the replay loop processes events in merge order and fills
replay serially.

### 2. `signal_debate_enricher.go` (not on backtest hot path)

**Location**: `backend/internal/app/strategy/signal_debate_enricher.go:140`

Reads positions inside `handleSignal` to add context to the AI
debate prompt. Only fires when `--no-ai=false`. `omo-replay
--backtest` uses `--no-ai=true` by default, so this handler is
disabled for GATE-3.

**Classification**: **(a) not on path**.

**Resolution**: none needed for backtest. If someone ever runs
`omo-replay --backtest --no-ai=false`, the enricher runs on the main
goroutine during replay (it's a bus subscriber) and sees live
positions — still correct.

### 3. `strategy/builtin/*` (five strategies)

```
grep -RIn 'ctx\.Position|posLookup|LookupPosition' backend/internal/app/strategy/builtin/
```

**Result**: no matches.

The built-in strategies (`ORB`, `AVWAP`, `AIScalper`, `BreakRetest`,
`MACD`) emit signals based purely on indicators + bar data. They
don't read position state from the `strategy.Context` interface.

**Classification**: **(a) pure indicator-based**.

### 4. `monitor.Service` — no position reads

```
grep -RIn 'posLookup\|PositionLookup\|\.Position(' backend/internal/app/monitor/
```

**Result**: no matches. Monitor deals with indicator + regime +
aggregator state keyed on symbol. Never reaches for positions.

**Classification**: **(a) pure indicator-based**.

### 5. `gate.*` chain

omo-replay does not wire a monitor gate chain
(`bootstrap.WireGateChain` is never called in
`backend/cmd/omo-replay/main.go`), so gate-level position reads are
not on the hot path for backtest. The execution gate chain reads
positions but runs downstream of risk sizer on the main goroutine
during replay — it's already in the serial path.

**Classification**: **(a) not on path**.

## Summary

| Site | Bucket | Resolution |
|---|---|---|
| `runner.handleBar` → `ReconcileSignals` | (b) defer needed | Move reconciler to replay loop via `Runner.deferReconcile` flag + inline `ReconcileSignals` call before publishing each signal |
| `signal_debate_enricher.handleSignal` | (a) not on backtest path | None |
| 5× strategy builtins | (a) pure indicator | None |
| `monitor.Service.*` | (a) pure indicator | None |
| Gate chain | (a) not wired in replay | None |

**One code change unblocks Phase 3**: add a `deferReconcile` flag to
`Runner` and a matching reconciliation step in the replay loop. No
strategy internals need to change. No gate-chain surgery needed.
Expected parity: **exact** — the slice-to-completion path produces
the same `SignalCreated` stream as single-threaded after the replay
runs reconciliation with live positions.

## Pre-requisites for P3-2

- `Runner.SetDeferReconcile(bool)` setter (mirrors
  `SetDeferSignalPublish`).
- `ReconcileSignals` is already exported — replay pass can call it
  directly from `backend/internal/app/strategy`.
- Replay loop needs the caller's `PositionLookupFunc` — passed in
  via `SliceCoordinator` or a dedicated field on the coordinator.
