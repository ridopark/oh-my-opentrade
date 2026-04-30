# pd_high / pd_low replay scope parity (live + backtest)

## Goal

`pd_high` and `pd_low` AVWAP anchors must produce byte-identical state
between live and backtest at the first 5m close of every RTH session,
within float tolerance, **using only RTH 1m bars from a single,
deterministic prior-day window**. Today they diverge: at 09:35 ET on
2026-04-29 for AAPL, live reports `pd_high.barCount = 1539`,
`vwap = 266.86`; the same backtest run reports `barCount = 537`,
`vwap = 270.45` (1.34% delta). Both have `RTHOnly = true`. The plan
brings them into agreement on a single canonical accumulation window.

This is the second of two parity bugs surfaced by the live<->backtest
indicator-state diff. The first (RTH gate on `session_open`) shipped in
PR #22 / commit b74d5dcb. With #22 merged, AAPL `session_open` AVWAP and
all calculator-emitted indicators (RSI / EMA / MACD / VWAP / volume
ratio) match live byte-identical at 09:35 ET. The remaining gap is
exclusively in the per-anchor `pd_high` / `pd_low` state, and explains
why backtest still produces 1 of 4 live trades on 2026-04-29 instead of
the >=2 the convergence target requires.

## Root cause (two sub-bugs that compound)

### Sub-bug A: replay path bypasses `RTHOnly`

`AnchoredVWAPCalc.Update` (anchored_vwap.go:135) honors the per-anchor
`RTHOnly` flag and skips non-RTH bars. **`UpdateSingleAnchor`**
(anchored_vwap.go:189) does not. The replay path uses
`UpdateSingleAnchor`:
- runner.go `replayBarsForAnchors` -> `AVWAPState.UpdateCalcAnchor`
  (avwap_v1.go:1034) -> `calc.UpdateSingleAnchor`.
- monitor service.go:853 directly calls `calc.UpdateSingleAnchor` in the
  standalone-AVWAP reset path.

`prevDayBarsFn` (cmd/omo-core/services.go:503) returns **every** 1m bar
in `[since, until)` from `market_bars` with no RTH filter. The replay
then feeds all of them through `UpdateSingleAnchor`, which counts each
bar regardless of session.

Concrete evidence on 2026-04-29 for AAPL:
- Replay log: `bars=532 from=2026-04-28T08:30:00-05:00 to=2026-04-28T18:59:00-05:00`.
- 09:30 ET to 19:59 ET on 2026-04-28 = ~10 hours of 1m data; only the
  09:30-16:00 ET window (390 minutes) is RTH; the rest is pre-market or
  after-hours.
