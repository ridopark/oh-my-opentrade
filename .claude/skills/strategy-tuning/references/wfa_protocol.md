# Walk-Forward Analysis Protocol

Discipline for avoiding in-sample-twice and menu-selection overfits in the
strategy-tuning skill. Referenced by SKILL.md Split-Half Validation and the
avwap_v4 re-plan doc.

## When this applies

Any parameter change that (a) touches the [params] or [[exit_rules]] sections,
(b) is derived from backtest data, AND (c) is proposed for live commit.
Exemptions: pure documentation changes, disabled-by-default feature flags,
reverts to prior known-good values.

## Anchored train/test split

Use a **single, anchored** split. Not k-fold. Not rolling. Not "run on both
halves." Anchored means: one train segment at the start, one test segment at
the end, test segment is never touched during fitting.

Default split for a 1-year backtest window (adjust in proportion for other
windows):
- **Train**: first 70% of the period (e.g., 2025-04-14 → 2025-12-20)
- **Test**: last 30% of the period (e.g., 2025-12-20 → 2026-04-14)

Test segment rule: **look at it exactly once**, after the decision is frozen.
If you peek at test results then re-tune, you have contaminated the holdout
and the test segment is dead. Extend the test cut forward and start over, or
accept that you are back to in-sample tuning.

## Pre-registration template

Before the first backtest of a parameter sweep, write down (in chat or in a
workspace file):

```
Hypothesis: {the parameter change improves {metric}}
Variants to test: {list, count = N}
Train period: {from → to}
Test period: {from → to}
Decision metric: {PF / Sharpe / combined}
Accept threshold: {delta ≥ X on train AND test}
Menu-selection correction: σ_threshold × √(2·ln(N))  where σ_threshold is the
  stddev of the metric under the null
Sample-size floor: {N_trades ≥ Y in both train and test}
Tiebreak: {if multiple variants pass, which wins}
```

The sweep runs on TRAIN. The best-of-N variant under the pre-registered
decision metric goes to the test segment ONCE. It either clears or it doesn't.

## Menu-selection correction (why this matters)

Under the null hypothesis that a parameter has zero true edge, running N
variants and picking the best still produces an apparent "win" by chance.
For N = 4 at α = 0.05, the false-positive rate is ~19%. For N = 10, it is
~40%. For N = 26 (one per 15-min bucket in RTH), it is ~73%.

Mitigation: require the winning variant's metric delta on TRAIN to exceed:

```
required_delta = baseline_stddev × √(2 × ln(N_variants))
```

This is a Bonferroni-like correction. If you don't know baseline_stddev,
bootstrap it from the baseline's trade-level PnL.

Applied to today's avwap_v4 case: 4 variants, baseline Sharpe ~7.14, typical
intraday strategy Sharpe stddev ~0.5. Required delta = 0.5 × √(2 × ln(4))
= 0.83. B-prime's +0.27 was well below this floor — not a real signal.

## What counts as "validation"

**Valid**: a single backtest of the frozen-config on the **unseen** test
segment, with pre-registered accept threshold, compared to baseline-A run on
the same test segment.

**NOT valid** (despite the name):
- "Split-half" where the parameter value was chosen using data from both
  halves (in-sample-twice). This is what the existing SKILL.md Split-Half
  section enables if misread.
- Running a 5-backtest sweep on H1, picking the best, running it on H2, and
  calling it walk-forward. That is still menu-selection plus in-sample-twice.
- The pair-validation backtest. That is cross-strategy robustness, not
  out-of-sample discipline.

## Anti-patterns observed in this project (2026-04-18 session)

- "Split the data, validate on each half" when the endpoint 09:55 was
  identified on the pooled data. The halves both saw the selection.
- "9/9 gates passed" framing when none of the 9 gates were pre-registered
  against a held-out segment.
- Comments like "09:50+ was breakeven" baking in bucket-selection overfit
  into the live config with no quantified uncertainty bound.
- Picking best-of-N on Sharpe without correcting for N.
- Declaring a +0.27 Sharpe delta a "win" when baseline Sharpe 7.14 is itself
  unrealistic (fill-model diagnostic was not run first).

## Gate chain for a parameter change going live

1. Fit on TRAIN. Pick best-of-N per pre-registered metric.
2. Menu-selection correction: does the winner's TRAIN delta exceed
   `σ × √(2·ln(N))`? If not, STOP. No live change.
3. Run winner on TEST exactly once. Does it beat baseline on TEST by the same
   pre-registered threshold? If not, STOP.
4. Sample-size check: ≥ 200 trades in both TRAIN and TEST.
5. Outlier check: PF excluding top-10 winners > 1.4.
6. Pair-validation: pair with deployed strategies, PnL regression < 2%.
7. Slippage stress: confirm the edge survives 3x slippage (see SKILL.md
   Diagnostic Backtests).
8. Fill-model sanity: confirm the baseline Sharpe is in the realistic range
   for the asset class. If Sharpe > 5 on intraday options, the fill model is
   suspect and the test result's ranking is unreliable.

Every check is binary, pre-registered, and gated. Skip any and the change is
not live-qualified — it becomes at best a documentation update.
