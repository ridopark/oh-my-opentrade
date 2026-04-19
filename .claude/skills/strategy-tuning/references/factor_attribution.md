# Factor Attribution Protocol

Discipline for separating true residual alpha from hidden factor exposure. A
strategy that "works" can be mostly long-beta + long-vol wearing a different
label. Run this check before promoting a tuning result to live and before
declaring a new strategy as genuine alpha.

## When this applies

- Baseline assessment for any new or materially changed strategy.
- Every accept decision in the strategy-tuning loop where the candidate clears
  the WFA holdout. Promoted metrics must include residual alpha, not just
  gross PF / Sharpe.
- Anytime a strategy's headline performance concentrates in a regime (e.g.,
  bull tape 2024-2025 for AVWAP long calls) — factor decomposition is how you
  tell "the strategy timed the regime" from "the strategy WAS the regime".

Exemptions: pure structural / bug-fix changes that do not alter return
distribution (rename, refactor, typo).

## Regression setup

Dependent variable: per-day realized P&L expressed as a return on initial
equity. One observation per trading day the strategy was live, including
zero-trade days.

Independent variables (equities book):
- SPY daily return (market beta)
- SPY intraday %change AM-to-close (intraday trend proxy — matters for AVWAP)
- VVIX or VIX daily change (vol of vol / vol beta — matters for long options)
- Sector ETF daily return if the universe clusters (XLK / XLF / XLE); skip if
  the universe is broad
- Overnight gap: SPY open/prior-close - 1 (gap exposure)

Independent variables (crypto book):
- BTC daily return
- ETH daily return (only if universe extends beyond BTC)
- Funding-rate proxy (avg perp funding across the universe) for perps
- DXY daily change (dollar regime)

Estimator: OLS. Report alpha (intercept), each beta, t-stats, R-squared,
residual Sharpe, residual max drawdown.

Minimum sample: 40 trading days. Below that, the regression is noise — flag
and defer until enough history accumulates.

## Decision rules

- **R-squared > 0.70**: strategy returns are mostly explained by factor
  exposure. This is not automatically disqualifying — known factor harvest
  can be a valid business — but it must be labeled as such. Do not call it
  novel alpha.
- **Alpha t-stat < 1.5**: intercept is not statistically distinct from zero
  after factors. Treat the strategy as a factor vehicle. Residual Sharpe is
  the number that matters now, not gross Sharpe.
- **Residual Sharpe < 0.5**: after stripping factor exposure, the strategy
  has no standalone edge. If the user wanted long-beta exposure they could
  just hold SPY with less operational complexity.
- **Negative beta on VIX change for a long-options strategy**: physically
  impossible over a meaningful sample — check for look-ahead or fill
  mispricing before trusting the result.

## Reporting format

Include in every tuning-pass summary and Discord notification for accepted
changes:

```
Factor attribution (N=<days>):
  alpha (daily):  <bps>   [t=<stat>]
  beta SPY:       <x>     [t=<stat>]
  beta VVIX:      <x>     [t=<stat>]
  beta <sector>:  <x>     [t=<stat>]  (or omit)
  R-squared:      <pct>
  residual Sharpe: <x>    (annualized, sqrt(252))
  residual DD:    <pct>
```

Headline PF / Sharpe from the backtest go in the top line as before. Factor
attribution goes right below, never omitted.

## Common pitfalls

- Using trade-count-weighted P&L instead of equity-weighted returns. Use
  returns so the regression is dimensionally consistent with factor returns.
- Running the regression on trade-level P&L instead of day-level aggregate.
  Trade level mixes intraday microstructure with macro factors and produces
  noise-heavy fits. Day level is right.
- Ignoring heteroskedasticity. Options P&L days are fat-tailed; use HC1 or
  HC3 robust standard errors if t-stats will drive decisions. For a gut
  check, plain OLS t-stats are OK as long as you treat t=1.5 as a soft floor
  not a hard wall.
- Treating positive residual Sharpe with negative alpha as edge. Residual
  Sharpe rewards volatility too; negative alpha means the timing adds noise
  while the factor does the work.

## What to do when the strategy fails this check

- If R-squared is high and alpha is insignificant, the strategy is a factor
  proxy. Decide explicitly whether that's the business you want. Document
  the decision. Do not re-tune to "beat" the factor — that is how menu
  selection overfits sneak in.
- If residual Sharpe is weak, the next research move is not parameter
  tuning. It is rethinking the signal's edge thesis — is there a
  microstructure rationale, data confluence, or event specificity that the
  current form does not capture?
