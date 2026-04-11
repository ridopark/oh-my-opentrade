# AVWAP v4 Entry Filter Research (2026-04-10)

## Objective

Improve AVWAP v4 entry filtering to reduce losers at the moment of entry,
before they happen. Exit-side optimization is tapped out (any exit rule
that fires before the 10% PREMIUM_TARGET destroys winners, as verified
by the FAST_FAIL_EXIT experiment earlier this session).

## Current state of the strategy

Verified baseline (`configs/backups/avwap_v4.3.0_verified_PF153_DD519.toml`):

| Period | Trades | PF | WR | P&L | DD | Sharpe |
|--------|--------|-----|-----|-----|-----|--------|
| 2025 OOS (calm) | 516 | 1.248 | 62.8% | +$40,098 | 10.01% | 1.70 |
| 2026 IS (volatile) | 162 | 1.529 | 70.4% | +$25,964 | 5.19% | 3.47 |
| 15mo combined | 678 | ~1.32 | 64.5% | +$66,062 | 10.01% | 2.04 |

Edge mechanism: **67% of trades hit PREMIUM_TARGET at 10% with 100% WR**
— that's the entire profit engine. The remaining 33% are time-exited via
STAGNATION_EXIT (90m) or EOD_FLATTEN at a loss.

## Key empirical finding — feature non-stationarity

**No entry feature we can currently compute is stationary between 2025
and 2026.** Everything flips sign between periods:

### Confluence components (cross-year comparison)

| Component | 2025 Net | 2026 Net | Flipped? |
|-----------|----------|----------|----------|
| `dp_sell` | **-$6,732** | **+$7,762** | ✅ |
| `fib_61.8` | -$4,099 | -$5,321 | No (but tiny N) |
| `band_zone` | +$15,529 | +$13,714 | No |
| `strength_candle` | +$8,661 | +$21,487 | No |
| `dp_buy` | +$29,146 | +$13,240 | No |

### Symbol performance

| Symbol | 2025 Net | 2026 Net | Flipped? |
|--------|----------|----------|----------|
| MSFT | -$3,664 | +$2,681 | ✅ |
| OXY | -$2,953 | +$1,872 | ✅ |
| META | -$2,358 | +$1,455 | ✅ |
| AMZN | -$1,647 | +$5,779 | ✅ |
| BA | -$262 | +$5,622 | ✅ |
| CRM | -$6 | +$2,925 | ✅ |
| **AVGO** (2025 winner) | **+$10,760** | **-$3,703** | ✅ |
| **MU** (2025 winner) | **+$7,310** | **-$4,848** | ✅ |

Only **NVDA** is consistently negative across both years (-$2,200 / -$1,685),
but the signal is marginal.

### Filter simulations (dropped-trade P&L)

Every filter tested dropped trades whose net P&L was **positive in BOTH years**:

| Filter | 2025 Dropped P&L | 2026 Dropped P&L |
|--------|------------------|------------------|
| ext >= 0.0 | +$12,875 | +$6,371 |
| ext >= +0.5 ATR | +$12,589 | +$7,223 |
| ext >= +1.0 ATR | +$14,241 | +$9,163 |
| drop pinch setup | +$10,045 | +$21,514 |
| drop breakout setup | +$30,053 | +$4,449 |
| breakout + ext >= 0 | +$22,920 | +$10,820 |

**Conclusion**: the strategy's existing filters (min_confluence=8,
morning window, breakout/pinch logic, capitulation filter) have already
removed the obvious noise. What's left is the irreducible "this was a
good setup that just didn't work" residual. No further filtering based
on the currently-captured features helps.

## Why exit-side optimization doesn't work

**FAST_FAIL_EXIT experiment**: Exit at minute 30 if `premium_mfe_pct ≤ 0`.

| Period | Baseline | With FAST_FAIL | Delta |
|--------|----------|----------------|-------|
| 2026 | +$25,964 | +$20,541 | **-$5,423** |
| 2025 | +$40,098 | +$39,523 | -$575 |

The rule fired a 52% false positive rate in 2025 — killed 35 future
winners that were slow starters. **End-of-trade MFE is a monotonic peak;
the mid-trade MFE at minute 30 does NOT cleanly separate slow-start
winners from doomed never-profitable losers.**

Rule code is preserved in the engine (`ExitRuleFastFail`,
`evaluateFastFail`) for future experimentation with longer windows or
combined MFE+MAE depth criteria.

## Quant analyst's recommended features (ranked)

From the quant-analyst session:

1. **ATR-normalized breakout extension** — `(close - avwap) / atr_14`.
   Sweet spot ~0.4-1.2 ATR above AVWAP. Too close = noise; too far =
   exhaustion. **Verified empirically: NOT a stationary filter in our data.**
