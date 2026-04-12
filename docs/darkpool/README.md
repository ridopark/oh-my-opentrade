# Dark Pool Alpha Strategies — Roadmap

This directory holds the design, research, and implementation plans for
strategies built on our dark pool data edge. These strategies exploit
FINRA ADF trade-level data sourced via Alpaca SIP — a data asset most
retail systems don't have and can't easily replicate.

## Current state (what we already have)

| Asset | Status | Location |
|---|---|---|
| Dark pool 5m bars (dp_volume, dp_vwap, buy/sell, large prints) | ✅ live | `darkpool_bars` table, `cmd/omo-backfill-darkpool` |
| DP confluence scoring (+10 pts: ratio + pressure + blocks) | ✅ live | `domain/strategy/confluence.go:ScoreDarkPool` |
| DP veto gate (block entries against DP flow) | ✅ live (MACD on, AVWAP off) | `confluence.go:DPVeto` |
| DP sizing multiplier (scale position by Z-score alignment) | ✅ live | `confluence.go:DPSizingMultiplier` |
| DP support/resistance (top-3 VWAP print levels) | ✅ computed | `strategy/runner.go` overlay |
| DP Z-score (100-bar rolling mean/std of ratio) | ✅ computed | `runner.go` dpRolling |
| 13F whale accumulation data | ✅ stored | `cmd/omo-backfill-13f` |
| Options chain (strike, IV, Greeks) | ✅ daily snapshots | IBKR + DoltHub pipeline |

## Strategy pipeline (7 strategies, 3 tiers)

### Tier 1 — Build now (data in-hand, highest ROI)

#### S1. Dark Pool Absorption Divergence
Dark prints accumulate at a price without moving the lit tape. When this
"absorption" resolves, price moves toward the dark-print VWAP.

#### S2. Dark-to-Lit Migration Momentum
DP volume elevated in first 30 min → then shifts to lit exchanges at
similar prices → informed flow going public → directional move follows.

### Tier 2 — Build soon (data partially available)

#### S3. 13F + Options Skew Confirmation
13F filing reveals whale position. Confirm accumulation is ongoing by
checking that put skew is declining post-filing. Gate 13F-derived signals
on skew z-score.

#### S4. Auction Imbalance Fade at Close
NYSE MOC/LOC imbalance at 3:50 PM partially reverses next morning. Fade
only when imbalance pushes price away from a DP AVWAP support/resistance
level.

### Tier 3 — Build later (highest ceiling, most work)

#### S5. Gamma Exposure (GEX) Flip Trigger
Aggregate dealer gamma flips from positive → negative at a strike. Dealers
shift from dampening to amplifying. GEX flip point = directional trigger.

#### S6. LLM Earnings Transcript Sentiment Drift
LLM scores qualitative content of earnings call. Trade post-earnings drift
only when LLM sentiment diverges from the market's numeric-surprise reaction.

#### S7. Options Vol-Spread as Informed Trading Proxy
Call IV minus put IV proxies stock borrow fee (cost of informed shorting).
JFE 2025 paper — very recent signal reframing.

## Reading order

1. This file — overview + priorities.
2. [`s1-absorption-divergence.md`](s1-absorption-divergence.md) — design +
   implementation plan for Strategy 1.
3. [`s2-dark-to-lit-migration.md`](s2-dark-to-lit-migration.md) — design +
   implementation plan for Strategy 2.
4. [`s3-13f-skew-confirmation.md`](s3-13f-skew-confirmation.md) — design for
   Strategy 3.
5. Per-strategy docs added as work progresses.

## Priority and timeline

| Phase | Strategies | Effort | Why now |
|---|---|---|---|
| **Phase A** (first sprint) | S1 + S2 | ~1 week | Reuse existing DP pipeline. Highest signal uniqueness. Backtestable today. |
| **Phase B** (second sprint) | S3 + S4 | ~1 week | 13F + options data already stored. S4 needs auction data source. |
| **Phase C** (when options pipeline is battle-tested) | S5 + S7 | ~2 weeks | GEX model is the biggest build. Vol-spread is easy once S5 infra exists. |
| **Phase D** (event-driven diversifier) | S6 | ~1 week | LLM is in-stack. Needs transcript source + scoring rubric. |

## Gate rule

Same philosophy as the perf roadmap: **only build Phase N+1 if Phase N
delivers measurable edge in backtest** (profit factor > 1.5, win rate > 55%,
or Sharpe > 1.5 on out-of-sample data). The measurement gate prevents
over-engineering.

## References

### Academic papers

| Short name | Full cite | Key insight |
|---|---|---|
| Zhu 2014 | "Do Dark Pools Harm Price Discovery?" *RFS* | Dark pool informed trading model; DP volume inversely correlated with price discovery when used by informed traders |
| Comerton-Forde 2015 | "Dark Trading and Price Discovery" *JFE* | DP price contribution varies by stock; high DP ratio + low lit displacement = accumulation |
| Rigsby 2025 | "Information Asymmetry, Liquidity, and Dark Pool Trading" SSRN 5699222 | Real-time informed-flow detector from DP routing decisions |
| Cremers 2010 | "Deviations from Put-Call Parity and Stock Return Predictability" *JFE* | Options skew predicts equity returns; proxy for informed options trading |
| Cushing 2000 | "Stock Returns and Trading at the Close" *JFM* | MOC/LOC auction imbalance → next-day reversal |
| Barbon 2021 | "Gamma Fragility" (working paper) | Dealer gamma hedging amplifies moves past GEX flip points |
| JFE 2025 | "Why does options market information predict stock returns?" | Vol-spread proxies borrow fee, not sentiment — reframed signal |
| Li 2010 | "Information Content of Forward-Looking Statements" *JAR* | Qualitative filing language predicts drift beyond numeric surprise |
| arXiv 2412.19245 | "Sentiment trading with large language models" (Dec 2024) | LLM sentiment long-short achieves Sharpe 3.05 |
| Cont 2023 | "Cross-Impact of Order Flow Imbalance" *QF* | Cross-asset OFI improves short-horizon return prediction |

### GitHub reference implementations

| Repo | What it does |
|---|---|
| [jensolson/SPX-Gamma-Exposure](https://github.com/jensolson/SPX-Gamma-Exposure) | SPX GEX calculation — reference for S5 |
| [Matteo-Ferrara/gex-tracker](https://github.com/Matteo-Ferrara/gex-tracker) | Real-time GEX dashboard |
| [Ranjan-V/MicrostructureAlphaEngine](https://github.com/Ranjan-V/MicrostructureAlphaEngine-Order_Flow_Imbalance_Strategy) | OFI strategy from Cont papers |
| [olohmann/trade-signal-forge](https://github.com/olohmann/trade-signal-forge) | Multi-agent LLM signal fusion (.NET) |
| [Bruh-Gang/options-analysis](https://github.com/Bruh-Gang/options-analysis) | UOA detection + Greeks dashboard |

## Revision log

- 2026-04-12 — Initial roadmap. S1-S7 outlined. No implementation yet.
