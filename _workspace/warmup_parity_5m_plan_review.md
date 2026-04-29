# Review: warmup_parity_5m_plan.md

## Verdict

APPROVE_WITH_CHANGES

The plan correctly identifies two real defects with the right root causes; the fix
direction (native HTF fetch + WarmUpNative) is sound. But several execution-level
issues will cause Phase 1 / Phase 2a to crash or silently no-op as written, the
phasing claim is wrong (Phase 1 is not safely shippable alone in the way the
plan describes), and there is a real concurrency hazard with `fillBarGaps` that
the plan ignores. Resolve the items in "Required changes" before any Edit.

## Verified claims (confirmed from code)

- `BarAggregator.Push` rejects `bar.Time.Before(a.sessionOpen)` silently with a
  counter only — no per-bar log. Confirmed at
  `backend/internal/domain/aggregator.go:87-93`. Counter is `aggRejectedSessionOpen`.
- `BarAggregator.Push` *also* rejects any bar whose `Timeframe != "1m"` at
  `aggregator.go:84-86`. The plan does not mention this gate but it is
  load-bearing for the runtime continuity argument (a 5m closed bar cannot be
  re-pushed through the same aggregator).
- Monitor's `Service.WarmUp` walks `anchorTimeframes`, looks up
  `s.aggregators[aggKey]`, `continue`s when missing, calls
  `s.calculator.Update(closed)` after `agg.Push(bar)` succeeds. Confirmed at
  `backend/internal/app/monitor/service.go:1226-1271`.
- `anchorTimeframes = []domain.Timeframe{"5m", "15m", "1h"}` at
  `service.go:25`. So monitor maintains aggregators (and thus orphan calc
  states) for **5m, 15m, AND 1h** — not just 5m as the prompt summary
  emphasizes. The plan does mention 15m in Phase 2a; it does not list 1h
  consistently in Phase 1.
- `IndicatorCalculator.Update` lazily creates a `symbolState` keyed by
  `(bar.Symbol, bar.Timeframe)` at `monitor/indicators.go:214-220`. Single
  shared `s.calculator` instance constructed once in `NewService`
  (`service.go:250`). The "two rows = two calculator instances" framing is
  correct: monitor's `s.calculator` and the runner's `r.htfCalcs[key]` are
  distinct `*IndicatorCalculator`s (runner.go:1913 allocates a fresh one per
  HTF key on first WarmUpTF).
- Parity-diag emit fires once per `Update()` call inside `IndicatorCalculator.Update`
  at `monitor/indicators.go:656-673`, gated only on `parity.Enabled()`. There
  is no caching or duplicate-emit path. Two rows for the same (sym, tf, ts)
  unambiguously means two separate `Update()` calls — and since 1m vs 5m
  state lookups go through different state-map entries, two rows at the same
  *closed-5m* timestamp must come from two distinct calculators.
- `IndicatorCalculator.Update` produces ema9/21/50/200/atr = 0 for the first
  closed bar of an un-seeded state (init flags false; gated emit at
  `indicators.go:600+`). VWAP is computed from cumulative numerator/denom and
  becomes non-zero on the first bar — matches the plan's "row A is all zeros
  except VWAP" claim.
- `EquitySpec.Required["5m"] = emaLongestPeriod*convergenceFactor = 200*4 = 800`,
  `RTHFilter=true`. Confirmed at `backend/internal/app/warmup/spec.go:24-35`.
- AVWAP (`avwap_v1.go:90`) and MACD (`macd_v1.go:56`) both declare
  `WarmupBars()=30`. Plan's calculation `30 * 5m * 1.2 = 3h` is right.
- `collectHTFWarmupReqs` at `cmd/omo-core/warmup.go:913-955` then clamps to
  `domain.PreviousRTHSession(time.Now())` (a single previous session) at
  line 448-457. With `lookback < sessionDur`, `from = prevStart`; else
  `from = to - lookback`. Plan's "78 5m bars max" is the upper bound for one
  RTH session at 5m (390 minutes / 5 = 78). Math holds.
