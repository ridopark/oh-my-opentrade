---
name: Strategy tuning - lock most params, tune only 11
description: When tuning AVWAP/ORB strategies, only optimize 11 key parameters out of ~110 total to avoid overfitting
type: feedback
---

When using the strategy tuning harness, only tune parameters that directly affect edge. Lock everything else at current defaults.

**AVWAP v4.2 — Tune only 6:**
1. `min_confluence_score` (trade selectivity)
2. `stop_bps` (R:R tradeoff)
3. `hold_bars` (breakout confirmation duration)
4. `volume_mult` (volume confirmation threshold)
5. `stagnation minutes` in exit_rules (time-based exit)
6. `max_position_bps` (position sizing)

**ORB Break & Retest — Tune only 4:**
1. `min_rvol` (volume filter)
2. `stop_bps` (R:R tradeoff)
3. `max_retest_bars` (retest patience)
4. `stagnation minutes` in exit_rules (time-based exit)

Lock `orb_window_minutes` at 15 (standard ORB definition).

**Lock everything else:** entry type toggles, sub-params (pullback RSI, pinch bps, etc.), structural filters, timing windows, confluence weight toggles, AVWAP stop sub-params, swing stop sub-params, dynamic risk sub-params, VIX filters, options params, regime overrides.

**Why:** With ~110 free params you'd need 17,000+ trades to validate — impossible for intraday strategies on 34 symbols. With 10 params, ~1,000-2,000 trades suffices (achievable in 6-12 months of 5m data).

**How to apply:** When the strategy-tuner agent runs, restrict the DNA search space to only the 11 params listed above. Require minimum 500 trades in any backtest before trusting results. Use walk-forward validation (tune on 6 months, test on next 2, roll forward). The 34-symbol universe has decent sector diversity but ~17/34 are tech/semis — treat correlated symbols as fewer independent samples.
