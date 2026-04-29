# Live-vs-backtest indicator parity on the 5m timeframe — plan (v2)

Revised after reviewer pass and runtime evidence from Phase 0 deployment (commit `c9c59208`).

## Problem statement

Yesterday's (2026-04-28) unified `warmup.EquitySpec` closed parity for 1m. Validation was byte-level on SPY 1m only; 5m wasn't checked. This morning the user observed live and backtest taking different trades. Investigation found three distinct calculator instances are emitting per-bar parity-diag rows for 5m, none of them properly seeded:

- `monitor` (`monitor.Service.s.calculator`) — all-zero state for `(sym, "5m")`. Aggregator silently rejects pre-today bars during warmup so 5m state never gets seeded.
- `runner_htf` (`strategy.Runner.r.htfCalcs[sym:5m]`) — short EMAs converge but **EMA200 = 0**. Seeded from 161 5m bars aggregated out of 800 1m bars; canonical spec demands 800.
- `runner_warmup_boot` (the boot-time helper closure at `cmd/omo-core/warmup.go:378`) — leaks from warmup into runtime; emits one extra row per live 5m close with the same EMA200 = 0 as `runner_htf`.

5m strategies (AVWAP_v4, MACD_only_v1) gate on EMA21/50/200 and `regime_score`. The runner-side calc is missing EMA200; the monitor-side calc (which feeds `AnchorRegimes["5m"]` consumed by 5+ strategies) has zero everything → `RegimeBalance` (zero-input default), silently suppressing 5m entries that gate on `TrendUp`. Result: the divergent trade set the user observed.

## Phase 0 — DONE (commit `c9c59208`)

Two prerequisites that unblock the rest:

1. **Added 15m to `warmup.EquitySpec().Required`** (`backend/internal/app/warmup/spec.go`). Sized at `ema15mPeriod * convergenceFactor = 50 * 4 = 200`. 15m anchors mid-horizon regime classification (ADX, EMA50 slope, BB bandwidth) — none need EMA200 convergence; matches 1h's reasoning.

2. **Added `Label` field to `IndicatorCalculator`** (`backend/internal/app/monitor/indicators.go`) and tagged each construction site so the parity-diag emit names which instance fired which row. Labels assigned: `monitor` (service.go), `runner_htf` (runner.go ×2), `runner_warmup_boot` and `runner_warmup_orb` (warmup.go ×2), `replay_snapshot_fn`, `backtest_snapshot_fn`, `bootstrap_snapshot_fn`.

Phase 0 confirmed both originally hypothesized defects (A and B) and surfaced a new one (C — `runner_warmup_boot` runtime leak).

## Defect A — `runner_htf` warms from 161 aggregated 5m bars instead of 800 native

**Files:** `backend/cmd/omo-core/warmup.go:417-427`, `backend/internal/app/strategy/runner.go:1962-2014` (`Runner.WarmUpHTF`).

**Mechanism:**
- `warmup.go:417-427` iterates `syms.all` and calls `svc.strategyRunner.WarmUpHTF(sym, warmupBarsCache[sym], runnerWarmupSnapshotFn, loc)`. `warmupBarsCache[sym]` was loaded from `warmup.Load(EquitySpec, "1m", ...)` at line 308 — exactly 800 1m RTH bars.
- `Runner.WarmUpHTF` (`runner.go:1962`) creates a fresh aggregator with `warmupSessionOpen` derived from `bars1m[0].Time` so the aggregator accepts pre-today bars. Pushes 800 1m bars through; emits ~161 5m closes, then calls `r.WarmUpTF(sym, "5m", htfBars, ...)`.
- 161 < `EquitySpec().Required["5m"] = 800`. EMA200 cannot converge (200 × 4 = 800 is the convergence target).

**Runtime evidence (2026-04-28, post-Phase-0 deploy):**
```
calc=runner_htf ts=2026-04-28T09:35 sym=AMD ema9=330.4 ema21=333.3 ema50=334.9 ema200=0 vwap=340.0
```

