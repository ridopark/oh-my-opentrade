# omo-signal-corr-hvn-ema

Read-only research harness that grades the HVN/EMA diagnostic tags
shipped on `avwap_v4_equity` (PR #91) and `avwap_v4` options (Track C
of `_workspace/avwap_hvn_diag_followups_plan.md`). Consumes a backtest
result JSON, pairs entry rows to exit rows to attach realized PnL,
buckets entries by HVN/EMA tag values, and reports per-bucket
profit-factor lift on a time-OOS split AND a symbol-OOS split.

## Decision rule (locked)

PF lift = full_PF - near/within_PF.

- **PASS** iff |lift| >= 0.10 on BOTH holdouts AND lift signs agree.
- **FAIL** if either holdout fails the threshold OR the holdouts
  disagree on sign (the documented cofire reversal-factor failure
  mode -- see `feedback_factor_validation.md`).

PASS on either factor -> draft a separate active-promotion plan
(HVN veto wrapper a la cofire, OR EMA gate a la dp_z_conditioning).
FAIL on both -> shelve. Tags stay in the trade log because removing
them would invalidate any future re-grading; they cost almost nothing
and a future analyst may find something this harness missed.

## Owner + revisit

Owner: quant-analyst sub-agent.
Revisit date: **2026-05-21** (14 days from PR #91 merge on 2026-05-07).

On the revisit date, EITHER:
- (a) Harness has been run, verdict documented; promotion plan drafted
  (PASS) or factor shelved (FAIL).
- (b) Harness has NOT been run -> escalate. Tags are dead-letter
  telemetry until graded.

## Usage

```
omo-signal-corr-hvn-ema --trade-log <path-or-dir>[,...] \
  [--strategy avwap_v4_equity] \
  [--out _tmp/signal_corr_hvn_ema/<timestamp>] \
  [--time-holdout-days 30] \
  [--symbol-holdout-fraction 0.30] \
  [--symbol-holdout-min-trades 5]
```

Inputs: backtest result JSON paths or a directory of them. Each file
must have a top-level `trades` array; each row is one fill (entry rows
have `direction in {"LONG","SHORT"}`; exit rows have
`direction in {"CLOSE_LONG","CLOSE_SHORT"}` and a numeric `pnl` field
holding the realized round-trip PnL).

Outputs (in `--out`):

- `summary.txt`: per-bucket n / win_rate / PF / avg_pnl, reported for
  full sample, time-OOS, and symbol-OOS scopes.
- `pf_lift.txt`: the decision-rule view -- per factor, lift on both
  holdouts and a PASS / FAIL / INSUFFICIENT_DATA verdict.
- `per_trade.csv`: every paired round-trip with input tags, bucket
  assignments, and split labels. Auditable; lets the next person
  re-bucket without re-running.

## Buckets (locked)

- **HVN**: `hvn_dist_atr <= 0.5` (near) vs `> 0.5` (far). The 0.5 ATR
  threshold is the original PR #91 hypothesis range.
- **EMA**: `|ema_dist_atr| <= 1.0` (within) vs `> 1.0` (outside).

Quantile cuts are NOT used; thresholds are hard-coded so verdicts are
reproducible across re-runs as more data arrives.

## Caveats

- **Options strategies (avwap_v4) have unique-per-contract symbols**
  (e.g. `AMD260213C00232500`). The symbol-holdout picks the smallest-N
  symbols that have at least `--symbol-holdout-min-trades` entries;
  each option contract typically has 1 trade in a 3-month window, so
  the holdout ends up empty and the symbol-OOS verdict is
  `INSUFFICIENT_DATA`. To grade options data with a meaningful symbol
  split, future work should extract the underlying ticker from the
  contract symbol before applying the holdout.
- **The v10 trade log was generated before PR #91**; it has zero
  entries with the HVN/EMA tags and the harness correctly reports
  `INSUFFICIENT_DATA` on it (the no-data smoke path).
- **PaperActive trade-log accumulation is slow** (~5 entries/day live
  across 34 symbols on a long-only book). Generate a fresh fat batch
  via backtest replay over the full available window before depending
  on live data; see plan section 5.9.
- **The harness reads `pnl` from the exit row** as the realized
  round-trip PnL -- the consequence of all the engine's exit logic.
  We are grading the entries the strategy actually fired, not "what
  if we had also fired here".

## Out of scope

- Loading bars from TimescaleDB (the existing `omo-signal-corr` tool
  serves that path).
- Synthesizing alternative signals.
- DB writes.
- Producing an active-promotion plan; that is the next step ONLY when
  this harness reports PASS on at least one factor.
