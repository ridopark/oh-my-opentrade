# Runbook: HVN/EMA factor shadow → active promotion (avwap_v4_equity)

Companion to `_workspace/avwap_hvn_ema_promotion_plan.md`. Phase 1
shipped the engine wiring + TOML knobs in shadow-only-by-default mode
(see PR following this commit). This runbook is the operator-facing
checklist for actually flipping the knobs over the calendar windows
the plan defined.

Phases 2-4 require live trading days to elapse and cannot be executed
by Claude in a single session. Each phase below names: trigger
condition, exact TOML edits, halt criteria, end-of-window verdict
gates.

## Pre-flight (one-time)

Confirm before starting any phase:

- avwap_v4_equity.toml currently has `hvn_factor_*` and `ema_factor_*`
  knobs all at default OFF (verify: `grep -E "^hvn_factor|^ema_factor"
  configs/strategies/avwap_v4_equity.toml` shows enabled=false and
  shadow=false on both factors).
- Phase 0 verdict file exists at
  `_workspace/v4eq_year_<run_date>/pf_lift.txt`. Note the current
  status (INSUFFICIENT_DATA on both factors as of 2026-05-07; see
  plan section 3.2 for the gate-pass paths).
- Pre-shadow snapshot of live trade log saved to
  `_workspace/avwap_v4_equity_preshadow_<date>.json` (use:
  `omo-replay --backtest --strategies avwap_v4_equity --from <90d ago>
  --to <today> --output-json _workspace/avwap_v4_equity_preshadow_<date>.json`)
  for the attribution baseline. Compute the trailing 20-day baseline
  metrics: trade count, win rate, single-day max loss.

## Phase 2: EMA shadow rollout (target start 2026-05-20 earliest)

### Trigger conditions to start

ALL must be true:
- [ ] Phase 0 verdict on EMA factor is PASS (n_near >= 50 on both
      holdouts AND |lift| >= 0.10 AND signs agree).
      OR explicit operator override documented in
      `_workspace/ema_shadow_override_<date>.md` if proceeding without
      PASS.
- [ ] Phase 1 PR merged to main (this PR).
- [ ] Pre-shadow snapshot saved.

### Edit to start shadow

```
# configs/strategies/avwap_v4_equity.toml
ema_factor_shadow = true   # was false
```

Single TOML edit, `omo-core` picks it up on next spec-watch cycle (or
at restart). No rebuild required.

### Daily halt-check (5 trading days into the window, then end-of-window)

Run an attribution query against the live PaperActive trade-log and
verify all of:

- [ ] Shadow-flag rate (entries with `ema_factor_would_extend = "1"`
      / total entries) is between 5% and 50% on the trailing 5-day
      window. Below 5%: signal too rare. Above 50%: too frequent.
- [ ] Tag-emission rate >= 95% on entries with the spec's
      `ema_factor_shadow=true` set at the bar's time.
- [ ] No panics, NaN tags, or snapshot serialization errors
      attributable to the EMA factor code path. Check Loki/journald
      for `level=error` lines containing `ema_factor`.
- [ ] Sign-flip check among shadow-flagged exits: realized PnL of
      would-extend exits vs non-flagged exits in first half vs second
      half of the window. If sign of (would-extend PnL minus baseline
      PnL) FLIPS between halves -> halt.

If any halt criterion fires: revert `ema_factor_shadow = false`,
document the trigger in `_workspace/ema_shadow_revert_<date>.md`,
shelve the factor.

### End-of-window verdict (target 2026-06-03 earliest)

Hard cap: 10 trading days OR 50 fresh tagged entries, whichever
first. One-time 5-day extension if entry count below 50.

Decision:

- **PASS**: realized PnL on would-extend-flagged exits beats baseline
  by margin matching harness prediction (within +-20%) AND sign-flip
  check passed. Advance to Phase 3.
- **FAIL**: PnL within noise band of baseline (no edge) OR sign-flip
  triggered. Revert TOML knob, document, shelve.
- **INSUFFICIENT_N**: entry count < 50 after extension. Auto-shelve.

Document verdict in `_workspace/ema_shadow_verdict_<date>.md`.

## Phase 3: EMA active promotion (target start after Phase 2 PASS)

### Trigger conditions

ALL must be true:
- [ ] Phase 2 produced a PASS verdict.
- [ ] Canary spec file scaffolded (see step below).

### Canary spec setup

Clone the equity spec to a canary file:

```
cp configs/strategies/avwap_v4_equity.toml configs/strategies/avwap_v4_equity_canary.toml
```

Then in the canary file:

1. Change `[strategy].id` to `avwap_v4_equity_canary`.
2. Change `[lifecycle].state` to `Inactive` if you want to soft-launch
   without immediate live exposure, OR `PaperActive` for live canary.