**Why the secondary `htfReqs` path doesn't save us:** `warmup.go:438` calls `collectHTFWarmupReqs` (line 913-955). For each registered HTF strategy, lookback = `Strategy().WarmupBars() × tfDur × 1.2`. AVWAP and MACD declare `WarmupBars()=30`, giving `lookback = 3h`, then clamped to one previous RTH session at `warmup.go:448-457` (~78 5m bars max). Total post-WarmUpHTF + htfReqs ≈ 239 bars. Still short.

## Defect B — `monitor` calc never seeded for `(sym, "5m")`

**Files:** `backend/cmd/omo-core/warmup.go:292,320`, `backend/internal/app/monitor/service.go:1226-1271`, `backend/internal/domain/aggregator.go:87-93`.

**Mechanism:**
- `warmup.go:292` calls `svc.monitor.InitAggregators(syms.all, todayOpen)`. The aggregators for 5m/15m/1h are anchored to **today's 09:30 ET**.
- `warmup.go:320` calls `svc.monitor.WarmUp(bars)` with 800 pre-today 1m RTH bars.
- `Service.WarmUp` walks bars, calls `s.calculator.Update(bar)` for the 1m state (this works), then for each anchor TF pushes the 1m bar through `s.aggregators[sym:tf].Push(bar)`.
- `BarAggregator.Push` at `aggregator.go:87-93` rejects every bar where `bar.Time.Before(a.sessionOpen)`, silently incrementing `aggRejectedSessionOpen`. **All 800 pre-today bars are dropped.** `s.calculator.Update(closed_5m)` is never called during warmup.
- At runtime, `IndicatorCalculator.Update` lazily creates a `symbolState` for `(sym, "5m")` on first call. The first call comes from the live 5m close path (`service.go:764`). That state has `ema9Init=false` etc. → all-zero indicators except VWAP (cumulative-from-zero on the first bar).

**Runtime evidence:**
```
calc=monitor    ts=2026-04-28T09:30 sym=AMD rsi=0 ema9=0 ema21=0 ema50=0 ema200=0 vwap=316.77
calc=monitor    ts=2026-04-28T09:35 sym=AMD rsi=0 ema9=0 ema21=0 ema50=0 ema200=0 vwap=318.46
```

**Why this matters even though strategies on 5m read the runner's calc:** monitor's snap flows into `r.indicators[sym].AnchorRegimes` via `applyStateUpdate` (`runner.go:936-976`). Five+ strategies read `AnchorRegimes["5m"]` — AVWAP_v1, MACD_v1, ORB_v1, AI scalping, break_retest. With monitor's 5m calc emitting zero indicators, `RegimeDetector.Detect` returns `RegimeBalance` (the zero-input default at `regime_detector.go:109`). 5m AVWAP entries gating on `TrendUp` are silently rejected.

The same defect applies to `(sym, "15m")` and `(sym, "1h")` — all three anchor TFs share the aggregator-rejection failure mode.

## Defect C — `runner_warmup_boot` leaks into runtime (NEW)

**Files:** `backend/cmd/omo-core/warmup.go:378-400` (boot helper closure), `backend/internal/app/strategy/runner.go:1925` (consumer).

**Mechanism:**
- `warmup.go:378` constructs `runnerWarmupCalc = monitor.NewIndicatorCalculator()` and assigns a closure `runnerWarmupSnapshotFn` that captures it. The closure is intended as a one-shot helper to produce `IndicatorData` snapshots from warmup bars.
- That same closure is reused as the `IndicatorSnapshotFunc` argument to `Runner.WarmUpTF`/`WarmUpHTF` for every warmup call site, then **passed again** through other code paths and ultimately invoked during live 1m and 5m bar processing.
- Confirmed via post-Phase-0 log evidence: `runner_warmup_boot` emits 1292 today-dated 5m rows (live runtime), not just warmup-window rows.

