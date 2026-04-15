# AVWAP v4 CHANDELIER_TRAIL Realism Assessment

**Date:** 2026-04-14
**Commit:** `a2c49f1` feat(exit): premium-aware CHANDELIER_TRAIL + adopt for avwap_v4
**Author:** strategy tuning harness + quant-analyst sign-off
**Status:** Shipped on paper; realistic planning numbers recorded here

## TL;DR

The tuning session headline number (PF 2.311, DD 2.59%, Sharpe 6.58) was run in the simulator's **optimistic mode** (no option entry spread, `option_spread_multiplier=1.0`). Under the **realistic planning mode** (`option_entry_spread_enabled=true`, `option_spread_multiplier=2.0`) the same config produces **PF 1.98, DD 3.97%, Sharpe 5.37** — roughly a 14% haircut that matches the historically-calibrated baseline haircut.

The chandelier's relative advantage over the prior `PREMIUM_TARGET 6%` baseline survives the spread stress: +13% PF, +$13k/yr PnL, -1.8pp DD. The strategy was shipped on paper accounts with guardrails, but operators should plan against the realistic numbers, not the tuning headline.

## Why the numbers initially looked too good

User callout ("this is too good to be true") triggered a realism audit. The session had reported, against an all-defaults `/backtest/run` request:

| Metric | PT 6% baseline | Shipped (a=0.04 g=0.05) |
|---|---|---|
| PF | 1.996 | **2.311** (+16%) |
| Sharpe | 5.70 | **6.58** |
| DD | 4.84% | **2.59%** |
| PnL (1yr, $100k base) | $178.6k | $193.1k |

Two red flags, only the first of which had been investigated during tuning:

1. **MFE-vs-exit path artifact** — could the simulator be capturing intrabar peaks it couldn't actually fill? Audited and refuted: both MFE tracker (`exit_eval.go:129`) and exit fill (`exit_eval.go:106`) read `snap.Price = bar.Close`, same time point. No look-ahead.

2. **Option fill realism** — this was NOT checked during tuning. The tuning harness used the API's default `option_entry_spread_enabled=false` and `option_spread_multiplier=1.0`, which the prior tuning pass (commit history on PREMIUM_TARGET 6%) had flagged as "optimistic — do NOT use for planning." That's the gap this document closes.

## The realism knobs the simulator actually exposes

From `backend/internal/adapters/simbroker/broker.go` and the backtest HTTP handler:

| Knob | Type | Default | Source |
|---|---|---|---|
| `SlippageBPS` | int64 | 5 | per-request API field `slippage_bps` |
| `OptionExitSpreadMultiplier` | float64 | 1.0 | per-request `option_spread_multiplier` |
| `OptionEntrySpreadEnabled` | bool | false | per-request `option_entry_spread_enabled` |
| `VIXIVBeta` | float64 | 0.7 | bootstrap-only, not per-request |
| `TODSeasonalEnabled` | bool | true | bootstrap-only |
| `EarningsRampEnabled` | bool | true | bootstrap-only |

**Tiered spread percentages** (`broker.go:639-650`), applied as a half-spread on each fill:

| Entry premium | Spread % |
|---|---|
| ≥ $10.00 | 0.3% |
| ≥ $5.00 | 0.5% |
| ≥ $2.00 | 0.8% |
| < $2.00 | 1.5% |

With `option_spread_multiplier=2.0` and `option_entry_spread_enabled=true`, a sub-$2 option pays 3% on entry AND 3% on exit — a 6% round-trip haircut before any directional P&L.

Exit-fill paths:
- **Multi-day holds:** historical bid from DoltHub via `HistoricalOptionsPort.GetHistoricalContract` when available (`broker.go:523-527`).
- **Same-day exits:** BSM recomputed with entry IV adjusted by VIX/TOD/earnings knobs, minus the tiered half-spread (`computeOptionExitPrice` at `broker.go:484+`).
- **Fallback:** delta-linear approximation (`broker.go:604-634`).

## Comparison matrix (27 symbols, 5m, 2025-04-14 → 2026-04-14)

All runs: `slippage_bps=10`, `max_positions=6`, `max_per_group=3`, `initial_equity=$100k`, `no_ai=true`, `compound_equity=true`.

| Config | Mode | PF | WR | PnL | DD | Sharpe | Trades |
|---|---|---|---|---|---|---|---|
| PT 6% baseline | optimistic (mult=1.0, entry=off) | 1.996 | 58.1% | $178,559 | 4.84% | 5.70 | 999 |
| PT 6% baseline | **realistic (mult=2.0, entry=on)** | **1.749** | 58.2% | $145,341 | 5.75% | 4.63 | 999 |
| chandelier a=0.05 g=0.05 | optimistic | 2.116 | 61.6% | $180,020 | 3.94% | 5.99 | 1014 |
| chandelier a=0.05 g=0.05 | **realistic** | **1.841** | 61.5% | $146,924 | 4.98% | 4.88 | 1014 |
| **chandelier a=0.04 g=0.05** (shipped) | optimistic | 2.311 | 64.2% | $193,062 | 2.59% | 6.58 | 1021 |
| **chandelier a=0.04 g=0.05** (shipped) | **realistic** | **1.981** | 63.1% | $158,305 | 3.97% | 5.37 | 1021 |
| chandelier a=0.04 g=0.05 | stress (mult=2.5, entry=on) | 1.816 | 61.2% | $138,070 | 4.69% | 4.70 | 1019 |