2. **Relative volume vs 20-day same-time-of-day average** — filters dead
   breakouts. **Partially already captured via `vol_ratio` tag; Q5 (extreme
   RVOL) is actually LOSING in 2026 — also non-stationary.**
3. **Minute-of-day bucketing** — 09:35-09:40 is variance zone, 09:40-09:50
   is institutional commit, 09:50-10:00 catches failed breakouts. **Added
   as telemetry (`minute_of_day`, `minute_bucket`) in commit b731f94.
   Needs more data to validate.**
4. **VIX level gate** — skip longs when VIX > 25 or 1.5σ above 20d avg.
   **Infrastructure exists (`SetVIXLevel`, `SetVIXThresholds`) but only
   wired to the monitor service's ORB setup detector, not AVWAP.**
5. **SPY/ES alignment at entry** — block longs when SPY is below its
   intraday VWAP. **Infrastructure exists (`market_tide` gate, used by
   orb_break_retest.toml:135) but AVWAP bypasses the monitor gate chain.**
6. **Pre-market gap classification** — >1×ATR gap is mean-reverting,
   <0.3×ATR continuation is high-quality. **Not currently tracked.**
7. **AVWAP slope** — require rising AVWAP. **Already captured as
   `avwap_slope_bps` telemetry.**

## Infrastructure discoveries

### market_tide gate already exists

- Location: `backend/internal/app/gate/monitor_market_tide.go`
- Does: blocks longs when SPY/QQQ deviation below VWAP > neutral band (bps);
  blocks shorts when above.
- Backed by: `IndexTideTracker` maintaining running intraday VWAP for SPY/QQQ.
- Wired in: `bootstrap/ingestion.go:93` — `tracker := gate.NewIndexTideTracker(30)`;
  fed to `gate.GateDeps{TideTracker: tracker}`; installed via
  `monitor.SetTideTracker(tracker)`.
- Used by: `configs/strategies/orb_break_retest.toml:135` —
  `{ name = "market_tide", params = { neutral_band_bps = 10 } }`

**Gap**: The gate chain is invoked from `monitor/service.go:789` in the
**ORB setup detection path** only. AVWAP signals generated in
`avwap_v1.go::evaluateBreakout/Pullback/Pinch/etc` do NOT consult this
gate chain at all. That's why my earlier attempt to add a VIX gate to
AVWAP had zero effect.

### What AVWAP currently has access to at entry time

Via `entryContext` struct:
- `bar start.Bar` — current bar OHLCV
- `avwapValues map[string]float64` — all anchors (session_open, pd_high, pd_low)
- `avwapSlope float64`, `slopeOK bool`
- `keyLevels map[string]float64`
- `regimeTag string`
- `etLocation *time.Location`