**Why it produces wrong values:** `runnerWarmupCalc` was warmed from 161 aggregated 5m bars during boot — same Defect A under-warming. EMA200 = 0. As live 5m closes increment its state, EMA200 will eventually populate, but for hours after restart it's wrong, and it's a redundant calculator emitting log volume + a potential drift source against `runner_htf` (which receives the same bars via a different path).

**Open question:** identify the exact code path that calls `runnerWarmupSnapshotFn` at runtime. The closure is captured in `warmup.go:379`; it's passed to `WarmUp/WarmUpHTF/WarmUpTF` during warmup. If those methods don't store it, something else is. Likely candidates: `Runner.snapshotFn` field assignment, `bootstrap/strategy.go` reuse, or a closure leak via a strategy instance. Investigation required before Phase 2.

## Backtest-side check

`backend/internal/app/backtest/runner.go:940` calls `monitorSvc.WarmUp(res.bars)` BEFORE `monitorSvc.InitAggregators` at line 980. At line 940 `s.aggregators` is empty, so the aggregator branch in `Service.WarmUp` (`service.go:1237`) silently skips with `if !exists { continue }`. **Same orphan exists on backtest side.**

`omo-replay`: legacy/sharded paths at `cmd/omo-replay/main.go:932,943` (`monitorSvc.WarmUp`) before `:951` (`monitorSvc.InitAggregators`). `pipeline.Runner.WarmUp` at `:965` then runs separately. **Same orphan pattern.**

The "backtest is right" framing in the original prompt is wrong. Backtest is silently broken in the same way; the divergent trade set surfaces because some downstream gate eats the under-warmed input differently in the two processes (likely a regime-edge sensitivity).

## Fix design

### Phase 1 (combined with Phase 2a — see Phasing) — native HTF fetch + monitor seeding

Cannot ship Phase 1 alone. Reviewer C1: post-Phase-1, runner-side EMA200 converges but monitor-side stays all-zeros, so strategies see fully-warmed indicators paired with `RegimeBalance` regime → 5m AVWAP/MACD entries silently suppressed for the first ~200 live 5m bars. Phase 1 alone makes the bug worse, not better. Land Phase 1 + Phase 2a as a single shippable unit.

#### Phase 1 component — `runner_htf` native fetch

**Edits (live, `backend/cmd/omo-core/warmup.go`):**
1. Replace the `WarmUpHTF`-from-1m loop at lines 417-427 with a per-HTF-timeframe native fetch:
   ```
   for sym in syms.all:
     for tf in collectHTFTimeframes(svc.strategyRunner, sym):
       bars, _ := warmup.Load(ctx, fetcher, equityOrCryptoSpec(sym), sym, tf, time.Now())
       svc.strategyRunner.WarmUpTF(sym, tf, bars, runnerWarmupSnapshotFn)
       cachedHTFBars[sym][tf] = bars  // for monitor reuse below
   ```
   `equityOrCryptoSpec(sym)` selects `EquitySpec()` or `CryptoSpec()` based on `sym.IsCryptoSymbol()` (see Crypto handling).
2. Delete `Runner.WarmUpHTF` call (line 422). Source of double-feed if both paths run.
3. Delete the `htfReqs := collectHTFWarmupReqs(...)` block at lines 438-473. Superseded.
4. Hoist `collectHTFTimeframes` from `internal/app/bootstrap/strategy.go:500` to a runner-package helper (single signature: `func (r *Runner) HTFTimeframesForSymbol(sym string) []string`).

**Edits (backtest, `backend/internal/app/backtest/runner.go`):**
1. After the existing `monitorSvc.WarmUp` (line 940) and `pipeline.Runner.WarmUp` (line ~1019), add a per-HTF-tf batch fetch alongside `batch1m, batch1d, batch1h` at lines 758-779.
2. Replace `pipeline.Runner.WarmUpHTF` (line 1036) with per-tf `pipeline.Runner.WarmUpTF(sym, tf, htfBars, snapshotFn)`.
3. Use `warmup.TrimWithBoot1` to apply RTH filter and boot+1 bar.

