# S1. Dark Pool Absorption Divergence

> **STATUS: DEPRIORITIZED (2026-04-12)**
>
> The predictive power study found that intraday rolling 30m DP buy_ratio
> has IC = -0.004 (t = -1.18) against 60m forward returns — indistinguishable
> from noise. The 5m bar granularity washes out the absorption signal.
> The quintile spread is ~1 bps, not exploitable after transaction costs.
>
> Could be revisited if tick-level DP data becomes available. The daily-
> horizon signal is real (IC = -0.039) and is captured by the new
> [Overnight Z-Score Bias](overnight-z-bias.md) strategy instead.

## Thesis

When large dark pool volume accumulates at a price level WITHOUT moving
the lit-market price, informed participants are absorbing supply (for longs)
or demand (for shorts). This "absorption" creates a coiled spring — when
the absorption ends or the lit market catches up, price resolves toward
the dark-pool VWAP.

The edge: we detect the divergence between dark-pool activity and lit-price
displacement in real time, and trade the resolution.

## Why it's hard to crowd

1. **Data moat**: requires trade-level FINRA ADF data classified by venue.
   Most retail feeds aggregate or delay dark pool prints. Our Alpaca SIP
   pipeline provides this.
2. **Signal degrades at scale**: a large trader executing this strategy
   would themselves become the dark pool flow, destroying the signal.
3. **Anchor specificity**: combining absorption with our AVWAP anchor
   points creates a signal unique to our data stack.

## Signal definition

### Core metric: Absorption Score

```
absorption_score(symbol, t, window=30min) =
    dark_volume_at_level / max(1, abs(lit_price_displacement))
```

Where:
- `dark_volume_at_level` = sum of DP volume in the window where
  `abs(dp_vwap - current_price) < ATR * 0.5` (prints clustering near
  current price)
- `lit_price_displacement` = `close[t] - close[t - window]` expressed
  as multiples of ATR

High absorption score = lots of dark volume absorbed without price
moving. This is the coiled spring.

### Entry conditions

**Long entry**:
- absorption_score > threshold (tune via backtest; start at 2.0)
- DP buy_ratio > 0.55 (dark flow is net-buy)
- Current price within 0.5 ATR of a DP VWAP support level
- AVWAP confluence score >= min threshold (existing gate)
- Regime is not TREND_DOWN (existing regime filter)

**Short entry** (mirror):
- absorption_score > threshold
- DP buy_ratio < 0.45
- Current price within 0.5 ATR of a DP VWAP resistance level
- Regime is not TREND_UP

### Exit conditions

- Price reaches DP VWAP level that absorption was building toward
  (target = dark-pool VWAP of the absorbing window)
- OR trailing stop at 1.5 ATR
- OR time-based exit after 60 minutes (absorption signal decays)
- OR absorption score drops below 0.5 (spring released without us)

## Data requirements

| Data | Available? | Source |
|---|---|---|
| 5m dark pool bars (dp_volume, dp_vwap, buy/sell) | ✅ | `darkpool_bars` table |
| 1m lit-market bars (OHLCV) | ✅ | `market_bars` table |
| ATR (14-period) | ✅ | `IndicatorCalculator` |
| DP support/resistance levels | ✅ | `Runner.handleBar` overlay (top-3 VWAP) |
| AVWAP confluence score | ✅ | `ScoreDarkPool` + `computeConfluence` |

**Nothing new to acquire.** All data is already in TimescaleDB.

## Implementation plan

### Step 1: AbsorptionCalculator (~150 LOC)

New file: `backend/internal/app/strategy/builtin/absorption_calc.go`

```go
type AbsorptionCalculator struct {
    window     time.Duration // default 30 min
    threshold  float64       // default 2.0
    atrMult    float64       // price proximity = ATR * atrMult
}

type AbsorptionReading struct {
    Score         float64
    DPVolumeNear  float64 // DP volume within price proximity
    LitDisplacement float64 // price move in ATR multiples
    DPBuyRatio    float64
    DPVWAPTarget  float64 // predicted resolution level
}

// Compute returns the absorption reading for the current bar.
// dpBars is the slice of 5m DP bars within the lookback window.
// litBars is the slice of 1m lit bars within the same window.
func (c *AbsorptionCalculator) Compute(
    currentPrice, atr float64,
    dpBars []domain.DarkPoolBar,
    litBars []domain.MarketBar,
) AbsorptionReading
```

### Step 2: DPAbsorptionStrategy (~400 LOC)

New file: `backend/internal/app/strategy/builtin/dp_absorption_v1.go`

Implements `start.Strategy` interface. Uses:
- AbsorptionCalculator for the core signal
- Existing DP indicators from `IndicatorData` (DPRatio, DPBuyRatio, etc.)
- Existing AVWAP confluence gate as a filter
- ATR from `IndicatorData` for stops and proximity

### Step 3: TOML config

New file: `configs/strategies/dp_absorption_v1.toml`

```toml
[meta]
id = "dp_absorption_v1"
name = "Dark Pool Absorption Divergence"
version = "1.0.0"

[routing]
symbols = ["AAPL", "MSFT", "NVDA", "META", "GOOGL", "AMZN", "SPY", "QQQ"]
timeframes = ["5m"]
asset_classes = ["EQUITY"]

[params]
absorption_window_minutes = 30
absorption_threshold = 2.0
atr_proximity_mult = 0.5
dp_buy_ratio_long = 0.55
dp_buy_ratio_short = 0.45
trailing_stop_atr = 1.5
max_hold_minutes = 60
min_confluence_score = 5
cooldown_seconds = 300
max_trades_per_day = 6
```

### Step 4: Backtest integration

The strategy needs access to dark pool bars within the lookback window.
Two options:

**Option A (preferred)**: extend `IndicatorData` with an `AbsorptionScore`
field, computed by the runner's DP overlay loop (already iterates DP bars
per decision bar). The strategy reads a single float.

**Option B**: pass the raw DP bar slice to the strategy via a new context
method. More flexible but breaks the clean IndicatorData boundary.

Recommend Option A for Phase A. If the strategy needs more granularity
later, migrate to Option B.

### Step 5: Wire into strategy registry

Register `dp_absorption_v1` in the strategy factory (same pattern as
MACD, ORB, AVWAP — switch on `spec.Meta.ID`).

## Backtest validation plan

1. Run on 8 symbols, 2025-04-01..2026-04-01 (1 year).
2. Compare profit factor, win rate, Sharpe against a random-entry baseline.
3. Check that signals are NOT correlated with AVWAP/MACD signals — if they
   are, the strategy adds no diversification.
4. Sensitivity sweep on `absorption_threshold` (1.0, 1.5, 2.0, 2.5, 3.0)
   and `absorption_window_minutes` (15, 30, 45, 60).

## Estimated effort

- AbsorptionCalculator: **4 hours**
- DPAbsorptionStrategy: **6 hours**
- TOML + wiring: **2 hours**
- Backtest + tuning: **4 hours**
- **Total: ~2 days**

## Risks

| Risk | Mitigation |
|---|---|
| Absorption score is noisy on low-volume symbols | Gate on min DP volume per window (e.g., > 1000 shares) |
| Signal rarely fires (too selective) | Start with liberal thresholds, tighten via backtest |
| DP VWAP target is unreliable | Use it as a soft target; primary exit is trailing stop |
| Overlap with existing DP confluence scoring | This is a DIFFERENT signal — absorption is about volume-without-displacement, not raw ratio |
