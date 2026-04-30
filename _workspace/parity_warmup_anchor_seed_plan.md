# Parity follow-up: drop live-only `ResolveAnchorsForWarmup`

## Goal

Eliminate the live-only `ResolveAnchorsForWarmup` call at
`cmd/omo-core/warmup.go:416` so live's strategy-state seeding flow
matches backtest's flow byte-for-byte. Closes the empirical 800-bar
`pd_high.barCount` delta + 0.9-3.4% VWAP delta + 3-7x slope delta
documented in `_workspace/parity_live_vs_backtest_divergence_audit.md`.
This is a primary trade-list divergence source (slope feeds the
bias/slope gate; differing slope → different gate decisions →
different trades).

## Empirical evidence (verbatim from audit)

Same `(sym, bar.Time)` rows joined via `payload->'bar'->>'time'`:

| Symbol | bar_time | live_slope | bt_slope | live_vwap | bt_vwap | live_bars | bt_bars |
|---|---|---:|---:|---:|---:|---:|---:|
| AAPL | 15:20 ET | 0.0322 | 0.0048 | 267.82 | 270.20 | 1895 | 1095 |
| AMZN | 15:20 ET | 0.333 | 0.098 | 252.92 | 261.45 | 1917 | 1117 |
| GOOGL | 15:20 ET | 0.268 | 0.062 | 341.43 | 350.24 | 1828 | 1028 |
| IWM | 15:20 ET | -0.009 | -0.044 | 271.68 | 272.93 | 1850 | 1050 |

`barCount` runtime increment is identical (5/5m on both sides). The
~800-bar gap is set entirely during warmup.

## Root cause (verbatim from audit H1)

`ResolveAnchorsForWarmup` exists only at `cmd/omo-core/warmup.go:416`
on the live path. Backtest never calls it. The call's effect is to
create AVWAP anchors (pd_high, pd_low, session_open) **before**
`WarmUpTF(HTF)` runs at `warmup.go:471`. With anchors already in
place, every HTF warmup bar that passes through
`safeWarmupOnBar → ReplayOnBar → Calc.Update` increments those
anchors' `barCount` (and `recentVWAPs` ring, and CumPV/CumV/M2,
etc.). Backtest creates anchors *after* all warmup completes, on
the first runtime bar via the `lastSessionDate != barDate` branch
in `handleBar` (`runner.go:1442`).

Net effect: live's HTF warmup feeds ~800 5m/15m/1h bars (subject to
`defaultRequired = emaLongestPeriod * convergenceFactor = 200 * 4`)
into pd_high/pd_low. Backtest feeds zero. Plus live's
`ResolveAnchorsForWarmup` itself triggers `replayBarsForAnchors`
which feeds the prev-day RTH window (~183 bars). Together: ~800-bar
inflation, polluted ring buffer for slope, perturbed CumPV/CumV
ratio for VWAP.

## Approach

**Fix**: remove the live-only `ResolveAnchorsForWarmup` call. Let
the runtime trigger at `runner.go:1442` handle anchor seeding on
the first runtime bar — exactly as backtest does.

```diff
--- a/backend/cmd/omo-core/warmup.go
+++ b/backend/cmd/omo-core/warmup.go
@@ -413,11 +413,6 @@
                allSymStrs[i] = string(s)
        }
-       svc.strategyRunner.ResolveAnchorsForWarmup(allSymStrs, time.Now())
```

After this change, the live warmup pipeline will:
1. `WarmUp(1m)` — populate IndicatorCalculator (EMA/RSI/MACD/ATR
   etc.) and the strategy state's prev-bars / prev-bar-count, but
   feed nothing to pd_high/pd_low because those anchors don't exist
   yet in `Calc`.
2. `InitAggregators(todayOpen)` — set 5m/15m/1h aggregator
   start time.
3. `WarmUpTF(HTF)` — feed HTF bars to `htfCalc` and to the strategy
   instance via `safeWarmupOnBar` → `ReplayOnBar` → `Calc.Update`.
   Same as before, but pd_high/pd_low don't exist yet → those bars
   don't seed CumPV/CumV/M2/recentVWAPs/barCount on those anchors.
4. First live runtime bar arrives → `lastSessionDate[symbol]` is
   empty → `handleBar:1442` triggers `resolveSessionAnchors` →
   `ResetAnchors` (creates anchors) + `replayBarsForAnchors`
   (seeds prev-day window via `UpdateSingleAnchor`).

This matches backtest's flow. Expected post-fix `pd_high.barCount`
at first 5m close: ~188 (183 prev-day RTH replay + 5 runtime),
not 987.

## Files to touch

- `backend/cmd/omo-core/warmup.go` — remove the `ResolveAnchorsForWarmup`
  call at line 416 and the surrounding `allSymStrs` slice prep at
  lines 412-415 (no other consumer).
- (Optional) `backend/internal/app/strategy/runner.go` — keep
  `ResolveAnchorsForWarmup` method as-is so other tooling that may
  depend on it (none found in this repo) doesn't break. Mark with a
  doc comment that it is no longer called from the live boot path.
- `backend/internal/app/strategy/runner_test.go` (or a new file) —
  test pinning that on a fresh runner with a synthetic 1-day warmup
  + first-RTH-bar trigger, pd_high.barCount lands within ~200, not
  ~990.

## Verification

### V1. Build + unit tests

`go build ./... && go test ./...` clean.

### V2. New unit test