**Edits (replay, `backend/cmd/omo-replay/main.go`):**
1. After `monitorSvc.WarmUp` at lines 932/943 and `pipeline.Runner.WarmUp` at line 965, add per-HTF-tf native fetch + `WarmUpTF` calls.
2. Same pattern as backtest. Both legacy and sharded branches must be edited.

**No fallback to the 1m-aggregated path.** Per CLAUDE.md no-shims rule. If DB returns < 800 5m bars, log degradation and proceed (matches existing 1m behavior).

#### Phase 2a component — `monitor` calc native seeding

In `backend/internal/app/monitor/service.go`, add:
```go
// WarmUpNative seeds the calculator's per-(sym, tf) state directly from
// native HTF bars, bypassing s.aggregators. The session-aligned aggregator
// rejects pre-today bars (aggregator.go:87) so the 1m-warmup path silently
// fails to seed 5m/15m/1h calc states. This method is the single seeding
// path for HTF indicator state.
func (s *Service) WarmUpNative(sym domain.Symbol, tf domain.Timeframe, bars []domain.MarketBar) int
```

Behavior:
1. Locks `s.mu`.
2. Iterates bars, calls `s.calculator.Update(bar)` for each.
3. After loop: stores last snap into `s.lastHTFSnaps[sym:tf]` (for `buildHTFMap`), detects regime via `s.regimeDetector.Detect(lastSnap)` and stores into `s.anchorRegimes[sym:tf]`. Without the latter, the first live close fires a spurious `EventRegimeShifted` (no-prior → first-detected transition).
4. Does NOT publish events.

Wire in all three call sites:
- `cmd/omo-core/warmup.go`: after the equity Load loop (line 326), iterate HTF timeframes and call `svc.monitor.WarmUpNative(sym, tf, cachedHTFBars[sym][tf])` reusing the bars fetched by Phase 1.
- `backtest/runner.go`: after the new HTF batch fetch.
- `cmd/omo-replay/main.go`: after the new HTF native fetch (both branches).

### Phase 2b — eliminate `runner_warmup_boot` runtime leak (Defect C)

Investigation first (Phase 0 follow-up): identify the runtime call path that invokes `runnerWarmupSnapshotFn` post-warmup. Static reading isn't enough. Add temp instrumentation if needed (e.g., panic-on-call after warmup completes, captured in a non-prod restart).

Once identified, fix:
- If the closure is being passed into a runner field (e.g., `r.snapshotFn`), null it after warmup completes or replace with a runtime-safe variant.
- If the closure is captured by strategy instances, ensure each strategy has its own runtime indicator pipeline (not the warmup helper).
- Cleanest: refactor warmup so `runnerWarmupCalc` lives only inside `warmupIndicators` scope and is impossible to leak. After warmup, `runnerWarmupCalc = nil` and any holders fail loudly.

### Phase 3 — cleanup

3a (focused, justified by this fix):
- Remove `Runner.WarmUpHTF` (`runner.go:1962-2014`) once Phase 1 callers migrate. Callers: `cmd/omo-core/warmup.go:422,524`, `backtest/runner.go:1036,1569`, `omo-replay/main.go:?`. The `:524` call (post-ORB warmup re-feed) needs equivalent native-fetch treatment for current-session bars.
- Remove `htfReqs` / `collectHTFWarmupReqs` / `htfWarmupReq` from `cmd/omo-core/warmup.go:907-955`.
- Remove `Service.WarmUpHTF` (service.go:226-243) **only if** the cold-start 1h path migration (next bullet) lands first.
- Migrate cold-start 1h slow path (`warmup.go:790-836`, called when stored EMA50 is 0) to `Service.WarmUpNative`. **Hard prerequisite:** synthetic cold-DB integration test gating this change. Do not ship Phase 3a's `Service.WarmUpHTF` removal without it.

3b (timeframe-duration helper consolidation): **dropped from this plan**. Open as separate task. Per CLAUDE.md no-speculative-abstractions rule.

## Validation recipe