- Backtest pd_high barCount at 09:35 ET 2026-04-29 = 537 = 532 (replayed)
  + 5 (today's RTH 1m). If RTHOnly were honored on replay, the count
  would be ~390 + 5 = 395 instead.

Live takes the same code path on first bar of each new session day
(monitor service.go:826-862), so this sub-bug also corrupts live's
state — but compounded with sub-bug B, the symptom in live is *over-
accumulation across days*, not the same arithmetic offset.

### Sub-bug B: live anchor state survives across sessions when anchor time doesn't change

`AVWAPState.ResetAnchors` (avwap_v1.go:957) preserves the accumulated
`AnchoredVWAPState` when the new anchor time equals the existing
anchor time:

```go
if oldAP, exists := existingPoints[name]; exists && oldAP.AnchorTime.Equal(t) {
    if oldState, hasState := existingStates[name]; hasState {
        newCalc.AddAnchor(ap)
        newCalc.Restore([]start.AnchorPoint{ap}, ...{name: oldState})
        ...
        continue
    }
}
```

Hypothesis (to verify in Phase 1): `sessionResolver.ResolveAnchors` in
live returns the same `pd_high` anchor time across multiple session
days because the underlying session table isn't refreshed daily, or the
resolver caches the value. With unchanged anchor time, ResetAnchors
preserves state, runtime `Update` calls on each new RTH session keep
incrementing barCount, and over ~4 session days the count climbs to
1539 (~4 * 390 RTH bars). The "preserve state when anchor unchanged"
clause is correct in principle (avoid losing seed during a same-day
re-resolve) but wrong when called across day boundaries with a stale
anchor time.

Live evidence on 2026-04-29 for AAPL:
- pd_high barCount = 1539 -> 25.65 RTH-hours of accumulation.
- pd_low barCount = 1532 -> 25.5 RTH-hours.
- 25.65 / 6.5 = 3.95 RTH days. Today is Wednesday; 4 trading days back
  is last Thursday 2026-04-23.
- session_open in the same payload shows barCount = 5, vwapCount = 5,
  i.e. session_open IS getting reset daily and only accumulating
  today's bars. So ResetAnchors fires; sub-bug B is specific to
  pd_high / pd_low whose anchor times are computed differently.

## Approach

Single semantic invariant: `pd_high` / `pd_low` state at the first RTH
5m close of session day D is exactly the volume-weighted typical-price
accumulation of the RTH 1m bars from anchor_time (= the time of D-1's
RTH high or low respectively) up to the current bar. Same window,
same filter, same answer in live and backtest.

Two-part fix matching the two sub-bugs:

### Edit 1 — replay path honors `RTHOnly`

In `AnchoredVWAPCalc.UpdateSingleAnchor` (anchored_vwap.go:189), after
the `e.active` check, gate the accumulation on the per-anchor
`RTHOnly` flag using the same `isRTH(barTime)` predicate that
`Update` already uses (anchored_vwap.go:60-65 — the local helper, not
warmup.IsRTH, since this file scopes its own RTH definition; if the
two should be unified, do it as a separate small change).

```go
if e.RTHOnly && !isRTH(barTime) {
    return
}
```

Place after `if !e.active { ... e.active = true }` so a non-RTH bar
still marks the anchor active (matching the runtime `Update`
semantics that flip active on the first post-anchor-time bar even if
that bar is non-RTH).

Result: replay arithmetic for `pd_high` / `pd_low` becomes RTHOnly-
respecting, matching the runtime path.

### Edit 2 — daily anchor refresh in live

Investigate (Phase 1, no code change yet) why live's pd_high /
pd_low anchor times don't change across session days. Two candidate
remediations once root cause is confirmed:

- 2a. If `sessionResolver` returns stale data: ensure the resolver's
  prior-day window is refreshed daily (cron, or lazy refresh on
  first-bar-of-new-session-day with an "as-of" parameter).
- 2b. If the resolver is correct but `ResetAnchors`'s "preserve state
  when anchor unchanged" clause is the wrong default for daily-
  recurring anchors: scope the preservation to same-session re-
  resolves only (e.g. additional flag on AnchorPoint or check that
  `barTime`'s session date equals the anchor's session date).

Pick whichever Phase 1 evidence supports. If both are wrong (anchor
time IS changing but state still survives) re-investigate.

### Edit 3 — unify the runtime + replay calc state contract

Once Edits 1 and 2 land, audit anchored_vwap.go for any other path
that mutates `e.barCount` / `e.state.CumPV` / `e.state.CumV` /
`e.state.M2` / `e.recentVWAPs`. There are currently two: `Update`
(line 135) and `UpdateSingleAnchor` (line 189). Confirm the two paths
produce identical state for the same input bar sequence. Add a unit
test that pins this — feeds the same synthetic RTH-only 1m bar
sequence through both paths and asserts the resulting `state.Value()`
and `barCount` match within float tolerance.

## Phase 1 (investigation, must complete before any code edit)

1. Query the session table (`session_data` or whatever the
   `sessionResolver` reads) to confirm whether
   `pd_high_time` / `pd_low_time` rows exist for 2026-04-28 with the
   correct timestamps. Compare to what live's resolver returns at
   09:30 ET on 2026-04-29.
2. Add a one-shot diagnostic log in
   `monitor/service.go:826-862` (or strategy/runner.go:389) that emits
   the resolved `pd_high.AnchorTime` and `pd_low.AnchorTime` on every
   first-bar-of-new-session resolve. Run live or replay one trading
   day and confirm whether the anchor times change between consecutive
   sessions.
3. If anchor times do change daily, re-examine `ResetAnchors`'s
   preserve-state branch — there must be another path that doesn't
   call ResetAnchors at session boundary in live.
4. If anchor times do NOT change daily, root cause is the resolver or
   its data source; fix there.

Deliverable from Phase 1: a single-paragraph root-cause statement that
names the file:line that needs to change, with the supporting log
evidence linked. Only after that lands does the implementation phase
begin.

## Files (expected, finalize after Phase 1)

- `backend/internal/domain/strategy/anchored_vwap.go` — Edit 1 (gate
  `UpdateSingleAnchor` on `e.RTHOnly`).
- `backend/internal/domain/strategy/anchored_vwap_test.go` (or similar)
  — new unit test for Edit 1 + Edit 3 invariant.
- One of: `backend/cmd/omo-core/services.go`, the session-resolver
  source file (locate in Phase 1), or
  `backend/internal/app/strategy/builtin/avwap_v1.go` — Edit 2.

Estimated diff: ~40-80 LOC across 2-3 files plus ~50 LOC of test.

## Verification

1. **Build + unit tests**: `go build ./... && go test ./...` pass.
2. **`UpdateSingleAnchor` RTHOnly unit test**: feed a deterministic 1m
   bar sequence containing both RTH and non-RTH bars; assert
   barCount only increments for RTH bars when `RTHOnly = true`, and
   for all bars when `RTHOnly = false`.
3. **Replay-vs-runtime parity unit test** (Edit 3): feed the same
   bar sequence through `Update` (with RTHOnly anchor) and through
   `UpdateSingleAnchor`; assert `state.Value()`, `state.SD()`,
   `barCount`, `vwapCount` match within 1e-9.
4. **Live-vs-backtest parity at 09:35 ET (the canonical query)**:
   re-run the post-fix backtest for 2026-04-29 with
   `PARITY_DIAG_ENABLED=true`, then run the same query that surfaced
   the gap:

       SELECT payload->'avwapState'->'anchors'->'pd_high'->>'vwap'
                AS live_pd_high_vwap,
              payload->'avwapState'->'anchors'->'pd_high'->>'barCount'
                AS live_pd_high_bars
       FROM strategy_signal_events
       WHERE ts = '2026-04-29 13:34:59.988651+00' AND symbol='AAPL';

   PASS criterion: backtest `pd_high.vwap` and `pd_high.barCount`
   match live within 1e-6 relative tolerance and exact integer
   respectively. Same for `pd_low`. (Currently 1.34% / +1002 bars
   apart.)
5. **Trade-list convergence on 2026-04-29**: re-run full-day backtest;
   verify >=2 of 4 live trades have a backtest analog (same underlying,
   same direction, entry within +/-15 minutes). This is the original
   parity-RTH plan's verification step 4 that's currently blocked at
   1/4 by this bug.
6. **Live regression on N+1 trading day**: query live's reason-class
   distribution for the trading day after deploy. Each class within
   +/-5% of pre-deploy distribution. `pd_high` / `pd_low` `barCount`
   on the first 5m close of that day should now match what backtest
   produces for the same day (run a sidecar backtest after RTH close
   to compare).

## Risks

- **Live behavior change at first session-day boundary post-deploy**:
  if sub-bug B was real and gets fixed, live's `pd_high` / `pd_low`
  state will *shrink* abruptly on the next daily rollover (from
  ~1500 bars to ~390 bars). VWAPs will jump from the
  4-day-cumulative value to the 1-day value. Strategies that read
  `pd_high.vwap` (avwap_v4 confluence scoring, slope gate) will see a
  step change. Expected magnitude: similar to the 1.34% delta
  observed in backtest today — bounded but real. Mitigation: deploy
  outside trading hours, or ramp by symbol.
- **Edit 1 alone (without Edit 2) shrinks backtest barCount but
  doesn't move live**: if Edit 1 lands first, backtest pd_high
  barCount drops from 537 -> ~395 and VWAP shifts. Live still shows
  1539. Verification step 4 (live<->backtest parity) FAILS at this
  intermediate state. Land both edits together, or land Edit 2
  first.
- **`isRTH` divergence between anchored_vwap.go and warmup**: the
  local `isRTH` in anchored_vwap.go:60 uses `< 16*60` rather than
  `domain.NYSECloseTime`, so it's wrong on early-close days. PR #22
  noted this as out-of-scope; this plan also leaves it alone unless
  Phase 1 evidence shows it's the actual root cause. Open as a
  separate plan if needed.
- **`SetKeyLevels` (avwap_v1.go:953)** stores per-anchor *prices*
  (pd_high / pd_low values) separate from the AVWAP anchor *times*.
  Sub-bug B may also affect KeyLevels staleness; investigate during
  Phase 1.

## Halt conditions

- Phase 1 fails to produce a single-paragraph root-cause statement
  after one investigation pass — halt and re-investigate from
  scratch with different hypotheses.
- After Edits 1 and 2, verification step 4 (live<->backtest pd_high
  / pd_low at 09:35 ET) still shows >5% relative VWAP delta — halt;
  there's a third sub-bug not in the diagnosis.
- Trade-list convergence (verification step 5) below 2 of 4 after
  this fix lands — halt; the residual gap is option-chain
  divergence / SimBroker fills (the original plan's
  Out-of-Scope items) and needs its own plan.
- Live `pd_high.barCount` does not drop to ~395 on the first session-
  day boundary after deploy — Edit 2's mechanism didn't fire as
  intended; investigate before continuing.

## Out of scope

- Option-chain divergence (live IBKR vs DoltHub historical) — same as
  PR #22's Out-of-Scope.
- SimBroker fill model differences — same.
- Late-session 16 ET hours-block delta — same.
- `session_open` AVWAP and calculator-emitted indicators (RSI, EMA,
  MACD, VWAP, BB) — already byte-identical post-PR #22.
- Anchored VWAP's local `isRTH` early-close-day discrepancy — covered
  in PR #22 review notes; address separately if Phase 1 doesn't
  surface it as the root cause.
- Other custom anchor types beyond `pd_high` / `pd_low` (e.g. AI-
  resolved capitulation anchors) — they may share the replay-path
  RTHOnly bug but their lifecycle is different; address per-anchor
  if needed in a follow-up.

## Reference data

- PR #22 (parity-rth-only-indicators) — merged 2026-04-30 03:10 UTC,
  commit b74d5dcb. Establishes byte-identical live<->backtest parity
  for `session_open` and calculator-emitted indicators; this plan
  extends parity to `pd_high` / `pd_low`.
- `_workspace/parity_rth_only_indicators_plan.md` — the predecessor
  plan; lines 22-25 of its diagnosis treated `pd_high` / `pd_low` as
  already correct (because they had `RTHOnly = true`), missing the
  fact that the replay path bypasses the flag entirely.
- Concrete divergence query (the canonical reproducer):
  `strategy_signal_events` row at `2026-04-29 13:34:59.988651+00`
  for `symbol='AAPL'`, `payload->avwapState->anchors->pd_high`.
