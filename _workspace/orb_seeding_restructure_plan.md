# ORB-warmup re-feed contamination — restructure plan (Phase D)

Eliminate the post-canonical-warmup re-feed of monitor's `s.calculator` so live's
1m calc state at runtime entry equals replay's, by construction. This extends
the parity work tracked in `_workspace/parity_residual_cleanup_plan.md` (Phase A
landed) and closes the residual flagged in
`project_warmup_window_parity.md` ("New residual surfaced 2026-04-28 evening").

## Goal

Establish the invariant: every 1m bar from `warmup.EquitySpec`'s 800-RTH window
flows through `monitor.s.calculator.Update` exactly once during boot. ORB
tracker recovery for today's session reuses the calc state and per-bar snaps
already produced by canonical warmup, with zero additional `calc.Update` calls
on `s.calculator`. The proof: a unit test that takes a deterministic 800-bar
fixture, runs the new boot path, and asserts `s.calculator` byte-state at the
end equals the byte-state produced by `WarmUpAndCollect(bars)` alone — i.e.,
ORB seeding is observably a no-op against calc state. Plus the existing
parity-diag harness (`/tmp/parity_diff2.py`) showing the 774 (calc=monitor,
sym, tf, ts) keys that previously diverged on the LAST emission converging
byte-for-byte with replay.

## Files touched

- `backend/cmd/omo-core/warmup.go` — delete the `WarmUp(orbBars)` +
  `WarmUpORB(orbBars)` block at lines 510-522. Replace with a single
  `SeedORBFromHistory(sym, todaysSnaps)` call where `todaysSnaps` is sliced
  out of the snaps already collected during canonical warmup. The
  `fetchIntraSessionBarsWithGapFill` call goes away entirely for the monitor
  path — today's session bars are a subset of the canonical 800-bar window
  when the canonical window's `warmupEnd = time.Now()`. (See "Open questions"
  for the gap-fill concern.)
- `backend/internal/app/monitor/service.go` —
  - Promote the existing `WarmUpAndCollect(bars)` (line 191) to be the boot
    path's seeding entry; it already returns `[]BarSnapshot`. The current
    `WarmUp(bars)` becomes a thin wrapper `WarmUp(bars) int { snaps :=
    s.WarmUpAndCollect(bars); s.finalizeWarmUp(lastBar, lastSnap); return
    len(bars) }` so behavior for crypto and other call sites is preserved.
    Note: `WarmUpAndCollect` today does NOT touch aggregators, anchorRegimes,
    or lastSnaps — promote the aggregator/finalize logic from `WarmUp` into
    `WarmUpAndCollect` (or a shared inner method) so callers get one canonical
    seeding behavior.
  - New method `SeedORBFromHistory(sym domain.Symbol, snaps []BarSnapshot)` —
    iterates the supplied `(bar, snap)` pairs and calls
    `s.feedORBBar(bar, snap, true)` for each. Does NOT call
    `s.calculator.Update`. Mirrors the existing `WarmUpORB` minus the
    calc-update line. Also handles the `RangeNotified = true` post-loop fixup
    (currently at service.go:1272-1277).
  - Delete `WarmUpORB(bars []domain.MarketBar)`. It has exactly two callers
    (live boot, activation) and after migration both use
    `SeedORBFromHistory`. Delete keeps the "single code path for calc.Update"
    invariant enforceable by grep.
- `backend/internal/app/activation/service.go` — replace the existing 1m
  fetch + WarmUp + WarmUpORB sequence (lines 303-345) with: widen the 1m
  fetch window to `[min(todayOpen, warmupTo - 120m), warmupTo]`, call
  `WarmUpAndCollect`, slice the resulting snaps for today's session,
  pass to `SeedORBFromHistory`. See "Activation path special handling" for
  rationale.
- `backend/internal/app/monitor/service_test.go` — new tests (see Test plan).
- `backend/cmd/omo-core/warmup_test.go` (or integration) — extend if any
  existing test exercises the post-warmup ORB block; otherwise covered by
  the parity-diag harness.

