# RTH-only indicators (live + backtest)

## Goal
All indicator state — RSI, MACD, EMA, VWAP, BB, AVWAP, HTF aggregates —
must be computed from regular-trading-hours (09:30-16:00 ET) bars only,
on every code path (live and backtest, runtime and warmup). Pre-market
and after-hours bars must not feed indicators. If more lookback is
needed for warmup or anchor replay, pull additional **prior RTH days**,
not extended-hours bars from the same day.

Strategy entry/exit invocation is out of scope: strategies may continue
to receive pre-market bars (the existing `hours` gate blocks pre-market
entries; exits use raw `bar.Close` and don't read indicators). This plan
fixes only what feeds indicators.

## Root cause (verified)
- `backend/internal/app/warmup/loader.go:64` filters pre-market via
  `filterRTH` when `spec.RTHFilter && tf in {1m, 5m}`. Equity warmup
  uses `RTHFilter: true`. **Warmup is RTH-only.**
- `backend/internal/app/monitor/indicators.go:219` (`IndicatorCalculator.
  Update`) has **no RTH gate**. Updates RSI/MACD/EMA/VWAP/BB on every bar.
- `backend/internal/domain/strategy/anchored_vwap.go:135`
  (`AnchoredVWAPCalc.Update`) has a per-anchor `RTHOnly` flag at line
  167. `avwap_v1.go:962` sets `RTHOnly = true` only for `pd_high` /
  `pd_low`. **`session_open` anchor accumulates pre-market**.
- `backend/internal/app/monitor/service.go:875` and
  `backend/internal/app/strategy/runner.go:1284` push raw 1m bars into
  HTF `BarAggregator` instances. **5m/15m/1h HTF bars include
  pre-market 1m data.**

Mismatch in practice:
- Live: IBKR's bar feed is sparse pre-market on most symbols (no
  trades = no bar), so indicators *incidentally* skip pre-market most
  days. On high-pre-market-activity days (earnings, halts), the
  calculator does eat pre-market bars — silently violating the warmup-
  time RTH design.
- Backtest: `market_bars` is grid-filled by Alpaca SIP; every
  pre/post-market minute has a row. Calculator eats all of them. Result:
  backtest indicator state at 09:30 reflects ~5 hours of pre-market
  accumulation that warmup spec said should not exist.

This is the dominant source of indicator-state divergence between live
and backtest, and the proximate cause of the trade-list divergence
observed today (1 of 4 live trades has a backtest analog after pre-
market is removed; 0 of 4 confidently match before).

## Approach: gate at the indicator boundary, not at the bar feed
Single semantic change: when an equity 1m bar arrives at any indicator
or HTF-aggregator update site, skip the update if the bar is outside
RTH. The bar still flows to the strategy runner (so the existing
`hours`-class block continues to fire and exit-monitor logic still
runs); only indicator state is gated.

Why not gate at the bar-event boundary (the `--rth-only-eval`
approach)? Because exit monitoring needs to run on pre-market bars to
fire stops on overnight positions, and that path uses raw bar prices
not indicators. Gating at the indicator layer surgically corrects the
state-consistency bug without affecting exit responsiveness.

### Edits

1. `monitor/indicators.go` — `IndicatorCalculator.Update`
   At function entry, skip the update for equity bars outside RTH.
   Crypto bypasses the gate (existing `IsCryptoSymbol()` check).
   Returns the cached snapshot from the last RTH update (so callers
   still get a valid snapshot during pre-market — strategies that
   read indicator state during pre-market see the previous RTH close
   state, which matches live's incidental behavior).

2. `domain/strategy/anchored_vwap.go` — promote per-anchor `RTHOnly`
   to a calc-level invariant for equity. Either:
   - set `RTHOnly = true` for `session_open` in `avwap_v1.go` when the
     symbol is non-crypto (the smaller change), OR
   - add a calc-level `EquityRTHOnly` flag set during `Init` that gates
     `Update` independently of per-anchor flags (cleaner long-term).

   Pick the smaller change unless review says otherwise.

3. `monitor/service.go` and `strategy/runner.go` — HTF aggregator push
   sites (`agg.Push(bar)` at `service.go:875`, `service.go:1284`,
   `runner.go:1284`, `runner.go:1368`). Skip the push for equity bars
   outside RTH. The aggregator's 5m/15m/1h close events therefore
   contain only RTH 1m data.

4. `cmd/omo-replay/main.go` — remove `--rth-only-eval` flag and the
   two filter call sites added in commit `<prior-fix>`. The flag is
   redundant once the indicator-layer gate lands. Removing the flag
   keeps the bar feed unchanged (strategy runner still sees pre-market
   bars and emits `hours` blocks, matching live's decision-distribution
   shape post-fix).

5. `internal/app/warmup/loader.go` — already exports `IsRTH` (from
   prior commit). No change needed.

## Files
- `backend/internal/app/monitor/indicators.go`
- `backend/internal/app/monitor/service.go`
- `backend/internal/app/strategy/runner.go`
- `backend/internal/domain/strategy/anchored_vwap.go`
- `backend/internal/app/strategy/builtin/avwap_v1.go`
- `backend/cmd/omo-replay/main.go` (removal)

Estimated diff: ~80-120 LOC across 6 files.

## Verification

1. **Build + unit tests**: `go build ./... && go test ./internal/app/...`
   pass.

2. **AVWAP session_open snapshot diff**: pick AAPL on 2026-04-29.
   Compute `avwapState.anchors.session_open.vwap` at the 09:30 RTH bar
   on:
   - Pre-fix backtest: includes pre-market accumulation.
   - Post-fix backtest: equals AAPL's 09:30 typical price (one bar's
     worth, since session_open anchor time = 09:30).
   The post-fix value must equal what a hand-computed RTH-only VWAP
   would produce.

