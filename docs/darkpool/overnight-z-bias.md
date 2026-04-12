# S0. Overnight Z-Score Bias

## Status: APPROVED FOR IMPLEMENTATION

Based on empirical validation from the 2026-04-12 predictive power study.
This is not a theoretical design — the signal has been tested across 34
symbols over 15 months with 10,000+ observations.

## Thesis

Late-session dark pool buy_ratio, when abnormally high, predicts lower
next-day returns (and vice versa). Institutions completing large orders
in the final hour of dark pool trading create a mean-reversion setup:
the informed flow is absorbed off-exchange, and the lit market corrects
the following day.

The edge: we detect abnormal late-session DP flow via Z-score and
trade the next-day reversal.

## Empirical basis

### Signal strength

| Metric | Value |
|---|---|
| Late-session Z → next-day IC | -0.039 (t = -3.92) |
| Q1-Q5 quintile spread | 46 bps/day |
| Long signal (Late Z < -1.5) mean | +29.3 bps/day (n=833) |
| Short signal (Late Z > +1.5) mean | -18.1 bps/day (n=828) |
| Annualized Sharpe (raw, Q1 long) | 0.96 |
| Annualized Sharpe (after costs) | 0.65-0.75 |

### Return decomposition

The return accrues in next-day intraday trading, not the overnight gap:

| Entry → Exit | Q1 Long bps |
|---|---|
| Close → Next open | +0.7 |
| Close → Next 10:00 AM | +0.7 |
| Close → Next 11:30 AM | +4.8 |
| Close → Next 1:00 PM | +6.0 |
| Close → Next close | +27.1 |

Optimal exit is next-day close. No intraday exit beats holding to MOC.

### Late-session Z quintile analysis

| Quintile | Overnight bps | Next Intra bps | Total bps | Mean Z |
|---|---|---|---|---|
| Q1 (low late buy) | +13.8 | +13.5 | +27.0 | -1.526 |
| Q2 | +5.2 | +24.2 | +29.4 | -0.578 |
| Q3 | +8.6 | +2.3 | +9.9 | -0.011 |
| Q4 | +8.3 | -8.8 | -1.2 | +0.565 |
| Q5 (high late buy) | -4.6 | -12.1 | -17.2 | +1.504 |

### Conditional filters

| Condition | n | Total bps |
|---|---|---|
| High DP-ratio day + Z low | 1570 | +26.4 |
| Low large-print ratio + Z low | 1072 | +33.1 |
| Low large-print + Z low (next intra) | 1072 | +39.9 |

Fragmented institutional selling (low large-print ratio) produces the
strongest next-day reversal (+40 bps intraday).

### Consistency

Monthly hit rates for Q1 long:

| Period | Hit Rate | Character |
|---|---|---|
| Jan-Mar 2025 | 0.45-0.51 | Negative (regime unfavorable) |
| Apr-Oct 2025 | 0.52-0.66 | Strongly positive |
| Nov 2025-Feb 2026 | 0.42-0.53 | Mixed |
| Mar 2026 | 0.48 | Moderate |

The signal has regime dependence. The kill switch is essential.

### Per-symbol IC (full-day Z → next-day return)

Best: XOM +0.09, NET +0.08, NFLX +0.07, JPM +0.06
Worst: QQQ -0.14, AFRM -0.10, HOOD -0.09, HIMS -0.09

QQQ's negative IC reflects ETF AP/hedging flow, not directional
institutional intent. Meme-adjacent names (AFRM, HOOD, HIMS) reflect
retail broker internalization. Exclude these.

## Strategy specification

### Signal computation

```
late_session_window = 14:00-15:30 ET (last 90 min before MOC cutoff)

late_buy_ratio = sum(buy_volume, late_session) /
                 sum(buy_volume + sell_volume, late_session)

late_z = (late_buy_ratio - rolling_mean_20d) / rolling_std_20d
```

Computed daily from existing 5m `darkpool_bars`. No new data required.

### Entry rules

**Long entry** (primary):
- Late Z < -1.5 (abnormally low DP buying = bullish reversal)
- Next day at 09:35 ET, market order on equity shares
- Not options — theta drag overnight eats ~50% of the edge

**Short entry** (Phase 2, disabled initially):
- Late Z > +1.5 (abnormally high DP buying = bearish reversal)
- Same timing and execution
- Enable only after 3+ months of live paper validation on longs

### Exit rules

- **Primary exit**: MOC at 15:45 ET (return builds monotonically to close)
- **Hard stop**: 200 bps against entry price (~8% of trades historically)
- **No trailing stop, no partial exits.** The signal is a daily-scale
  mean-reversion bet. Cutting early destroys the edge.

