# pd_high / pd_low cross-day state retention (sub-bug B, take 2)

## Goal

Live `pd_high` / `pd_low` AVWAP state must reset at every session-day
boundary so its `barCount` and `vwap` reflect *only* the prior session's
RTH window, just like backtest does on every fresh process start.
Today live retains accumulated state across multiple consecutive session
days; backtest does not. Closing this gap is the last known residual
preventing live↔backtest byte-identical parity for these two anchors.

This is the **third** parity plan in the series and supersedes
`_workspace/parity_replay_on_bar_warmup_feed_plan.md`, whose diagnosed
sub-bug C ("HTF-warmup feed inflating both sides") was empirically
falsified — see "Empirical refutation of sub-bug C" below.

## Empirical decomposition (AAPL, 09:35 ET, 2026-04-29)

```
Live (pre-PR-#23, 4-day continuous run):  pd_high.barCount = 1539
                                          pd_high.vwap     = 266.8620

Backtest (post-PR-#23, fresh process):    pd_high.barCount = 397
                                          pd_high.vwap     = 270.3112  (same query, fresh run on 2026-04-30 05:31 local)

Gap:                                      +1142 bars
                                          1142 / 390 RTH bars per day = 2.93 ≈ 3 prior session days retained

Predecessor plan's read:                  1539 / 390 = 3.95 ≈ 4 RTH days
                                          (slightly higher because live's count includes today's accumulation
                                           through 09:34, plus one trailing day's worth of post-RTH non-RTH
                                           inflation from sub-bug A which is now PR-#23-fixed)
```

Same-day live progression on 2026-04-29 (before any new fix):
```
13:34:59 UTC  bar=09:35 ET  pd_high.barCount = 1539
13:39:59 UTC  bar=09:40 ET                   = 1544  (+5)
13:44:59 UTC  bar=09:45 ET                   = 1549  (+5)
...
20:09:59 UTC  bar=16:05 ET                   = 1930  (RTH closed, no further increments)
```

