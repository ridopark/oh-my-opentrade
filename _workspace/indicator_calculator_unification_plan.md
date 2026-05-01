# IndicatorCalculator unification - architectural plan

Status: SHIPPED 2026-05-01. Drafted 2026-04-30 in response to issue
#46 (EMA50/MACD precision drift between live and backtest). The
drift's root cause was that the codebase carried multiple
independently-warmed `IndicatorCalculator` instances. The
consolidation shipped as eight PRs; the migration log at the bottom
of this document records the merged commits. The audit closure note
is at `_workspace/parity_live_vs_backtest_divergence_audit.md`.

## Authoritative inventory of `monitor.IndicatorCalculator`

Three logical instances per process. Some have multiple
construction sites that are mutually exclusive (gated by
`if x == nil` or by build path).

### Live (`omo-core`, `useStrategyV2=true`)

- L1 `monitor.Service.calculator`, label `"monitor"`. Constructed
  at backend/internal/app/monitor/service.go:381. Warmup feed:
  `monitor.WarmUpAndCollect` (1m), `WarmUpHTF` (1H),
  `WarmUpNative` (HTF native), `WarmUp` (crypto 1m). Runtime
  feed: `Service.handleBarCore`. Consumers: enriched-bar bus
  events, `GetLastSnapshot`. Holds state for every `(sym, tf)`
  keyed by symbol and timeframe.
- L2 `runnerWarmupCalc`, label `"runner_warmup_boot"`. Constructed
  at backend/cmd/omo-core/warmup.go:371. The `:502` site is the
  same logical instance under an `if runnerWarmupCalc == nil`
  guard and never fires when `:371` already ran. Warmup-only;
  closure-fed by `runnerWarmupSnapshotFn` driven by
  `strategyRunner.WarmUp` and `WarmUpTF`. Consumers: per-bar
  `WarmupOnBar` callbacks during initial attach.
- L3 `strategy.Runner.htfCalcs[(sym,tf)]`, label `"runner_htf"`.
  Lazily allocated at backend/internal/app/strategy/runner.go:1723
  and seeded at :2034 by `WarmUpTF`. Runtime fed via the runner's
  own aggregator at runner.go:1727. Consumers: HTF gates in
  strategy `OnBar`, regime detector at runner.go:1730.

### Backtest (`backtest.runner.Run`)

- B1 `monitor.Service.calculator`, label
  `"monitor_backtest_<id>"` (set by `Service.TagBacktest`).
  Same construction site as L1. Warmup feed:
  `monitorSvc.WarmUp` at backtest/runner.go:1002, `WarmUpHTF` at
  :1012, bridge `WarmUp` at :1030, `WarmUpNative` at :1108.
- B2 `makeSnapshotFn` calc, label `"backtest_snapshot_fn"`.
  Constructed at backend/internal/app/backtest/runner.go:2157.
  Distinct closure from bootstrap's. Driven by
  `pipeline.Runner.WarmUpTF` and `pipeline.Runner.WarmUp`.
  Warmup-only.
- B3 same as L3 (`runner_htf`).

### Other snapshot-fn closures in the repo

There are four `makeSnapshotFn`-shaped closures total. Two are
in-process and listed above (L2, B2). Two are out of scope for
this plan because they belong to separate one-shot processes:

- backend/internal/app/bootstrap/strategy.go:469
  (`bootstrap_snapshot_fn`). NOT used during live boot. Fires
  only on mid-session strategy activation through
  `StrategyActivator.Activate`. Will be migrated by PR 5.
- backend/cmd/omo-replay/main.go:1769 (`replay_snapshot_fn`).
  One-shot replay tool. Out of scope unless replay-vs-live
  parity becomes a goal.
- backend/cmd/omo-backfill-indicators/main.go:169. One-shot SQL
  backfill. Out of scope.

## How drift originates

Three separately-warmed calcs hold state for overlapping
`(sym, tf)` keys, fed by independent paths. Empirically:

- L1 (live monitor) is fed by `WarmUpAndCollect` and `WarmUpHTF`
  in cmd/omo-core/warmup.go.
- B1 (backtest monitor) is fed by `monitorSvc.WarmUp` and
  `WarmUpHTF` in backtest/runner.go BEFORE `InitAggregators`
  runs, so the 5m aggregator path short-circuits during
  backtest warmup.
