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

## Empirical findings (2026-04-12 predictive power study)

We ran a comprehensive predictive power test across all 34 active
symbols using ~15 months of 5m dark pool data (2025-01 to 2026-04,
~2M bars) against 12.7M 1m market bars.

### Key results

| Signal | Horizon | Rank IC | t-stat | Verdict |
|---|---|---|---|---|
| Early 30m buy_ratio | next-day | +0.003 | 0.30 | **No signal** |
| Full-day buy_ratio Z | next-day | -0.021 | -2.12 | **Reversal** (inverted) |
| Late 60m buy_ratio Z | next-day | -0.039 | -3.92 | **Strongest reversal** |
| 30m rolling buy_ratio | 60m fwd | -0.004 | -1.18 | **Noise** |

### What the data says

1. **The signal is inverted.** High DP buying today predicts *lower*
   returns tomorrow — mean-reversion, not momentum. Consistent with
   Zhu 2014: informed DP flow is contrarian.

2. **The signal exists at the daily horizon only.** Intraday rolling
   buy_ratio (30m → 60m fwd) has IC of -0.004 and ~1 bps quintile
   spread. Not exploitable after transaction costs.

3. **Late-session flow is most predictive.** The last-hour DP buy_ratio
   Z-score achieves IC = -0.039 (t = -3.92), with a Q1-Q5 spread of
   46 bps/day.

4. **Return accrues next-day intraday, not overnight gap.** Q1 long:
   +0.7 bps overnight gap, +26.8 bps next-day intraday. Builds
   monotonically: +4.8 bps by 11:30, +27 bps by close.

5. **Late-session extreme signals** (|Z| > 1.5): Long side +29.3 bps
   (n=833), short side -18.1 bps (n=828). Annualized Sharpe ~0.96 raw,
   ~0.65-0.75 after costs.

6. **Consistency caveat.** Jan-Mar 2025 was flat-to-negative. The signal
   has regime dependence.

### Implications for strategy design

- **S1 (Absorption Divergence): Thesis not supported at intraday horizon.**
  The core signal (absorption_score from rolling 30m DP data) does not
  predict 30-60m forward returns. Do not build as designed.

- **S2 (Dark-to-Lit Migration): Thesis not supported.** Early-session
  (9:30-10:00) buy_ratio has zero predictive power (IC = +0.001, t = 0.13).
  The migration detection mechanism would fire on noise.

- **New direction: Overnight Z-Score Bias.** The data supports a daily-
  horizon strategy using late-session DP Z-score as a next-day directional
  signal. See [`overnight-z-bias.md`](overnight-z-bias.md).

## Strategy pipeline (revised)

### Tier 1 — Build now (data validated)

#### S0. Overnight Z-Score Bias (NEW)
Late-session DP buy_ratio Z-score predicts next-day returns (inverted).
Entry at 09:35 ET, exit MOC. Long-only initially. Also serves as a
daily bias filter for AVWAP+MACD intraday entries.
**[Design doc →](overnight-z-bias.md)**

### Tier 2 — Build later (data partially available, needs validation)

#### S3. 13F + Options Skew Confirmation
13F filing + options skew as freshness check. Swing strategy (1-20 day).
Genuinely diversifying but extremely sparse signal (~5-15 per quarter).
**Run in shadow mode for 4+ quarters before committing engineering.**
**[Design doc →](s3-13f-skew-confirmation.md)**

#### S7. Options Vol-Spread as Informed Trading Proxy
Call IV minus put IV proxies borrow fee (JFE 2025). Computable daily
from existing options chain data. Worth investigating once S0 validates.

### Tier 3 — Deprioritized (thesis weakened or crowded)

#### ~~S1. Dark Pool Absorption Divergence~~ — DEPRIORITIZED
Intraday signal is noise (IC -0.004). Could be revisited if we obtain
tick-level DP data (current 5m aggregation washes out the signal).
**[Original design doc →](s1-absorption-divergence.md)**