Increment is exactly +5 bars per 5m RTH close — the runtime `Update`
path is RTH-gated correctly (PR #22). The +1142 base inflation is
load-bearing state from before today's RTH started.

Today (2026-04-30) live shows `pd_high.barCount = 402` flat across
premarket — exactly the pre-RTH-gate raw replay count for the
04-29 11:57→18:59 ET prior-day window (replay loads 402 bars, all
counted because live is still running pre-PR-#23 code without the
`UpdateSingleAnchor` RTH gate). This means live's process restarted
between 04-29 16:00 ET and 04-30 04:04 ET, dropping the 4-day
accumulation. Once PR #23 deploys and live restarts again, today's
`barCount` should drop further to ~240 (RTH-only portion of that 7h
window). With live restarted today and accumulating only one fresh
day, the 1539 mode hasn't recurred yet — but it will return on
day-N+4 if the underlying mechanism isn't fixed.

## Empirical refutation of sub-bug C (the prior plan's claim)

Prior plan claimed the residual gap was a live-only HTF-warmup feed:
`AVWAPStrategy.ReplayOnBar` at `avwap_v1.go:97` calling `Calc.Update`
for every warmup bar, where the canonical-spec HTF warmup
(`warmup.go:471`) feeds 800 5m bars into a 5m strategy and each
RTH 5m bar increments the anchor's barCount.

Phase 1 investigation under `/execute-plan` produced:
1. Static-analysis claim from `general-purpose` agent: backtest also
   calls `runner.WarmUpTF` on the same `warmup.EquitySpec()` from at
   least 6 sites (`omo-replay/main.go:1019,1087`,
   `backtest/runner.go:1036,1048,1077,1609,1629`). Both sides route
   through `Instance.WarmupOnBar` → `AVWAPStrategy.ReplayOnBar` →
   `Calc.Update`, accumulating ~400 RTH bars. Therefore the gap
   cannot be a "live-only feed."
2. Empirical verification: a fresh post-PR-#23 backtest of AAPL
   2026-04-29 produced `pd_high.barCount = 397`, with NO HTF-warmup
   inflation visible. The backtest accumulates only the
   `replayBarsForAnchors` RTH-portion + runtime bars; the WarmUpTF
   path's `Calc.Update` calls accumulate nothing because anchors
   aren't added to the calc until `replayBarsForAnchors` runs *after*
   `WarmUpTF`. Static analysis didn't account for this runtime
   ordering.

So the WarmUpTF / ReplayOnBar `Calc.Update` call is a no-op for AVWAP
anchors on both sides. The sub-bug C mechanism does not exist.
`_workspace/parity_replay_on_bar_warmup_feed_plan.md` should be
archived (kept on disk for git history / lessons-learned, but marked
superseded).

## Hypothesized root cause (to verify in Phase 1)

The pre-PR-#23 1539 reading is consistent with **multi-day
accumulation through `AVWAPState.ResetAnchors`'s preserve-state
branch** at `avwap_v1.go:998`:

```go
if oldAP, exists := existingPoints[name]; exists && oldAP.AnchorTime.Equal(t) {
    if oldState, hasState := existingStates[name]; hasState {
        newCalc.AddAnchor(ap)
        newCalc.Restore([]start.AnchorPoint{ap}, ...{name: oldState})
        ...
        continue   // skip the reset, keep accumulated CumPV/CumV/M2
    }
}
```

For session_open this is irrelevant — its anchor time is "today's
09:30 ET," changing every day, so `Equal(t)` is false and state
resets. The predecessor plan confirmed `session_open.barCount = 5`
behaves correctly.

For pd_high / pd_low the predecessor plan ruled this branch out
based on Phase 1 logging that anchor times "change daily." But the
empirical 1539 reading contradicts that — *something* is preserving
state across ~4 days. Candidate mechanisms (Phase 1 must distinguish
between them):

1. **`ResetAnchors` is not called every session day.** The trigger
   path (session resolver refresh, or first-bar-of-new-session-day,
   or some scheduled reseed) might run once at process start and
   only on explicit reseed, leaving the runtime `Update` path to
   keep accumulating against the same anchor for multiple days.
   → fix: ensure `ResetAnchors` fires on every session-day boundary,
     not just at startup or on reseed.

2. **`ResetAnchors` is called but the anchor time literally does
   not change** because the session resolver returns a cached or
   computed-the-same-way value. E.g., resolver returns the
   *time-of-day* portion only (always 12:00 ET if pd_high was at
   noon yesterday and noon today), or returns yesterday's date for
   multiple consecutive days because it doesn't re-query.
   → fix: scope the preserve-state branch so it only fires on
     same-session-date re-resolves (e.g., compare `barTime`'s
     calendar date with `oldAP.AnchorTime`'s calendar date in NY
     time — preserve only when both are in the same RTH session).

3. **`ResetAnchors` is called and anchor times do change, but the
   preserve branch fires anyway** due to a bug in the equality
   check or because something else (a separate Restore path, a
   cached `existingStates` reference) re-injects the old state.
   → fix: locate the offending path and gate it.

4. **`ResetAnchors` is correct but a *different* code path mutates
   `s.Calc.states[name].state` directly, bypassing the reset.** E.g.,
   a serialization round-trip during snapshot/restore that doesn't
   honor the new anchor time.
   → fix: locate and gate.

## Phase 1 (investigation, must complete before any code edit)

1. **Capture the live anchor-time-and-barCount progression across
   day boundaries.** Add a one-shot diagnostic log in
   `monitor/service.go` (or wherever `ResetAnchors` is invoked) that
   emits, on every call:
   - Old `pd_high.AnchorTime` (RFC3339 in UTC and NY)
   - New `pd_high.AnchorTime` (same)
   - Whether the preserve branch fired for `pd_high`
   - Whether the preserve branch fired for `pd_low`
   - Same for `session_open`
   - The current bar's time
   - Stack trace at depth 5 to identify which trigger called
     ResetAnchors.
   Run live in paper mode through one (ideally two) session-day
   boundaries on a non-holiday weekday and capture the log lines.

2. **Read the session resolver / anchor-time source.** Locate
   whatever computes `pd_high` / `pd_low` anchor times. Determine
   how often it runs, what it returns on consecutive days, and
   whether its output ever stays literally `time.Time.Equal` across
   day boundaries when the underlying high *time* differs by even
   a minute.

3. **Read every call site of `AVWAPState.ResetAnchors`.** Map which
   triggers fire it (session-rollover hook, AI-anchor reseed, etc.)
   and at what cadence. Confirm whether each session-day boundary
   produces at least one ResetAnchors call.

4. **Reproduce the multi-day inflation deterministically.** Either:
   - Run live for 2-3 session days continuously in paper mode and
     capture daily `pd_high.barCount` at first 5m close. Confirm
     each day adds ~390 RTH bars to the count (matching the +5/5m
     pattern from 2026-04-29 progression).
   - OR construct a Go integration test that drives
     `AVWAPState.ResetAnchors` + runtime `Update` over a synthetic
     3-day RTH bar sequence with anchor times designed to hit
     candidate-mechanism 1, 2, 3, or 4. Whichever sequence inflates
     barCount above one-day-equivalent identifies the broken path.

Deliverable from Phase 1: a single-paragraph root-cause statement
naming (a) which of the four candidate mechanisms is the actual
cause, (b) the file:line of the offending code, and (c) the
specific log evidence + integration-test failing case that pins it.
Only after that lands does the implementation phase begin.

## Approach

Two candidate fixes, picked after Phase 1 evidence:

### Approach A (preferred if mechanism is candidate 1 — ResetAnchors not fired daily)

Wire `ResetAnchors` into the daily session-rollover path so it fires
unconditionally on every first-bar-of-new-session-day, with
freshly-resolved anchor times. The preserve-state branch stays as-is;
the bug is that the trigger never fires.

### Approach B (preferred if mechanism is candidate 2 or 3 — preserve branch fires when it shouldn't)

Tighten the preserve-state branch's equality check at
`avwap_v1.go:998`: instead of `oldAP.AnchorTime.Equal(t)`, compare
the *NY-session-date* portion of both timestamps. Same-session
re-resolves preserve state (avoiding seed loss during a same-day
re-anchor); cross-day re-resolves with same time-of-day reset
state. Concretely:

```go
nyLoc := domain.NYLocation()
oldDate := oldAP.AnchorTime.In(nyLoc).Truncate(24 * time.Hour)  // simplified
newDate := t.In(nyLoc).Truncate(24 * time.Hour)
if exists && oldAP.AnchorTime.Equal(t) && oldDate.Equal(newDate) {
    ...preserve...
}
```

(Truncate(24h) on time.Time isn't NY-session-aware; the real check
should use a session-bucket helper. Do the right thing per project
conventions — `warmup.SessionDate` or similar if it exists; otherwise
add it.)

Tradeoff: this changes a load-bearing branch in live's anchor
lifecycle. Phase 1 must surface every call path that depends on the
preserve semantics (e.g., AI-anchor reseed mid-session) so Approach B
doesn't regress same-day re-resolves.

### Approach C (fallback — defensive cap)

If neither A nor B is feasible (e.g., the anchor lifecycle is too
entangled to safely modify), add a defensive cap in
`UpdateSingleAnchor` and runtime `Update`: if `barCount` exceeds
"one trading day's worth of RTH bars" (~390 + buffer for sparse
data), refuse further accumulation and emit a metric. This is a
band-aid, not a fix; only adopt if Approach A and B both prove
unsafe.

## Files (expected, finalize after Phase 1)

- `backend/internal/app/strategy/builtin/avwap_v1.go:998` —
  preserve-branch equality check (Approach B).
- `backend/cmd/omo-core/monitor/service.go` (or wherever the
  session-rollover hook lives) — Approach A trigger wiring.
- `backend/internal/app/strategy/builtin/avwap_v1_test.go` (or
  similar) — new integration test pinning that 3 consecutive
  session days of RTH bars produce barCount ≤ ~395 at the first
  5m close of day 3, not ~1170.

Estimated diff: ~30-60 LOC across 1-2 files plus ~80 LOC of test.

## Verification

1. **Build + unit tests**: `go build ./... && go test ./...` pass.

2. **Multi-day accumulation unit test**: drive a synthetic 3-day
   sequence through `AVWAPState.ResetAnchors` (called between days
   with new anchor times for each day's prior-day RTH high) plus
   runtime `Update` calls. Assert
   `pd_high.barCount` at end-of-day-3 ≤ 395 (one fresh day plus
   tolerance), not 3 * 390 = 1170. Currently fails at 1170.

3. **Live↔backtest parity (the canonical reproducer)**:
   pre-condition: PR #23 must already be deployed to live (else the
   sub-bug A inflation masks this fix). Run live in paper mode on a
   non-holiday weekday for at least one session-day boundary. At
   the first 5m close of day-2, capture EntryGated for AAPL:
   ```
   SELECT payload->'avwapState'->'anchors'->'pd_high'->>'vwap'
            AS live_pd_high_vwap,
          payload->'avwapState'->'anchors'->'pd_high'->>'barCount'
            AS live_pd_high_bars
   FROM strategy_signal_events
   WHERE ts = '<first-5m-close-of-day-2>+00' AND symbol='AAPL'
     AND signal_id NOT LIKE '%backtest%';
   ```
   PASS criterion: backtest matches live within 1e-6 relative on
   `vwap` and exact integer on `barCount`.

4. **Multi-day live regression**: same query at the first 5m close
   of day-3, day-4, day-5 of a continuous live run. Each must show
   `barCount` between ~390 (clean reset) and ~395 (with normal
   tolerance). Currently day-N grows by ~390 each session.

5. **Trade-list convergence**: re-run the AAPL 2026-04-29 retro
   backtest after live deploys this fix, and verify >=2 of 4 live
   trades have a backtest analog (same underlying, same direction,
   entry within ±15 minutes). This is the original parity goal
   from `_workspace/parity_rth_only_indicators_plan.md` step 4.

## Risks

- **Multi-day live state still differs even with this fix** because
  some other path (snapshot/restore, AI-anchor reseed, monitor
  service standalone-AVWAP path) re-injects stale state. Phase 1
  must enumerate all paths; the fix may need to be applied at
  multiple sites.

- **Approach B regresses same-day re-resolves**: the preserve-state
  branch was added to handle the case where session resolver runs
  twice in one day (e.g., AI-anchor reseed) and the second run
  must not lose seed. Phase 1 must identify all same-day re-resolve
  scenarios so Approach B's "compare session date" check honors
  them.

- **Live behavior change at first session-day-boundary post-deploy**:
  symbols whose live `pd_high.vwap` was running ~3-4 days of
  accumulation will see VWAP shift abruptly to the one-day value.
  Magnitude depends on intra-period drift; bounded by the same
  ~1.34% delta the predecessor plan documented. Strategies reading
  `pd_high.vwap` (avwap_v4 confluence, slope gate) will see a step.
  Mitigation: deploy outside trading hours, or ramp by symbol.

- **PR #23 deploy must precede this fix's measurement window.**
  Until live runs PR #23, sub-bug A's per-day inflation
  (~140 non-RTH bars per session) compounds with sub-bug B. The
  validation step 3 above only works post-PR-#23-deploy.

- **Confounding with the predecessor plan's "Risk: live behavior
  change at first session-day boundary post-deploy."** Both this
  plan and PR #23 produce a step change in live VWAPs on first
  rollover. After PR #23 deploys and runs through one session day,
  measure the VWAP delta; deploy this fix in a separate window so
  the two changes don't compound and confuse strategy-level
  monitoring.

## Halt conditions

- **Phase 1 fails to deterministically reproduce the multi-day
  accumulation** in the integration test or via a 2-3 day live
  observation — halt; the 1539 reading may have been a transient
  / pre-PR-#22 artifact, not a current bug. Re-confirm against a
  fresh multi-day live run before continuing.

- **Phase 1 surfaces a fifth root-cause mechanism not in the
  candidate list (1-4 above)** — halt and update the plan with the
  newly-found mechanism before proposing a fix.

- **After the fix lands, multi-day live still inflates `barCount`
  beyond +5/RTH-bar daily** — halt; another retention path exists.
  Investigate before continuing.

- **Same-day re-resolve regression**: any test or live observation
  shows same-day AI-anchor reseed losing seed because Approach B's
  date check is too aggressive — halt; refine the date check.

## Out of scope

- Option-chain divergence (live IBKR vs DoltHub historical) — same
  as predecessor plans.
- SimBroker fill model differences — same.
- Late-session 16 ET hours-block delta — same.
- Anchored VWAP's local `isRTH` early-close-day discrepancy — still
  separate, still flagged in PR #22 review notes.
- Custom anchor types beyond `pd_high` / `pd_low` (AI-resolved
  capitulation anchors, etc.) — they may share this lifecycle bug
  but their reseed cadence is different and warrants a separate
  per-anchor analysis if it surfaces.

## Reference data

- PR #22 (parity-rth-only-indicators) — merged 2026-04-30 03:10 UTC,
  commit b74d5dcb. RTH-gated `IndicatorCalculator.Update` and HTF
  push sites; established session_open byte-identical parity.
- PR #23 (parity-pd-anchor-replay-scope) — merged 2026-04-30 10:05
  UTC, commit 24d0d489. Closed sub-bug A (`UpdateSingleAnchor`
  RTHOnly gate) and Edit 1b (`monitor/service.go:835` RTHOnly
  propagation). Body explicitly notes Edit 2 (this plan's territory)
  was deferred and the *then-hypothesized* sub-bug C was the
  remaining residual; sub-bug C has since been falsified.
- Predecessor plan: `_workspace/parity_pd_anchor_replay_scope_plan.md`
  (commit 762697a8). Diagnosed sub-bug B in lines 55-91 with the
  4-day accumulation analysis. Edit 2 was deferred there based on
  Phase 1 logging that turned out to be incomplete.
- Superseded plan: `_workspace/parity_replay_on_bar_warmup_feed_plan.md`
  (commit 9dafc76d). Sub-bug C falsified — keep on disk for git
  history but mark superseded by this plan.
- Canonical reproducer: AAPL `strategy_signal_events` row at
  `2026-04-29 13:34:59.988651+00` with `pd_high.barCount = 1539`
  and `pd_high.vwap = 266.8620`. Same query post-fix should show
  barCount ≈ 395 (one-day RTH-replay+runtime) and vwap matching
  backtest's 397/270.3112 within float tolerance, on a multi-day
  live run.
- Backtest validation point captured 2026-04-30 05:31 local during
  sub-bug-C falsification: AAPL 2026-04-29 09:35 ET emits
  `pd_high.barCount = 397`, `pd_high.vwap = 270.3112`. This is the
  canonical "what live should converge to" value.
