# ReplayOnBar HTF-warmup feed parity (sub-bug C)

## Goal

`pd_high` / `pd_low` AVWAP barCount and VWAP must match between live
and backtest at the first 5m close of every RTH session, within float
tolerance. PR #23 closed sub-bug A (the replay path's missing RTHOnly
gate) but live↔backtest parity is still blocked by a second mechanism
this plan addresses: AVWAPStrategy.ReplayOnBar feeds Calc.Update for
every warmup bar, so when the canonical-spec HTF warmup loads 800 5m
bars and routes them to a strategy configured for 5m, every RTH 5m
warmup bar increments the anchor's barCount even though that anchor's
state is supposed to come exclusively from the prior-day replay.

This is the second of the two distinct bugs surfaced during PR #23
verification. With sub-bug A fixed, the remaining 800-bar gap between
live (1539) and backtest (392) is exactly the HTF-warmup feed.

## Concrete decomposition (AAPL, 09:35 ET, 2026-04-29)

```
Live   pd_high.barCount = 1539 = 734 (replay)        [sub-bug A — fixed in PR #23]
                                + 800 (HTF warmup)    [sub-bug C — this plan]
                                +   5 (runtime)       [correct]

Backtest (post-PR #23)  =  392 ≈ 387 (replay, RTH-only)  + 5 (runtime)
```

After PR #23 deploys to live, live's barCount will drop from 1539 to
~1192 (the replay portion correctly RTH-filters from 734 to 387, the
HTF-warmup contribution remains at 800). Closing the gap to backtest's
392 requires removing the HTF-warmup feed.

## Root cause

`AVWAPStrategy.ReplayOnBar` at
`backend/internal/app/strategy/builtin/avwap_v1.go:97`:

```go
func (s *AVWAPStrategy) ReplayOnBar(_ start.Context, _ string, bar start.Bar, st start.State, indicators start.IndicatorData) (start.State, error) {
    avwapSt, ok := st.(*AVWAPState)
    ...
    avwapSt.Indicators = indicators
    avwapSt.Calc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)  // <-- this
    avwapSt.CalcBarCount++
    ...
}
```

Calc.Update is called for every bar passed to ReplayOnBar. This is
correct when the bar is a runtime bar in the same anchor window, but
wrong during HTF warmup: the warmup loader is supposed to seed the
indicator calculator (RSI/EMA/MACD), not retroactively fill the AVWAP
anchor. The anchor's barCount and CumPV/CumV should come only from
`replayBarsForAnchors` which knows the anchor_time and the correct
replay window.

The boot path that triggers it is at
`backend/cmd/omo-core/warmup.go:471`:
```go
n := svc.strategyRunner.WarmUpTF(string(sym), string(htfTF), bars, runnerWarmupSnapshotFn)
```
WarmUpTF iterates instances assigned to the given timeframe, calls
`Instance.WarmupOnBar` which calls `ReplayableStrategy.ReplayOnBar`
for replay-aware strategies (AVWAPStrategy is one). For each of the
800 5m warmup bars, ReplayOnBar runs and Calc.Update increments
pd_high.barCount when isRTH(bar.Time) and RTHOnly is set.

avwap_v4 is configured for `timeframes = ["5m"]` per
`backend/configs/strategies/avwap_v4*.toml`, so the 5m HTF warmup
delivers all 800 bars to its instances. Other strategies on different
timeframes would experience the same issue at their own scale.

## Approach

The semantic invariant: **AVWAP anchor state comes from
replayBarsForAnchors only.** Warmup feeds the indicator calculator,
not the per-anchor accumulator. ReplayOnBar's call to Calc.Update is
correct for non-warmup paths (mid-session bar replay during state
recovery) but wrong during HTF warmup.

Two candidate approaches; pick during Phase 1.

### Approach A (preferred): Warmup-mode signal threaded through ReplayOnBar

Add a `Warmup bool` flag to `start.IndicatorData` (or a new
parameter on the ReplayableStrategy interface). Set it true from
`WarmUpTF`'s call path, false from any production replay path.
`ReplayOnBar` skips `avwapSt.Calc.Update` when `indicators.Warmup`
is true — the anchor stays driven exclusively by
`replayBarsForAnchors`.

Tradeoff: one new field on a frequently-passed struct, but the change
is localized and explicit. Keeps the per-anchor invariant (state =
replayBarsForAnchors output) load-bearing in the type system, not
just by convention.