#### ~~S2. Dark-to-Lit Migration Momentum~~ — DEPRIORITIZED
Early-session DP data has zero predictive power. Migration detection
at 5m resolution fires on noise.
**[Original design doc →](s2-dark-to-lit-migration.md)**

#### S4. Auction Imbalance Fade at Close — DEPRIORITIZED
Well-studied and heavily arbitraged. Edge vs dedicated closing auction
firms is near zero.

#### S5. Gamma Exposure (GEX) Flip Trigger — DEFERRED
Popular retail signal with thin academic support. Dealer positioning
assumptions are unverifiable.

#### S6. LLM Earnings Transcript Sentiment Drift — DEFERRED
Different engineering problem entirely. Needs transcript source +
scoring rubric. Do not let this distract from the core roadmap.

## Priority and timeline (revised)

| Phase | Strategy | Effort | Gate |
|---|---|---|---|
| **Phase A** (now) | S0 Overnight Z Bias | ~5 days | PF > 1.15, Sharpe > 0.6 after costs |
| **Phase A+** (with S0) | Per-strategy Z conditioning | ~2 days | AVWAP PF > 1.5 on favorable Z; MACD entry suppression eliminates PF < 1.0 regime |
| **Phase B** (after S0 validates, 3+ months) | S3 shadow mode + S7 research | ~2 weeks | S3: 4 quarters shadow data; S7: IC > 0.02 |
| **Phase C** (if tick data acquired) | S1 revisit | TBD | Tick-level DP data source |

## Gate rule

Same philosophy as the perf roadmap: **only build Phase N+1 if Phase N
delivers measurable edge.** S0 gate: PF > 1.15, Sharpe > 0.6 after
realistic costs, on out-of-sample data. The measurement gate prevents
over-engineering.

**New addition:** Before building ANY dark pool strategy, validate the
underlying signal's predictive power at the target horizon. The
`scripts/dp_buyratio_predictive_power.sql` script is the template.

## Empirical analysis artifacts

| File | What it does |
|---|---|
| `scripts/dp_buyratio_predictive_power.sql` | Phase 1+2 predictive power test (16 variants) |

## References

### Academic papers

| Short name | Full cite | Key insight |
|---|---|---|
| Zhu 2014 | "Do Dark Pools Harm Price Discovery?" *RFS* | Dark pool informed trading model; DP volume inversely correlated with price discovery when used by informed traders |
| Comerton-Forde 2015 | "Dark Trading and Price Discovery" *JFE* | DP price contribution varies by stock; high DP ratio + low lit displacement = accumulation |
| Buti 2017 | "Dark Pool Trading Strategies, Market Quality and Welfare" *JFE* | Dark-lit flow switching dynamics; institutional migration patterns |
| Rigsby 2025 | "Information Asymmetry, Liquidity, and Dark Pool Trading" SSRN 5699222 | Real-time informed-flow detector from DP routing decisions |
| Cremers 2010 | "Deviations from Put-Call Parity and Stock Return Predictability" *JFE* | Options skew predicts equity returns; proxy for informed options trading |
| JFE 2025 | "Why does options market information predict stock returns?" | Vol-spread proxies borrow fee, not sentiment — reframed signal |

## Revision log

- 2026-04-12 — Initial roadmap. S1-S7 outlined. No implementation yet.
- 2026-04-12 — Predictive power study completed. S1/S2 deprioritized
  (intraday signal is noise). S0 Overnight Z-Score Bias added as Tier 1
  based on late-session reversal signal (IC=-0.039, t=-3.92).
- 2026-04-12 — Cross-referenced 13,204 AVWAP+MACD backtest trades
  against late Z. AVWAP and MACD respond oppositely: AVWAP benefits from
  low-Z days (PF 1.86), MACD benefits from high-Z days (PF 1.74).
  Replaced uniform bias filter with per-strategy Z conditioning.
  MACD on low-Z days has PF 0.85 (loses money) — suppress those entries.
