# Backtest Session Awareness Plan

**Created:** 2026-03-17
**Status:** Draft
**Goal:** Enable avwap_v2 and break_retest strategies to produce signals during backtesting by properly resolving session-dependent indicators (anchored VWAPs, opening range, previous day levels).

**Depends on:** [backtest-page-plan.md](./backtest-page-plan.md)

---

## Problem Statement

Currently only `ai_scalping_v1` produces trades during backtest because it uses simple indicator thresholds (RSI + Stochastic). The other two strategies fail:

| Strategy | Issue | Root Cause |
|---|---|---|
| `avwap_v2` | Zero signals over 2+ months | Anchored VWAP calculator receives zero-timestamps for anchors like `pd_high`, `pd_low`, `on_high`, `or_high` — VWAP values never computed, breakout/bounce detection finds nothing |
| `break_retest_v1` | Zero signals (rare patterns) | Works architecturally but candle patterns (breakout + retest) are genuinely rare on 5m bars for a single stock. May produce signals with more symbols or longer timeframes. Less critical to fix. |

### Why avwap_v2 Fails

The `AVWAPStrategy.Init()` adds anchor points but only resolves the timestamp for `session_open` (sets it to `ctx.Now()`). All other anchors (`pd_high`, `pd_low`, `on_high`, `on_low`, `or_high`, `or_low`) get **zero timestamps**:

```go
// avwap_v1.go line 238-249
for _, name := range anchorNames {
    var anchorTime time.Time
    if name == "session_open" {
        anchorTime = ctx.Now()  // only this one gets a real time
    }
    calc.AddAnchor(AnchorPoint{Name: name, AnchorTime: anchorTime})  // others get zero time
}
```

The anchored VWAP calculator computes VWAP from the anchor time forward. With zero time, it either computes from the beginning of all data (producing a meaningless average) or produces zero values.

### What the Anchors Need

| Anchor | Meaning | Requires |
|---|---|---|
| `pd_high` | Previous Day High | Time when yesterday's high occurred |
| `pd_low` | Previous Day Low | Time when yesterday's low occurred |
| `on_high` | Overnight High | Time of the high between yesterday's close (16:00 ET) and today's open (9:30 ET) |
| `on_low` | Overnight Low | Time of the low in the overnight session |
| `or_high` | Opening Range High | Time of the high in the first 15-30 minutes of RTH |
| `or_low` | Opening Range Low | Time of the low in the first 15-30 minutes of RTH |
| `session_open` | Today's open price | 9:30 AM ET timestamp |

In live trading, the monitor/warmup code computes these from real-time session tracking. In backtest, we need to pre-compute them from historical data.

---

## How Production Platforms Handle This

### QuantConnect LEAN
- Uses `SetWarmUp()` to pre-load bars before backtest start
- Has `Consolidator` classes that aggregate bars into sessions
- Exchange calendar determines RTH boundaries
- Session VWAP resets automatically at session open

### Backtrader
- Session boundaries defined in data feed configuration
- `sessionstart` and `sessionend` parameters on data feeds
- Indicators auto-reset at session boundaries

### Common Pattern
1. **Pre-compute daily OHLC** for each symbol before replay starts
2. **Detect session boundaries** by comparing bar timestamps against RTH schedule (9:30 AM - 4:00 PM ET)
3. **Resolve anchor times** at the start of each new trading day using the previous day's OHLC
4. **Reset session indicators** (VWAP, opening range) at session open

---

## Implementation Plan

### Phase 1: Pre-compute Session Data

**Effort:** ~1 day

Before the replay loop starts, query daily OHLC for each symbol:

```sql
SELECT DATE(time AT TIME ZONE 'America/New_York') as trading_day,
       MIN(time) FILTER (WHERE close = MAX(close) OVER (PARTITION BY DATE(time AT TIME ZONE 'America/New_York'))) as high_time,
       MAX(close) as day_high,
       MIN(close) as day_low,
       -- ... etc
FROM market_bars
WHERE symbol = $1 AND timeframe = '1m'
  AND time::time >= '14:30:00' AND time::time < '21:00:00'  -- RTH in UTC
GROUP BY trading_day
ORDER BY trading_day;
```

Store as a `map[string]SessionData` keyed by date string:

```go
type SessionData struct {
    Date       time.Time
    Open       float64
    High       float64
    HighTime   time.Time
    Low        float64
    LowTime    time.Time
    Close      float64
    CloseTime  time.Time
    ORHigh     float64  // Opening range high (first 30 min)
    ORHighTime time.Time
    ORLow      float64
    ORLowTime  time.Time
}
```

### Phase 2: Anchor Time Resolution

**Effort:** ~0.5 day

Create an `AnchorResolver` that the strategy runner calls at each new session:

```go
type AnchorResolver struct {
    sessions map[string]SessionData  // date string → session data
    loc      *time.Location
}

func (r *AnchorResolver) ResolveAnchors(currentTime time.Time, anchorNames []string) map[string]time.Time {
    today := currentTime.In(r.loc).Format("2006-01-02")
    yesterday := currentTime.In(r.loc).AddDate(0, 0, -1).Format("2006-01-02")
    
    prevDay := r.sessions[yesterday]
    result := make(map[string]time.Time)
    
    for _, name := range anchorNames {
        switch name {
        case "pd_high":
            result[name] = prevDay.HighTime
        case "pd_low":
            result[name] = prevDay.LowTime
        case "on_high":
            result[name] = r.overnightHighTime(yesterday, today)
        case "or_high":
            result[name] = r.sessions[today].ORHighTime
        case "session_open":
            result[name] = r.sessionOpenTime(today)
        }
    }
    return result
}
```

### Phase 3: Wire Into Strategy Runner

**Effort:** ~0.5 day

The strategy runner's `handleBar` method needs to:
1. Detect session boundary (bar time crosses 9:30 ET for a new day)
2. Call `AnchorResolver.ResolveAnchors()` to get anchor times
3. Re-initialize the AVWAP calculator's anchor points with resolved times
4. Pass the updated anchors to the strategy's `Evaluate` method

This requires adding a hook in the strategy runner or modifying how `AVWAPStrategy.Init` works — allow anchors to be re-resolved on session changes.

### Phase 4: Opening Range Detection

**Effort:** ~0.5 day

During replay, track the first 30 minutes of each RTH session:
- When bar time is between 9:30-10:00 ET, track high/low
- At 10:00 ET, finalize the opening range and set `or_high`/`or_low` anchor times
- The AVWAP strategy can then use these for opening range breakout/bounce detection

### Phase 5: Overnight Session Tracking

**Effort:** ~0.5 day

Track bars between 16:00 ET (previous close) and 9:30 ET (today's open):
- Identify overnight high/low and their timestamps
- Set `on_high`/`on_low` anchor times at session open

---

## Implementation Order

```
Phase 1 (pre-compute) → Phase 2 (resolver) → Phase 3 (wire in) → Phase 4 (OR) → Phase 5 (overnight)
```

All phases are sequential. Phase 1-3 are required for basic avwap functionality. Phases 4-5 add opening range and overnight anchors.

| Phase | Effort | Impact |
|---|---|---|
| Phase 1: Pre-compute session data | ~1 day | Foundation |
| Phase 2: Anchor time resolution | ~0.5 day | Enables pd_high/pd_low anchors |
| Phase 3: Wire into strategy runner | ~0.5 day | avwap_v2 produces signals |
| Phase 4: Opening range detection | ~0.5 day | Enables or_high/or_low anchors |
| Phase 5: Overnight session tracking | ~0.5 day | Enables on_high/on_low anchors |
| **Total** | **~3 days** | |

---

## break_retest_v1 Status

break_retest works architecturally — it receives 5m bars and has regime data. The issue is that its entry conditions (breakout candle + retest + engulfing confirmation) are genuinely rare patterns. With the loosened thresholds, it may produce signals when:
- More symbols are included (larger sample)
- The market has sharper moves (high ATR days)
- Longer backtest periods are used

No code changes needed for break_retest — it's working as designed. The patterns are just rare for NVDA in the Jan-Mar 2026 period.

---

## Interim Workaround

Until session awareness is implemented:
- Use `ai_scalping_v1` and `backtest_test` strategies for backtesting (they work with simple indicator thresholds)
- For avwap testing, use the live paper trading environment where session tracking is fully operational
- Consider adding a "simple VWAP" mode to avwap that uses the standard session VWAP (already computed by the monitor) instead of anchored VWAPs