### Approach B: Make backtest symmetric instead of removing live's feed

Have backtest also do an HTF warmup that feeds Calc.Update through
ReplayOnBar (currently it does not — `omo-replay/main.go` doesn't go
through `WarmUpTF` for HTF). Pin the symmetry test in unit tests.

Tradeoff: leaves a load-bearing semantic where the anchor's state
depends on what bars happened to be in the indicator-warmup window,
which is conceptually fragile. Backtest convergence might be achieved
but the design is less defensible. Reject unless Phase 1 surfaces a
reason live cannot stop feeding Calc during warmup.

### Edit (assuming Approach A)

1. `backend/internal/ports/strategy/strategy.go` (or wherever
   `start.IndicatorData` is defined): add `Warmup bool` field.
2. `backend/cmd/omo-core/warmup.go`: in `runnerWarmupSnapshotFn` (and
   any other warmup snapshot function), set `indicators.Warmup = true`
   on the returned `start.IndicatorData`.
3. `backend/internal/app/strategy/builtin/avwap_v1.go:97` — wrap the
   `avwapSt.Calc.Update` call: `if !indicators.Warmup { ... }`.
4. Companion: scan other ReplayableStrategy implementations for the
   same pattern (`break_retest_v1.go`, `macd_v1.go`, anything else).
   Each should be audited; AVWAP is the one with the parity gap, but
   the warmup-vs-runtime distinction may matter for others too.

## Phase 1 (investigation; complete before any code edit)