- `Runner.WarmUpHTF` constructs a fresh aggregator with
  `warmupSessionOpen` derived from `bars1m[0].Time`'s ET date at
  `runner.go:1985-1986`, so `bar.Time.Before(a.sessionOpen)` is **not** the
  rejection cause for the runner-side path. Pre-today bars do flow through
  on the runner side. The 161-vs-800 gap is purely arithmetic: 800 1m bars
  span ~2.05 RTH sessions; 800/5 ≈ 160 5m closes. Plan's "161" is consistent.
- Backtest call ordering at `internal/app/backtest/runner.go`: `monitorSvc.WarmUp(res.bars)`
  is at line 940, `monitorSvc.InitAggregators` at line 980. Same orphan
  pattern as live. **Backtest is silently broken in the same way.**
- Replay legacy path at `cmd/omo-replay/main.go:932,951`: `monitorSvc.WarmUp`
  before `InitAggregators`. Sharded path at line 914 ALSO calls
  `InitAggregators` after `WarmUp` (line 885). Same pattern.
- Strategy code reads `AnchorRegimes["5m"]` in AVWAP, MACD, ORB, AI scalping,
  break_retest. Confirmed via grep. The plan's open-question "is monitor's 5m
  calc parity academic" is *answered no* — at least 5 in-tree strategies
  consume `AnchorRegimes["5m"]`, and `applyStateUpdate(snap)` at
  `runner.go:936-976` is the only writer (sourced from monitor's snap).
- `s.calculator` is a single shared `*IndicatorCalculator` per Service.
  Adding state keys for `(sym, "5m")`, `(sym, "15m")` post-Phase-2a does not
  contaminate the warmed `(sym, "1m")` state — they're separate map entries.

## Challenged claims

### C1. Phase 1 is NOT independently shippable as "low risk additive"

The plan says (line 152): "Phase 1 — native 5m HTF fetch (low risk, additive). ...
Validation: parity-diag still shows two rows but row B (runner's) is fully
converged."

Reality: Phase 1 does not just leave row A unchanged — it **widens** the
divergence in a way that affects strategy decisions. Today both calculators
are partially wrong but symmetrically partially wrong (both running on 161 5m
bars; both ema200 not converged). After Phase 1, runner's calc is fully warmed
(ema200 converged) but monitor's calc is still all-zeros for the first ~9 5m
bars and partially seeded for the next ~200. Strategies read `EMA9/21/50/200/ATR`
*from the runner side* (via `htfCalcs` snapshot for the strategy's own
timeframe), but they read `AnchorRegimes["5m"]` *from monitor's side* (via
`applyStateUpdate`). Post-Phase-1, those two sources reflect totally different
5m worlds:

- Runner-side: real ema200, real atr, real regime score in the strategy's
  HTF snapshot.
- Monitor-side: ema9/21/50/200/atr=0, regime classified by `RegimeDetector.Detect`
  on a zero-input snap → `RegimeBalance` (the "no signal" default at
  `regime_detector.go:109`).

So Phase 1 turns the bug from "everyone partially wrong" into "the strategies
get mismatched indicator vs. regime data". For an AVWAP entry that gates on
`AnchorRegimes["5m"].Type == TrendUp`, this means **all 5m AVWAP entries are
suppressed** until row A's calc converges (~50-200 live bars). This is a worse
behavior than today's bug — it silently turns off the 5m strategy book.

**Fix:** ship Phase 1 + Phase 2a together. Do not advertise Phase 1 as
shippable alone. The phasing in the plan should be reframed as:
"Phase 1 is land-but-do-not-tag; cut release after Phase 2a".

Alternative: reorder so Phase 2a (monitor native warmup) ships first; then
Phase 1 (runner native warmup). Phase-2a-only state is "monitor right, runner
still 161-bar approx" — still inconsistent, but the regime classification is
correct (the dominant strategy gate), and runner-side EMAs are at least
*populated* even if under-warmed. Less bad than Phase-1-only.

### C2. `warmup.Load(... "15m" ...)` will return an error, not bars

The plan calls for `warmup.Load(ctx, fetcher, warmup.EquitySpec(), sym, "15m", time.Now())`
in Phase 2a (monitor) and Phase 1 (runner). But `warmup.Load` at
`backend/internal/app/warmup/loader.go:108-118` returns
`fmt.Errorf("warmup: no required bars configured for timeframe %q", tf)` when
`spec.Required[tf]` is unset. `defaultRequired()` at `spec.go:24-31` only has
1m, 5m, 1h, 1d — **no 15m entry**.

So the proposed call will hard-fail for every symbol on 15m. The plan says
"WarmUp the 5m AND 15m monitor calc states" but never adds 15m to the spec.

**Fix:** Phase 0 (one-line precondition): add `"15m": ema21Period * convergenceFactor`
or equivalent to `defaultRequired()`. Decide the period — `ema9 * 4 = 36` is
too small; `ema21 * 4 = 84` matches the runner's `volumeSMAPeriod=20`-ish
scale; `ema50 * 4 = 200` is conservative. Pick deliberately and document.
Without this, the 15m Phase 2a code is dead.

Same applies to Phase 3's "consolidate timeframe-duration helpers" — the
spec needs to be the single source of truth before consolidation.

### C3. `aggregator-anchor` math: 161 is right but for a subtle reason

The plan asserts 800 1m bars / 5 = 161 5m bars via the runner aggregator. This
is arithmetically right but glosses over two boundary cases:

- The runner aggregator's `warmupSessionOpen` is derived from `bars1m[0].Time`
  (yesterday-or-earlier). After RTH-only filtering at the loader, the 1m
  bars span ~2 RTH sessions. The aggregator's `sessionAlignedBucketEnd` is
  `sessionOpen + k*5m` where `k = ceil(delta/5m)`. Day1 RTH covers 390 min /
  5 = 78 buckets. The overnight gap to day2's 09:30 is `(09:30 day2 - 09:30
  day1) = 23h30m = 282 5m intervals`. The bucket math is preserved across
  the gap because `23h30m / 5m` is integer (282). So day2's 09:30 1m bar
  aligns to its own canonical bucket end. Conclusion: bar timestamps in the
  closed 5m output ARE canonical.
- BUT: with no bars between session A close and session B open, the aggregator
  has `hasCur=true` at session A's last bucket (15:55-16:00), and the next
  pushed bar from session B at 09:30 day2 lands in a much-later bucket via
  `end.After(a.curEnd)` → emits the 15:55-16:00 closed bar then starts the
  new 09:30 bar. So the gap is handled cleanly. Plan's count is right.

This is fine; flagging only because future changes to RTH boundary handling
would break the arithmetic silently and the plan's tests don't pin it.

### C4. Phase 3 "consolidate timeframe-duration helpers" is scope creep

CLAUDE.md says "no speculative abstractions". Phase 3 lists six call sites that
each have their own `timeframeDuration`-style switch. The plan flags this as
"out of scope unless cheap" — but Phase 3 also commits to removing
`Runner.WarmUpHTF`, `htfReqs`, `collectHTFWarmupReqs`, and conditionally
`Service.WarmUpHTF`. That's a lot of cleanup riding on Phase 2 acceptance.

**Fix:** split Phase 3 into 3a (delete dead code that this fix obsoletes —
focused, justifiable) and 3b (consolidate timeframe helpers — defer to a
separate plan). Don't mix the two.

### C5. The "cold-start 1h slow path" claim

Phase 3's note "convert the slow path to `WarmUpNative` from Phase 2a" assumes
`Service.WarmUpHTF` (service.go:226-243) is reachable on cold-start. But that
path is called from `cmd/omo-core/warmup.go:827,832` only when `bars1h[i].EMA50 == 0`
(no fast-path seed). With omo-data running, this branch is rarely taken in
production. Removing it without a cold-DB test (the plan says "via a synthetic
cold-DB test" in Phase 3 but doesn't define one) risks shipping a regression
that surfaces only on a fresh deploy.

**Fix:** either keep `Service.WarmUpHTF` as a no-op-shim until omo-data
catches up, or hard-require the cold-DB integration test gate before Phase 3.
Plan should explicitly call this out as a Phase 3 blocker.

### C6. "Both edges held within 1-3% of prior PF" is speculation, not an SLA

The plan predicts ±1-3% PF drift after the 5m fix and "ship anyway" if PF
degrades >5%. There's no rollback criterion. The 1m fix's outcome (yesterday's
memory: AVWAP_v4 1.65→1.60, MACD 1.14→1.13) was small *and the trade count
moved up*. This time the prediction is "trade count drops 5-15%" — opposite
direction. If it drops 30% the strategy book becomes capacity-constrained
and the live system underperforms backtest expectations. The plan says
"investigate" but not "stop, revert, and re-tune".

**Fix:** add a hard rollback gate. Concrete suggestion:
- If post-fix `AVWAP_v4 trade_count < 0.75 * pre-fix` OR
  `PF < 0.95 * pre-fix`, do not enable in live. Revert.
- Cap exposure during the first week post-deploy by halving position size
  (e.g. via `live_size_multiplier=0.5`), measure parity at end-of-week,
  then size up.

### C7. The "two rows means two calculators" argument is correct but the *pre-fix logging* doesn't prove which calc is which

Plan recommends (Open question 1) adding a tag/pointer to the parity-diag
emit to identify the calculator. Good. The risk: if Defect B is misdiagnosed
and the orphan is actually somewhere else (e.g. a third calc instance via
`runnerWarmupCalc` at warmup.go:378 or `pipeline.Runner.r.htfCalcs`), the
"add tag" diagnostic should land **and report results** before Phase 1
edits. Plan says "Recommend ... Verify pre-Phase-1" but doesn't make it a
blocker.

**Fix:** make the labelled-pointer parity-diag emit a Phase 0 commit. Block
Phase 1 on its log output naming the orphan as `monitor.Service.s.calculator`.
If the log shows `runnerWarmupCalc` (the boot-time helper at warmup.go:378)
or `r.htfCalcs[sym:5m]` → the diagnosis is wrong and the fix changes.

### C8. The runtime aggregator is also session-anchored — but at a *different* session_open

`HandleMarketBar` runtime path at `service.go:751` calls `agg.Push(bar)` where
`agg = s.aggregators[aggKey]`. These aggregators were created by
`InitAggregators(syms.all, todayOpen)` at warmup.go:292 with `sessionOpen=todayOpen`
(today's 09:30 ET). At runtime, today's live 1m bars have `bar.Time >= todayOpen`,
so `bar.Time.Before(a.sessionOpen)` is false — bars accepted, 5m closes flow
to `s.calculator.Update(closed)`.

This continues to work post-Phase-2a because `s.calculator`'s `(sym,"5m")`
state (now warmed natively from 800 5m DB bars) just continues incrementally
when runtime appends each new closed 5m bar. **Important detail not in the
plan:** the natively-warmed bars and runtime-emitted bars come from
different sources (DB-stored vs runtime-aggregated), and there is a real
risk of bar-content mismatch on the boundary bar. Specifically: the most
recent natively-fetched 5m bar is the one omo-data wrote to the DB; the
next runtime-emitted 5m bar comes from the 1m feed. If the 1m bar at
`warmupEnd-tfDur` was suspect/spike-filtered differently in the omo-data
aggregation than it will be at live runtime → boundary 5m bar drift.

**Likely small** (omo-data's aggregation matches `barbackfill.AggregateHTF`
logic which does pure OHLCV aggregation) but the plan should call this out
as a known acceptable residual.

### C9. `fillBarGaps` runs concurrently with `warmupIndicators` — race condition

`backend/cmd/omo-core/main.go:34-35`:
```
go fillBarGaps(ctx, cfg, infra, log)  // background goroutine
warmupIndicators(ctx, cfg, infra, svc, syms, log)
```

`fillBarGaps` does `repo.SaveMarketBars(ctx, htfBars)` for 5m/15m/1h aggregates
(warmup.go:236) racing with `warmupIndicators`'s `warmup.Load(... "5m" ...)`
read at the same DB. The amount of new 5m data depends on how long omo-core
has been down — typically minutes-to-hours. Outcomes:

1. Gap-fill finishes first → `warmup.Load` reads complete 5m up through "now".
   Best case.
2. Gap-fill in progress when `warmup.Load` reads → `warmup.Load` gets stale
   tail (missing the last K 5m bars before "now"). The boot+1 bar may be
   absent. Live runtime then aggregates the gap from the live 1m feed — but
   only for 1m bars arriving live, not for the 1m bars that gap-fill is
   inserting. Result: **the period between latest 5m DB bar and first live
   5m close has no warmup coverage**, and live's `s.calculator.Update`
   feeds an incremental EMA on a slightly-discontinuous bar series.

The 1m fix had the same race but was less visible because 1m gap-fill IS the
write path for 1m and there's no aggregation step. For 5m the aggregation
step adds a window where 5m DB lags 1m DB.

**Fix:** Phase 1 should `time.Sleep`-wait or sync-block on `fillBarGaps`
completion before `warmup.Load(... "5m" ...)`. Alternatives:
- Make `fillBarGaps` a synchronous call (move from goroutine).
- Add a done-channel `infra.barGapFillDone` and read from it before the 5m
  load.
- Bypass DB for the gap window: read 1m up to "now" and aggregate in-process
  to fill the 5m tail (mirrors `barbackfill.AggregateHTF`).

The plan does not address this. Without a fix, Phase 1's bench will look
clean (DB has all the data by the time tests run) but production will hit
the race on every restart.

### C10. The Phase 2a `WarmUpNative` ordering vs `lastHTFSnaps[sym:1h]`

Phase 2a's `WarmUpNative(sym, "1h", bars)` needs to populate `s.lastHTFSnaps[sym:1h]`
to satisfy `buildHTFMap` at service.go:1178. The plan mentions `lastHTFSnaps`
in passing but doesn't enumerate the side-state. There's also `s.anchorRegimes[sym:tf]`
(used by HandleMarketBar's regime-shift detection at service.go:766) — if
`WarmUpNative` doesn't populate it, the first live 5m close will fire a
spurious `EventRegimeShifted` (going from "no anchor regime stored" to
"first detected"). Plan does call this out for 5m at line 73 ("without the
latter, the first live 5m close after warmup will compute regime shift") —
good. Same applies for 15m and 1h. Plan should say so.

## Missing concerns

### M1. `Required["1d"] = 800` is asserted by EquitySpec but the live boot uses `dailyBarsNeeded=200`

`cmd/omo-core/warmup.go:777` defines `dailyBarsNeeded = 200` separately from
the spec. After Phase 3 cleanup, are there two sources of truth for daily?
The 1m parity fix already routed equities through `EquitySpec` for the base
timeframe but the 1d EMA200 path (line 840-902) is unchanged. Phase 3
"removes Service.WarmUpHTF only after migrating cold-start 1h path" — but
1d isn't even mentioned. Plan should explicitly say "1d remains on the
SetStaticHTFData path; do not migrate".

### M2. Test plan is implementation-pinning, not invariant-pinning

The proposed `TestWarmUp_AggregatorRejectsPreTodayBars` test pins
`aggRejectedSessionOpen` counter. That's testing the BUG, not the FIX. A
better invariant test:

```
TestMonitorWarmUp_5mCalcStateSeeded:
  Service.InitAggregators + Service.WarmUpNative(sym, "5m", 800-bars)
  → s.calculator.states[(sym,"5m")].ema200Init == true
  → calculator.Update(probe_bar) returns ema200 ≈ expected value within 0.1%