1. **Pre-fix baseline (already captured Phase 0).** With `PARITY_DIAG_ENABLED=true` and labelled emit, three calc instances visible: `monitor` (all-zero), `runner_htf` (EMA200=0), `runner_warmup_boot` (EMA200=0, runtime-leaked).

2. **Post-Phase-1+2a.** Restart live + run replay over the same window. Diff `IndicatorSnapshot` rows for `(sym, tf=5m, ts)`:
   - `monitor` and `runner_htf` should both have full state (rsi, ema9/21/50/200, atr, bb_pct_b, regime_score) byte-identical to each other.
   - Same byte-equality between live and replay for both calcs.
   - `runner_warmup_boot` rows: still under-warmed if Phase 2b hasn't shipped — accept as known. **Block ship if it's emitting today-dated rows after Phase 2b.**

3. **Acceptable residuals.** VWAP and VWAP_SD on the very first 5m close of the live session — session-bounded VWAP semantics, separate from warmup pipeline.

4. **15m and 1h validation.** Run the same diff for `tf=15m` and `tf=1h`. The 15m fix is identical to 5m (same orphan, same gap). 1h is fixed by the existing `SeedHTFSnapshot` fast path for symbols with stored EMA values; cold-start slow path covered by Phase 3 prereq.

5. **Boundary-bar drift (reviewer C8).** The most recent natively-fetched 5m bar is from omo-data's aggregation; the next runtime-emitted 5m bar comes from the live 1m feed. Boundary-bar OHLCV may differ if 1m spike-filtering diverges. Likely small (`barbackfill.AggregateHTF` is pure OHLCV aggregation), but track in the parity-diag diff and document any consistent residual.

## Strategy re-validation

Re-run year-long backtests for AVWAP_v4 and MACD_only_v1 over 2025-04-22 to 2026-04-25, full active-symbol universe (per `project_active_strategies.md`).

**Hard rollback gates** (no judgment calls; revert deploy if any trip):
- `AVWAP_v4 trade_count < 0.75 × baseline`
- `AVWAP_v4 PF < 0.95 × baseline`
- `MACD_only_v1 trade_count < 0.75 × baseline`
- `MACD_only_v1 PF < 0.95 × baseline`

Baselines from yesterday's memory: AVWAP_v4 PF=1.60 trades=3754; MACD_only_v1 PF=1.13 trades=981.

If any gate trips: revert (`git revert`), re-tune the affected strategy under the new spec, re-run backtest, ship retuned version. Do **not** ship the fix and re-tune later — the live system would underperform expectations during the gap.