Via `s.Indicators` (IndicatorData):
- RSI, ATR, EMA9-200, VWAP (underlying's own, not SPY), BB*, MACD*, ADX
- DPRatio, DPBuyRatio, DPLargePrintPct, DPRatioZScore, DPSupport/Resistance
- AnchorRegimes map
- HTF map

**What's missing for the quant's recommendations**:
- VIX level at entry time
- SPY/QQQ intraday VWAP deviation
- Pre-market range / gap classification
- 20-day same-time-of-day volume baseline (we have 20-bar SMA only)

## Three paths forward

### Path A: SPY tide in AVWAP (ENGINE_CHANGE)

**Goal**: Make AVWAP block longs when SPY is below its intraday VWAP by
more than a neutral band (e.g., 10 bps).

**Implementation**:
1. Add `IndexTideTracker` field to `strategy.Runner`:
   ```go
   type Runner struct {
       // ... existing fields ...
       tideTracker *gate.IndexTideTracker
   }
   ```
2. Wire it in bootstrap:
   ```go
   // In backend/internal/app/bootstrap/strategy.go BuildStrategyPipeline:
   // Optionally accept an IndexTideTracker from deps and plumb to the runner.
   runner.SetTideTracker(deps.TideTracker)
   ```
3. Feed the tracker every SPY/QQQ bar in `handleBar`:
   ```go
   if bar.Symbol == "SPY" || bar.Symbol == "QQQ" {
       r.tideTracker.OnBar(bar)
   }
   ```
4. Surface tide data in `entryContext`:
   ```go
   type entryContext struct {
       // ... existing ...
       spyTideDevBps float64 // 0 = SPY at VWAP, positive = above, negative = below
       spyTideReady  bool
   }
   ```
5. Add gate check in AVWAP `maybeEmit` (or each entry evaluator):
   ```go
   if ec.spyTideReady && ec.spyTideDevBps < -neutralBandBps {
       return nil // block long, SPY weak
   }
   ```
6. Add TOML param: `[params] spy_tide_neutral_band_bps = 10`

**Recommended rollout**:
- **Phase 1**: Telemetry only. Log `spy_tide_dev_bps` on every entry but
  don't gate. Run 2025/2026 backtests. Re-run entry filter analysis.
- **Phase 2**: Only if Phase 1 shows consistent signal (same sign across
  both years), add as a hard gate.

**Risks**:
- Most tractable of the three paths (infrastructure exists, just needs
  plumbing). About 150 lines of Go changes.
- Could overfit if we gate too aggressively on limited data. Telemetry
  first is essential.
- The backtest runner already replays SPY bars (SPY is in the AVWAP
  routing symbols list), so no new data source needed.

### Path B: VIX gate for AVWAP (ENGINE_CHANGE)

**Goal**: Skip AVWAP entries when VIX > 25 (or some percentile threshold).

**Implementation**:
1. The `monitor.Service` already has `SetVIXLevel()` and `vixSkipAbove`
   but scoped to the monitor path. We'd need to either:
   a. Add similar fields to the strategy runner and plumb VIX through
      `entryContext`, or
   b. Expose a shared `VIXProvider` interface that both the monitor and
      strategy runner can query.
2. Load VIX bars in the backtest runner (currently the VIX data loader
   may not exist).
3. Add check in AVWAP: `if ec.vixLevel > cfg.VixSkipAbove { return nil }`.

**Risks**:
- VIX data loading into the backtest is not verified. My earlier test
  adding a VIX gate to AVWAP via gate_chain resulted in identical metrics
  even at `skip_above = 15`, suggesting VIX data isn't loaded.
- Adding VIX to the backtest data path is additional complexity.

**Priority: DEFERRED until Path A is validated.**

### Path C: Pre-market gap classification (ENGINE_CHANGE)

**Goal**: Classify each day's opening as continuation vs fade based on
pre-market range and gap vs previous close.

**Implementation**:
1. Track pre-market bars (04:00-09:30 ET) separately from RTH bars.
2. At session open, compute:
   - `gap_pct = (session_open - prev_close) / prev_close`
   - `premarket_range_atr = premarket_high - premarket_low`
   - Classification: `small_gap_continuation`, `mid_gap_coin_flip`, `large_gap_fade`
3. Surface as telemetry / filter.

**Risks**:
- Requires pre-market data loading (most backtest data is RTH-only).
- Implementation is large — new data source + new feature pipeline.

**Priority: DEFERRED until Path A is validated AND pre-market data
is available.**

## Recommended next steps (in order)

1. **Collect more telemetry data**. The `entryTelemetryTags` helper added
   in commit `b731f94` logs 10+ new fields per entry. After 100+ more
   trades (live or via re-backtest against new data), re-run the entry
   filter analysis with the richer feature set. This might reveal
   combinations that weren't visible before.

2. **Implement Path A Phase 1 (SPY tide telemetry)**. Scope is small,
   infrastructure exists, the quant flagged it as the single best
   remaining entry signal. Log it but don't gate. Estimated effort:
   half-day of Go work.

3. **Validate Path A Phase 1**. If SPY tide deviation shows stationary
   sign (same direction of effect in 2025 and 2026), promote to hard gate
   as Phase 2.

4. **If Path A doesn't show signal**, accept that the AVWAP strategy
   has reached its feature-based entry filtering ceiling. Look for
   improvements elsewhere:
   - Different strategies (orb_break_retest, macd_only_v1) may have
     higher filtering headroom.
   - Multi-strategy portfolio allocation (dynamic sizing based on
     recent strategy performance).
   - Completely different edge (mean reversion, vol harvesting,
     pairs trading).

## What NOT to do

- ❌ Drop symbols based on 2025 performance. They flipped in 2026.
- ❌ Drop confluence components based on 2025 performance. Same issue.
- ❌ Add ATR extension filter as a hard gate. Every threshold tested
  rejects net-positive trades in both years.
- ❌ Tighten stagnation exit. Clips winners before PREMIUM_TARGET.
- ❌ Add premium stop/trail. They compete with PREMIUM_TARGET for the
  same trades and reduce WR.
- ❌ Re-tune exit rules. Exit-side is tapped out.

## References

- Quant-analyst session transcript: (consulted inline in this session)
- FAST_FAIL_EXIT experiment: commit `0e8cb43`
- Entry telemetry: commit `b731f94`
- Verified baseline backup: `configs/backups/avwap_v4.3.0_verified_PF153_DD519.toml`
- AVWAP tuning history: `git log configs/strategies/avwap_v4.toml`
- market_tide gate: `backend/internal/app/gate/monitor_market_tide.go`
- IndexTideTracker: `backend/internal/app/gate/index_tide_tracker.go`
- Strategy runner (where SPY tide would plug in):
  `backend/internal/app/strategy/runner.go`
- AVWAP entry code (where gate check would go):
  `backend/internal/app/strategy/builtin/avwap_v1.go`
