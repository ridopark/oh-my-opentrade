# Inducement / Failed-Breakout Detector for AVWAP Confluence

## Context

When price sweeps past a prior swing high/low (taking out stops) then reverses back within a few bars, that's a "liquidity sweep" or "inducement." Weak hands are shaken out, creating higher-conviction entries for the reversal direction.

Inspired by Waqar Asim's scalping strategy (ICT/smart-money concepts). The quant analysis concluded this should NOT be a standalone strategy (edge-after-friction too thin on equities, discretionary skill is the real edge) but the liquidity sweep concept has value as a **confluence input** to AVWAP entries.

Reuses existing `SwingDetector` on 5m bars — no 1m bar tracking needed.

## Confluence Scoring (max 5 pts)

| Condition | Points | Tag |
|-----------|--------|-----|
| Same-bar reversal + volume confirmed | **5** | `inducement_strong` |
| Multi-bar reversal (2-3 bars) + volume confirmed | **3** | `inducement_moderate` |
| Same-bar reversal, volume NOT confirmed | **2** | `inducement_weak` |

Raises AVWAP-specific confluence max from 23 to 28. Added as Factor 7 in `computeConfluence()`.

## State Tracking

Add to `AVWAPState`:

```go
RecentSwingHighs []SwingLevel // ring buffer, capacity InducementSwingDepth
RecentSwingLows  []SwingLevel // ring buffer, capacity InducementSwingDepth
PendingInducement *PendingInducement // multi-bar reversal tracker
CurrentInducement *InducementSignal  // fires when confirmed, consumed by computeConfluence
```

```go
type SwingLevel struct {
    Price    float64
    BarTime  time.Time
    Strength float64   // from SwingDetector.swingStrength()
    BarIndex int       // monotonic bar counter for age calculation
}

type PendingInducement struct {
    SwingPrice    float64
    Direction     bool // true = bullish (sweep of low), false = bearish (sweep of high)
    BreachBPS     float64
    VolConfirmed  bool
    BarsRemaining int
}

type InducementSignal struct {
    Direction  bool
    BreachBPS  float64
    Points     int
    FactorName string // "inducement_strong", "inducement_moderate", "inducement_weak"
}
```

Populated from existing `SwingDetector.Push()` output (reuses anchor detection results).

## Config Params

Add to `AVWAPConfig`:

```go
InducementEnabled        bool    `toml:"inducement_enabled"`          // default false
InducementSwingN         int     `toml:"inducement_swing_n"`          // default 3
InducementSwingDepth     int     `toml:"inducement_swing_depth"`      // default 8
InducementMaxAgeBars     int     `toml:"inducement_max_age_bars"`     // default 60 (5 hours at 5m)
InducementBreachMinBPS   int     `toml:"inducement_breach_min_bps"`   // default 5
InducementBreachMaxBPS   int     `toml:"inducement_breach_max_bps"`   // default 80
InducementReversalBars   int     `toml:"inducement_reversal_bars"`    // default 3
InducementVolumeMinRatio float64 `toml:"inducement_volume_min_ratio"` // default 1.2
```

## Detection Algorithm

Called once per bar in `OnBar`, after updating `RecentSwingHighs/Lows`:

```
func detectInducement(bar, prevBars, swingHighs, swingLows, cfg, indicators) *InducementSignal
```

### Step 1: Find candidate sweep

For each recent swing high (bearish setups) and swing low (bullish setups):

```
// For swing highs (bearish inducement):
breachBPS = (bar.High - swingHigh.Price) / swingHigh.Price * 10000

// For swing lows (bullish inducement):
breachBPS = (swingLow.Price - bar.Low) / swingLow.Price * 10000
```

Qualify if: `breachBPS >= breach_min_bps AND breachBPS <= breach_max_bps AND swingAge <= max_age_bars`

### Step 2: Confirm reversal

**Same-bar reversal (strongest):**
```
sweepHigh: bar.High > swingHigh.Price AND bar.Close < swingHigh.Price
sweepLow:  bar.Low  < swingLow.Price  AND bar.Close > swingLow.Price
```

