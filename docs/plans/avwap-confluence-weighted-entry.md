# Confluence Weighting for AVWAP Entry Scoring

## Context

Currently every qualifying entry fires with a fixed strength score. Per Brian Shannon research, the best trades have multiple confirmation factors converging. Adding confluence weighting will filter low-conviction entries and boost signal strength on high-confluence setups, improving win rate.

## Confluence Factors

| Factor | Weight | Detection |
|--------|--------|-----------|
| **Fibonacci 50%/38.2%** | +3 | AVWAP aligns with Fib retracement (within ATR/2 tolerance) |
| **Key Level alignment** | +3 | AVWAP converges with pd_high, pd_low, or_high, or_low price |
| **Candlestick pattern** | +2 | Inside Bar, Morning Star, or strength candle (first match only) |
| **AVWAP Band zone** | +2 | Price is within the zone between min and max AVWAP lines |

Max score = 10. Min score for entry configurable via `min_confluence_score`.

## Implementation Steps

### Step 1: Add state fields to AVWAPState

- `PrevBars [2]start.Bar` + `PrevBarCount int` — 2-bar lookback for candlestick patterns
- `BarHighs50 []float64` + `BarLows50 []float64` — rolling 50-bar window for Fibonacci computation
- Update OnBar + ReplayOnBar to maintain these
- Add to serialization (avwapStateJSON)

### Step 2: Add KeyLevels pipeline

**session.go:** Add `KeyLevelPrices(symbol, barTime)` method returning `{"pd_high": price, "pd_low": price, "or_high": price, "or_low": price}`.

**runner.go:** Add `keyLevelPricesFn` field. Call on session boundary, store per-symbol. Pass to strategy via `keyLevelSetter` interface.

**avwap_v1.go:** Add `KeyLevels map[string]float64` to AVWAPState. Copy to `entryContext.keyLevels`.

### Step 3: Add config params

```go
MinConfluenceScore        int
FibConfluenceEnabled      bool // default true
KeyLevelConfluenceEnabled bool // default true
CandleConfluenceEnabled   bool // default true
BandConfluenceEnabled     bool // default true
```

### Step 4: Implement `computeConfluence`

```go
type confluenceResult struct {
    Score   int
    Factors []string
}

func computeConfluence(cfg, bar, avwapValue, avwapValues, indicators,
    prevBars, prevBarCount, keyLevels, barHighs50, barLows50) confluenceResult
```

**Fibonacci (+3):** Scan BarHighs50/BarLows50 for swing high/low. Compute 38.2%, 50%, 61.8% levels. Check if AVWAP is within ATR/2 of any level. Skip if < 20 bars.

**Key Level (+3):** Check if AVWAP is within ATR/2 of any key level price.

**Candlestick (+2):** Check Inside Bar (`bar range inside prev bar`), Morning Star (3-bar pattern), or strength candle (`close-open > 60% of range`). First match only.

**Band Zone (+2):** Price between min and max AVWAP values (requires 2+ anchors).

### Step 5: Wire into entry methods

Before each `NewSignal` call in all 7 evaluate methods (~12 insertion points):

```go
conf := computeConfluence(...)
if cfg.MinConfluenceScore > 0 && conf.Score < cfg.MinConfluenceScore {
    continue
}
adjustedStrength := min(1.0, baseStrength + float64(conf.Score)*0.03)
// Add tags: "confluence": "7", "confluence_detail": "fib_50+key_pd_high+inside_bar"
```

### Step 6: TOML config

```toml
min_confluence_score = 0
fib_confluence_enabled = true
key_level_confluence_enabled = true
candle_confluence_enabled = true
band_confluence_enabled = true
```

Start with `min_confluence_score = 0` to collect data. Analyze confluence distributions in backtest, then set threshold (likely 3-5).

## Files

| File | Change |
|------|--------|
| `avwap_v1.go` | PrevBars, BarHighs50/Lows50, KeyLevels, computeConfluence, wire into entries, config parsing, serialization |
| `session.go` | `KeyLevelPrices()` method |
| `runner.go` | keyLevelPricesFn, pass to strategy |
| `avwap_v4.toml` | Confluence config |
| `avwap_v1_test.go` | Tests for each factor + gating + backward compat |

## Verification

1. `go build` + `go test ./internal/app/strategy/builtin/...` passes
2. Run backtest with `min_confluence_score = 0` — check "confluence" and "confluence_detail" in signal tags/logs
3. Analyze confluence score distribution — what % of winners had score >= 5 vs losers
4. Set `min_confluence_score = 3` — verify fewer but higher-quality entries
5. Compare win rate before/after