`TestRunner_FirstRuntimeBar_AnchorSeedSingleFed`: drives a synthetic
warmup window into a fresh `Runner` with an `anchorResolver` that
returns a known `pd_high.AnchorTime`, then publishes a single first
runtime bar matching today's RTH open. Asserts
`pd_high.barCount <= 200` after the first 5m close — i.e., the
anchor was seeded once via the runtime trigger, not twice via
warmup-time + runtime-time.

### V3. Live↔backtest parity at first 5m close (Day 0 baseline)

After deploying the fix, on a non-holiday weekday boot before
market open, capture for AAPL (or any liquid symbol) at the first
5m close:

```sql
SELECT
  to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') AS ts_utc,
  payload->'bar'->>'time' AS bar_time,
  payload->'avwapState'->'anchors'->'pd_high'->>'vwap'     AS vwap,
  payload->'avwapState'->'anchors'->'pd_high'->>'barCount' AS bars,
  payload->'avwapState'->'anchors'->'pd_high'->>'slopeBPS' AS slope
FROM strategy_signal_events
WHERE symbol = 'AAPL'
  AND ts >= '<deploy-day> 13:34:00+00' AND ts < '<deploy-day> 13:36:00+00'
  AND COALESCE(payload->>'tag','') NOT LIKE 'backtest_%'
ORDER BY ts ASC LIMIT 5;
```

PASS criterion: `bars` between 100 and 250 (one prev-day RTH portion
based on actual prev-day high time). Not 800+.

### V4. Bar-keyed live↔backtest comparison

Re-run the SQL diff from the divergence audit. PASS criterion:
across at least 5 (sym, bar.Time) pairs in the RTH window:

- `barCount` delta `< 50` between live and backtest.
- `vwap` delta `< 0.1%` between live and backtest.
- `slopeBPS` delta `< 50%` between live and backtest.

If the bias/slope gate is the same on both sides, PASS the
trade-decision invariance check.

### V5. Trade-list pairing

Run `scripts/parity_pair_trades.sh --date <deploy-day>` and verify
the pairing rate (paired / (paired + live_only + backtest_only))
improves from today's baseline (~5%) to at least 30% (target 50%
per the v2 plan's Phase 4).

## Risks

- **Mid-session boot**: if omo-core boots at 14:00 ET (mid-RTH), the
  first runtime bar arrives at 14:00 ET. `lastSessionDate[symbol]`
  is empty → runtime trigger fires, creates anchors, replays
  anchor-time → 14:00 ET window. Same as backtest's `cfg.From=14:00`
  scenario. **Should be fine** but needs explicit test.

- **Pre-RTH boot** (today's typical case): boot at 06:30 CDT, first
  runtime bar at 09:30 ET. Runtime trigger fires at 09:30 ET, replays
  prev-day RTH portion. Matches backtest with `cfg.From=09:30 ET`.

- **Symbol rotation post-boot**: if a new symbol joins the universe
  after boot, its `lastSessionDate` is empty → runtime trigger fires
  on its first bar. Correct.

- **Crypto (24/7)**: runtime trigger fires on whichever bar comes
  first; same as live today's path because crypto resolveSessionAnchors
  uses different anchor logic. No regression expected.

- **HTF indicator state**: HTF strategies (avwap_v4 5m, macd_only_v1
  5m) read indicators from `IndicatorData` (EMA/RSI/MACD/ATR computed
  by `runnerWarmupCalc`). Those are unaffected by AVWAP anchor
  creation timing. **No regression on HTF indicator warmup.**

- **avwap_v4 confluence on bar #1**: post-fix, the first runtime 5m
  bar fires `resolveSessionAnchors` *before* the strategy's `OnBar`
  evaluates that bar. Looking at `handleBar`'s sequence
  (`runner.go:1414-1450`): the `lastSessionDate` check + trigger
  runs at line 1442; strategy dispatch runs at line 1631. So
  anchors are seeded BEFORE strategy OnBar fires. **Bar #1's
  AVWAP confluence has correct pd_high state.** Same as backtest.

## Halt conditions

- V2 test fails after fix (anchor seeded twice or zero times) →
  halt; the fix interaction with the runtime trigger is broken.
- V3 measurement shows `bars > 250` on Day 0 → halt; another seeding
  path exists that we missed.
- V4 shows `vwap` delta `> 0.5%` after fix → halt; warmup state is
  still polluted by something else (audit M-tier issues may apply).
- Trade volume drops `> 50%` post-fix vs pre-fix on a same-day
  re-run → halt; the fix is over-restrictive (anchors not seeded
  when they should be on edge cases).

## Out of scope

- H4 (EntryGatedWriter not wired in HTTP backtest) — separate fix,
  needed for verification but not for the underlying state-seed
  divergence.
- H5/H6 (broker port, AI on/off) — independent trade-divergence
  sources; this plan only addresses the warmup-state-seed source.
- M2 (`SeedIndicatorSnapshot` backtest-only) — one-bar transient,
  separate fix.
- M9 (DP/whale lookup seeding) — separate confluence-data fix.

## Reference data

- Divergence audit: `_workspace/parity_live_vs_backtest_divergence_audit.md`
- Empirical SQL output: see "Empirical evidence" above.
- Audit recommendation: option (a) "drop the live `ResolveAnchorsForWarmup`
  call and let the date-rollover branch in `resolveSessionAnchors`
  handle anchor seeding on bar #1 like backtest does. Smallest diff."
- Warmup spec: `backend/internal/app/warmup/spec.go:29-37`
  (`defaultRequired["1m"] = 800`, etc.).
- Cross-references: PR #22 (RTH gate on indicator updates),
  PR #23 (RTH gate on UpdateSingleAnchor), PR #24 (cross-day
  SessionResolver refresh), PR #25 (observability), PR #26
  (Phase 0 instrumentation).