### Position sizing

- 2% of equity per signal (conservative start)
- Max 6 concurrent positions
- Max 2 per sector group
- Total overnight exposure capped at 12% of equity

### Universe

28 symbols (exclude QQQ, AFRM, HOOD, HIMS from the 34 active):

```
AAPL, AMD, AMZN, AVGO, BA, COIN, CRM, GOOGL, HIMS→excluded,
HOOD→excluded, IWM, JPM, LLY, META, MRNA, MRVL, MSFT, MU, NET,
NFLX, NVDA, OXY, PLTR, QQQ→excluded, RBLX, RIVN, SMCI, SNOW,
SOFI, SOXL, SPY, TSLA, XOM, AFRM→excluded
```

### Risk management

- **Kill switch**: If trailing 20-trade win rate drops below 0.38,
  disable entries for 5 trading days.
- **Correlation with AVWAP+MACD**: Near zero by construction.
  AVWAP+MACD closes flat by EOD. Overnight Z holds overnight.
  Different instruments (equity vs options).

### Why not options

The AVWAP+MACD stack uses 3-7 DTE options at 0.60-0.75 delta. This
strategy must NOT use options because:
- Theta decay overnight on 3 DTE at ~$5 premium = 10-15 bps drag
- Bid-ask spread widens at open (gap risk on premium)
- The edge is 27 bps on underlying — options friction eats half of it

Trade equity shares directly.

## TOML config

```toml
[meta]
id = "overnight_z_v1"
name = "Overnight Z-Score Bias"
version = "1.0.0"
schema_version = 2

[routing]
symbols = [
    "AAPL", "AMD", "AMZN", "AVGO", "BA", "COIN", "CRM", "GOOGL",
    "IWM", "JPM", "LLY", "META", "MRNA", "MRVL", "MSFT", "MU",
    "NET", "NFLX", "NVDA", "OXY", "PLTR", "RBLX", "RIVN", "SMCI",
    "SNOW", "SOFI", "SOXL", "SPY", "TSLA", "XOM"
]
timeframes = ["1d"]
asset_classes = ["EQUITY"]

[params]
late_session_start = "14:00"
late_session_end = "15:30"
z_lookback_days = 20

late_z_long_threshold = -1.5
late_z_short_threshold = 1.5
long_only = true

entry_time = "09:35"
exit_time = "15:45"
hard_stop_bps = 200

risk_per_trade_pct = 2.0
max_positions = 6
max_per_sector = 2

rolling_wr_kill_threshold = 0.38
rolling_wr_kill_window = 20
rolling_wr_cooldown_days = 5
```

## Integration: AVWAP+MACD trade conditioning

### Critical finding: AVWAP and MACD respond OPPOSITELY to Late Z

Cross-referencing 13,204 actual backtest trades (6,224 AVWAP, 6,980 MACD)
against the prior-day late Z score reveals that the two strategies have
**inverted** responses to the DP signal. A single directional filter
applied uniformly to both would help one and destroy the other.

### AVWAP — Z signal works as expected (mean-reversion alignment)

| Z Bucket | Trades | Win Rate | PF | Avg P&L | Total P&L |
|---|---|---|---|---|---|
| **Favorable (z<-1)** | 1,061 | **73.2%** | **1.863** | **$212** | **$225K** |
| Neutral | 4,236 | 69.4% | 1.359 | $105 | $445K |
| Adverse (z>1) | 927 | 65.6% | 1.419 | $121 | $112K |

AVWAP is a mean-reversion strategy. Low DP buying yesterday creates a
bullish reversal tailwind that aligns with AVWAP's long entries.

**Extreme buckets:**
- Z < -1.5: PF **2.239**, WR **79.3%** (492 trades) — exceptional
- Z > +1.5: PF **1.037**, WR 63.8% (351 trades) — breakeven

### MACD — Z signal is INVERTED (momentum alignment)

| Z Bucket | Trades | Win Rate | PF | Avg P&L | Total P&L |
|---|---|---|---|---|---|
| Favorable (z<-1) | 1,092 | 40.4% | **0.874** | **-$32** | **-$35K** |
| Neutral | 4,595 | 46.1% | 1.310 | $68 | $310K |
| **Adverse (z>1)** | 1,293 | **48.8%** | **1.741** | **$140** | **$181K** |

MACD is a momentum strategy. High DP buying yesterday means the prior
trend was strong enough to attract institutional participation. MACD
catches the continuation; the "reversal" hasn't happened yet during the
next intraday session — it comes later.

