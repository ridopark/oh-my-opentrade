# S2. Dark-to-Lit Migration Momentum

## Thesis

Informed institutions begin accumulating (or distributing) in dark pools
where their orders won't move the market. At some point, the flow
"migrates" to lit exchanges — either because the dark pool filled their
need, or because they're now comfortable showing their hand. This
dark-to-lit migration precedes a directional move as the information
becomes public.

The signal: the intra-session dark/lit volume ratio shifts from
above-normal to below-normal. When the ratio flips, the informed flow
is going public. Trade the direction of the original dark accumulation.

## Why it's hard to crowd

1. **Same data moat as S1** — trade-level venue classification is rare
   in retail feeds.
2. **Timing is session-relative** — the ratio regime change depends on
   the specific session's initial state. No fixed thresholds means no
   easy parameter snooping by competitors.
3. **Combines two time-regimes** — early-session dark accumulation +
   mid-session lit breakout is a two-part pattern that single-indicator
   systems can't capture.

## Signal definition

### Core metric: Dark/Lit Ratio Regime

For each symbol within a trading session:

```
early_dark_ratio = sum(dp_volume, 9:30-10:00) / sum(total_volume, 9:30-10:00)
current_dark_ratio = sum(dp_volume, rolling 30m) / sum(total_volume, rolling 30m)
historical_dark_ratio = median(daily_dark_ratio, trailing 20 sessions)
```

**Migration detected when**:
- `early_dark_ratio > historical_dark_ratio * 1.5` (unusually high early DP)
- AND `current_dark_ratio < historical_dark_ratio * 0.8` (DP ratio has
  dropped back to normal or below)
- AND the transition happened within the same session

This means: someone was heavily using dark pools early, then stopped.
The information is now migrating to lit venues.

### Direction inference

- If `early_dp_buy_ratio > 0.55` during the high-DP window → **long**
  (they were accumulating)
- If `early_dp_buy_ratio < 0.45` → **short** (they were distributing)
- If between 0.45-0.55 → no signal (direction ambiguous)

### Entry conditions