**Soft expectations** (informational, don't gate):
- AVWAP_v4: trade count drops 5-15% (previously fail-open under-warmed gates now properly reject), PF drift ±1-3%.
- MACD_only_v1: smaller impact, PF drift <5%.

**Live deploy safety:** halve position size (`live_size_multiplier=0.5`) for the first week post-deploy. Measure parity at week-end against backtest expectations, then size up.

## Blast radius

- **Live boot timing.** Phase 1 adds N-symbols × M-HTF-timeframes DB queries. With `GetMarketBarsMulti` batching, ~50ms per timeframe. 5m + 15m + 1h = ~150ms additional. Negligible.
- **Memory.** 800 5m + 800 15m + 200 1h × ~30 symbols × ~80 bytes/bar = ~5 MB heap. Negligible.
- **Test impact.** `runner_test.go` tests asserting `r.htfCalcs[sym:5m]` post-`WarmUpHTF(800 1m,...)` will need expected counts updated. `backtest/runner_test.go` golden snapshots tied to under-warmed values will need golden updates.
- **Failure mode: short DB coverage.** Symbols with < 800 5m bars in `market_bars` log degradation, proceed with what's available. Matches existing 1m behavior.
- **Aggregator continuity post-Phase-2a.** Runtime aggregator at `s.aggregators[sym:5m]` is anchored to `todayOpen`; live 1m bars `bar.Time >= todayOpen` flow through and aggregate to 5m closes that get fed via `s.calculator.Update(closed)`. The natively-warmed state continues incrementally — same calculator, just with proper history.
- **Crypto.** `CryptoSpec().Required["5m"] = 800` matches equity. Existing crypto warmup window at `cmd/omo-core/warmup.go:337` is 600 minutes (~120 5m bars), inconsistent with spec. Out of scope for this fix; document as separate follow-up. Phase 1 must route crypto symbols through `CryptoSpec` (not `EquitySpec`) to avoid RTH-filtering 24/7 instruments.
- **`s.mu` lock hold during `WarmUpNative`.** 800 bars × ~5µs Update × 3 timeframes × 30 symbols ≈ 360ms total hold. Live `HandleMarketBar` queues during that window. Acceptable (matches existing `Service.WarmUp` 1m path latency budget).

## fillBarGaps race condition (reviewer C9 — must address)

**File:** `backend/cmd/omo-core/main.go:34-35`:
```go
go fillBarGaps(ctx, cfg, infra, log)   // background goroutine
warmupIndicators(ctx, cfg, infra, svc, syms, log)
```

`fillBarGaps` (warmup.go:158-258) writes 1m bars to `market_bars`, then aggregates and writes 5m/15m/1h to the same table (warmup.go:236). Phase 1's `warmup.Load(ctx, fetcher, EquitySpec, sym, "5m", time.Now())` reads `market_bars` for 5m. **Race window:** if gap-fill is mid-flight during the HTF Load, the 5m read returns a stale tail (latest 5m bars missing for the gap-fill window). Live runtime then aggregates the gap from incoming live 1m bars — but only those, not the 1m bars gap-fill is inserting concurrently. **Result: a discontinuous 5m series feeding monitor and runner calcs**.

The 1m warmup already had this race but was less visible because 1m is the gap-fill write path, not a downstream aggregation.

**Fix options (pick one):**
1. **Sync `fillBarGaps` before `warmupIndicators` for HTF reads only.** Convert `fillBarGaps` to return after 1m-bars-saved + HTF-aggregation complete, expose a `done` channel/flag, and gate Phase 1's HTF Load on it.
2. **In-memory aggregation for the tail.** After `warmup.Load("5m"...)`, fetch 1m bars for the period `[lastSeenIn5mDB, time.Now()]` and aggregate via `barbackfill.AggregateHTF` to fill the tail. Same logic as gap-fill but in-process.
3. **Move `fillBarGaps` out of goroutine entirely.** Synchronous warmup → fillBarGaps → indicator warmup. Boot latency increases by gap-fill duration (typically seconds to a minute).

**Recommendation: option 1.** Cleanest separation, smallest diff, no duplication of aggregation logic. Boot latency unchanged for the typical case (gap-fill faster than indicator warmup) and bounded for the worst case.

## Test plan

**warmup package (`backend/internal/app/warmup/`):**
- `loader_test.go`: `TestLoad_5m_AppliesRTHFilter` and `TestLoad_5m_TruncatesToRequired` — mirror existing 1m tests with `tf="5m"` and `Required=800`.
- `loader_test.go`: `TestLoad_15m_TruncatesToRequired` — sized at 200.
- `loader_test.go`: `TestTrimWithBoot1_5m_PicksLastPreCutoffBar`.

**monitor package (`backend/internal/app/monitor/`):**
- `service_warmup_test.go` (new): `TestWarmUpNative_SeedsCalculatorState_5m` — feed 800 synthetic 5m bars via `WarmUpNative`, assert:
  - `s.calculator.states[(sym, "5m")].ema200Init == true`
  - `s.calculator.Update(probe_bar)` returns ema200 within 0.1% of expected
  - `s.lastHTFSnaps[sym:5m]` populated
  - `s.anchorRegimes[sym:5m]` populated
  - **No events published** (verify via mock event bus)
- Same triplet for 15m and 1h.
- Regression pin (secondary): `TestWarmUp_AggregatorRejectsPreTodayBars_DoesNotSeedHTFCalc` — pins the bug we're fixing so a future change doesn't silently re-introduce the silent rejection. Pre-today bars + `InitAggregators(syms, todayOpen)` + `WarmUp(bars)` → assert `s.calculator.states[(sym, "5m")]` is **not** populated (i.e., the rejection is real). Primary correctness test is the seeded-state test above; this is the regression pin only.

**strategy/runner package:**
- `runner_warmup_test.go`: `TestWarmUpTF_Native5m_SeedsHTFCalc` — `InitAggregators` + `WarmUpTF(sym, "5m", 800-bars, ...)` → `r.htfCalcs[sym:5m].states[(sym, "5m")].ema200Init == true`.

**backtest package:**
- `runner_parity_test.go` (new): tiny replay (1 symbol, 1 day), capture parity-diag log lines, assert that for every closed 5m bar in the test window all `IndicatorSnapshot` field values match between `monitor` and `runner_htf` calcs — and that `runner_warmup_boot` does NOT emit any rows after Phase 2b.

**Existing test impact:**
- `runner_test.go`: tests asserting `r.htfCalcs[sym:5m]` bar count after `WarmUpHTF(800 1m, ...)` — update from 161 to 800.
- `backtest/runner_test.go`: golden EMA200 / regime values for 5m bars — update goldens.

## Phasing

**Phase 0 — DONE (commit `c9c59208`).** 15m in spec, labelled IndicatorCalculator instances, both defects A and B confirmed via runtime evidence, defect C surfaced.

**Phase 1+2a — single shippable unit.** Native HTF fetch (Phase 1) + monitor `WarmUpNative` (Phase 2a) + fillBarGaps sync (must address). Validation: `monitor` and `runner_htf` byte-equal at every 5m close, live ↔ replay byte-equal too. Strategy re-validation with hard rollback gates. Live deploy at half size for one week. **Do not ship Phase 1 alone.**

**Phase 2b — `runner_warmup_boot` runtime leak (Defect C).** Investigation first; fix once root cause is known. Lower priority than 1+2a but must ship before declaring 5m parity closed.

**Phase 3a — cleanup.** Remove dead code obsoleted by 1+2a. `Service.WarmUpHTF` removal gated on cold-DB integration test (Phase 3 prereq).

**Phase 3b — DROPPED from this plan.** Open separate.

## Open questions / out of scope

- **Defect C runtime call path.** Identify exactly how `runnerWarmupSnapshotFn` is invoked at runtime. Phase 2b investigation.
- **Crypto 5m warmup window.** `cmd/omo-core/warmup.go:337` uses 600 minutes; spec says 800 bars. Out of scope for equity fix; separate plan.
- **`Required["1d"] = 800` vs `dailyBarsNeeded = 200` at warmup.go:777.** Two sources of truth for daily. The 1m parity fix migrated 1m to spec but left 1d on `dailyBarsNeeded`. Plan does not migrate 1d — explicitly out of scope; document and defer.
- **`anchorTimeframes` mutability.** `monitor/service.go:25` is a package-level `[]Timeframe`. Read-only by inspection; should add a test asserting it.
- **Universe of strategies reading `AnchorRegimes`.** Confirmed 5+ readers (AVWAP_v1, MACD_v1, ORB_v1, AI scalping, break_retest). Run grep before ship to verify no new readers added since.

## Critical files

- /home/ridopark/src/oh-my-opentrade/backend/cmd/omo-core/warmup.go
- /home/ridopark/src/oh-my-opentrade/backend/cmd/omo-core/main.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/monitor/service.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/strategy/runner.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/backtest/runner.go
- /home/ridopark/src/oh-my-opentrade/backend/cmd/omo-replay/main.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/warmup/spec.go (Phase 0)
- /home/ridopark/src/oh-my-opentrade/backend/internal/app/warmup/loader.go
- /home/ridopark/src/oh-my-opentrade/backend/internal/domain/aggregator.go (Defect B mechanism)