- L3 and B3 (runner.htfCalcs) are fed by `Runner.WarmUpTF` from
  the runner's own aggregator chain.

After warmup completes, the runner's view of HTF static fields
(DailyATR, NR7, Bias) is seeded into `IndicatorData.HTF` once
via `pipeline.Runner.SeedIndicatorSnapshot` at
backtest/runner.go:1120 and carried forward at
strategy/runner.go:1766. Per-bar runtime EMA/MACD divergence
comes from L3 vs B3 having been seeded by slightly different
bar streams during warmup, not from per-bar monitor reads.

Result: `B1.states[(sym,5m)].ema50 != L1.states[(sym,5m)].ema50`,
and likewise B3 vs L3, with seed-bias decay carrying the delta
forward at `(1 - 2/51)^N`. The broadened parity SQL measures
this as ~1.6e-3 EMA50 delta after 50 bars on a $200 stock.

## Target architecture

A new `internal/app/indicator/` package owns the canonical
calculator and the 1m -> HTF aggregator chain. Both are
per-context instances, not process singletons. Three consumers
read via a port:

- `monitor.Service` for enriched-bar emission.
- `strategy.Runner` for HTF gates and `WarmUpTF`.
- `bootstrap.StrategyActivator` and equivalent activation paths
  (replacing all four `makeSnapshotFn` closures).

### Architectural decisions

D1. Lifecycle: per-context (per-process for live, per-backtest
    for backtest). `indicator.Service` is constructed alongside
    `monitor.Service` and lives in the same DI container. The
    backtest runner constructs its own. Rationale:
    `Service.TagBacktest` exists today precisely because parallel
    backtests need isolated calcs. A process singleton would
    regress that.

D2. Aggregator ownership: `indicator.Service` owns the 1m->HTF
    aggregator chain. Both `monitor.Service.aggregators` and
    `strategy.Runner.aggregators` are removed. Rationale:
    aggregation is a pure transformation that two services
    consume. Putting it in monitor leaves monitor as a de-facto
    bar-routing hub, exactly the coupling we are removing.

D3. Port shape: pull-based primary, push-based optional.
    - `Update(bar) Snapshot` returns the snapshot at this bar.
    - `LastSnapshot(sym, tf) (Snapshot, bool)` for runtime reads.
    - `Subscribe(sym, tf) <-chan ClosedBar` for consumers that
      need the closed-HTF stream (the runner, post PR 6a).
    - Snapshots are returned by value (not pointer) so callers
      cannot mutate internal state.
    - Concurrency: single `sync.RWMutex` on the state map for
      v1. If runtime benchmarks show contention, shard by symbol
      hash in a follow-up. PR 1 must include a microbenchmark.

D4. Snapshot-fn collapse scope: the two in-process closures (L2
    and B2) are removed. They become thin wrappers over
    `indicator.Service.LastSnapshot` after PR 3. The bootstrap
    activation closure (bootstrap/strategy.go:469) is migrated
    in PR 5. The omo-replay closure stays out of scope.

D5. PR sequence: PR 3 (unified warmup) lands BEFORE PR 4
    (consumer migration) so consumers switch over to already
    seeded state. PR 6 is split into 6a (synchronous HTF
    callback) and 6b (event-bus subscription experiment, deferred
    if not needed).

## Migration sequencing - 8 PRs

Each PR is independently reviewable, leaves the system green on
day one, and is independently revertable.

### PR 1: Introduce `internal/app/indicator/` package

- New package with `Service` struct. Wraps the existing
  `monitor.IndicatorCalculator` for v1; collapse the wrapper in
  a follow-up if desirable.
- Exposes `Update(bar) Snapshot`, `LastSnapshot(sym, tf)
  (Snapshot, bool)`, `Subscribe(sym, tf) <-chan ClosedBar`,
  `WarmUp(bars []domain.MarketBar)`.
- Owns aggregator chain (D2). Construction is per-context
  (D1).
- Includes a microbenchmark on `Update` and `LastSnapshot` under
  realistic concurrent read load.
- No existing code paths change.
- ~250 LOC including benchmarks.

### PR 2: Wire `indicator.Service` into the monitor in shadow mode

- `monitor.Service` keeps its calc but also calls
  `indicator.Service.Update(bar)` on every bar.