## Method signatures

    // WarmUpAndCollect (existing, signature unchanged) becomes the canonical
    // calc-seeding entry. Promotion from passive collector to first-class
    // boot method.
    func (s *Service) WarmUpAndCollect(bars []domain.MarketBar) []BarSnapshot

    // WarmUp keeps its current signature; internally delegates to
    // WarmUpAndCollect plus the existing finalize logic. Crypto and any
    // other caller that doesn't need snaps stays unchanged.
    func (s *Service) WarmUp(bars []domain.MarketBar) int

    // SeedORBFromHistory feeds (bar, snap) pairs into the ORB tracker
    // without touching s.calculator. Pure ORB state mutation.
    func (s *Service) SeedORBFromHistory(sym domain.Symbol, snaps []BarSnapshot)

    // WarmUpORB — DELETED.

The snap-capture choice: option 2 from the prompt (use the existing
`WarmUpAndCollect` pattern). Rationale: it already exists at service.go:191
as a precedent, requires no API design churn, and the alternative (functional
callback parameter on `WarmUp`) would force every existing caller to either
pass a no-op closure or accept a second method anyway. The single-call
`WarmUpAndCollect` returning `[]BarSnapshot` is more Go-idiomatic than a
callback for this read-after-write pattern.

## Order of operations

Live boot (`warmup.go`, replacing lines 322-342 + 508-522):

    bars := warmup.Load(...)              # canonical 800-bar 1m window
    snaps := svc.monitor.WarmUpAndCollect(bars)   # one calc.Update per bar
    warmupBarsCache[sym] = bars
    # ... HTF warmup, spike filter seed, runner warmup all run as today ...
    if isOpen && nowET.After(todayOpen):
        for _, sym := range syms.equity:
            todaysSnaps := sliceSnapsAfter(snaps, todayOpen)
            svc.monitor.SeedORBFromHistory(sym, todaysSnaps)

`sliceSnapsAfter` is a tiny helper in warmup.go: `func sliceSnapsAfter(snaps
[]monitor.BarSnapshot, t time.Time) []monitor.BarSnapshot { ... }`. Linear
scan; small constant overhead.

Activation path (`activation/service.go`, replacing lines 303-345):

    warmupFrom := minTime(todayOpen, warmupTo.Add(-120*time.Minute))
    bars1m := s.retryFetchBars(ctx, sym, "1m", warmupFrom, warmupTo, l)
    snaps := s.monitor.WarmUpAndCollect(bars1m)
    s.monitor.ResetSessionIndicators(symbol)
    s.monitor.InitAggregators(...)
    if isOpen && nowET.After(todayOpen):
        todaysSnaps := sliceSnapsAfter(snaps, todayOpen)
        s.monitor.SeedORBFromHistory(sym, todaysSnaps)

Both call sites converge on the same pattern: load bars → WarmUpAndCollect →
slice today's-session subset → SeedORBFromHistory. Nothing else calls
`calc.Update` for `Label="monitor"`.

## Snap capture design

Picked option 2 (`WarmUpAndCollect` precedent). Rationale already in Method
signatures section. Key implementation note: `WarmUpAndCollect` currently
skips the aggregator-push, anchor-regime, and `lastSnaps[sym]` finalization
that live `WarmUp` does. The migration must move that finalization logic
into `WarmUpAndCollect` (or a shared inner `warmUpInner` that both expose) so
that `WarmUp(bars)` and `WarmUpAndCollect(bars)` produce identical side
effects on `s.calculator`, `s.aggregators`, `s.anchorRegimes`,
`s.lastHTFSnaps[1h]`, and `s.lastSnaps`. The only additional thing
`WarmUpAndCollect` does is return the per-bar snap slice.

This is the load-bearing refactor. Verify by reading the current `WarmUp`
body (service.go:1284-1336) and porting the `for _, tf := range
anchorTimeframes` block + the `if len(bars) > 0` finalization tail into the
shared inner.

## Activation path special handling

Picked widen-the-window, not separate-path. Rationale:

- Activation is a per-symbol on-demand re-warm; it's not on the latency-
  critical boot path. The current 120-minute window was chosen for cold
  symbol activation where today's session-open is far in the future or the
  past. Widening to `min(todayOpen, warmupTo - 120m)` only extends the fetch
  on days where activation is mid-session, which is exactly when ORB seeding
  matters. On non-trading-day activation the window stays at 120 minutes.
- Single code path is the design constraint. A separate activation path
  would mean two `WarmUpAndCollect` call sites that differ only in window
  width, which is a smell.
- Activation latency impact: at most ~390 minutes of 1m bars (full RTH
  session) vs. 120 minutes today. ~270 extra bars × ~5µs Update ≈ 1.4ms per
  symbol. Negligible relative to DB fetch round-trip.

Implication: activation's `bars1m` window is no longer guaranteed to be
exactly 120 minutes. Any downstream code in activation that assumes a
120-bar slice (spike filter seeding, strategy activation) needs verification.
Spot-check: `s.spikeFilter.Seed(sym, bars1m)` and
`s.strategy.ActivateSymbol(symbol, bars1m, bars1h, todayOpen)` — both consume
the slice as opaque history; longer is strictly more data, no semantic
change. Verify by reading both call sites before coding.

## Test plan

Unit tests in `backend/internal/app/monitor/service_test.go`:

- `TestSeedORBFromHistory_DoesNotMutateCalcState`: build a synthetic 200-bar
  fixture, call `WarmUpAndCollect(bars)`, snapshot
  `s.calculator.states[(sym, "1m")]` via deep-copy or field-by-field
  extraction, call `SeedORBFromHistory(sym, snaps)`, assert post-state
  field-equal to pre-state. Byte-equal across rsi, ema9/21/50/200, atr,
  bb_pct_b, regime_score, vwap_sd. (vwap may differ if SeedORBFromHistory
  inadvertently routes through any session-VWAP path — assert it doesn't.)
- `TestSeedORBFromHistory_FeedsORBTracker`: same fixture, assert
  `s.orbTracker.GetSession(sym)` post-call has correct `RangeHigh`,
  `RangeLow`, `State == ORBStateRangeSet`, `RangeNotified == true`.
- `TestWarmUpAndCollect_SnapOrderMatchesCalcUpdates`: feed N bars, assert
  `len(snaps) == N` and `snaps[i].Snapshot` byte-equals the snap that
  `s.calculator.Update(bars[i])` would produce in isolation against a fresh
  calc seeded with `bars[:i]`. Pins the per-bar snap correctness.
- `TestWarmUp_DelegateBehavior_PreservesAggregatorState`: pre-refactor
  parity check. Build fixture, run old `WarmUp(bars)` (captured via test
  helper), run new `WarmUp(bars)`, assert `s.aggregators`, `s.anchorRegimes`,
  `s.lastHTFSnaps[1h]`, `s.lastSnaps[sym]` all field-equal. Catches the
  finalization-logic-moved-into-WarmUpAndCollect mistake.

Integration:

- Re-run `/tmp/parity_diff2.py` end-to-end: live restart with
  `PARITY_DIAG_ENABLED=true` post-deploy, omo-replay over the same window.
  Pass criterion: for every (calc=monitor, sym, tf, ts) key, the LAST
  emission per key matches replay's emission byte-for-byte. The 774
  divergent keys from the 2026-04-28 evening run (per memory) drop to 0.
  First emission was already byte-clean (4800/4800); this fix targets the
  last-emission column.
- `monitor` and `runner_htf` should remain mutually byte-equal at every 5m
  close (regression check for the Phase A work).

Regression:

- `TestWarmUpORB_DeletedSymbolHasORBSession_PreFix`: build a deterministic
  RTH session, run the OLD `WarmUp + WarmUpORB` path under a feature flag
  (or just keep a copy in test code), capture ORB session state. Run the
  new `WarmUpAndCollect + SeedORBFromHistory` path. Assert ORB session
  state equal (`RangeHigh`, `RangeLow`, `OpeningRangeBars`, `State`).

## Validation gates

PF re-run for AVWAP_v4 and MACD_only_v1 over 2025-04-22 to 2026-04-25,
34-symbol universe. Hard rollback gates:

- AVWAP_v4 PF within ±5% of post-Phase-B baseline (1.5989); trades ±10%
  (3755).
- MACD_only_v1 PF within ±5% (1.1265); trades ±10% (981).

Soft expectation: zero PF impact. Both strategies read `runner.htfCalcs`
for HTF state, not `monitor.s.calculator`. The contamination this plan
removes only affects monitor's 1m calc state, which feeds
`monitor.lastSnaps[sym]` (regime detection on HTF closes for fields that
*aren't* HTF-native), `monitor.anchorRegimes`, and the ORB tracker (ORB is
deprecated per `project_active_strategies`). Verify zero impact empirically.

If gates trip: revert immediately, file a bug. The fix is a correctness
restoration, not a strategy change; PF impact would indicate an unintended
side effect of the refactor (probably the WarmUpAndCollect finalization
port).

## Blast radius

Downstream consumers of `monitor.s.calculator` (the Label="monitor" calc):

- `monitor.lastSnaps[sym]` — populated in `WarmUp` finalization tail and on
  every live 1m bar. Consumed by `GetLastSnap` (line 178) used by HTTP
  handlers (snapshot endpoints).
- `monitor.anchorRegimes` — populated for 5m/15m/1h on aggregator close.
  Consumed by `applyStateUpdate` flowing into `IndicatorData.AnchorRegimes`,
  read by ~5 strategies (AVWAP_v1, MACD_v1, ORB_v1, AI scalping,
  break_retest). AVWAP_v4 and MACD_only_v1 do NOT read AnchorRegimes
  (verify via grep before ship).
- `monitor.lastHTFSnaps[sym:1h]` — populated only on 1h aggregator close.
  Consumed by `buildHTFMap`. Affects HTF map served via `lastSnap.HTF`.
- `monitor.orbTracker` — populated by `feedORBBar`. ORB tracker state
  consumed by ORB-family strategies (deprecated).

After fix, `s.calculator` is fed strictly by `WarmUpAndCollect` (boot) and
by live 1m bars (runtime). One pass, no re-feeds. All four consumers above
read state that's strictly less drifted than before; no consumer requires
the post-warmup re-fed state to be correct.

## Sequencing

Single PR. Restructure is small enough (~150 LOC across 3 files + tests)
that splitting would force a transient state where `WarmUpAndCollect` does
seeding-finalization but `WarmUp` is the unchanged old method — i.e., two
calc-seeding paths exist, exactly the invariant we're trying to eliminate.
Ship as one commit, validate end-to-end, then strategy re-validation.

Parallel work: write the unit tests against the existing API surface first
(red), confirm they fail or are skipped, then refactor `WarmUpAndCollect`
+ add `SeedORBFromHistory` + delete `WarmUpORB` + update call sites (green).
TDD-green can do this without further consultation.

## Open questions to resolve before coding

1. **Gap-fill coverage.** `fetchIntraSessionBarsWithGapFill` (current ORB
   bar fetch) is a gap-aware variant: it fetches DB bars, then asks Alpaca
   for any missing minute buckets in `[todayOpen, now)`. Canonical
   `warmup.Load` uses `EquitySpec().Required` which is 800 bars and DB-only.
   If the canonical warmup window's tail (today's session) is missing
   recent minutes that gap-fill would have filled in, `SeedORBFromHistory`
   would seed an incomplete tracker. Resolve: confirm `fillBarGaps`
   (`backend/cmd/omo-core/main.go:34`) ran before warmup and closed any
   minute gaps, OR keep an `s.barFetcher`-backed gap-fill inside
   `warmup.Load` for the fresh-tail case. The Phase A fillBarGaps sync
   (Phase 1's "must-address" item from `warmup_parity_5m_plan.md` line
   211-220) is a hard prerequisite. If it didn't ship in Phase A, ship it
   here.