1. Verify the decomposition: rebuild omo-core with PR #23 merged,
   bring up live in paper mode at pre-market on a non-holiday day, and
   capture the EntryGated payload at the first 5m close. Predict
   barCount = 387 (replay, RTH-only post-#23) + 800 (HTF warmup) +
   today's runtime RTH bars. Confirm or root-cause the gap.
2. Read the omo-replay backtest path (`backend/cmd/omo-replay/main.go`
   plus the backtest pipeline) and confirm that backtest does NOT go
   through `strategyRunner.WarmUpTF` for HTF — i.e., the 800-bar feed
   is live-only. If backtest does have an analog feed, both sides are
   inflated (just different windows) and the right fix is symmetric.
3. Audit other ReplayableStrategy implementations
   (`break_retest_v1.go`, anything else) for `Calc.Update`-style state
   mutations in their ReplayOnBar — the warmup-mode skip may need to
   apply to them too, or may be specifically scoped to AVWAP.

Deliverable: a one-paragraph confirmation that the live-only HTF
warmup feed is the entire gap, plus a per-strategy table showing
which ReplayOnBar implementations need the warmup-mode guard.

## Files (expected, finalize after Phase 1)

- `backend/internal/ports/strategy/strategy.go` (or wherever
  `IndicatorData` lives) — Edit 1.
- `backend/cmd/omo-core/warmup.go` — Edit 2 (set `Warmup` flag in
  warmup snapshot funcs).
- `backend/internal/app/strategy/builtin/avwap_v1.go:97` — Edit 3
  (gate the Calc.Update on `!Warmup`).
- One unit test pinning that `WarmUpTF` for a 5m strategy does not
  inflate `pd_high.barCount` beyond the replay window.
- Possibly `break_retest_v1.go`, `macd_v1.go` — pending Phase 1 audit.

Estimated diff: ~30-50 LOC across 3-5 files plus ~80 LOC of test.

## Verification

1. **Build + unit tests**: `go build ./... && go test ./...` pass.
2. **Targeted unit test**: `WarmUpTF(sym, "5m", 800 bars, ...)` on a
   strategy with an RTHOnly pd_high anchor must leave
   `pd_high.barCount == 0` (anchor state should be empty until
   `replayBarsForAnchors` runs separately). Currently fails at
   barCount = ~400 (RTH portion of 800).
3. **Live↔backtest parity (the canonical reproducer)**: re-run the
   query that surfaced the gap:
   ```
   SELECT payload->'avwapState'->'anchors'->'pd_high'->>'vwap'
            AS live_pd_high_vwap,
          payload->'avwapState'->'anchors'->'pd_high'->>'barCount'
            AS live_pd_high_bars
   FROM strategy_signal_events
   WHERE ts = '<first-5m-close-of-target-day>+00' AND symbol='AAPL';
   ```
   PASS criterion: backtest matches live within 1e-6 relative on vwap
   and exact integer on barCount on a non-holiday weekday post-deploy.
4. **Trade-list convergence on 2026-04-29 retro-backtest** (and at
   least one more representative day): `>=2 of 4` live trades have a
   backtest analog (same underlying, direction, ±15 minutes). This is
   the original parity_rth_only_indicators_plan's verification step
   that's currently blocked at 1/4 by the residual pd_high/pd_low
   divergence. Once both sub-bugs are closed, the residual gap should
   reduce to option-chain divergence + SimBroker fills only — those
   are documented Out-of-Scope items and do not block this plan.

## Risks

- **Live behavior change at first session-day-boundary post-deploy**:
  pd_high / pd_low VWAP will shift by another few bps once the 800-bar
  HTF-warmup contribution drops out. Combined with PR #23's shift,
  total move from current state will be ~1.5-2% on those VWAPs.
  Strategies reading them (avwap_v4 confluence scoring, slope gate)
  will see a corresponding step. Bounded; deploy outside trading hours
  or ramp by symbol.
- **`Warmup bool` on IndicatorData is a cross-cutting type change**:
  every strategy that consumes `start.IndicatorData` could in
  principle observe and act on the flag. Audit the consumers in Phase
  1 to confirm none today branches on absence of the field, then add
  the field as zero-value-default-false so existing strategies
  silently keep their current behavior.
- **Audit-coverage risk on other ReplayableStrategy implementations**:
  if another strategy (e.g., break_retest) has its own state-mutating
  warmup path, this plan must catch it. Phase 1 must produce a table.
- **Approach B fallback**: if some downstream consumer relies on
  AVWAP state being warmed up by the indicator-warmup window (e.g., a
  metric that reads pd_high.vwap immediately after boot before
  replayBarsForAnchors runs), removing the feed could regress that
  consumer. Phase 1 should grep for such reads.

## Halt conditions

- Phase 1 fails to confirm that `WarmUpTF` is the source of the
  remaining 800 bars — halt and re-investigate (the decomposition
  hypothesis is wrong, look elsewhere for the residual gap).
- Backtest also has an HTF-warmup feed equivalent — halt and switch
  to Approach B (or a hybrid).
- Audit reveals 3+ other ReplayableStrategy implementations with the
  same pattern but each needing a different fix — halt and split the
  plan into per-strategy fixes.
- After fix, live<->backtest parity at 09:35 ET still shows >5%
  relative VWAP delta — halt; there's a fourth sub-bug not in the
  diagnosis. Investigate before continuing.

## Out of scope

- Option-chain divergence (live IBKR vs DoltHub historical) — same as
  prior plans.
- SimBroker fill model differences — same.
- Late-session 16 ET hours-block delta — same.
- Anchored VWAP's local `isRTH` early-close-day discrepancy — flagged
  in PR #22 review notes, still a separate follow-up.
- The `ResetAnchors` "preserve state when anchor unchanged" branch
  (avwap_v1.go:998) — Phase 1 of the predecessor plan ruled it out as
  the cause of the day-boundary case. Revisit only if Phase 1 here
  surfaces a same-day-restart accumulation pattern that this plan's
  fix doesn't address.

## Reference data

- PR #22 (parity-rth-only-indicators) — merged 2026-04-30 03:10 UTC,
  commit b74d5dcb. Closed RTH gating for `IndicatorCalculator.Update`
  and HTF push sites; established session_open byte-identical parity.
- PR #23 (parity-pd-anchor-replay-scope) — open at time of writing.
  Closes sub-bug A (UpdateSingleAnchor RTHOnly gate) and Edit 1b
  (monitor service.go:835 RTHOnly propagation).
- Predecessor plan: `_workspace/parity_pd_anchor_replay_scope_plan.md`
  (commit 762697a8). This plan extends the same parity goal to the
  remaining gap surfaced during PR #23 verification.
- Canonical reproducer query and 1539 = 734 + 800 + 5 decomposition
  are documented in PR #23's body.
- Strategy timeframe wiring: `backend/configs/strategies/avwap_v4*.toml`
  declares `timeframes = ["5m"]`, which is why this strategy's
  AVWAPState is fed by the 5m HTF warmup path.