- Migration detected (ratio flip criteria above)
- Direction inferred from early DP buy ratio
- Current price has NOT already moved > 1 ATR in the inferred direction
  (don't chase; the edge is catching the transition, not the aftermath)
- Regime filter: not counter-trend (existing regime gate)
- Time: 10:00 AM - 2:00 PM ET (after the accumulation window, before
  close-of-day noise)

### Exit conditions

- **Target**: 1.5 ATR move in direction (capture the information release)
- **Stop**: 1.0 ATR against (tight — if it doesn't move, the signal was
  wrong)
- **Time exit**: 90 minutes max hold (the migration edge is intraday)
- **Signal invalidation**: dark/lit ratio reverses back above the early
  level (accumulation resumed in dark; we were wrong about migration)

## Data requirements

| Data | Available? | Source |
|---|---|---|
| 5m dark pool bars (dp_volume, dp_vwap, buy_volume, sell_volume, total_volume) | ✅ | `darkpool_bars` table |
| 1m lit-market bars | ✅ | `market_bars` table |
| Historical daily DP ratio (for baseline) | ✅ | compute from `darkpool_bars` aggregated daily |
| ATR | ✅ | `IndicatorCalculator` |
| Session boundaries (9:30 ET open) | ✅ | `domain.CalendarFor` + NYLocation |

**Nothing new to acquire.**

## Implementation plan

### Step 1: MigrationDetector (~200 LOC)

New file: `backend/internal/app/strategy/builtin/dp_migration_calc.go`

```go
type MigrationDetector struct {
    earlyWindowEnd    time.Duration // 30 min from open (9:30-10:00)
    rollingWindow     time.Duration // 30 min rolling
    ratioElevatedMult float64       // 1.5 (early must be 1.5x historical)
    ratioDroppedMult  float64       // 0.8 (current must be < 0.8x historical)
    buyRatioLong      float64       // 0.55
    buyRatioShort     float64       // 0.45
}

type MigrationSignal struct {
    Detected        bool
    Direction       string  // "long", "short", ""
    EarlyDPRatio    float64
    CurrentDPRatio  float64
    HistoricalRatio float64
    EarlyBuyRatio   float64
    MigrationTime   time.Time // when the ratio flipped
}

// Evaluate checks for dark-to-lit migration within the current session.
// sessionDPBars: all 5m DP bars for this symbol today, ordered by time.
// historicalDailyRatio: median daily DP ratio from trailing 20 sessions.
func (d *MigrationDetector) Evaluate(
    now time.Time,
    sessionOpen time.Time,
    sessionDPBars []domain.DarkPoolBar,
    historicalDailyRatio float64,
) MigrationSignal
```

### Step 2: SessionDPAccumulator (~100 LOC)

Per-symbol state that tracks intra-session DP accumulation. Reset on
new session day. Maintains:
- Running totals for early window (dp_vol, buy_vol, total_vol)
- Rolling window totals for current period
- Historical daily ratio buffer (last 20 sessions)

This lives inside the strategy state (same pattern as AVWAP's
`AnchoredVWAPCalc` stored on the state struct).

### Step 3: DPMigrationStrategy (~350 LOC)

New file: `backend/internal/app/strategy/builtin/dp_migration_v1.go`

Implements `start.Strategy`. On each bar:
1. Update SessionDPAccumulator with new DP data
2. Call MigrationDetector.Evaluate
3. If migration detected and direction clear → emit entry signal
4. Manage open positions (trailing stop, time exit, invalidation exit)

### Step 4: TOML config

```toml
[meta]
id = "dp_migration_v1"
name = "Dark-to-Lit Migration Momentum"
version = "1.0.0"

[routing]
symbols = ["AAPL", "MSFT", "NVDA", "META", "GOOGL", "AMZN", "SPY", "QQQ",
           "AMD", "AVGO", "PLTR", "CRM"]
timeframes = ["5m"]
asset_classes = ["EQUITY"]

[params]
early_window_minutes = 30
rolling_window_minutes = 30
ratio_elevated_mult = 1.5
ratio_dropped_mult = 0.8
dp_buy_ratio_long = 0.55
dp_buy_ratio_short = 0.45
max_chase_atr = 1.0
target_atr = 1.5
stop_atr = 1.0
max_hold_minutes = 90
allowed_hours_start = "10:00"
allowed_hours_end = "14:00"
cooldown_seconds = 600
max_trades_per_day = 3
historical_lookback_days = 20
```

### Step 5: Historical baseline computation

At warmup time, compute the trailing 20-day median DP ratio per symbol
from `darkpool_bars`. Store in the strategy state. Update daily.

This can reuse the existing DP bar loading infrastructure from the
backtest runner (`dpRepo.GetDarkPoolBarsMulti`).

### Step 6: Wire + backtest

Same wiring pattern as S1. Register in factory, run backtests.

## How S1 and S2 complement each other

| | S1 Absorption | S2 Migration |
|---|---|---|
| **When** | During accumulation (coiled spring) | After accumulation ends (spring released) |
| **Signal** | High dark volume, no price move | Dark ratio dropping, lit volume rising |
| **Hold time** | 30-60 min (wait for resolution) | 60-90 min (ride the migration) |
| **Best regime** | Balance / compression | Early trend / breakout |
| **Overlap risk** | Low — S1 fires BEFORE S2 by construction |

They're sequential: S1 detects the accumulation, S2 detects the release.
Running both gives you two bites at the same institutional flow.

## Backtest validation plan

1. Run on 12 symbols, 2025-04-01..2026-04-01.
2. Check signal frequency — expect 1-3 signals per symbol per week
   (migration is a relatively rare event).
3. Win rate target: > 55%. The directional inference from early DP buy
   ratio should be correct more often than not.
4. Correlation with S1: compute signal overlap. If > 50% of S2 signals
   have a preceding S1 signal within 2 hours, the strategies are
   complementary. If > 80%, they're redundant — consider merging.

## Estimated effort

- MigrationDetector + SessionDPAccumulator: **5 hours**
- DPMigrationStrategy: **5 hours**
- TOML + wiring + historical baseline: **3 hours**
- Backtest + tuning: **4 hours**
- **Total: ~2 days**

## Risks

| Risk | Mitigation |
|---|---|
| Migration is too rare (low signal count) | Relax ratio_elevated_mult to 1.3; expand symbol universe |
| Early-session data is noisy (pre-market, low volume) | Use 9:35-10:00 instead of 9:30-10:00 to skip the opening cross noise |
| Historical ratio baseline shifts after earnings/events | Use median (robust to outliers) instead of mean; cap lookback at 20 days |
| Direction inference from buy_ratio is wrong | The 0.55/0.45 thresholds leave a dead zone — no signal when ambiguous |