**Extreme buckets:**
- Z < -1.5: PF **0.854**, WR 35.3% (479 trades) — loses money
- Z > +1.5: PF **2.303**, WR **57.1%** (669 trades) — strongest MACD regime

### Why the inversion makes sense

AVWAP enters on mean-reversion setups (price returning to anchored VWAP).
The DP reversal signal adds a tailwind — yesterday's DP flow was absorbed,
today's lit market corrects. Both forces align.

MACD enters on momentum breakouts. When DP buying was high yesterday,
the trend was strong. MACD catches intraday continuation of that trend
before the daily-scale reversal kicks in (which takes until EOD/next day).
On "favorable" Z days (low DP buying), there's no momentum for MACD to
catch.

### Hold time analysis

**AVWAP hold time by Z regime:**

| Z Regime | <30m | 30-60m | 60-120m | >120m |
|---|---|---|---|---|
| Favorable (z<-1) | +$738, 100% WR | +$498, 100% | -$31, 55% | **-$462, 37%** |
| Neutral | +$638, 99% | +$546, 100% | -$52, 56% | -$552, 33% |
| Adverse (z>1) | +$841, 100% | +$528, 100% | +$35, 64% | **-$503, 29%** |

**Pattern:** Quick wins work in all regimes. Long holds bleed in all
regimes but bleed worst on adverse Z days. On adverse Z days, the >120m
bucket drops to 29% WR with -$503 avg loss.

**MACD hold time by Z regime:**

| Z Regime | <30m | 30-60m | 60-120m | >120m |
|---|---|---|---|---|
| Favorable (z<-1) | +$522, 72% | +$205, 45% | **-$161, 31%** | **-$285, 30%** |
| Neutral | +$686, 68% | +$260, 63% | +$47, 46% | -$241, 32% |
| Adverse (z>1) | **+$964, 78%** | +$270, 57% | +$36, 43% | -$194, 37% |

**Pattern:** MACD on adverse Z days produces the strongest <30m bucket
(+$964, 78% WR) — the momentum is fast and front-loaded. On favorable
Z days, even short holds struggle.

### Exit reason breakdown

**AVWAP favorable Z days:** 70% exit via PREMIUM_TARGET (quick winners),
23% via EOD_FLATTEN (losers held to close).

**AVWAP adverse Z days:** 60% PREMIUM_TARGET, 32% EOD_FLATTEN — more
trades dragging into close as losers.

**MACD adverse Z days:** 30% PREMIUM_TARGET, 23% MAX_HOLDING_TIME,
23% EOD_FLATTEN — balanced exit profile.

### Recommended integration: per-strategy Z conditioning

**For AVWAP — use Z as hold time / target adjuster:**

```
If yesterday's Late Z < -1.0 (favorable):
  → Extend max hold time (allow longer for target to hit)
  → Widen target multiplier (e.g., premium_target * 1.2)
  → Standard stops

If yesterday's Late Z > +1.0 (adverse):
  → Tighten time exit (reduce max hold by 25-30%)
  → Tighten target (take profits faster)
  → Consider blocking entries after 60m without fill
```

**For MACD — use INVERTED Z conditioning:**

```
If yesterday's Late Z > +1.0 (MACD-favorable):
  → Allow entries (momentum regime)
  → Tighten time exit anyway — the edge is front-loaded (<30m)
  → Keep standard targets

If yesterday's Late Z < -1.0 (MACD-adverse):
  → Suppress entries entirely (PF = 0.854, loses money)
  → Or at minimum: require higher MACD signal strength to enter
```

### TOML params for Z conditioning

**AVWAP config additions:**
```toml
[params]
# ... existing params ...
dp_z_conditioning_enabled = false
dp_z_favorable_threshold = -1.0
dp_z_adverse_threshold = 1.0
dp_z_favorable_target_mult = 1.2      # widen target on favorable days
dp_z_adverse_hold_time_mult = 0.70    # tighten hold time on adverse days
```

**MACD config additions:**
```toml
[params]
# ... existing params ...
dp_z_conditioning_enabled = false
dp_z_macd_favorable_threshold = 1.0   # NOTE: inverted — high Z is good for MACD
dp_z_macd_suppress_threshold = -1.0   # low Z suppresses MACD entries
dp_z_macd_suppress_mode = "block"     # "block" or "raise_threshold"
```

## Implementation plan

### Phase 1: Indicator pipeline (1-2 days)

Add `LateSessionDPZ float64` to `IndicatorData` in
`backend/internal/domain/strategy/contract.go`.

Compute it in the runner's DP overlay pass (`runner.go`):
1. During daily bar processing, aggregate 5m DP bars from 14:00-15:30 ET
2. Compute buy_ratio for the late window
3. Maintain a 20-day rolling mean/std per symbol
4. Output the Z-score as `LateSessionDPZ`

