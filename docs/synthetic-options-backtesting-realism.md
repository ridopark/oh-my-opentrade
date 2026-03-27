# Synthetic Options Backtesting: Failure Modes, Industry Solutions, and Practical Fixes

**Context**: oh-my-opentrade uses BSM synthetic pricing for backtesting options trades.
The current implementation produces a 100% win rate because it systematically overstates
P&L through several compounding biases. This document catalogs those biases, surveys how
professional platforms handle them, and provides tiered implementation recommendations
specific to our ORB (Opening Range Breakout) strategy.

---

## Table of Contents

1. [Current System Audit](#1-current-system-audit)
2. [Part 1: What Makes Options Backtesting Unrealistic](#2-part-1-failure-modes)
3. [Part 2: How Professional Platforms Solve This](#3-part-2-professional-platforms)
4. [Part 3: Practical Implementation Tiers](#4-part-3-implementation-tiers)
5. [Part 4: Specific Formulas and Code Patterns](#5-part-4-formulas-and-code)
6. [Part 5: What Is Good Enough for ORB](#6-part-5-good-enough-for-orb)
7. [Appendix: Data Sources](#appendix-data-sources)

---

## 1. Current System Audit

### How IV Is Estimated (debate/service.go, lines 517-531)

```go
iv = (dailyATR / underlyingPrice) * math.Sqrt(252)
iv += 0.03          // +3 vol points "variance risk premium"
iv = max(iv, 0.10)  // 10% floor
iv = min(iv, 2.00)  // 200% ceiling
```

### How Entry Price Is Computed

BSM is called with the estimated IV to find a strike matching the target delta (0.40-0.55).
The BSM theoretical mid-price becomes the limit price for the entry order.

### How Exit Price Is Computed (simbroker/broker.go, lines 414-459)

The `computeOptionExitPrice` method re-prices the option using BSM with:
- Current underlying price (correct)
- Updated DTE from bar time to expiry (correct)
- **The same `iv_at_entry` value** (incorrect -- this is the core bias)

### The Compounding Bias Stack

| Bias | Direction | Magnitude |
|------|-----------|-----------|
| Same IV at entry and exit (no crush/expansion) | Always inflates winners | 5-40% of premium |
| No bid-ask spread at entry | Overstates entry price quality | 2-8% of premium |
| No bid-ask spread at exit | Overstates exit price quality | 2-8% of premium |
| Flat vol surface (no skew) | Misprices OTM strikes | 1-5% of premium |
| ATR-to-IV conversion error | Directional bias depends on regime | +/- 10-30% of IV level |
| No intraday IV mean reversion | Overstates directional option P&L | 3-15% of premium |
| No theta drag on intraday holds | Understates time decay | 0.5-2% of premium per day |

The combined effect: a trade that earns 1% on the underlying with a 0.47-delta call
might show a 2.1% option return when the realistic return is 0.8-1.4%. Over hundreds
of trades, this systematic upward bias produces the observed 100% win rate.

---

## 2. Part 1: Failure Modes

### 2.1 IV Crush After Earnings and Events

**What happens**: Implied volatility collapses 20-60% after binary events (earnings,
FDA decisions, FOMC). A stock might move +3% on earnings, but the 30-DTE ATM straddle
priced at 45% IV reprices at 28% IV the next morning. A long call holder can lose
money even when the stock moves in their direction.

**Magnitude**: IV crush on major tech names around earnings averages 10-15 vol points.
For a 40-DTE, 0.47-delta call, each vol point is worth approximately 0.15-0.20% of the
underlying price. A 12-point crush costs ~2% of underlying price -- enough to wipe out
a 1.5% directional move.

**Our exposure**: The ORB strategy trades intraday and exits by EOD. If entry happens
on an earnings day morning, the entry IV estimate (derived from prior-day ATR) will be
stale. The ATR has not yet reflected the post-earnings move. The entry IV will be
**too low** relative to the actual market IV (which is crashing from a high level).
Conversely, the exit IV used (same as entry) will be **too high** because it doesn't
model the crush that occurred during the trading day.

### 2.2 Bid-Ask Spread

**What happens**: Options have wider spreads than equities. The theoretical BSM price
sits at the midpoint, but real executions occur at the ask (for buys) and the bid
(for sells).

**Typical spreads by category**:

| Category | Bid-Ask as % of Mid | Example |
|----------|---------------------|---------|
| SPY/QQQ weeklies, ATM | 0.5-1.5% | $0.02-0.05 on a $3.00 option |
| AAPL/MSFT monthlies, ATM | 1.5-3% | $0.05-0.15 on a $5.00 option |
| TSLA/AMZN monthlies, ATM | 2-5% | $0.20-0.50 on a $10.00 option |
| Illiquid names, OTM | 5-20% | $0.10-0.30 on a $1.50 option |

**Round-trip cost**: For a buy-then-sell cycle, the spread cost is approximately the
full spread width. On a $5.00 option with a $0.15 spread, the round-trip cost is $0.15
per share, or 3% of premium. Over 200 trades per year, this accumulates to a significant
drag.

**Our exposure**: The strategy targets 35-45 DTE, 0.40-0.55 delta options on liquid names.
Typical premiums are $3-$12. Expected round-trip spread cost: 2-4% of premium per trade.

### 2.3 Volatility Skew and Smile

**What happens**: BSM assumes a single volatility for all strikes. Real markets price
OTM puts at higher IV (skew) and sometimes OTM calls at higher IV too (smile). The
"volatility surface" is a function of both strike (moneyness) and expiration.

**Typical skew structure for equities**:
- 25-delta put: IV is 3-8 vol points above ATM IV
- ATM (50-delta): baseline IV
- 25-delta call: IV is 1-3 vol points below ATM IV (for indices) or 0-2 above (for single stocks with upside skew)

**Impact on our strategy**: We target 0.40-0.55 delta, which is near-ATM. The skew
effect at these deltas is relatively small -- typically 0-2 vol points versus true ATM.
This makes skew a **secondary concern** for our specific use case.

**Quantified impact**: For a 40-DTE, 0.45-delta call at $200 stock price, 1 vol point
of skew error translates to approximately $0.12 per share, or ~1% of a $12 premium.

### 2.4 Pin Risk Near Expiry

**What happens**: As options approach expiration (<7 DTE), the gamma surface becomes
extremely steep near ATM strikes. Small moves in the underlying cause large swings
in option value. The BSM model breaks down because it assumes continuous hedging,
while real markets have discrete hedging intervals.

**Our exposure**: Minimal. We use 35-45 DTE options and hold intraday only. Even if
held for a few days, the options are far from expiry. This is a non-issue.

### 2.5 Early Exercise Considerations

**What happens**: American options (which all US equity options are) can be exercised
before expiration. This creates a premium over European BSM prices, particularly for:
- Deep ITM puts (interest rate effect)
- Calls on dividend-paying stocks near ex-dividend dates
- Deep ITM options with high intrinsic value

**The BSM error**: Our BSM implementation prices European options. For 35-45 DTE options
at 0.40-0.55 delta, the early exercise premium is typically 0-0.3% of premium for calls
and 0-1% for puts. The error is small because we trade near-ATM options with significant
time value remaining.

**Our exposure**: Negligible for intraday holds. The early exercise premium does not
change materially within a single trading session.

### 2.6 Greeks Hedging Costs

**What happens**: Market makers who provide liquidity hedge their delta exposure. The
cost of this hedging is embedded in the spread. When a market maker sells you a call,
they buy shares to hedge. Their hedging costs (from discrete rebalancing, transaction
costs, and gamma risk) are reflected in wider spreads, not in the theoretical BSM price.

**Our exposure**: This is already captured in the bid-ask spread analysis (section 2.2).
We do not hedge our options positions with the underlying, so direct hedging costs are
not applicable. However, understanding that market maker hedging costs drive spreads
helps us model spreads more accurately.

### 2.7 Slippage on Options Orders

**What happens**: Beyond the bid-ask spread, options orders experience slippage from:
- Market impact: large orders move the quote
- Latency: price moves between decision and execution
- Partial fills: limit orders may not fill completely

**Magnitude**: For single-contract orders on liquid options, slippage beyond the spread
is typically 1-3 cents ($0.01-$0.03 per share). For multi-contract orders (10+ contracts),
slippage can be 3-10 cents.

**Our exposure**: We trade 1-5 contracts per position on liquid names. Slippage is
primarily the bid-ask spread itself. Additional slippage is minimal.

### 2.8 Volume and Open Interest Liquidity Effects

**What happens**: Options with low open interest and volume have:
- Wider bid-ask spreads
- Higher slippage
- Risk of stale quotes (displayed prices may not be actionable)
- Lower fill probability for limit orders

**Practical thresholds**:
- Open interest > 500: generally liquid for small orders
- Open interest > 5000: liquid for moderate-size orders
- Daily volume > 100: active enough for reliable pricing

**Our exposure**: The contract selection service already filters on `min_open_interest`
(configured at 100) and `max_spread_pct` (configured at 10%). However, in backtesting
with synthetic pricing, these filters are meaningless because there is no real chain
data. This is a gap: the backtest assumes perfect liquidity at all times.

---

## 3. Part 2: How Professional Platforms Solve This

### 3.1 QuantConnect (Lean Engine)

**Data approach**: Uses actual historical options chain data from multiple vendors:
- **Primary**: Algoseek tick-level options data (every quote and trade)
- **Alternative**: proprietary universe with daily OHLCV and Greeks for all listed
  US equity options back to 2012

**IV handling**: Does **not** estimate IV from the underlying. Uses the IV reported by
the data vendor for each contract at each timestamp. The IV is derived from the actual
market bid-ask midpoint via numerical inversion of BSM.

**Spread modeling**: Fills at the ask for buys, bid for sells (worst-case) by default.
Users can configure fill models:
- `ImmediateFillModel`: fills at last price (unrealistic, fast)
- `LatestPriceFillModel`: fills at bid for sells, ask for buys
- Custom fill models that interpolate within the spread based on order size and
  available volume

**Skew/surface**: Inherited from data. Each contract has its own IV, so the surface
is naturally captured.

**Limitations**: Historical options data costs $500-$5,000+ depending on granularity.
Lean is open-source; data is the moat.

### 3.2 OptionStack

**Data approach**: Uses ORATS (Options Research & Technology Services) historical data
with proprietary adjustments. Monthly-expiration chains for all US equities back to 2007.

**IV handling**: Uses ORATS smoothed IV surface data. Each strike/expiry combination
has a historical IV value interpolated from the ORATS surface model.

**Spread modeling**: Uses historical bid-ask data from ORATS. Fills use the natural
bid-ask at the time of the signal.

**Unique feature**: Pre-built strategy templates with "what-if" scenario analysis.
Users can adjust IV assumptions to stress-test strategies.

### 3.3 TastyTrade Backtester

**Data approach**: Uses proprietary historical data from the TastyTrade brokerage.
Granularity: end-of-day options chains for major underlyings.

**IV handling**: Uses the broker's recorded IV for each contract. No synthetic
estimation.

**Spread modeling**: Uses a fixed model: fills at mid-price minus a configurable
fraction of the spread. Default assumption is mid-price fill (optimistic).

**Limitations**: Only covers options traded on the TastyTrade platform. Limited
customization. Focuses on defined-risk strategies (spreads, iron condors).

### 3.4 Thinkorswim OnDemand

**Data approach**: Replays actual historical market data tick-by-tick, including the
full options chain with bid/ask/last and Greeks.

**IV handling**: Uses the actual IV recorded by the exchange/OPRA at each timestamp.

**Spread modeling**: Full Level II replay. The spread is whatever the market showed
at that timestamp.

**Unique feature**: True "replay mode" -- you see the same quotes, charts, and chain
that existed historically. No synthetic pricing at all.

**Limitations**: Only useful for manual/discretionary backtesting. Not scriptable.
Data goes back approximately 3 years.

### 3.5 ORATS (Options Research & Technology Services)

**Role**: ORATS is primarily a **data vendor**, not a backtesting platform. Many
platforms (including OptionStack) build on ORATS data.

**What they provide**:
- **Smoothed IV surfaces**: fitted skew curves for every underlying, every day, going
  back to 2007. The surface is parameterized using a modified SABR model.
- **Implied earnings moves**: pre/post-earnings IV estimates.
- **Historical Greeks**: BSM Greeks computed from the smoothed surface.
- **Bid-ask data**: historical bid/ask for each contract.
- **Earnings and event flags**: binary flags for earnings dates, dividends, etc.

**IV surface model**: ORATS fits a volatility surface using:
1. Collect all traded strikes/expiries for a given underlying on a given day.
2. Filter out stale/illiquid contracts.
3. Fit a parametric skew model (modified cubic or SABR) to the remaining points.
4. Interpolate/extrapolate to produce IV for any strike/expiry combination.

**Cost**: Data subscriptions range from $100/month (limited) to $500+/month (full
historical). API access for backtesting: custom pricing.

### 3.6 Deltix / QuantOffice

**Data approach**: Enterprise-grade. Typically uses Thomson Reuters (now LSEG) or
Bloomberg historical tick data, including full OPRA options feed.

**IV handling**: Computes IV in real-time from the bid-ask midpoint using Newton-Raphson
inversion of BSM. For backtesting, replays the OPRA feed and recomputes IV at each
timestamp.

**Spread modeling**: Full Level II order book replay. Models impact and slippage from
order size vs. displayed depth.

**Unique feature**: Can model custom vol surfaces, including stochastic volatility
models (Heston, SABR) calibrated to the historical surface.

**Limitations**: Enterprise pricing ($50K+/year). Overkill for retail strategies.

### 3.7 Summary: What Do They All Have in Common?

| Feature | All professional platforms |
|---------|--------------------------|
| Real historical IV per contract | Yes -- never estimate from ATR |
| Bid-ask spread in fills | Yes -- at minimum bid/ask, often with models |
| Greeks from real IV | Yes -- Greeks computed from actual IV, not flat vol |
| Event-aware IV | Most flag earnings dates and model crush |
| Full vol surface | Yes -- either from data or fitted model |

**The fundamental lesson**: No professional platform estimates IV from the underlying's
ATR for backtesting. They all use either (a) actual historical options data with
recorded IV, or (b) fitted volatility surfaces built from historical options data.
The ATR-to-IV conversion is a research shortcut, not a production approach.

---

## 4. Part 3: Implementation Tiers

### Tier 1: Quick Wins (1-2 hours each)

These changes do not require external data. They improve realism within the
constraints of synthetic pricing.

#### 1A. Intraday IV Decay for Exit Pricing

**Problem**: `computeOptionExitPrice` uses `iv_at_entry` unchanged.

**Fix**: Apply a small IV decay during the trading day. Empirically, ATM 30-45 DTE
IV tends to decline 0.3-1.0% (relative, not absolute) during a normal trading session
as uncertainty resolves. On event days, the decline is much larger.

**Expected impact**: Reduces option exit prices by 0.5-2%, breaking the systematic
upward bias. This single change will likely reduce the win rate from 100% to 70-85%.

#### 1B. Bid-Ask Spread on Entry and Exit

**Problem**: Entry uses BSM mid-price as limit price. Exit uses BSM mid-price as fill.

**Fix**: Apply a configurable half-spread penalty on both sides.

**Expected impact**: Round-trip cost of 2-5% of premium per trade. On a strategy
that averages 2-3% return per trade (underlying move * delta), this can turn marginal
winners into losers. Expected win-rate reduction: 5-15 percentage points.

#### 1C. Improved IV Estimation from ATR

**Problem**: `iv = (dailyATR / price) * sqrt(252) + 0.03` is a rough approximation.
The relationship between ATR and realized volatility is not linear, and the 3-vol-point
premium is a constant that should vary by regime.

**Fix**: Use the close-to-close realized volatility (standard deviation of log returns)
instead of ATR, and apply a regime-dependent variance risk premium.

**Expected impact**: More accurate entry pricing. The directional bias depends on
whether ATR systematically overestimates or underestimates realized vol for the
specific symbols traded.

### Tier 2: Medium Effort (1-2 days each)

#### 2A. Historical IV Surface Approximation

**Approach**: Instead of computing IV from ATR on each trade, build a lookup table of
historical ATM IV by symbol and date. This can be populated from:
- Free sources: CBOE VIX for SPY, and VIX-derived estimates for other names
- Cheap sources: ORATS trial data, or Polygon.io options snapshots ($100/month)
- The existing `ivcollector` service (which collects real IV daily)

Use the historical ATM IV for the trade date, then apply a simple moneyness adjustment
(skew approximation) for non-ATM strikes.

**Expected impact**: Eliminates the ATR-to-IV conversion error entirely for dates
where historical IV data is available. This is the single highest-value improvement
after Tier 1 changes.

#### 2B. Intraday IV Regime Changes

**Approach**: Model the intraday IV pattern. Empirically, IV follows a U-shaped pattern
during the trading day:
- Open: elevated (overnight uncertainty)
- Mid-morning: declining (information incorporated)
- Lunch: lowest point
- Late afternoon: slightly rising (overnight risk repricing)

Apply a time-of-day multiplier to the daily IV level.

**Expected impact**: 1-3% improvement in P&L accuracy for intraday strategies.
Important for ORB because entries happen at open (high IV) and exits happen later
(lower IV), creating a systematic headwind for long option positions.

#### 2C. Realized Vol vs. Implied Vol Tracking

**Approach**: Compute rolling 20-day realized volatility alongside the implied
volatility estimate. Track the IV-RV spread (variance risk premium). Use this to:
- Better estimate entry IV (RV + historical VRP for the symbol)
- Model whether IV is "rich" or "cheap" relative to historical norms
- Adjust exit IV based on how much the VRP typically compresses intraday

**Expected impact**: 2-5% improvement in P&L accuracy. Particularly valuable for
identifying trades where IV was abnormally high at entry (earnings, events).

### Tier 3: Full Realism (Multi-day)

#### 3A. Historical Options Chain Data Integration

**Approach**: Subscribe to a historical options data provider and replay actual
bid/ask/IV data during backtesting. This requires:
- Data pipeline to ingest and store options chain snapshots
- Modified simbroker to look up actual contract prices instead of computing BSM
- Schema changes to store historical chain data efficiently

**Providers** (ordered by cost):
1. **Polygon.io**: $100/month for options snapshots (15-min delayed real-time, EOD historical)
2. **Theta Data**: $25-50/month for EOD options data, $200/month for intraday
3. **ORATS**: $100-500/month for smoothed surfaces and raw data
4. **Algoseek via QuantConnect**: $500+/month for tick-level OPRA data

**Expected impact**: Eliminates all synthetic pricing biases. The backtest becomes
a true replay of historical market conditions.

#### 3B. Full IV Surface Interpolation

**Approach**: Fit a parametric volatility surface model (SABR or SVI) to available
data points, then interpolate IV for any strike/expiry combination. This enables:
- Accurate pricing for strikes between available data points
- Greeks computed from the fitted surface (not flat BSM)
- Scenario analysis with surface perturbations

**Expected impact**: 1-3% improvement over using raw contract-level IV data, because
raw data can have stale quotes and the fitted surface smooths these out.

#### 3C. Greeks-Based P&L Attribution

**Approach**: Instead of repricing with BSM, decompose P&L into:
- Delta P&L: delta * underlying move
- Gamma P&L: 0.5 * gamma * (underlying move)^2
- Theta P&L: theta * time elapsed
- Vega P&L: vega * IV change
- Residual (higher-order terms)

This provides transparency into *why* a trade made or lost money, and catches cases
where a favorable underlying move is offset by IV crush (vega P&L negative).

**Expected impact**: No change in P&L accuracy vs. full repricing, but dramatically
better analytical insight. Essential for strategy refinement.

---

## 5. Part 4: Formulas and Code Patterns

### Tier 1A: Intraday IV Decay

**Formula**:

```
exitIV = entryIV * (1 - intradayDecayRate * fractionOfSessionElapsed)
```

Where:
- `intradayDecayRate`: 0.02 for normal days, 0.05-0.15 for earnings days
- `fractionOfSessionElapsed`: (currentTime - 09:30) / (16:00 - 09:30)

**Go code pattern for simbroker/broker.go**:

```go
// intradayIVDecay adjusts entry IV for intraday mean reversion.
// Normal trading days see ~1-2% relative IV decline from open to close.
// This models the resolution of overnight uncertainty.
func intradayIVDecay(entryIV float64, entryTime, exitTime time.Time) float64 {
    et, _ := time.LoadLocation("America/New_York")
    exitET := exitTime.In(et)

    // Market hours: 9:30 - 16:00 ET = 390 minutes
    marketOpen := time.Date(exitET.Year(), exitET.Month(), exitET.Day(), 9, 30, 0, 0, et)
    marketClose := time.Date(exitET.Year(), exitET.Month(), exitET.Day(), 16, 0, 0, 0, et)

    if exitET.Before(marketOpen) || exitET.After(marketClose) {
        return entryIV
    }

    elapsed := exitET.Sub(marketOpen).Minutes()
    sessionLength := marketClose.Sub(marketOpen).Minutes()
    fraction := elapsed / sessionLength

    // Normal day: 1.5% relative decline over full session
    // Concentrated in first 2 hours (ORB window)
    decayRate := 0.015
    // Apply more decay early in the session (exponential front-loading)
    effectiveFraction := 1.0 - math.Exp(-2.0*fraction)

    return entryIV * (1.0 - decayRate*effectiveFraction)
}
```

**Expected P&L impact**: On a $5.00 premium, 0.45-delta, 40-DTE call:
- Vega is approximately $0.08 per vol point
- 1.5% relative decay on 30% IV = 0.45 vol points
- Price impact: 0.45 * $0.08 = $0.036 per share, or $3.60 per contract
- As percentage of premium: 0.7%

This is small per trade but systematic. Over 200 trades, it accumulates to ~$720
total drag, or about 0.7% of a $100K account.

### Tier 1B: Bid-Ask Spread Modeling

**Formula**:

```
entryFillPrice = bsmMidPrice + halfSpread       (buying at ask)
exitFillPrice  = bsmMidPrice - halfSpread       (selling at bid)

halfSpread = bsmMidPrice * spreadPctHalf

where spreadPctHalf depends on:
  - underlying liquidity tier
  - option moneyness (ATM tighter, OTM wider)
  - DTE (shorter DTE = wider spreads)
  - time of day (open wider, midday tighter)
```

**Simplified model (good enough for Tier 1)**:

```go
// optionHalfSpreadPct returns the estimated half-spread as a fraction of
// the option mid-price. Based on empirical analysis of US equity options.
func optionHalfSpreadPct(premium float64, dte int, absDelta float64) float64 {
    // Base spread: inversely related to premium size
    // Cheap options have wider percentage spreads
    var base float64
    switch {
    case premium >= 10.0:
        base = 0.008 // 0.8% half-spread for expensive options
    case premium >= 5.0:
        base = 0.012 // 1.2%
    case premium >= 2.0:
        base = 0.020 // 2.0%
    case premium >= 1.0:
        base = 0.030 // 3.0%
    default:
        base = 0.050 // 5.0% for cheap options
    }

    // Delta adjustment: OTM options have wider spreads
    if absDelta < 0.30 {
        base *= 1.5
    } else if absDelta > 0.60 {
        base *= 1.2 // deep ITM also slightly wider
    }

    // DTE adjustment: shorter DTE = wider spreads (as % of premium)
    if dte < 14 {
        base *= 1.3
    }

    return base
}
```

**Expected P&L impact**: For our typical trade (delta 0.45, premium $5-8, 40 DTE):
- Half-spread: ~1.2% of premium
- Round-trip cost: ~2.4% of premium
- On a $6.00 premium: $0.144 per share, $14.40 per contract
- Over 200 trades at 2 contracts each: $5,760 total drag

This is the single largest source of unrealism in the current backtest. Implementing
this alone should reduce the win rate by 10-15 percentage points.

### Tier 1C: Improved IV Estimation

**Current formula and its problems**:

```
iv_current = (dailyATR / price) * sqrt(252) + 0.03
```

Problems:
1. ATR measures range, not standard deviation. ATR/price * sqrt(252) overestimates
   volatility because ATR captures the full high-low range, not the close-to-close
   return distribution. The correction factor is approximately 0.80 (Yang-Zhang).
2. The 3-vol-point VRP is constant but should scale with IV level. High-IV regimes
   have larger absolute VRP.
3. No distinction between symbols with different VRP characteristics.

**Improved formula**:

```
realizedVol = stddev(log(close[i]/close[i-1]) for i in last 20 days) * sqrt(252)
vrpMultiplier = 1.10 + 0.05 * ivRank    // 10-15% premium, scaled by IV rank
iv = realizedVol * vrpMultiplier
iv = max(iv, 0.10)
```

Where `ivRank` is the current IV rank (0-1) from the `IVStats` domain type.

**Go code pattern**:

```go
// estimateIVFromRealized computes a synthetic IV from realized volatility
// with a regime-adaptive variance risk premium.
func estimateIVFromRealized(closePrices []float64, ivRank float64) float64 {
    if len(closePrices) < 5 {
        return 0.25 // default
    }

    // Compute log returns
    n := len(closePrices)
    returns := make([]float64, n-1)
    for i := 1; i < n; i++ {
        if closePrices[i-1] > 0 {
            returns[i-1] = math.Log(closePrices[i] / closePrices[i-1])
        }
    }

    // Standard deviation of returns
    mean := 0.0
    for _, r := range returns {
        mean += r
    }
    mean /= float64(len(returns))

    variance := 0.0
    for _, r := range returns {
        d := r - mean
        variance += d * d
    }
    variance /= float64(len(returns) - 1)
    dailyVol := math.Sqrt(variance)

    // Annualize
    realizedVol := dailyVol * math.Sqrt(252)

    // Apply variance risk premium (VRP)
    // VRP averages 10-15% of IV level for equities
    // Higher when IV rank is elevated (mean-reversion effect)
    vrpMult := 1.10 + 0.05*ivRank // 10% base + 5% scaled by rank
    iv := realizedVol * vrpMult

    return math.Max(iv, 0.10)
}
```

**Expected P&L impact**: The ATR-based method overestimates IV by approximately 10-20%
relative to close-to-close realized vol. Since we add a VRP on top, the net effect
depends on the regime. In low-vol regimes, the current method is approximately correct
(ATR overestimate roughly offsets the missing VRP scaling). In high-vol regimes, the
current method significantly overestimates IV, leading to overpriced premiums at entry.

Fixing this changes the entry premium by approximately +/- 5-15%, which directly
affects position sizing (more expensive options = fewer contracts) and P&L calculation.

### Tier 2A: Historical IV Lookup

**Approach**: Use the existing `IVSnapshot` data (collected by `ivcollector`) to
look up the actual ATM IV for a symbol on a given date, then apply a moneyness
adjustment.

**Formula**:

```
iv(strike, DTE) = atmIV(symbol, date) * moneyness_adjustment(strike/spot)

moneyness_adjustment(m) = 1.0 + skewSlope * (0.5 - delta)

where:
  skewSlope ~= 0.08 for indices, ~= 0.05 for single stocks
  delta is the BSM delta at the given strike
```

**Go code pattern**:

```go
// historicalIV looks up the ATM IV for a symbol on a given date and applies
// a simple linear skew adjustment based on moneyness.
func historicalIV(
    ivRepo ports.IVHistoryPort,
    symbol domain.Symbol,
    date time.Time,
    strike, spot float64,
    isCall bool,
) (float64, error) {
    snap, err := ivRepo.GetIVSnapshot(ctx, symbol, date)
    if err != nil {
        return 0, err // fall back to ATR-based estimate
    }

    atmIV := snap.ATMIV
    if atmIV <= 0 {
        return 0, fmt.Errorf("no ATM IV for %s on %s", symbol, date)
    }

    // Simple linear skew: puts are more expensive (higher IV) as they go OTM
    moneyness := strike / spot // >1 = OTM call, <1 = OTM put
    skewSlope := 0.05         // typical for single-stock equities
    skewAdj := skewSlope * (1.0 - moneyness)

    return atmIV + skewAdj, nil
}
```

**Expected P&L impact**: This is the highest-value Tier 2 improvement. On days where
the ATR-estimated IV differs from actual market IV by 5+ vol points (common around
earnings, macro events), the P&L error can be 10-30% of the option premium.
Historical IV lookup eliminates this error entirely for dates with data.

### Tier 2B: Intraday IV Time-of-Day Pattern

**Empirical pattern** (based on published research by Andersen & Bondarenko, 2007):

```
iv_multiplier(t) = 1.0 + amplitude * cos(pi * (t - t_min) / session_length)

where:
  t = minutes since market open
  t_min = ~210 minutes (around 1:00 PM ET, the intraday IV trough)
  amplitude = 0.015 for 30-45 DTE options (1.5% relative swing)
  session_length = 390 minutes
```

This creates a pattern where IV is ~1.5% above average at the open, dips to ~1.5%
below average around 1 PM, and recovers slightly into the close.

**Go code pattern**:

```go
func intradayIVMultiplier(barTime time.Time) float64 {
    et, _ := time.LoadLocation("America/New_York")
    t := barTime.In(et)
    marketOpen := time.Date(t.Year(), t.Month(), t.Day(), 9, 30, 0, 0, et)
    minutesSinceOpen := t.Sub(marketOpen).Minutes()

    if minutesSinceOpen < 0 || minutesSinceOpen > 390 {
        return 1.0
    }

    // Cosine model: peak at open, trough around 210 min (1:00 PM)
    amplitude := 0.015
    trough := 210.0
    phase := math.Pi * (minutesSinceOpen - trough) / 390.0
    return 1.0 + amplitude*math.Cos(phase)
}
```

**Expected P&L impact**: For ORB trades that enter between 9:30-10:00 and exit by
12:00-16:00:
- Entry IV multiplier: ~1.015 (IV elevated at open)
- Exit IV multiplier: ~0.99-1.005 (IV lower later)
- Net effect on long options: ~1-2.5% of vega-driven P&L headwind
- On a $6 premium with $0.08 vega: ~$0.05-0.12 per share per trade

### Tier 3C: Greeks-Based P&L Attribution

**Formula**:

```
P&L = delta * dS + 0.5 * gamma * dS^2 + theta * dt + vega * dIV + residual

where:
  dS    = underlying price change
  dS^2  = squared price change (gamma effect)
  dt    = time elapsed in years
  dIV   = change in implied volatility
```

**Go code pattern**:

```go
// GreeksPnLAttribution decomposes option P&L into Greek components.
type GreeksPnLAttribution struct {
    DeltaPnL  float64 // delta * dS * multiplier
    GammaPnL  float64 // 0.5 * gamma * dS^2 * multiplier
    ThetaPnL  float64 // theta * dt * multiplier
    VegaPnL   float64 // vega * dIV * multiplier
    Residual  float64 // actual - (delta + gamma + theta + vega)
    TotalPnL  float64 // actual option price change * multiplier
}

func ComputeGreeksPnL(
    entryPrice, exitPrice float64,
    delta, gamma, thetaPerDay, vega float64,
    underlyingMove, ivChange float64,
    holdTimeHours float64,
    multiplier float64,
) GreeksPnLAttribution {
    dt := holdTimeHours / (24 * 365) // fraction of year
    dtDays := holdTimeHours / 24.0

    deltaPnL := delta * underlyingMove * multiplier
    gammaPnL := 0.5 * gamma * underlyingMove * underlyingMove * multiplier
    thetaPnL := thetaPerDay * dtDays * multiplier
    vegaPnL := vega * ivChange * multiplier

    totalPnL := (exitPrice - entryPrice) * multiplier
    residual := totalPnL - deltaPnL - gammaPnL - thetaPnL - vegaPnL

    return GreeksPnLAttribution{
        DeltaPnL: deltaPnL,
        GammaPnL: gammaPnL,
        ThetaPnL: thetaPnL,
        VegaPnL:  vegaPnL,
        Residual: residual,
        TotalPnL: totalPnL,
    }
}
```

---

## 6. Part 5: What Is Good Enough for ORB?

### Strategy Profile Recap

| Parameter | Value |
|-----------|-------|
| DTE at entry | 35-45 days |
| Delta at entry | 0.40-0.55 (near ATM) |
| Hold period | Intraday only (1-6 hours) |
| Purpose | Leveraged equity substitute |
| Vol trading | None -- purely directional |
| Symbols | Large-cap, liquid (AAPL, MSFT, GOOGL, TSLA, etc.) |
| Entry window | 9:30-12:00 ET |
| Exit | EOD flatten or intraday stop/target |

### What Matters for This Strategy

**High impact (must fix)**:
1. **Bid-ask spread** -- This is the largest source of unrealism. Round-trip spread
   cost of 2-4% of premium directly reduces every trade's P&L. For a strategy with
   average option return of 3-5%, failing to model this overstates returns by 40-100%.
2. **IV decay between entry and exit** -- The same IV at entry and exit means that
   vega P&L is always zero. In reality, IV declines during the day, creating a
   systematic headwind for long options. Failing to model this overstates returns by
   5-15%.

**Medium impact (should fix when practical)**:
3. **ATR-to-IV conversion accuracy** -- The current formula works reasonably well
   for liquid large-caps in normal vol regimes, but it can be 5-10 vol points off
   around events. Using historical IV data (from the existing ivcollector) would
   be a significant accuracy improvement.
4. **Intraday IV time-of-day pattern** -- ORB entries happen at the open when IV
   is highest. This means we systematically buy expensive vol and sell cheaper vol.
   The effect is 1-2% of premium per trade.

**Low impact (can defer or skip)**:
5. **Skew/smile** -- We trade 0.40-0.55 delta, which is close to ATM. Skew effects
   at these deltas are minimal (0-2 vol points, or <1% of premium).
6. **Pin risk** -- We hold 35-45 DTE options intraday. Completely irrelevant.
7. **Early exercise premium** -- For near-ATM options with 35+ DTE, the American
   vs. European price difference is negligible.
8. **Full IV surface** -- Overkill for near-ATM strikes. A simple skew adjustment
   captures 90%+ of the surface effect at our delta range.
9. **Stochastic vol models** -- Adds complexity without meaningful accuracy gain
   for near-ATM, intraday-hold strategies.

### Recommended Implementation Order

**Phase 1 (do now -- 2-3 hours total)**:
1. Add bid-ask spread to `computeOptionExitPrice` and entry fill pricing (Tier 1B)
2. Add intraday IV decay to `computeOptionExitPrice` (Tier 1A)

These two changes will likely reduce the win rate from 100% to 65-80%, which is a
realistic range for a well-designed intraday directional strategy.

**Phase 2 (do when Tier 1 results are analyzed -- 1-2 days)**:
3. Integrate historical IV from `ivcollector` data into the backtest (Tier 2A)
4. Add intraday IV time-of-day adjustment (Tier 2B)

**Phase 3 (only if considering strategy productionization or capital allocation)**:
5. Subscribe to Polygon.io or Theta Data for historical options prices
6. Replace synthetic pricing with actual historical chain lookup (Tier 3A)

### What Can We Skip Entirely?

Given that the ORB strategy uses options purely as a leveraged equity substitute
(not trading vol), and holds for only a few hours:

- **Full vol surface fitting (SABR/SVI)**: Skip. The strategy does not depend on
  accurate pricing across the entire surface. We only need accurate pricing at one
  strike near ATM.
- **Stochastic vol models (Heston)**: Skip. These are for vol trading strategies
  and exotic options pricing.
- **Greeks hedging simulation**: Skip. We are not market-making or delta-hedging.
- **Early exercise modeling**: Skip. The American premium at 40 DTE, 0.45 delta
  is ~$0.01-0.03, which is noise.
- **Volume/OI-based dynamic spread models**: Skip for now. Our symbols are all
  highly liquid large-caps. A fixed spread model is sufficient.

### Expected Backtest Results After Phase 1

With bid-ask spread and IV decay implemented:

| Metric | Current (broken) | Expected (Phase 1) | Notes |
|--------|-------------------|---------------------|-------|
| Win rate | 100% | 65-80% | Still optimistic without real IV |
| Avg return per trade | ~5%+ | 1-3% | More realistic |
| Sharpe ratio | artificially high | 1.5-2.5 | Reasonable for intraday |
| Max drawdown | near zero | 5-15% | Now models losing streaks |

---

## Appendix: Data Sources

### Free Historical IV Data

| Source | Coverage | Granularity | Notes |
|--------|----------|-------------|-------|
| CBOE VIX/VXN | SPY, QQQ only | Daily | Use as proxy for market-wide IV level |
| Yahoo Finance | Limited options chains | EOD, 1 expiry | Unreliable, gaps |
| Our ivcollector | Configured symbols | Daily, ATM only | Best free option -- already integrated |

### Paid Historical Options Data

| Provider | Cost/month | Coverage | Granularity |
|----------|------------|----------|-------------|
| Polygon.io | $100-300 | All US equities | EOD chains, 15-min snapshots |
| Theta Data | $25-200 | All US equities | EOD to tick-level |
| ORATS | $100-500 | All US equities | EOD, smoothed surfaces |
| Algoseek | $500+ | Full OPRA feed | Tick-level |
| IVolatility | $100-300 | Major underlyings | EOD, surfaces |

### Recommended Path

For oh-my-opentrade, the most cost-effective path is:

1. **Immediate**: Use existing `ivcollector` data to populate historical IV for
   backtesting. This is free and already integrated.
2. **If ivcollector coverage is insufficient**: Add Theta Data EOD subscription ($25/month)
   for comprehensive historical IV data.
3. **Only if pursuing full realism**: Polygon.io ($100/month) for intraday options
   chain snapshots.

The goal is not perfect realism -- it is **enough realism to avoid false confidence**.
A backtest that correctly identifies a strategy with a 70% win rate and 2:1 reward-risk
is far more valuable than one that shows 100% wins and cannot be trusted.
