# whale_pullback_v1 — Pass 1+2 Tuning Postmortem

Date: 2026-05-06
Status: Overfit; reverted TOML to pre-tune baseline. Strategy not ready
for live or extended paper.

## TL;DR

Two passes of tuning on 4 symbols (NVDA/JPM/RIVN/LLY) over a 4-month
in-sample window (2026-01-01 to 2026-04-30) lifted backtest PF from
0.485 → 1.042 (+115%). When the same configs were run on the held-out
12-month window (2025-01-01 to 2025-12-31), PF only moved from 0.481 to
0.587 (+22%) and stayed deeply negative-EV. The strategy as-defined
does not have generalizable edge in this universe with these rules,
and the in-sample headline gains were regime-specific artifacts.

## In-sample vs holdout matrix (10 bps)

    Config             2026 Q1 (IS)        2025 (OOS)
    -----------------  ------------------  ------------------
    Original baseline  PF 0.485, -14.07%   PF 0.481, -33.69%
    Pass 1 (7 chg)     PF 0.983,  -0.27%   PF 0.575, -24.35%
    Pass 2 (+2 chg)    PF 1.042,  +0.59%   PF 0.587, -22.36%

Tuning ROI was almost entirely captured in 2026 Q1. 2025 holdout shows
the strategy is structurally negative-EV in this universe.

## What was overfit

- All micro-tweaks to entry-quality (vwap_break_atr 0.5→0.8,
  pullback_touch_atr 0.15→0.10) extracted local edge in 2026 Q1 that
  doesn't hold in 2025.
- vp_clear_atr 0.6→1.0 (Pass 2): looked counterintuitive when accepted
  ("looser HVN-ahead lookahead improves edge"). Almost certainly a
  noise fit — physically defensible behavior would be that tighter
  veto helps.
- cooldown_seconds 1800→3600 (Pass 2): probably noise.
- max_loss pct 0.015→0.025: this one might be defensible since the
  loosening lets winners breathe; needs a wider window test before
  ruling on it.
- EOD_FLATTEN minutes_before_close 15→60: peak found at 60 in 2026 Q1;
  no idea if that peak survives in other regimes.

## What's likely real

- **TradesToday daily reset** — bug fix shipped commit `31415965`. Not
  a tuning choice; survives because it was always a correctness bug.
  Stays in the codebase.
- **STAGNATION_EXIT removal** — engine-level override was pre-empting
  the strategy's body-close exit. This is structural cleanup, not
  parameter tuning. Should ship even if other tuning rolls back —
  BUT the body-close exit is currently dead in backtest (separate
  engine bug, see backtest_fill_event_finding.md), so we can't yet
  validate the removal.
- **Universe pruning (drop SPY)** — SPY's 0.1% intraday ATR ratio is
  too tight for the ATR-relative thresholds. This is a setup-design
  observation, not a parameter tweak. Defensible in principle but
  not yet validated on 2025 specifically.

## Methodology errors

1. **Tuned on too narrow a window.** 4 months × 5 symbols = 282K bars.
   Looks like a lot, but it's a single regime. Quant's original
   recommendation was 16 months × 34 symbols. We took the shortcut.

2. **No anchored walk-forward.** The skill explicitly warns against
   "in-sample twice" — running split-half on data that was used to
   select the parameter value. We didn't run the held-out test until
   after both passes, so we got the false-positive PF lift without
   the cost.

3. **Trusted PF lift over fragility checks.** Pass 2's outlier
   dependency was flagged (removing largest winner drops PF below 1.0)
   but we still treated PF 1.042 as a result. Should have been a
   stronger stop signal.

4. **Backtest exit-rule bug masked the rule design.** The strategy's
   own body-close + ATR exits don't fire in backtest (see
   `whale_pullback_v1_backtest_fill_event_finding.md`). Most of the
   actual PnL captured by tuning came from engine-level rules
   (MAX_LOSS, EOD_FLATTEN), not from the strategy's spec'd exits.
   So we were tuning a different system than the one we'd ship.

## What to do next

Options ranked by recommendation:

1. **Fix the backtest fill-event delivery bug first** (engine-level).
   Until the strategy's own exits fire in backtest, tuning the
   strategy is tuning a phantom. Affects `whale_pullback_v1`,
   `break_retest_v1`, `avwap_v1`, `crypto_*` — anything that uses
   PendingEntry → PositionSide. See
   `_workspace/whale_pullback_v1_backtest_fill_event_finding.md`.

2. **Re-tune on the proper window once exits fire correctly.** Use
   the full 16-month range (2025-01-01 → 2026-04-30), the full 34-
   symbol universe, and a true train-test split. Anchored walk-forward.

3. **Skip whale_pullback_v1 for now.** Park the implementation, file
   the engine bug, return when the backtest harness can faithfully
   represent the strategy's intended exits.

4. **Add new structural rules** (e.g., the EMA9>EMA21 secondary bias
   from the TradingView Pine Script reference, or an ADX > 25 trend-
   strength filter). These would address the underlying issue — the
   strategy fires on too many weak setups — rather than tuning around
   it.

## Files & artifacts

- TOML reverted to `configs/backups/whale_pullback_v1_pre_tune_20260506_0813.toml`
  contents.
- Tuning result JSONs preserved in `_workspace/whale_pullback_v1_*.json`
  for archeology.
- Holdout artifacts:
  - `_workspace/whale_pullback_v1_holdout_2025_10bps.json` (Pass 2 cfg)
  - `_workspace/whale_pullback_v1_holdout_2025_pass1cfg.json`
  - `_workspace/whale_pullback_v1_holdout_2025_baseline.json`

## Lessons for the next strategy tuning session

1. ALWAYS test on a held-out segment BEFORE accepting any final pass.
   The split-half check is necessary but not sufficient.
2. If a strategy uses PendingEntry/PositionSide, verify
   FillConfirmation actually arrives during backtest by checking
   trade tags for strategy-emitted exit reasons. If all exits show
   `exit_monitor:EOD_FLATTEN` or `exit_monitor:MAX_LOSS`, the
   strategy's own exits aren't running.
3. A 4-month window is for spot-checking, not tuning. Anything that
   gets shipped as a parameter change needs >= 12 months of data.
4. If outlier-dependent (rule 11), don't accept the result; treat as
   a hint that more data is needed.
5. Trust the headline gain less. PF 0.485 → 1.042 looks like 2x
   improvement. PF 0.485 → 0.587 (the gain that actually generalizes)
   is +21% and still negative-EV — much closer to the truth.