This reuses the existing `dpRepo.GetDarkPoolBarsMulti` infrastructure.

### Phase 2: Strategy engine (2-3 days)

New file: `backend/internal/app/strategy/builtin/overnight_z_v1.go`

Implements `strategy.Strategy` interface:
- State: yesterday's Late Z, current position, entry price
- `OnBar` at 09:35 bar: if yesterday's Late Z < -1.5 and no position
  and kill switch not active → emit LONG entry
- `OnBar` at 15:45 bar: emit exit for any open position
- `OnBar` every bar: check hard stop (200 bps)
- Track rolling 20-trade win rate for kill switch

Register in strategy factory (same pattern as MACD, AVWAP).

### Phase 3: Backtest validation (1 day)

Run on all 30 symbols, 2025-01-01 to 2026-03-31.

**Gate criteria** (must pass before Phase 4):
- Profit factor > 1.15 (conservative, accounts for costs)
- Annualized Sharpe > 0.6 after 8 bps round-trip slippage
- Max drawdown < 15% of allocated capital
- Minimum 200 trades for statistical significance

### Phase 4: Per-strategy Z conditioning (2 days)

**AVWAP** (`avwap_v4`): Add `dp_z_conditioning_enabled` and related
params to TOML. In `OnBar`, read `LateSessionDPZ` from `IndicatorData`:
- Favorable Z (< -1.0): multiply `premium_target` by
  `dp_z_favorable_target_mult` (default 1.2)
- Adverse Z (> +1.0): multiply `max_hold_time` by
  `dp_z_adverse_hold_time_mult` (default 0.70)

**MACD** (`macd_only_v1`): Add `dp_z_conditioning_enabled` and related
params. **NOTE: inverted logic.**
- MACD-favorable Z (> +1.0): allow entries normally
- MACD-adverse Z (< -1.0): suppress entries entirely
  (`dp_z_macd_suppress_mode = "block"`) or require higher signal
  strength (`"raise_threshold"`)

**Important:** Do NOT apply a uniform directional filter. The two
strategies respond oppositely to the Z signal (see empirical findings).

### Phase 5: Paper trading (2-4 weeks)

Live paper on staging with standalone strategy.

**Gate before real capital:**
- 20+ completed trades
- Profit factor > 1.10
- Max drawdown < 8% of allocated capital
- No kill switch triggers lasting > 2 consecutive periods

## Capacity and P&L estimates

| Equity | Size/trade | Trades/yr | Net bps/trade | Annual P&L | Sharpe |
|---|---|---|---|---|---|
| $500K | $10K (2%) | ~375 | ~20 | ~$7,500 | 0.65-0.75 |
| $1M | $20K (2%) | ~375 | ~20 | ~$15,000 | 0.65-0.75 |
| $2M | $50K (2.5%) | ~375 | ~18 | ~$33,750 | 0.60-0.70 |

Capacity ceiling: ~$2M before MOC orders move mid-cap prices.

This is a supplemental alpha source. Primary value is:
1. Uncorrelated daily returns alongside intraday AVWAP+MACD
2. Per-strategy Z conditioning — AVWAP PF jumps from 1.36 → 1.86 on
   favorable days; MACD PF jumps from 1.31 → 1.74 on its favorable
   days (inverted). Suppressing MACD on adverse days eliminates a
   PF 0.85 regime that loses $35K in backtest.
3. Validates dark pool data at a second timescale

## Risks

| Risk | Mitigation |
|---|---|
| Regime dependence (Jan-Mar 2025 was negative) | Kill switch at WR < 0.38 over 20 trades |
| FINRA ADF composition shift | Monitor ADF volume as % of consolidated; alert on trend |
| Signal decay (others discover the same edge) | Edge requires venue-classified trade-level data — high barrier |
| Late-session Z computation delay | Pre-compute at 15:35 ET, cache for next-day use |
| Correlation with AVWAP+MACD in drawdowns | Near zero — different timescale, different instruments |
| Only 15 months of data | Phase 5 paper trading validates OOS before real capital |

## Revision log

- 2026-04-12 — Initial design based on predictive power study results.
  Long-only standalone + AVWAP/MACD bias filter.
- 2026-04-12 — Cross-referenced 13,204 actual backtest trades against
  late Z. Key finding: AVWAP and MACD respond **oppositely** to the Z
  signal. Replaced uniform directional filter with per-strategy Z
  conditioning (hold time/target adjustment for AVWAP, entry suppression
  for MACD). Added hold time analysis and exit reason breakdown.
