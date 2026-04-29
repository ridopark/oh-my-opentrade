---
name: backtest-analysis
description: "Backtest result interpretation, performance metric analysis, and strategy improvement direction. Use this skill when analyzing backtest results or interpreting trading performance metrics like PF/WR/drawdown/expectancy. Triggers on 'backtest result', 'profit factor', 'win rate', 'drawdown', 'expectancy', 'Sharpe', 'trade log', 'performance' keywords. Does NOT trigger for running a backtest itself."
---

# Backtest Analysis

Interpret oh-my-opentrade backtest results and derive strategy improvement directions.

**Evidence tags required.** Every metric, comparison, and conclusion in analysis output must carry `[actual]`, `[inference]`, or `[assumption]` per `../strategy-tuning/references/evidence_tags.md`. Untagged narrative is banned — it's the firewall against decisions being made on unclear provenance.

**Factor attribution before declaring edge.** If the analysis is being used to decide whether a strategy has real alpha (vs. factor exposure in disguise), run the regression in `../strategy-tuning/references/factor_attribution.md` and report alpha / betas / R-squared / residual Sharpe alongside the headline metrics. Gross PF and Sharpe alone can hide long-beta-long-vol as "edge".

## Running a Backtest

```bash
# Via HTTP API
curl -X POST http://localhost:8080/backtest/run -d '{
  "symbols": ["GOOGL","META","NFLX"],
  "from": "2025-01-01",
  "to": "2026-03-01",
  "timeframe": "5m",
  "initial_equity": 100000,
  "slippage_bps": 10,
  "strategies": ["{strategy_id}"],
  "no_ai": true
}'

# Retrieve results
curl http://localhost:8080/backtest/results/{id}
```

## Core Metric Interpretation Framework

### Tier 1: Profitability (always check)
| Metric | Meaning | Judgment Criteria |
|--------|---------|------------------|
| **Profit Factor** | Gross profit / gross loss | < 1.0 = losing, 1.2~2.0 = healthy, > 3.0 = overfitting suspect |
| **Net P&L** | Net profit (incl. slippage/commissions) | % of capital matters more than absolute value |
| **Win Rate** | Percentage of winning trades | 30~50% + high Win/Loss ratio is ideal |
| **Avg Win / Avg Loss** | Average profit / average loss | > 1.5 required, 2.0+ target |

### Tier 2: Risk (stop-loss effectiveness)
| Metric | Meaning | Judgment Criteria |
|--------|---------|------------------|
| **Max Drawdown** | Maximum peak-to-trough decline | < 10% ideal, > 20% dangerous |
| **Sharpe Ratio** | Risk-adjusted return | > 0.5 minimum, > 1.0 good |
| **Expectancy** | Expected profit per trade | Must be positive, account for slippage |

### Tier 3: Statistical Significance
| Metric | Meaning | Judgment Criteria |
|--------|---------|------------------|
| **Trade Count** | Total number of trades | < 30 = statistically meaningless |
| **Backtest Period** | Test duration | < 6 months = market regime bias |
| **Symbol Coverage** | Number of symbols | 1 symbol only = overfitting risk |

## Analysis Workflow

### Step 1: Grasp the Overall Summary
- Check Net P&L, PF, WR, trade count
- Basic sanity checks (trade count >= 30, PF < 3.0)

### Step 2: Analyze Trade Distribution
- P&L by symbol — not concentrated in a single symbol
- P&L by time of day — not only profitable in one time slot
- Max consecutive losses — within psychological tolerance

### Step 3: Analyze Exit Rule Effectiveness
- Which exit rule fired most frequently
- Ratio of SWING_STOP vs STAGNATION_EXIT vs EOD_FLATTEN vs MAX_LOSS
- Among stopped-out trades, what % would have turned profitable (premature exit detection)

### Step 4: Derive Improvement Directions
- PF < 1.2 — tighten entry filters (strategy-specific: confluence score, volume threshold, confidence, etc.)
- WR < 25% — entry conditions too loose or stops too tight
- Max DD > 15% — reduce risk_per_trade_bps, max_position_bps
- Trade count < 20 — filters too strict, loosen parameters
- Losses concentrated in specific hours — tighten time window or enable time-based filters
- Strategy-specific: read `[params]` section of the target strategy's TOML to identify which parameters map to entry/exit/timing

## Comparison Analysis Pattern

When comparing before/after DNA changes:
```
| Metric        | Before | After | Delta |
|--------------|--------|-------|-------|
| Profit Factor |        |       |       |
| Win Rate      |        |       |       |
| Net P&L       |        |       |       |
| Trade Count   |        |       |       |
| Max Drawdown  |        |       |       |
```

**Key rule**: If trade count dropped significantly but PF only went up, that's just filtering, not improvement. Real improvement is PF increase with trade count maintained (or slightly reduced).

### Reading reason-class distribution as a runner-bug signal

When `--emit-gated-diag` is on, bucket `strategy_signal_events.reason` by class for the new run_id and compare against live's distribution. If one class dominates at counts that don't match live (e.g. backtest `bias=2210, slope=0` vs live `bias=0, slope=1536`), the cause is usually runner-side state-zeroing per bar — NOT a strategy threshold bug. Common offenders: anchor-reset loops, indicator warmup wiped by a per-bar reset, key-level cache pinned at zero. Diagnose at the runner before tuning DNA. See `go-hexagonal: Per-bar ResetX must be additive`.

## Known Data Coverage Gaps

### DoltHub options dataset is monthlies only

`historical_option_chain` is sourced from DoltHub's `post-no-preference/options`. Verified 2026-04-17: AAPL on 2026-04-15 has 4 expirations at 14/30/44/64 DTE — no weeklies. SOFI and many smaller names absent entirely. Weekly-biased strategies (macd_only_v1: 5-14 DTE, avwap_v4: 3-7 DTE) return zero trades without the synthetic-chain fallback because every DoltHub row sits at 23+ DTE.

Implications when reading a backtest result:
- With `[backtest.synthetic_chain] enabled = true` (default), weeklies are BSM-synthesized. P&L is indicative, NOT a faithful live replay — Alpaca's real chain bid/ask/skew differs from flat-IV BSM.
- With synthetic disabled, weekly-biased strategies produce zero trades. Widen DTE as a stopgap or re-enable synthetic.
- Live/backtest divergence expected on wing-strike strategies: synthetic assumes flat IV, real market has skew.

`iv_snapshots` is one ATM value per symbol per trading day — synthetic fans this across all strikes. Gamma-scalping / tail-hedge strategies will see material divergence from live.

### Dial impact ordering when chasing backtest-vs-live fidelity

Measured on the 2026-04-17 27-sym × 6-strat today-only run when spread + fill model were tightened at the same time:

| Dial | Change | PF impact | Net $ impact |
|---|---|---|---|
| Synthetic spread | 3% → 8% (2.7× wider) | 2.76 → 2.44 (−12%) | −$1,012 on $6,035 profit (−17%) |
| Fill model | realistic → pessimistic | (same measurement — bundled) | (same) |

Backtest went from +$6,035 to +$5,023 — still ~40× the live number (~+$124 realized). The dominant inflator is **flat-IV BSM in the synthetic chain**, not spread or fill model. Future backtest-realism work should skip straight to skew modeling or IV dynamics — tuning spread/fill defaults alone won't close the gap.

Rule: if backtest PF > 2.0 and the strategy exits on a % premium target (e.g. 15% PREMIUM_TARGET), assume flat-IV is lifting PF by at least 2× and treat the absolute number as directional only.