2. **`feedORBBar(bar, snap, replay=true)` calc dependency.** Does
   `feedORBBar` read any field from `snap` that depends on calc state being
   in a specific phase relative to the bar's timestamp? Read service.go
   `feedORBBar` body and confirm. If yes, single-pass seeding may produce
   a snap with subtly different field values than the current double-pass.
   Resolve before coding.
3. **`ResetSessionIndicators` placement.** Live boot calls
   `svc.monitor.ResetSessionIndicators(sym.String())` at warmup.go:336
   right after `WarmUp(bars)`. Activation calls it at activation/service.go:
   309. After the refactor, this still runs after `WarmUpAndCollect` and
   before `SeedORBFromHistory`. Confirm it doesn't reset calc state that
   the snaps captured during WarmUpAndCollect referenced (it shouldn't —
   it resets session-VWAP, not EMAs — but verify).
4. **Crypto path.** Crypto warmup at warmup.go:357-371 calls `WarmUp(bars)`
   too. Crypto doesn't run ORB. After the refactor, crypto still calls
   `WarmUp(bars)` (the wrapper) which delegates to `WarmUpAndCollect` and
   discards the snaps. Confirm no behavior change. (Should be a no-op since
   crypto never had a downstream `WarmUpORB`.)
5. **HTF warmup interaction.** `warmupHTF` at warmup.go:383 runs between
   the equity warmup and the ORB block. It calls `WarmUpNative(sym, htfTF,
   bars)` for each HTF — already the single-pass canonical seeding for HTF.
   After this fix, HTF and 1m are both single-pass. Confirm HTF doesn't
   re-feed `s.calculator` for 1m state (it shouldn't — `WarmUpNative` is
   per-tf calc state, not the 1m calc — but verify).

## Reconciliation with qa-inspector boundary audit

A parallel qa-inspector pass surfaced findings that the implementer must
fold in before declaring this plan ready for tdd-green. These are listed
in priority order; **#A is highest-impact and was under-resolved as Open
Question #3 above**.

### A. Session-VWAP reconstruction is load-bearing on the re-feed (HIGHEST)

The current intra-session re-feed at `warmup.go:521-522` is not pure
contamination — it is partly load-bearing for **session VWAP recovery**.
Sequence today:

1. Canonical `WarmUp(bars)` at line 335 — feeds 800 bars including today's
   session bars; session VWAP correctly accumulated for today.
2. `ResetSessionIndicators(sym)` at line 336 — **wipes** session VWAP
   (`vwapNumerator/vwapDenom/vwapM2 = 0`) regardless of whether today's
   bars were just seen.
3. `WarmUp(orbBars)` at line 521 — re-feeds today's bars, which **rebuilds**
   the session VWAP that step 2 just wiped.
4. `WarmUpORB(orbBars)` at line 522 — re-feeds AGAIN. This third pass is
   the genuinely-redundant one driving the EMA drift we observed.