### Sim calibration check

The PT 6% baseline drops 1.996 → 1.749 under realistic mode (-12.4%), which exactly matches the planning number recorded in `configs/strategies/avwap_v4.toml` during the earlier tuning pass ("Realistic (mult=2.0, entry spread on): PF 1.75"). The simulator is internally consistent across sessions.

### Realistic-mode relative advantage

- **Chandelier vs baseline (realistic):** PF 1.981 vs 1.749 = **+13.3%** ; PnL +$13k/yr ; DD -1.78pp ; Sharpe +16%.
- **Chandelier under 2.5x stress vs baseline realistic:** PF 1.816 vs 1.749 — edge shrinks but does not invert.
- **Haircut symmetry:** PT6 takes a 12.4% PF hit going optimistic → realistic; chandelier takes 14.3%. The chandelier pays slightly more spread because it exits ~5% more often (1021 vs 999 trades, and more of those are stop/trail closes that cross the spread twice).

## What realism knobs *don't* capture

The 14% haircut is a partial correction, not a complete one. Known gaps (none corrected in this session):

1. **IV crush on momentum spikes.** Entry IV is carried through same-day exits with only VIX/TOD/earnings adjustments. Real mid-cap options often see 10-30% IV contraction after the initial directional burst that triggered entry; BSM using entry IV will overstate exit premium.
2. **Partial fills and order rejects.** Sim fills 100% instantly. Real paper/live would see rejects on illiquid strikes and partial fills that force worse averages.
3. **Same-bar fill semantics.** When an exit triggers at bar close, the sim fills at that same bar close. In live, the market order submitted at bar close fills at the NEXT bar open plus slippage — approximately a 1-bar look-ahead per exit.
4. **Bid-ask skew.** The spread model is a symmetric half-spread; real options have ATM/OTM skew and gamma premium that isn't captured.
5. **Symbol survivorship.** The 27-name universe is hand-picked liquid tickers. No delisted / ticker-changed names in the set.
6. **Dark-pool / confluence data timing.** Replay data is bar-aligned; live feed has ingestion lag and revisions.

These biases are *directionally the same* for baseline and chandelier, so the **relative** advantage is more trustworthy than the **absolute** number. That's the basis for the ship decision: the chandelier's +13% PF edge is expected to survive the unmodeled realism gaps, but the absolute +$13k/yr uplift could be optimistic by a further 10-20% in live.

## Planning numbers to use going forward

When quoting avwap_v4 CHANDELIER_TRAIL performance to stakeholders, dashboards, or risk memos, use the realistic column:

| | Value |
|---|---|
| Profit Factor | **1.98** |
| Win Rate | 63% |
| Annual PnL ($100k base) | **~$158k** |
| Max Drawdown | **~4.0%** |
| Sharpe (daily, ann.) | 5.37 |
| Trade Frequency | ~4/day |

The tuning headline (PF 2.31) should only appear in tuning-internal contexts with a `[optimistic mode]` tag.

## Live ship guardrails (refreshed)

Original guardrails (set against optimistic expectation):
- Monitor first 50 live trades for realized spread delta
- Alert if rolling 20-trade PF < 1.6
- Hot-swap fallback: a=0.05 g=0.05

**Refreshed for realistic planning:**
- Monitor first 50 live trades for realized spread delta **vs modeled 2x tiered spread** (not the optimistic 1x).
- Alert if rolling 20-trade PF < **1.4** (was 1.6) — calibrated to realistic PF 1.98 with ~30% tail room.
- Alert if rolling 20-trade spread cost exceeds 3% of gross premium (model prediction for sub-$2 strikes).
- Hot-swap fallback: a=0.05 g=0.05 (realistic PF 1.84, still better than PT6 realistic 1.75).
- Hard fallback: revert to `PREMIUM_TARGET 6%` (the commented block in the TOML).

## Process notes

The tuning pass ran through the full strategy-tuning harness — baseline, MFE diagnostic, single-variable sweep, walk-forward halves, outlier ex-top-N, pair validation with macd_only_v1, quant-analyst consultation before and after — all in optimistic mode. The realism mode was only applied after the user's post-ship "too good to be true" callout. Future tuning passes that touch option exit rules should include a realistic-mode final-check by default; the harness skill should either make realistic-mode the default or require an explicit optimistic-mode flag.

The tuning skill file is `/home/ridopark/src/oh-my-opentrade/.claude/skills/strategy-tuning/SKILL.md` and does reference realism knobs in the TOML comment conventions but does not currently mandate a realistic-mode rerun before the ship decision.

## Appendix — TOML state

The shipped config in `configs/strategies/avwap_v4.toml` carries the optimistic-mode tuning metrics in its comment block. Those numbers are kept for tuning archaeology; this document is the single source of truth for **planning** numbers.

```toml
[[exit_rules]]
type = "CHANDELIER_TRAIL"
[exit_rules.params]
activate_pct = 0.04
giveback_pct = 0.05
```