**Multi-bar reversal:**
Store as `PendingInducement { SwingPrice, Direction, BarsRemaining }`. Each subsequent bar: decrement `BarsRemaining`; confirm if close crosses back inside swing level. Discard if countdown expires.

### Step 3: Volume filter

Sweep bar must have: `bar.Volume / indicators.VolumeSMA >= inducement_volume_min_ratio`

### Step 4: Direction alignment

- Sweep of swing HIGH then reversal down = **bearish** inducement (favors short entries)
- Sweep of swing LOW then reversal up = **bullish** inducement (favors long entries)
- Only award confluence points when inducement direction matches trade direction

## False Positive Guards

1. **Min breach (5 bps)**: Wicks that merely touch the level score 0
2. **Max breach (80 bps)**: Sustained moves past the level are real breakouts, not sweeps. On a $200 stock, 80 bps = $1.60.
3. **Volume gate (1.2x)**: Low-volume wicks (pre-market leak, thin tape) filtered out
4. **Age cap (60 bars = 5 hours)**: Stale swing levels lose intraday relevance
5. **Reversal speed (3 bars = 15 min)**: If price doesn't close back inside within 3 bars, the breakout is likely real
6. **Both sides swept**: Sweep of both a swing high AND swing low on the same bar = 0 (volatility event, not targeted sweep)

## Implementation Steps

### Step 1: Add config fields and defaults

Add the 8 fields above to `AVWAPConfig` and `defaultAVWAPConfig()`.

### Step 2: Add state fields

Add `RecentSwingHighs`, `RecentSwingLows`, `PendingInducement`, `CurrentInducement` to `AVWAPState`. Update serialization.

### Step 3: Populate swing levels

In `OnBar`, after existing `SwingDetector.Push()` for anchor detection, append detected swings to the ring buffers. Evict entries older than `InducementMaxAgeBars`.

### Step 4: Implement `detectInducement()`

Run after swing level update, before entry mode evaluation. Store result as `state.CurrentInducement`.

### Step 5: Score in `computeConfluence()`

After Factor 6 (Whale), add Factor 7:

```go
indComp := start.ComponentScore{Name: "inducement", Group: "structure", Weight: 5}
if cfg.InducementEnabled && state.CurrentInducement != nil {
    ind := state.CurrentInducement
    if ind.Direction == isLongEntry {
        indComp.Fired = true
        indComp.Value = float64(ind.BreachBPS)
        res.Score += ind.Points
        res.Factors = append(res.Factors, ind.FactorName)
    }
}
res.Components = append(res.Components, indComp)
```

### Step 6: Add signal tags

Add `inducement=strong,breach_bps=25` to entry signal tags for analysis.

## Edge Cases

- **Multiple qualifying sweeps on one bar**: Use strongest (highest breach BPS with volume confirmation). Do not stack.
- **Pending inducement + new sweep**: New sweep replaces pending. Only one active inducement at a time.
- **Gap opens past swing then closes inside**: Valid inducement (gap-and-reverse pattern). Breach BPS from bar.High/Low, not bar.Open.
- **Sweep of both high AND low**: Award 0 — directions cancel.

## Validation Plan

1. **Baseline run**: Current config, no inducement scoring. Record per-trade confluence scores and P&L.
2. **Scoring-only run**: Enable `inducement_enabled = true` with defaults. Log inducement factor occurrences without changing `MinConfluenceScore`. Compare WR and PF for trades where inducement fired vs. did not.
3. **Gate integration**: If inducement-tagged trades show PF delta >= 0.3, raise `MinConfluenceScore` by 2-3 points to let inducement-confirmed trades through while filtering marginal non-inducement trades.
4. **Parameter sensitivity**: Sweep `inducement_breach_min_bps [3, 5, 10]` and `inducement_reversal_bars [2, 3, 5]`. Ensure trade count stays above 100 in sample.

## Files to Modify

- `backend/internal/app/strategy/builtin/avwap_v1.go` — config, state, `OnBar` swing tracking, `detectInducement()`, `computeConfluence()`, signal tags
- `backend/internal/domain/strategy/swing_detector.go` — no changes needed (existing API sufficient)