If the restructure removes both lines 521 and 522 without addressing
step 2, **session VWAP for today's session goes to zero in live and stays
zero at runtime entry**. Replay's session VWAP at the same moment is
populated (from canonical warmup processing today's bars). The
divergence we set out to close re-opens, just on different fields.

**Resolution path** — pick one before coding:

- **(a) Drop `ResetSessionIndicators(sym)` at warmup.go:336** if the calc
  auto-resets session VWAP on session-boundary detection inside
  `Update`. Read `monitor/indicators.go` Update body for session-boundary
  logic. If the calc detects `bar.Time` crossing a session boundary and
  zeros session-VWAP fields automatically, the explicit reset is
  redundant and removing it is safe. (Most likely correct path.)
- **(b) Move `ResetSessionIndicators(sym)` to before canonical
  WarmUp(bars)** so today's session bars in the canonical warmup tail
  build session VWAP cleanly, with no later wipe. Requires confirming
  the reset doesn't depend on calc state being already-seeded.
- **(c) Add session-VWAP-only rebuild in `SeedORBFromHistory`** that
  re-accumulates `vwapNumerator/vwapDenom/vwapM2` from the today-session
  bars without touching EMAs. Forces a parallel-state-rebuild path for a
  single accumulator — adds complexity, doesn't honor the "single code
  path for calc.Update" constraint.

**Recommendation**: prefer (a) if the auto-reset exists; (b) otherwise.
Add a unit test pinning the session-VWAP value at runtime entry against
a canonical-warmup-only scenario.

### B. `RangeNotified=true` post-loop fixup must be carried forward

`WarmUpORB` at service.go:1272-1277 marks any ORB range that locked
during seeding as `RangeNotified=true`. This suppresses re-emission of
`EventORBRangeSet` at runtime for ranges already locked during boot.
The plan already acknowledges this (line 47-48), but call out
explicitly: a unit test must pin "boot replay sees ranges already
locked → RangeNotified flag set → no spurious EventORBRangeSet at first
runtime bar."

### C. Canonical bars freshness vs gap-fill (refines Open Question #1)

Confirmed via QA: the canonical `warmup.Load(... time.Now())` path is
**DB-only**, not gap-aware. `fetchIntraSessionBarsWithGapFill` (current
orb fetch) explicitly asks the broker (Alpaca) for missing minute
buckets after the DB read. If `fillBarGaps` (running concurrently as a
background goroutine at `main.go:34`) hasn't finished closing the
trailing-edge gap by the time canonical warmup runs, slicing today's
session out of canonical bars yields a **shorter** today-window than
the current orbBars query.

**Resolution**: gate canonical warmup's HTF and 1m fetches on the same
`gapFillDone` channel/flag that already exists for HTF (per Phase 1's
"must-address" item from `warmup_parity_5m_plan.md` lines 211-220). If
that gate didn't ship in Phase A, ship it here as a hard prerequisite.

### D. Activation does NOT have the boot-side contamination bug

Critical clarification from QA: `activation/service.go:303-345` only
calls `WarmUpORB(orbBars)` — it does NOT have a prior
`WarmUp(orbBars)` redundant call. Activation's bug profile is therefore
**only** the side-effect-preservation issue (`RangeNotified`,
`feedORBBar` semantics). The "activation's calc gets re-fed twice"
worry doesn't apply.

The widen-the-window proposal in this plan is still valid for honoring
the single-code-path invariant, but the rationale should clarify:
activation's restructure is a code-path-unification cleanup, not a
contamination fix. If single-code-path proves expensive to retrofit on
activation, **the activation change is independently optional** —
correctness is preserved by just swapping `WarmUpORB` for
`SeedORBFromHistory` with the existing 120-min window's snaps. The
single-code-path benefit applies equally to either approach.

### E. PrimeAggregators at warmup.go:552-553 must remain intact

The runner-side `WarmUp(orbBars)` and `PrimeAggregators` calls at
warmup.go:552-553 are NOT in this plan's edit scope, but call out
explicitly: do not touch them. They prime the strategy runner's HTF
aggregators with the partial in-progress 5m bucket and must continue
to consume today-session bars (sliced from canonical `bars`, same as
the new monitor path).

### F. Replay/backtest do not seed ORB at all (out of scope)

QA confirmed `omo-replay/main.go` and `backtest/runner.go` never call
`WarmUpORB` or any ORB-seeding equivalent. This is a pre-existing
parity gap (live boot mid-session has ORB state recovered; backtest
mid-session-resume does not exist as a code path). **Out of scope for
this restructure.** Note in the project memory if not already
captured.

### G. WarmUpAndCollect is currently unused — promotion is the load-bearing refactor

QA confirmed `WarmUpAndCollect` at service.go:191 has zero non-test
callers. The plan's proposal to make it the canonical seeding entry is
sound, but the work of porting `WarmUp`'s aggregator/anchorRegimes/
lastSnaps finalization tail INTO `WarmUpAndCollect` (or a shared inner)
is the actual refactor risk. The pin test
`TestWarmUp_DelegateBehavior_PreservesAggregatorState` is correctly
prioritized.
