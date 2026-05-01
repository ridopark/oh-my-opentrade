# IndicatorCalculator unification — architectural plan

Status: PROPOSED. Drafted 2026-04-30 in response to issue #46
(EMA50/MACD precision drift). The drift's root cause is that the
codebase carries three independently-warmed `IndicatorCalculator`
instances. This document plans the consolidation. No code changes
yet — the plan exists to surface scope, sequencing, risks, and open
questions so the migration can be sized and approved before
implementation.

## Current state: three instances per process

All three are the same type (`monitor.IndicatorCalculator`) but
each owns a separate `states map[stateKey]*symbolState`.

### Instance 1 — `monitor.Service.calculator`

- Construction: `monitor/service.go:381` (`NewIndicatorCalculator()`).
- Label: `"monitor"` in production; `"monitor_backtest_<id>"` in
  in-process backtest (set by `Service.TagBacktest`).
- Warmup feed: `Service.WarmUp` / `WarmUpAndCollect` / `WarmUpHTF` /
  `WarmUpNative` — called from the live warmup
  (`cmd/omo-core/warmup.go:309-311`) and the backtest warmup
  (`internal/app/backtest/runner.go:1002, 1030`).
- Runtime feed: `Service.handleBarCore` -> `s.calculator.Update(bar)`
  on every market bar received from the event bus
  (`monitor/service.go:834`).
- Consumers: enriched-bar events on the event bus (chart streams,
  regime detection, dashboard); `monitor.Service.GetLastSnapshot`
  used by the strategy runner's HTF warmup
  (`backtest/runner.go:1114-1116`).

### Instance 2 — `bootstrap.makeSnapshotFn`'s closure-owned calc

- Construction: `bootstrap/strategy.go:470` (`monitor.NewIndicatorCalculator()`).
- Label: `"bootstrap_snapshot_fn"`.
- Lifecycle: ONE instance shared across all `WarmUpTF` calls (the
  closure captures it). Returns `IndicatorSnapshotFunc` consumed by
  `strategy.Runner.WarmUpTF` at `runner.go:2044` to compute per-bar
  indicators that feed strategies' `WarmupOnBar` callback.
- Warmup feed: each call to `snapshotFn(bar)` runs `calc.Update(bar)`.
- Runtime feed: never (warmup-only).
- Consumers: strategy `WarmupOnBar` callbacks at attachment time.

### Instance 3 — `strategy.Runner.htfCalcs[key]`

- Construction: `strategy/runner.go:1723, 2034` (`monitor.NewIndicatorCalculator()`).
- Label: `"runner_htf"` plus optional suffix.
- Lifecycle: lazily allocated per `(sym, htfTF)` key, lives for the
  process.
- Warmup feed: `Runner.WarmUpTF` at runner.go:2037-2039 — feeds bars
  through `htfCalc.Update(bar)`.
- Runtime feed: `Runner.handleBarCore` aggregates 1m -> HTF closed
  bars, runs `htfCalc.Update(closed)` at runner.go:1727.
- Consumers: strategies' real-time `OnBar` callbacks read indicators
  for the active timeframe from `htfSnap`. Anchor regime detection
  at runner.go:1730 also consumes the snap.

## How drift originates

The three calcs are fed by independent warmup paths. Even when those
paths intend to feed the same bars in the same order, no test
enforces that they actually do. Empirically:

- `monitor.Service.calculator` and `strategy.Runner.htfCalcs` both
  hold real-time state for `(sym, htfTF)` keys. They are computed
  from THE SAME bars at runtime (the runner aggregates 1m to HTF;
  the monitor's aggregator does the same). But during warmup, the
  paths diverge:
  - Live: monitor's calc is fed via `WarmUpAndCollect` for 1m and
    `WarmUpHTF` for 1H; strategy runner's `htfCalcs` are fed via
    `Runner.WarmUpTF` from the bars `pipeline.Runner.WarmUp` collects.
  - Backtest: monitor's calc is fed via `monitorSvc.WarmUp(res.bars)`
    BEFORE `InitAggregators` runs (so the 5m aggregator path
    short-circuits); strategy runner's `htfCalcs` are fed via the
    same `WarmUpTF` path live uses.

- Consequence: `monitor.calc.states[(sym,5m)].ema50` ≠
  `runner.htfCalcs[(sym,5m)].ema50` post-warmup. Because EMA carries
  seed bias forward at `(1 - 2/51)^N` decay, even a 0.012 seed delta
  shows up as 1.6e-3 drift after 50 bars — exactly what the
  broadened parity SQL measures.

- `strategy_signal_events.payload.indicators` reads from whichever
  calc the strategy event-handler reaches at gating time. In runtime
  (which is what we're measuring), that's the runner's `htfCalcs` for
  HTF gates. Live's runner calc and backtest's runner calc are both
  fed `WarmUpTF` — but `WarmUpTF` itself produces different state
  because it depends on `monitor.GetLastSnapshot` (which IS the
  monitor's calc) for HTF static fields. Backtest's monitor calc is
  un-seeded for 5m (per the InitAggregators ordering bug), so the
  HTF static fields it serves to runner's WarmUpTF are stale, and
  the runner's calc inherits a slightly different starting point.

The drift is real, deterministic, and originates in the
warmup-path divergence between the three calcs that all should be
seeing the same bars.

## Target architecture: one calc per `(sym, tf)`

A single owning service exposes per-`(sym, tf)` state via a port.
All consumers (monitor's enrichment path, strategy runner's HTF
gates, bootstrap's WarmUpTF snapshots) read from the same state
map. The state is updated in exactly one place per bar.

Concretely:

- A new `internal/app/indicator/` service owns the canonical
  `IndicatorCalculator`.
- `monitor.Service` no longer owns a calc; calls
  `indicator.Service.Update(bar)` and reads `Snapshot(sym, tf)` for
  enrichment.
- `strategy.Runner` removes `htfCalcs`. Aggregator-driven HTF closes
  call `indicator.Service.Update(closed)` (or the indicator service
  subscribes to `EventHTFBarClosed`). Runtime gates read
  `Snapshot(sym, tf)`.
- `bootstrap.makeSnapshotFn` either disappears (warmup state read
  directly from indicator service after a unified warmup pass) or
  becomes a per-bar replay helper that doesn't own state — the state
  it would compute is already there.

Single source of truth. Drift is impossible because there's only one
state map.

## What's hard about this

1. **Warmup-time per-bar snapshots are a different shape from
   runtime-time current state.** Today's `bootstrap.makeSnapshotFn`
   gives strategies indicators AS THEY WERE at each warmup bar's
   time. The unified calc only stores latest state per `(sym, tf)`.
   To collapse, either:
   - Warm up the unified calc with the historical bar sequence in
     order, capturing per-bar snapshots into a transient store the
     strategy reads during `WarmupOnBar`, OR
   - Keep `bootstrap.makeSnapshotFn` as a transient warmup-only
     calc. It's not a runtime-state duplicator, so it doesn't
     contribute to the drift observed in #46.

   **Recommendation**: keep `makeSnapshotFn` as warmup-only. The
   real fix is collapsing instances 1 and 3, not all three.

2. **Mid-session strategy attach.** When a strategy attaches at 13:00
   ET, it expects warmup bars 12:00-13:00 to flow through its
   `WarmupOnBar`. The unified calc has those bars' state available,
   but per-bar snapshots require a backfill query to the bar
   repository. Acceptable — the bar repo already serves this for the
   existing warmup path.

3. **Test coupling.** ~50 test files construct `monitor.Service`
   directly with its own calc. Removing `Service.calculator` field
   forces every test to either inject an indicator service or use a
   shared test fixture. Mechanical but wide.

4. **The strategy runner's `htfCalcs` lazy allocation** at
   `runner.go:1721-1726` is keyed by `(sym, tf)`. The unified calc's
   state map is keyed by `stateKey{Symbol, Timeframe}`. Same shape;
   just need the runner to read from the indicator service instead
   of its own map.

5. **Aggregator ownership.** Today the monitor owns the 1m->HTF
   aggregators. The runner owns its OWN aggregators
   (`runner.aggregators`) and runs them in `runner.handleBarCore`.
   Two aggregator instances per `(sym, tf)`. Both produce the same
   closed HTF bars (from the same 1m input). The unified-calc plan
   only needs ONE aggregator chain. Either monitor's or runner's
   aggregator is removed; the other publishes closed-bar events that
   downstream reads.

   **Recommendation**: keep monitor's aggregator (it's the canonical
   bar-stream consumer). The runner subscribes to closed-HTF events
   instead of running its own aggregator. Touches the runner's
   bar-handling more than the calc collapse alone.

## Migration sequencing — 7 PRs

Each PR is independently reviewable, leaves the system in a
working state, and ships green-on-day-one. Numbered for ordering.

### PR 1: Introduce `internal/app/indicator/` package

- New package with `Service` struct wrapping the existing
  `monitor.IndicatorCalculator`. Exports `Update(bar) Snapshot` and
  `LastSnapshot(sym, tf) (Snapshot, bool)`.
- No changes to existing code paths. The new service has no
  consumers yet.
- ~150 LOC. Tests via the existing `brokerporttest` pattern (a
  contract harness for the indicator service).

### PR 2: Wire `indicator.Service` into the monitor

- `monitor.Service` keeps its calc but ALSO calls
  `indicator.Service.Update(bar)` on every bar. Equivalent state
  maintained in two places temporarily.
- Add a parity test asserting `monitor.calc.states[k] ==
  indicator.Service.states[k]` after every bar.
- ~50 LOC. No behavior change yet.

### PR 3: Migrate the strategy runner's `htfCalcs` to read from `indicator.Service`

- `runner.handleBarCore` calls `indicator.Service.Update(closedHTFBar)`
  instead of `htfCalc.Update(closed)`.
- `runner.htfCalcs` field stays but becomes unused.
- Existing parity tests must continue to pass; backtest/live drift
  on 5m EMA50 should drop measurably (#46).
- ~100 LOC. **First PR with observable behavior change for users:
  backtest indicator numbers for HTF gates may shift by ~0.001
  toward live's numbers.** Document the shift in the PR; flag for
  strategy-tuning baselines that may need re-baselining (per the
  earlier blast-radius analysis, no committed baselines exist —
  only operator notes).

### PR 4: Migrate the strategy runner's aggregator to subscribe to monitor's closed-HTF events

- `runner.aggregators` removed; `runner.handleBarCore` subscribes
  to `monitor.EventHTFBarClosed` (or equivalent) and runs gates
  against the closed bar.
- Eliminates the second aggregator chain. Only one closed-HTF stream
  exists.
- ~150 LOC. Possibly larger; the runner's bar-handling is intricate.
- **Risk**: HTF event ordering vs 1m event ordering on the bus. The
  audit's H3 (`directDispatch`) is relevant — backtest already uses
  direct dispatch to bypass the bus for monitor->runner state
  updates. This PR may need to extend that path.

### PR 5: Remove `monitor.Service.calculator`

- `monitor.Service.handleBarCore` reads from `indicator.Service`
  instead of its own calc. The duplicate `Update` from PR 2 is
  removed.
- `monitor.Service.calculator` field deleted.
- Test fixtures across `internal/app/monitor/` updated to inject
  the indicator service.
- ~200 LOC. Mostly mechanical test updates.

### PR 6: Backfill the unified warmup path

- `cmd/omo-core/warmup.go` and `internal/app/backtest/runner.go`
  call `indicator.Service.WarmUp(bars)` once at process startup.
- Both paths feed the same bar set in the same order. The
  `InitAggregators`-ordering bug from #46 root-cause investigation
  is structurally impossible: there's only one calc, only one
  aggregator chain, one warmup entry point.
- ~100 LOC. Removes the `WarmUpAndCollect` / `WarmUpHTF` /
  `WarmUpNative` / `WarmUp` proliferation in `monitor.Service`.

### PR 7: Lock parity via contract test

- Re-run the broadened parity SQL post-PR-6. Expected: zero drift
  on EMA21/EMA50/MACD across all 3 anchors and full RTH window.
- Add a parity contract test that the indicator service's state is
  byte-identical between live and a backtest of the same date.
- Document #46 closed in the audit doc.
- ~50 LOC test + documentation.

## Risks

### Behavior change in backtest numbers

PRs 3 and 6 will shift backtest EMA50/MACD values by up to 1.6e-3
on a $200 stock (per the empirical drift). Strategy-tuning numbers
may need re-baselining if any operator process compares pre-PR-3
backtest results to post-PR-3 results. Per the earlier blast-radius
analysis, no committed performance baselines exist in the repo;
operator's tuned parameters (avwap_v4 DNA) were calibrated under
pessimistic-fill semantics, not against specific EMA values, so the
impact is operator-process-only, not codebase-correctness.

### Aggregator chain consolidation

PR 4 is the riskiest — runner currently runs its own aggregator and
emits HTF bars to its own gates. Removing that aggregator and
subscribing to monitor's closed-HTF events introduces ordering
sensitivity. Audit H3 (`directDispatch`) is the precedent for this
kind of bus-bypass coordination; the PR may need to extend that
path.

### Test churn

PR 5 touches every test that constructs `monitor.Service`. The
`brokerporttest` harness pattern from Tier 3 is a useful precedent:
a `monitortest` helper package can centralize the construction.

### Rollback

Each PR is independently revertable. PR 3 and PR 5 are the
load-bearing behavior changes; either can be reverted to restore
pre-unification behavior. PR 6's warmup consolidation can be
preserved on revert (it's a structural cleanup, not a behavior
change).

## Open questions

1. **Should `bootstrap.makeSnapshotFn` collapse too?** The current
   plan keeps it as warmup-only (per "What's hard about this" item
   1). If we want to truly eliminate all three instances, we need a
   per-bar snapshot store. Decide before PR 1.

2. **Should the indicator service own the aggregators?** PR 4
   removes `runner.aggregators`. We could go further and remove
   `monitor.Service.aggregators` too, having `indicator.Service`
   own the only aggregator chain. Cleaner; larger PR 4.

3. **Should monitor's parity-diag log emission move?** Today
   `parityIndicatorLog` is in `monitor/indicators.go:13`. After
   collapse, it lives in the indicator service. Trivial but
   touches the parity-diag tooling that operators use.

4. **Should we add a feature flag?** The behavior changes in PR 3
   and PR 6 are deterministic shifts. A flag (`unified_calc:
   bool`) would let operators ramp gradually. Adds complexity;
   probably not worth it given the small magnitude (~1.6e-3).

5. **Timeline.** This is roughly 5-8 PRs across ~3-4 days of focused
   work. Worth doing as a concentrated effort or spread across
   weeks?

## Decision points needed before PR 1

- Confirm target architecture (single owning service, three
  consumers reading via interface).
- Decide on `bootstrap.makeSnapshotFn` (open question 1).
- Decide on aggregator consolidation scope (open question 2).
- Confirm the empirical EMA50 drift is the only #46 symptom worth
  closing this way (i.e., that the smaller MACD drift will follow
  for free).

## What this plan does NOT do

- It does NOT immediately fix #46. The drift remains until PR 3
  ships at minimum.
- It does NOT close audit H5 fully — fill timing / partial-fill
  semantics are separate Tier 3 concerns.
- It does NOT change SimBroker's fill model (separate scope).
- It does NOT touch the AVWAP-side calc (`AnchoredVWAPCalc` in
  `domain/strategy/anchored_vwap.go`) — that's a different state
  type with its own warmup path; already byte-identical per the
  parity SQL.

## Recommended next step

Before any code, get the open questions answered. The architecture
question (open question 1: should `makeSnapshotFn` collapse too?)
materially changes PR 1's surface area. The aggregator question
(open question 2) materially changes PR 4's risk.

Then ship PR 1 as the foundation. Each subsequent PR is reviewable
independently and can pause for re-evaluation.