- Add a parity test asserting
  `monitor.calc.states[k].EMA50 == indicator.Service.LastSnapshot(k).EMA50`
  bit-for-bit (`math.Float64bits` equality) after every bar.
- Test runs across the broadened parity SQL window for at least
  three symbols and three anchor dates.
- ~80 LOC. No behavior change for users.

### PR 3: Unified warmup path

- New `indicator.Service.WarmUp(bars)` is the single entry point
  for both live boot and backtest boot.
- backend/cmd/omo-core/warmup.go: replace `WarmUpAndCollect`,
  `WarmUpHTF`, `WarmUpNative` calls into monitor with one
  `indicator.WarmUp`. Delete `runnerWarmupCalc` and its closure
  (L2 collapses).
- backend/internal/app/backtest/runner.go: replace
  `monitorSvc.WarmUp` / `WarmUpHTF` / `WarmUpNative` with one
  `indicator.WarmUp`. Delete `makeSnapshotFn` at :2156 (B2
  collapses).
- The `WarmUpNative` double-write at warmup.go:473 (which
  currently feeds both `runnerWarmupSnapshotFn` and
  `monitor.WarmUpNative`) is structurally impossible after this
  PR.
- monitor.Service still exposes `WarmUpAndCollect` etc. as
  legacy facades that delegate to the indicator service, until
  PR 7 deletes them.
- ~250 LOC. First user-observable behavior change. Backtest
  EMA50/MACD numbers shift by up to 1.6e-3 toward live's
  numbers. Document magnitude in PR description.

### PR 4: Migrate strategy runner's `htfCalcs` to indicator service