3. **Indicator value diff at 09:30**: pick AAPL. Pre-fix vs post-fix
   backtest RSI / EMA21 / MACDLine values at the 09:30 ET bar:
   - Pre-fix: reflects ~5h pre-market data + warmup.
   - Post-fix: equals warmup-final state on the prior RTH close
     (no intra-day pre-market accumulation).
   The post-fix values must match what live computes on the same
   symbol at the same 09:30 bar (within float tolerance, given live
   sees few or no pre-market bars for AAPL).

4. **Trade-list convergence (today, 2026-04-29)**: re-run full-day
   backtest, compare trades to live's 4 (QQQ@09:55, OXY@12:00,
   MRVL@14:20, SNOW@15:45). Pass if at least 2 of 4 live trades have
   a backtest analog: same underlying, same direction (CALL/PUT),
   entry within ±15 minutes. Strike match not required (option-chain
   divergence is a separate plan).

5. **Live regression check**: query live's reason-class distribution
   for `2026-04-30` (post-deploy) and compare to today's pre-deploy
   distribution. Each class within ±5%. Hours-class count specifically
   may shift modestly because live's runtime indicator state will now
   match warmup's design intent on high-pre-market days, but most days
   should be near-identity.

6. **Backtest reason-class distribution**: hours-class count
   approximately unchanged from current baseline (1898) because the
   strategy runner still receives pre-market bars and the hours gate
   still fires. The interesting change is in slope/confluence/
   entry_specific distributions, which should now reflect indicator
   state computed without pre-market pollution. No specific numeric
   target — these are observational.

## Risks / blast radius

- **Live behavior change on high-pre-market days**: indicators that
  previously absorbed pre-market activity now ignore it. Stop/limit
  orders triggered by indicator-derived levels may fire at slightly
  different prices on those days. Risk is small (most days have
  negligible pre-market on the trading universe) and the change moves
  toward the documented warmup-time design intent.
- **HTF aggregator gap**: 5m bars built from RTH-only 1m data have
  potentially fewer 1m components on early-RTH bars (e.g. 09:30-09:34
  builds the first 5m from 5 RTH 1m bars instead of 5 1m bars that
  could include 09:25-09:29 pre-market). The current behavior has the
  inverse mistake — pre-market 1m bars contaminate HTF aggregation.
  Post-fix is correct per design.
- **Cached snapshot during pre-market**: `IndicatorCalculator.Update`
  must return a valid snapshot for pre-market bars (last RTH state)
  so callers don't crash on missing data. Implementation must store
  and return the prior RTH snapshot; do not return zero values.
- **AnchoredVWAPCalc state during pre-market**: the per-anchor `active`
  flag may need adjustment. Currently a pre-market bar sets `e.active
  = true` once `barTime >= AnchorTime` even on RTH-only anchors
  (line 147-149). With session_open RTH-only, the anchor's `active`
  flag should still flip true exactly at 09:30, not earlier. Verify
  this in the unit test.
- **Holiday / early-close days**: `warmup.IsRTH` already uses
  `domain.NYSECloseTime` (early-close Fridays end at 13:00). Inherit
  this behavior.

## Halt conditions
- Live reason-class distribution shifts > 5% on the post-deploy day
  for any single class — investigate before continuing.
- Trade-list convergence below 1 of 4 live trades — investigate (the
  current `--rth-only-eval` baseline already produces 1 of 4; this
  plan's surgical fix should match or exceed that).
- AVWAP session_open snapshot diff at 09:30 doesn't match hand
  computation — implementation bug; halt and re-investigate.

## Out of scope
- Option-chain divergence (live IBKR vs DoltHub historical).
- SimBroker fill model differences.
- Late-session 16 ET hours-block delta (live blocks 250, backtest
  blocks 31 in same window) — separate phenomenon, separate plan.
- The `hours` reason-class count target. Pre-market bars still reach
  the strategy runner and emit hours blocks; that distribution is not
  a goal of this plan.

## Reference data
- Today's verification baseline: live untagged rows + backtest run
  `0bfa6d56-ddd5-5cd9-a611-fcdf69dc24f0` for 2026-04-29.
- Prior plan PR #20 (parity-fix) and the in-progress trade-convergence
  plan `_workspace/parity_trade_convergence_plan.md` (this plan
  supersedes Phase 1 of that plan with a more accurate root-cause
  diagnosis).