```

The aggregator-rejection test is fine as a regression pin (so future "let's
allow pre-today bars" doesn't silently break the warmup contract), but the
primary correctness test should be on the seeded state, not the rejection.

### M3. No check for `runnerWarmupSnapshotFn` reuse between symbols

The boot path uses a single `runnerWarmupCalc = monitor.NewIndicatorCalculator()`
at warmup.go:378 for ALL symbols' WarmUpHTF calls. That's a shared `IndicatorCalculator`
where `Update(bar)` keys on `(bar.Symbol, bar.Timeframe)` — different symbols
get different state entries, fine. But after Phase 1's native fetch path,
each symbol's HTF warmup will run through `WarmUpTF(sym, tf, bars, runnerWarmupSnapshotFn)`
where `runnerWarmupSnapshotFn` updates `runnerWarmupCalc` — also keyed by
(sym, tf). Confirm with a test that the snapshot function is called with the
correct symbol on each bar (it is — `bar.Symbol` is preserved). The plan
doesn't pin this in tests.

### M4. Crypto path edge case

The plan says crypto is "out of scope, preserve current behavior by routing
crypto through `warmup.CryptoSpec` rather than `EquitySpec`". But the live
`crypto warmup` path at warmup.go:336-356 uses a hardcoded 600-min window,
NOT `warmup.CryptoSpec`. After Phase 1, what does the new code path do for
crypto symbols on 5m? The plan needs to specify: skip the native HTF fetch
for crypto, OR add a crypto-specific HTF native fetch using `CryptoSpec`.
Currently the plan is silent. If Phase 1 wires the new path uniformly via
`syms.all`, crypto symbols will get an `EquitySpec` HTF fetch (RTH-filtered
on a 24/7 instrument → wrong). Source of bugs.

### M5. SSE warmup-replay event suppression

`SetSuppressProgressEvents(true)` is set at warmup.go:376 and cleared at
:589 (`SetSuppressProgressEvents(false)` after `ClearAllPendingStates`). The
new monitor `WarmUpNative` calls (Phase 2a) will trigger... what events?
`s.calculator.Update(bar)` itself doesn't emit events — only logs the
parity-diag line. But Phase 2a's "store the regime in `s.anchorRegimes[sym:tf]`"
might fire an `EventRegimeShifted` if naively wired. Plan correctly notes
this for live closes but should also confirm `WarmUpNative` does NOT emit
events. (The mirror, `Service.WarmUpHTF` at service.go:226-243, does not
emit events. Phase 2a should match.)

### M6. Race between Phase 2a `WarmUpNative` lock and live event arrival

`WarmUpNative` will hold `s.mu` while iterating 800 bars per symbol. Total
hold = 800 bars × ~5µs Update = 4ms per symbol × 30 symbols = 120ms blocking.
Live `HandleMarketBar` waits on the same mutex. If a 1m bar arrives during
warmup, it queues. Plan should call out this latency budget. Probably
acceptable (the existing `Service.WarmUp` 1m path already does this for 800
1m bars × 30 symbols = ~120ms) but should be noted.

### M7. No `omo-replay` audit beyond a stub

Plan says `omo-replay` (line 41) "exhibits the same pattern" with line
number `?`. The replay path is split across legacy and sharded branches
(`main.go:885,914,932,951`). Plan should specify which lines change in
each branch, especially the sharded path since it's the production replay
path.

## Required changes before implementation

Ordered by importance:

1. **Add 15m to `defaultRequired()`** in `backend/internal/app/warmup/spec.go`.
   Without this, `warmup.Load(... "15m" ...)` errors and Phase 2a 15m code is
   dead. Pick a period (suggest `ema21Period * convergenceFactor = 84` since
   15m anchors short-term swing detection, not 200-period reversion).
   This is a Phase 0 precondition.
2. **Make Phase 1 + Phase 2a a single shippable unit, or reverse the order.**
   Phase-1-only worsens the bug for `AnchorRegimes`-gated entries (the
   majority of 5m strategy gating). See C1.
3. **Resolve the `fillBarGaps` race** — block `warmup.Load("5m"...)` on gap-fill
   completion, or read 1m and aggregate in-memory for the tail. See C9.
4. **Convert "verify orphan identity" to a Phase 0 blocker.** Add a
   labelled-pointer to the parity-diag emit, capture one production log,
   confirm `monitor.Service.s.calculator` is the un-warmed instance. Only
   then proceed. See C7.
5. **Add a hard rollback criterion** to the strategy re-validation step. Trade
   count drop >25% OR PF degradation >5% must trigger revert, not "ship and
   investigate". See C6.
6. **Clarify crypto handling in Phase 1.** Either skip native HTF for crypto
   symbols or wire `CryptoSpec`. Don't leave it implicit. See M4.
7. **Specify line numbers in `omo-replay/main.go`** for both legacy and
   sharded branches. The "?" in the plan masks two distinct edit sites.
   See M7.
8. **Replace `TestWarmUp_AggregatorRejectsPreTodayBars` with the seeded-state
   test described in M2.** Keep the rejection test as a secondary regression
   pin only.
9. **Drop Phase 3b** (timeframe-duration consolidation) from this plan. Open a
   separate plan for it. CLAUDE.md no-speculative-abstractions rule. See C4.
10. **Phase 3 cold-start 1h migration must list the synthetic cold-DB test as
    a hard prerequisite**, not a recommendation. See C5.

## Optional improvements

- The plan's "WarmUp the strategy runner via the new path AND have it cache
  the fetched bars for monitor reuse" is correct but cache lifetime isn't
  specified. Suggest: a `map[symbol]map[tf][]MarketBar` scoped to the
  `warmupIndicators` function call only — discard after both monitor and
  runner have been warmed. Avoids accidental retention.
- After Phase 2a, consider adding a one-time post-warmup assertion: for each
  active symbol, log `s.calculator.states[(sym,"5m")].ema200` at INFO. If any
  is zero, the warmup is silently incomplete. One log line, easy revert,
  high diagnostic value.
- Consider deleting `Service.WarmUpHTF` (service.go:226-243) — currently a
  one-call helper for the cold-start 1h slow path that Phase 3 plans to
  migrate. After migration it has no callers. Plan acknowledges this in
  Phase 3 but should note the helper is dead-stripped, not retained as a
  shim.
- The `convertAnchorRegimesInto` reuse pattern (`runner.go:1225-`) suggests
  the team has had recent allocation/perf scrutiny. Add a benchmark for the
  new `WarmUpNative` path to confirm no regression.

## Files inspected (absolute paths)

- /home/ridopark/src/oh-my-opentrade/_workspace/warmup_parity_5m_plan.md
- /home/ridopark/.claude/projects/-home-ridopark-src-oh-my-opentrade/memory/project_warmup_window_parity.md
- /home/ridopark/src/oh-my-opentrade/backend/cmd/omo-core/warmup.go
- /home/ridopark/src/oh-my-opentrade/backend/cmd/omo-core/main.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/domain/aggregator.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/strategy/runner.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/monitor/service.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/monitor/indicators.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/warmup/spec.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/warmup/loader.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/backtest/runner.go
- /home/ridopark/src/oh-my-opentrade/backend/cmd/omo-replay/main.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/strategy/builtin/avwap_v1.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/strategy/builtin/macd_v1.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/strategy/builtin/orb_v1.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/strategy/builtin/ai_scalping_v1.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/strategy/builtin/break_retest_v1.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/barbackfill/aggregate.go