3. Edit `[routing].symbols` to the 5 canary symbols only:
   ```
   symbols = ["AAPL", "MSFT", "SPY", "QQQ", "JPM"]
   ```
4. Edit `[lifecycle]` priority lower than the main spec (e.g. 70 vs
   80) so any conflict resolves toward the main spec.
5. Reduce risk knobs (locked for first 10 active days):
   ```
   risk_per_trade_bps = 250    # was 500
   max_position_bps = 750      # was 1500
   ```
6. Flip the EMA factor active:
   ```
   ema_factor_enabled = true   # was false
   ema_factor_shadow = false   # was true
   ```

Set the main `avwap_v4_equity.toml` back to `ema_factor_shadow=true`
(continue shadowing on the wide universe for ongoing observation).

### Auto-revert halt criteria (active mode)

ANY of the following triggers immediate revert via TOML knob flip
(`ema_factor_enabled=false, ema_factor_shadow=true` on the canary spec):

- Strategy-level realized PnL drawdown > 1.5% of $100k initial equity
  ($1,500) over any 5-trading-day rolling window AND attribution shows
  >= 50% of the loss from EMA-factor-affected exits.
- Trade count drops > 40% vs trailing 20-day pre-flip baseline.
- Win-rate falls below pre-flip baseline minus 10 percentage points
  over 30+ trades.
- Any single-day loss > 2x the worst pre-flip single-day loss in the
  trailing 60 days.
- Tag-emission rate drops below 95%.
- Panic/NaN/snapshot error attributable to factor code path.

Document every revert with the trigger condition that fired in
`_workspace/ema_active_revert_<date>.md`.

### End-of-window verdict (target 2026-06-17 earliest, 10 trading days
   after canary active start)

Decision:

- **STABLE**: all halt criteria green for 10 trading days. Restore
  risk knobs (`risk_per_trade_bps=500, max_position_bps=1500`) on
  the canary, then widen universe to full 34 by deleting the canary
  spec and flipping the main `avwap_v4_equity.toml`:
  ```
  ema_factor_enabled = true
  ema_factor_shadow = false
  ```
- **REVERTED**: document halt cause; if wiring bug, fix and re-shadow;
  if edge-decay finding, shelve.

## Phase 4: HVN shadow + active (target start after Phase 3 STABLE on
   wide universe for 14 days OR Phase 3 shelve)

Same shape as Phase 2 + Phase 3, substituting `hvn_factor_*` knobs for
`ema_factor_*`. Differences:

- HVN is a binary BLOCK; calibrate the `would_block` rate (not
  `would_extend`). Acceptable range: 1-30% of entries (lower than
  EMA's 5-50% because HVN strictly REDUCES the entry stream by
  design).
- Trade-count starvation halt criterion tightens from 40% to 70%
  (vetoes are expected to reduce trade count substantially).
- **Sign-flip awareness:** the year-long v4_equity verdict
  contradicts the original "near-HVN HURTS" hypothesis (n=10 with
  PF=2.162 vs far-HVN n=229 PF=0.875). Before flipping
  `hvn_factor_enabled=true`, RE-RUN the harness on the latest
  trade-log and confirm that the locked decision rule clears with
  signs in the SAME direction the wired-in veto would block. If the
  signs say "near-HVN OUTPERFORMS" (lift negative on both holdouts
  with n_near>=50), DO NOT flip enabled=true. The veto wiring would
  block the only profitable bucket. Switch to the booster wiring
  shape (plan section 7.1) before promoting.

## Halt summary (one-line each, for quick reference)

- shadow flag rate out of [5%, 50%] band 5-day rolling -> halt
- tag emission rate < 95% -> halt
- sign flip across halves -> halt
- DD > 1.5% in 5d AND >50% factor-attributable -> halt
- trade count drop > 40% (EMA) or 70% (HVN) -> halt
- WR drop > 10ppt over 30+ trades -> halt
- single-day loss > 2x worst pre-flip 60d -> halt
- panic / NaN tag / snapshot error -> halt

## Files this runbook touches (no engine code changes)

- `configs/strategies/avwap_v4_equity.toml` (knob flips)
- `configs/strategies/avwap_v4_equity_canary.toml` (Phase 3 only;
  delete after wide-rollout)
- `_workspace/{ema,hvn}_shadow_verdict_<date>.md`
- `_workspace/{ema,hvn}_active_revert_<date>.md`
- `_workspace/avwap_v4_equity_preshadow_<date>.json` (one-time
  baseline)

## Owner + cadence

- Owner: ridopark (user). Reassignable to quant-analyst sub-agent on
  explicit transfer.
- 14-day check-in cadence from Phase 1 merge date. Each check-in
  updates the verdict file with status and any halt-criterion that
  fired.
