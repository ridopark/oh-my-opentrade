# avwap_long_v1 Phase 0 verdict: HALT

Date: 2026-05-06
Status: Plan halted at Phase 0 per its own decision rule.
Plan: _workspace/avwap_long_v1_plan.md

## Decision rule (from plan section 4)

- Train PF >= 0.95 AND holdout PF >= 0.95 -> proceed to Phase 2.
- Either window PF in [0.85, 0.95) -> structural diagnostic before Phase 2.
- Either window PF < 0.85 -> stop. avwap engine has no long-only edge
  on this universe; new spec with same engine will not fix it.

## Result

Both windows fall below the 0.85 floor. Halt is the plan-correct call.

| Window  | Range                       | PF     | WR    | Trades | PnL ($100k) | MaxDD | Sharpe |
|---------|-----------------------------|--------|-------|--------|-------------|-------|--------|
| Train   | 2025-05-06 → 2026-01-31    | 0.7316 | 46.25%| 240    | -$5,447     | 6.83% | -2.32  |
| Holdout | 2026-02-01 → 2026-05-06    | 0.7338 | 45.87%| 109    | -$2,529     | 3.61% | -3.03  |

Config: `/tmp/avwap_phase0/avwap_v4_equity.toml` (a copy of
`configs/strategies/avwap_v4_equity.toml` with the single change
`allowed_directions = ["LONG"]`). 10 bps slippage, 5m, 34-sym universe,
$100k equity, max_positions=6, max_per_group=3, no AI.

Backtest result files:
- `_workspace/avwap_v4_equity_long_only_train_10bps.json`
- `_workspace/avwap_v4_equity_long_only_holdout_10bps.json`

## Quant breakdown on the train baseline (n=240)

Per-symbol (n >= 8):
- Train winners (PF, n, PnL): AVGO 2.33/9/+$425, HOOD 2.16/18/+$917,
  SOFI 1.36/13/+$303, SOXL 1.33/17/+$380, COIN 1.09/15/+$240,
  SMCI 1.05/14/+$46. Six names, +$2,150 train.
- Train losers (PF, n, PnL): MRVL 0.17/9/-$1,047, RIVN 0.27/12/-$1,079,
  AFRM 0.28/10/-$597, MU 0.37/17/-$1,218, MRNA 0.45/10/-$793.
- Holdout sanity on the six train-winners: 4 of 6 flip negative
  (AVGO 0.55, HOOD 0.51, SOFI 0.00, SOXL 0.28). Per-symbol edge does
  NOT generalize.

Per entry-hour (ET, 30-min buckets):
- 09:30  n=197 (82% of flow)  PF 0.693  PnL -$5,138
- 10:00  n=31                 PF 0.840  PnL -$430
- All other buckets n <= 3.
- No bucket clears PF >= 1.0 AND n >= 30. Engine fires almost
  exclusively at 09:30 and that is where the bleed concentrates.

Per regime (TOML allows TREND_UP and TREND_DOWN):
- TREND_UP    n=198  PF 0.699  PnL -$5,647
- TREND_DOWN  n=42   PF 1.130  PnL +$200  (flips to PF 0.517 holdout)
- Counter to the "longs in TREND_DOWN fade trend" hypothesis: the
  bleed is in TREND_UP, not TREND_DOWN. Restricting to TREND_UP would
  not help; restricting to TREND_DOWN gives n=42 train and flips OOS.

Per confluence-score bucket:
- 10/0.59, 11/0.79, 12/0.77 (modal n=104), 13/0.85, 15/0.76 — no
  monotone separation. Tightening to 15 lifts PF to ~0.79, still
  below 0.85. Confluence stack does not separate long-side winners.

## What this confirms

The session's prior finding (whale_pullback_v1 sub-slice diagnostic)
held: on this universe at this regime, the SHORT side had edge and the
LONG side bled. avwap_v4_equity inherits the same directional asymmetry.
Long-only equity on this universe is unprofitable through this engine
and no parameter narrowing within the existing config space rescues it.

## What this rules out for this iteration

- A narrower long-only sub-slice (per-symbol, per-hour, per-regime,
  per-confluence-bucket) that justifies writing avwap_long_v1.toml.
- Any tuning pass on the proposed Phase 2 spec — the entries are
  wrong, not the exits.

## What is left for a future pass (out of scope here)

Per plan section 4: "change the universe choice, change the engine
selection (e.g., a momentum or breakout-only engine), or shelve the
long-only equity ambition entirely." All three are scope-bumps that
need a new plan, not a parameter sweep on this one.

## Plan execution outcome

- Phase 0: completed. Verdict HALT.
- Phase 1: proceeds (independent of Phase 0).
- Phase 2: skipped (plan-gated on Phase 0 passing).
- Phase 3: skipped (plan-gated on Phase 2).
