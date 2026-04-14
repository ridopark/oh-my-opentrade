# Session-Time Weighting for AVWAP Entry Strength

## Context

Currently time-of-day is a binary gate (`AllowedHoursStart/End`) — entries are either fully allowed or fully blocked. This wastes good setups at gate edges and lets weak setups through during low-conviction windows like midday chop.

Inspired by Waqar Asim's "time confirmation" concept (trade only during session opens). Academic and practitioner evidence shows US equity intraday patterns have statistically different directional conviction by time bucket: opening drive (9:30-10:00) and MOC imbalance (15:00-15:30) outperform, while lunch hour (12:00-14:00) underperforms for trend-following.

## Design Decision: Multiplier on Strength, Not Additive Points

Session time doesn't predict direction — it predicts how much to trust existing confluence. Therefore it should be a **multiplier on entry strength**, not additive points in the confluence score. This keeps `MinConfluenceScore` gate unaffected — a 30-point trade during lunch still has 30 points of confluence, it just gets reduced position size.

## Time Buckets (ET)

| Window | Default Weight | Rationale |
|--------|---------------|-----------|
| 09:30-10:00 | **1.15** | Opening drive, highest volume, directional conviction from overnight flow |
| 10:00-10:30 | **1.10** | Extended open, initial retracements, still high conviction for pullbacks |
| 10:30-12:00 | **1.00** | Mid-morning, neutral |
| 12:00-14:00 | **0.85** | Lunch chop, lowest volume, worst trend-following performance |
| 14:00-15:00 | **1.00** | Afternoon re-engagement, neutral |
| 15:00-15:30 | **1.10** | MOC imbalance window, strong directional moves |
| 15:30-16:00 | **0.95** | Last 30 min, erratic rebalancing/hedging |
| Outside RTH | **0.00** | Blocks entry (replaces binary AllowedHours gate) |

## Config Params

Add to `AVWAPConfig`:

```go
SessionWeightEnabled     bool    `toml:"session_weight_enabled"`      // default false
SessionWeightTZ          string  `toml:"session_weight_tz"`           // default "America/New_York"
SessionWeightOpen        float64 `toml:"session_weight_open"`         // 09:30-10:00, default 1.15
SessionWeightExtendedOpen float64 `toml:"session_weight_extended_open"` // 10:00-10:30, default 1.10
SessionWeightMidMorning  float64 `toml:"session_weight_mid_morning"`  // 10:30-12:00, default 1.00
SessionWeightLunch       float64 `toml:"session_weight_lunch"`        // 12:00-14:00, default 0.85
SessionWeightAfternoon   float64 `toml:"session_weight_afternoon"`    // 14:00-15:00, default 1.00
SessionWeightMOC         float64 `toml:"session_weight_moc"`          // 15:00-15:30, default 1.10
SessionWeightClose       float64 `toml:"session_weight_close"`        // 15:30-16:00, default 0.95
SessionWeightOutside     float64 `toml:"session_weight_outside"`      // outside RTH, default 0.0
```

## Strength Formula Change

Current:
```go
adjustedStrength := conf.applyDPSizing(math.Min(1.0, 0.7+float64(conf.Score)*0.03))
```

New:
```go
baseStrength := math.Min(1.0, 0.7+float64(conf.Score)*0.03)
sessionMult := conf.applySessionWeight(bar.Time, cfg)
adjustedStrength := conf.applyDPSizing(baseStrength * sessionMult)
```

## Implementation Steps

### Step 1: Add config fields and defaults

Add the 10 fields above to `AVWAPConfig` struct and `defaultAVWAPConfig()`.

### Step 2: Add `applySessionWeight()` method

```go
func (cr confluenceResult) applySessionWeight(barTime time.Time, cfg AVWAPConfig) float64 {
    if !cfg.SessionWeightEnabled {
        return 1.0
    }
    loc := cachedLocation(cfg.SessionWeightTZ)
    hhmm := barTime.In(loc).Format("15:04")
    switch {
    case hhmm >= "09:30" && hhmm < "10:00":
        return cfg.SessionWeightOpen
    case hhmm >= "10:00" && hhmm < "10:30":
        return cfg.SessionWeightExtendedOpen
    case hhmm >= "10:30" && hhmm < "12:00":
        return cfg.SessionWeightMidMorning
    case hhmm >= "12:00" && hhmm < "14:00":
        return cfg.SessionWeightLunch
    case hhmm >= "14:00" && hhmm < "15:00":
        return cfg.SessionWeightAfternoon
    case hhmm >= "15:00" && hhmm < "15:30":
        return cfg.SessionWeightMOC
    case hhmm >= "15:30" && hhmm < "16:00":
        return cfg.SessionWeightClose
    default:
        return cfg.SessionWeightOutside
    }
}
```

### Step 3: Update all entry call sites (~12 locations)

Find every `conf.applyDPSizing(math.Min(1.0, ...))` pattern in `avwap_v1.go` and insert the session weight multiplier between base strength and DP sizing.

### Step 4: Add signal tags for observability

Add `session_bucket` and `session_mult` to entry signal tags for post-hoc P&L analysis by time bucket.

### Step 5: Backward compatibility

When `session_weight_enabled = true`, bypass the binary `AllowedHoursStart/End` gate (the session weight system subsumes it via `session_weight_outside = 0.0`). When `false`, existing gate remains active.

### Step 6: Gate on strength floor

If `adjustedStrength < 0.10` after session weighting, do not emit the signal (avoids trivially-sized positions during penalized windows).

## Edge Cases

- **Half-day sessions**: Market closes at 13:00 ET. Shift MOC bucket to 12:30-13:00. Requires knowing session end time (already tracked by backtest session).
- **DST transitions**: Handled by IANA timezone + `In(loc)`.
- **Overnight strategy**: Not affected — session weighting is AVWAP-only.
- **Pre/after-hours**: Covered by `session_weight_outside = 0.0`. Users wanting extended hours set this positive.

## Validation Plan

1. **Diagnostic run**: All weights = 1.0, add `session_bucket` tags. Verify zero P&L change from baseline.
2. **Bucket P&L analysis**: Compute per-bucket trade count, win rate, PF, average R from tagged backtest.
3. **Weight calibration**: Set weights as `clamp(bucketPF / overallPF, 0.70, 1.20)`.
4. **Out-of-sample**: 60/40 data split. Train weights on 60%, validate on 40%. If PF improvement < 0.05 OOS, feature is not adding value.

## Files to Modify

- `backend/internal/app/strategy/builtin/avwap_v1.go` — config, strength calculation, signal tags