- `runner.handleBarCore` reads HTF state via
  `indicator.Service.LastSnapshot(sym, tf)` and calls
  `indicator.Service.Update(bar)` for the 1m bar (the indicator
  service's aggregator emits the closed HTF bar internally).
- `runner.htfCalcs` field deleted.
- Existing parity tests must continue to pass; backtest/live HTF
  drift on EMA50 should drop to zero (per PR 2's bit-level test).
- ~150 LOC.

### PR 5: Migrate `bootstrap.StrategyActivator.makeSnapshotFn`

- `bootstrap/strategy.go:469` closure becomes a thin wrapper
  reading `indicator.Service.LastSnapshot`.
- Mid-session strategy activation flows through the same state
  map as boot warmup, so newly-attached strategies see the
  identical indicator stream.
- ~80 LOC.

### PR 6a: Migrate runner aggregator (synchronous callback)

- `runner.aggregators` removed. The runner registers a
  synchronous callback via `indicator.Service.Subscribe(sym, tf)`
  that fires on the SAME goroutine as the originating 1m
  `Update(bar)`. This preserves backtest determinism without
  going through the event bus.
- Equivalent to extending the existing audit-H3 `directDispatch`
  pattern that backtest already uses.
- monitor's aggregator is also removed in this PR; both
  consumers now read from the indicator service.
- ~200 LOC. The largest behavior surface in the migration.
- Risk: ordering of HTF-closed callbacks vs 1m bar handlers.
  Spec: HTF callbacks fire AFTER the 1m `Update` returns, BEFORE
  control returns to the caller. Test by feeding a deterministic
  bar sequence and asserting handler invocation order.

### PR 6b: Optional event-bus subscription path (DEFERRED)

- If a future use case needs cross-process or async HTF
  subscribers, expose `indicator.Service` as an event-bus
  publisher. NOT planned for this migration. Tracked separately.

### PR 7: Remove `monitor.Service.calculator` and legacy facades

- `monitor.Service` deletes its `calculator` field and the
  `WarmUpAndCollect` / `WarmUpHTF` / `WarmUpNative` / `WarmUp`
  methods (or they become no-ops that log).
- Test fixtures across `backend/internal/app/monitor/` updated
  to inject `indicator.Service` via a `monitortest` helper
  (precedent: `brokerporttest`).
- Concrete count of `monitor.NewService` callers (production +
  test) before PR 7 lands so reviewers can size the diff.
- ~150 LOC if the count is ~10-15 sites; revisit estimate if
  larger.

### PR 8: Lock parity via contract tests

- Re-run the broadened parity SQL post-PR-7. Acceptance:
  `math.Float64bits(ema50_live) == math.Float64bits(ema50_backtest)`
  for all `(sym, tf, anchor_date)` triples in the parity test
  set, across full RTH.
- Add a contract test on `EventEnrichedBar` payload asserting
  the snapshot shape is byte-identical to pre-migration. This
  protects chart streams, regime detection, and dashboard
  consumers.
- Document #46 closed in the audit doc.
- ~100 LOC.

## Risks

### Behavior shift in backtest numbers

PR 3 and PR 4 will shift backtest EMA50/MACD values by up to
1.6e-3 on a $200 stock toward the live numbers. No committed
performance baselines exist in the repo; tuned avwap_v4 DNA was
calibrated under pessimistic-fill semantics, not against
specific EMA values. Operator-process-only impact.

### Aggregator consolidation (PR 6a)

PR 6a is the riskiest PR. Subscribing the runner to
`indicator.Service`'s closed-HTF stream replaces both monitor's
and runner's aggregators with one shared chain. Callback
ordering is load-bearing for backtest determinism. The PR ships
with an explicit ordering spec and a deterministic-feed test;
revertable as a unit if the ordering test fails in production.

### Test churn (PR 7)

Every test that constructs `monitor.Service` needs to inject
`indicator.Service`. The unit tests inside
backend/internal/app/monitor/indicators_test.go (~50
constructions in one file) are tests OF the calc itself and do
not change. Real churn is `monitor.NewService` callers, count
TBD before PR 7 lands.

### Backtest isolation

D1 commits to per-backtest construction. `Service.TagBacktest`
remains operative. Parallel backtests stay isolated.

### Rollback

PR 4 and PR 6a are the load-bearing behavior changes; either
can be reverted to restore pre-unification HTF behavior. PR 3's
unified warmup is structural and can be preserved on revert
(it removes duplication without changing semantics if PR 2's
parity tests pass).

## Acceptance criteria (PR 8)

A migration is "done" iff all of the following hold:

1. `math.Float64bits(ema50)` equality between live and a
   backtest of the same date+symbol set across full RTH, for at
   least three anchor dates and the full universe of trading
   symbols.
2. Same for `ema21`, `macd_line`, `macd_signal`, `macd_hist`.
3. `EventEnrichedBar` payload contract test passes (no
   downstream subscriber regresses).
4. Parallel backtests produce isolated state (concurrent runs
   on different `(sym, tf)` keys do not interfere).
5. The deterministic-feed test for PR 6a's callback ordering
   passes.
6. Microbenchmark from PR 1 shows no `Update` regression vs
   pre-migration calc and no `LastSnapshot` regression vs the
   monitor's existing `GetLastSnapshot`.

## What this plan does NOT do

- It does not touch the AVWAP calc
  (`AnchoredVWAPCalc` in `domain/strategy/anchored_vwap.go`);
  already byte-identical per parity SQL.
- It does not change SimBroker fill semantics; separate scope.
- It does not migrate `cmd/omo-replay/main.go`'s snapshot
  closure; out of scope unless replay-vs-live parity becomes a
  goal.
- It does not address audit H5 (fill-timing semantics); separate
  Tier 3 concern.

## Next step

PR 1 is unblocked. The five architectural decisions are
committed (D1-D5). Open the PR with the package skeleton, the
port surface, the microbenchmark, and zero consumer migration.
Each subsequent PR is independently reviewable.

## Migration log

All eight PRs merged 2026-05-01. Each landed independently
revertable; commit SHAs are on `main`.

- PR 1 (#58 → 31081a10): introduce `internal/app/indicator/` package
- PR 2 (#59 → 445035f4): wire shadow `indicator.Service` into monitor
- PR 3 (#60 → 370fabd7): unified warmup, delete `L2`/`B2` calcs
- PR 4 (#61 → 4cb5a987): migrate runner `htfCalcs` to indicator
- PR 5 (#62 → da61586e): migrate bootstrap activator closure
- PR 6a-1 (#63 → 22617880): introduce `Subscribe` API + aggregator
- PR 6a-2 (#64 → d2231000): migrate aggregator chains onto Subscribe
- PR 7 (#65 → d92640bd): collapse `monitor.calc` into indicator
- PR 8 (this branch): parity contract tests + audit closure
