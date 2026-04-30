# Live vs Backtest Code-Path Divergence Audit

Cataloged divergences between live (`cmd/omo-core`) and backtest
(`internal/app/backtest`, `cmd/omo-replay`) data paths that can change
strategy decisions. Compiled 2026-04-30 from three parallel sub-agent
audits (warmup-state seeding, runtime bar dispatch, config/toggle
wiring). Severity = HIGH/MEDIUM/LOW based on whether the divergence
affects gating decisions on the same `(symbol, bar.Time)` key.

## Empirical evidence (the divergence is real)

Same `(sym, bar.Time)` rows from `strategy_signal_events`, joined by
`payload->'bar'->>'time'`, comparing live vs backtest pd_high state
on AAPL 2026-04-29 RTH window:

| Symbol | bar_time   | live_slope | bt_slope | live_vwap | bt_vwap | live_bars | bt_bars |
|--------|------------|-----------:|---------:|----------:|--------:|----------:|--------:|
| AAPL   | 15:20 ET   | 0.0322     | 0.0048   | 267.82    | 270.20  | 1895      | 1095    |
| AMZN   | 15:20 ET   | 0.333      | 0.098    | 252.92    | 261.45  | 1917      | 1117    |
| GOOGL  | 15:20 ET   | 0.268      | 0.062    | 341.43    | 350.24  | 1828      | 1028    |
| IWM    | 15:20 ET   | -0.009     | -0.044   | 271.68    | 272.93  | 1850      | 1050    |

- **slope_bps differs 3-7x** → bias/slope gate decisions differ → trades differ.
- **VWAP differs 0.9-3.4%** → the bar SETS being fed differ (pure double-feed
  is VWAP-invariant). Shifts bias gate, SD bands, confluence-via-AVWAP.
- **barCount delta ≈ 800 bars** = ~2 full RTH days. Set during warmup;
  runtime increment is identical (5/5m on both).

This is the primary trade-list divergence source — upstream of the
`entry_specific` gate that the original v2 plan targeted as Phase 2.

---

## HIGH severity (change gate decisions)

### H1. `ResolveAnchorsForWarmup` is live-only

Single call site: `backend/cmd/omo-core/warmup.go:416`. Backtest
(`internal/app/backtest/runner.go`) and `omo-replay/main.go` never
invoke it.

The call triggers `replayBarsForAnchors` (`runner.go:358-383`) →
`UpdateCalcAnchor` → `UpdateSingleAnchor`. `UpdateSingleAnchor` has
its own `lastReplayedBarTime` dedup (`anchored_vwap.go:230-233`)
that **does not consult** `Calc.lastBarTime` (which `runner.WarmUp(1m)`
already advanced via `Calc.Update`). Result: the prev-day RTH window
that `WarmUp(1m)` already fed into pd_high/pd_low gets fed AGAIN.

This fully explains the empirical 800-bar barCount delta (one prev-day
RTH ≈ 390 bars; double-fed pd_high+pd_low ≈ 780).

**Trade impact**: pollutes `recentVWAPs` ring buffer (slope), shifts
CumPV/CumV/M2 (VWAP/SD), inflates `CalcBarCount` (stabilization gate),
moves `barCount` past `minBarsForSD` early (SD-band readiness).

### H2. SessionRefresher wired only on live

Live: `cmd/omo-core/services.go:494` calls `SetSessionRefresher`
which routes through `SessionResolver.RefreshIfStale` (PR #24).
Backtest: never called (`internal/app/backtest/runner.go:1140-1144`,
`omo-replay/main.go:1117-1123` only wire `SetAnchorResolver` /
`SetPrevDayBarsFn`).

Backtest is structurally fine on this front (fresh per-run resolver),
but the asymmetric wiring means *both sides resolve anchors from
their own snapshot of `sessions[]`*. If those snapshots differ at
load time, anchor times differ — which produces different
`prevDayBarsFn` ranges → different bar sets → different VWAPs. This
is consistent with the empirical 0.9-3.4% VWAP gap.

### H3. `directDispatch` on monitor: live=false, backtest=true

`backtest/pipeline.go:129` calls `monitor.SetDirectDispatch(true)`.
Live never sets it. Effect (`monitor/service.go:983, 1026, 1278`):
`StateUpdated`, `SetupDetected`, `RegimeShifted` events go into
`pendingStrict`/`pendingStateUpdates` slices for Pipeline to drain
into the runner via typed `HandleStateUpdatedSnap`, **bypassing the
event bus**. Any third-party subscriber to those events
(SSE handlers, dashboard, persistence) is silently mute on backtest.

**Trade impact**: limited if no subscriber outside the runner cares
about those events for decisions. But it breaks the symmetry the
plan assumes when comparing live vs backtest event streams.

### H4. EntryGatedWriter is NEVER subscribed in the in-process HTTP backtest

`cmd/omo-core/services.go:909-911` always subscribes
`EntryGatedWriter.Handle` to `EventEntryGated` (live persists every
EntryGated row). `cmd/omo-replay/main.go:232-235` subscribes ONLY
when `--emit-gated-diag` is passed.

The in-process backtest path (`POST /backtest/run`) at
`internal/app/backtest/runner.go:574+` **never wires EntryGatedWriter
at all**. Explains the empirical observation today: `bt-2d96ed3062430808`
ran successfully but wrote zero rows to `strategy_signal_events` —
the writer wasn't subscribed. **Phase 2 of the v2 plan cannot be
measured against in-process HTTP backtests** until this is fixed
(or runs are routed through omo-replay with `--emit-gated-diag`).

### H5. Broker port: SimBroker (backtest) vs IBKR (live)

`cmd/omo-replay/main.go:330` and `backtest/runner.go:350+` use
`simbroker.New(...)` with `--slippage-bps=5`. Live uses real IBKR or
Alpaca paper. Different fill timing (next-bar vs venue latency),
slippage model, partial-fill semantics, and reject paths.

**Trade impact**: drives `PositionSide` state transitions on
different bars. Affects strategies that read position state in their
gating (avwap's `position` early-gate, MACD's pending-entry timeout).

### H6. AI advisor: live AI-on, backtest `--no-ai=true`

Live: `services.go:453` `DisableAI: !cfg.AI.Enabled` — typically AI
enabled. Backtest entry points: `DisableAI: r.cfg.NoAI` /
`noAIFlag`, historically run with NoAI=true (per
`parity_trade_convergence_plan.md`: "All current evidence is from
`--no-ai=true` runs").

Effect: enricher's `WithSkipAI` (`bootstrap/strategy.go:167`)
short-circuits LLM debate. Stamps `EntryGatedPayload.AIEnabled =
false`. Strategies emit unchanged but downstream confidence /
direction rationale paths differ, and any AI-modulated risk-sizer
branch diverges.

---

## MEDIUM severity (change non-decision telemetry, ordering, or one-bar transients)

### M1. `PrimeAggregators` live-only on mid-session boot

`warmup.go:529` (only fires when boot detects mid-session) pushes
today's pre-boot 1m bars through HTF aggregators without driving
`htfCalc.Update`. Backtest never runs this — bars start at
`cfg.From`. **Affects the SHAPE of the first post-boot 5m HTF candle
on live**, but only on mid-session boots and not the AVWAP path
(AVWAP is 1m-driven).

### M2. `SeedIndicatorSnapshot` backtest-only

`backtest/runner.go:1089-1093` calls
`pipeline.Runner.SeedIndicatorSnapshot(snap)` so HTF static fields
(`DailyATR`, `NR7`, `Bias`) reach strategies on bar #1. No equivalent
in `cmd/omo-core/warmup.go` — live's `r.indicators[symbol]` is
populated by `runnerWarmupSnapshotFn` which uses a fresh
`runnerWarmupCalc` lacking HTF context. **One-bar transient**: live
strategies see `HTF.DailyATR=0` on first runtime bar where backtest
sees populated values.

### M3. Bridge warmup feeds first 50 replay bars through `monitor.WarmUp`

`backtest/runner.go:1000` and `omo-replay/main.go:985`. Live has no
analog — bars 1..N follow runtime path directly. Bridge feeds
`s.calculator.Update` for those 50 bars, then runtime re-processes
the same bars via `HandleMarketBar`, calling `s.calculator.Update`
again. Whether this double-feeds depends on
`IndicatorCalculator.Update`'s dedup (separate from AVWAP's). Worth
verifying.

### M4. Sanitized-handler invocation order

Sharded backtest (`backtest/pipeline.go:184-235`) skips
`publishEnrichedBar` and the async HTF-persistence subscriber that
live wires (`services.go:892, 658`). Same handlers run for the
strategy runner and monitor, but downstream subscribers (chart, SSE)
see different event streams.

### M5. `idemKey` empty in backtest typed path

`monitor.HandleMarketBarTyped` (`service.go:775`) passes `idemKey=""`
to `handleBarCore`. HTF closed-bar republish at line 910 builds keys
like `"-5m-htf-bar"` — collapsed across symbols/bars. Live carries
upstream `IdempotencyKey`. Affects bus dedup; doesn't directly alter
strategy state.

### M6. `ResetAggregators` cadence

Backtest calls `ResetAggregators(dayOpen)` on each new trading day
(`runner.go:1838`, sharded coord 2453). Live calls
`InitAggregators` once at warmup and never `ResetAggregators` —
relies on the aggregator self-rolling on session boundary. Code
paths differ; behavior currently equivalent for single-session
runs.

### M7. `EvalExitRules` timing

Backtest calls `posMonBundle.Service.EvalExitRules(...)` synchronously
after every bar batch + `Flush()`s the bus. Live has no such
bar-aligned exit eval; exits ride the same `EventMarketBarSanitized`
subscription. Affects the ORDER of strategy-emitted vs broker-driven
exits.

### M8. `disableAI`-related risk-sizer branches

Beyond H6, any code that branches on `DisableAI`/`AIEnabled`
(risk_sizer, confluence multipliers, signal-debate-enricher) takes
different paths.

### M9. Dark pool / whale lookup seeding

Live: streaming `DPSource` adapter, time-keyed lookups with grace
window. Backtest: pre-built static map keyed by exact `(sym, t)`
(`backtest/runner.go:741-860`). **Confluence component scoring can
differ** when bar timestamps don't align with map keys. omo-replay
non-backtest path appears not to seed whale/DP at all — confluence
scoring runs without those signals.

### M10. `disableLiveness`

Backtest sets true; live false. Pure telemetry — `liveness.Record*`
atomics skipped. HOLD-reason recording (`recordHoldReason`) is
unconditional, so strategy decisions identical.

### M11. `suppressProgressEvents`

Live: false at runtime (warmup-only true). In-process backtest:
never set, stays false. omo-replay: true post-warmup unless
`--emit-gated-diag`. Effect: when true, `emitEarlyGated` /
`emitMACDEntryGated` / `emitEntryGated` short-circuit before payload
construction; `recordHoldReason` still records. **Telemetry-only**;
strategy decisions unchanged. But omo-replay is the only path that
toggles this asymmetrically vs in-process backtest.

---

## LOW severity (cosmetic / verified equivalent)

- **`ctx.Now()`**: `instCtx.now = bar.Time` on both paths
  (`runner.go:1658, 1817`). Strategy decision context sees `bar.Time`
  on both — equal.
- **Ingestion DB-write skip**: backtest `isBacktest=true` skips
  `SaveMarketBar`, uses `NewBacktestEvent` (deterministic IDs,
  `OccurredAt=bar.Time`). Live uses UUIDs and wall-clock OccurredAt.
  Cosmetic re: AVWAP.
- **Tide tracker SPY/QQQ feed**: identical on both paths.
- **EnvMode**: every entry point passes `EnvModePaper`. No `Live`
  branch is dead-code.
- **`BacktestID` propagation**: stamps telemetry only.
- **AdaptiveFilter**: same `NewAdaptiveFilter(20, 4.0)` with
  `SetPassthrough(true)` on both.
- **`gateHTFEquity` (`runner.go:1685`)**: applied identically on both
  paths post-PR-#22.
- **`PARITY_DIAG_ENABLED`**: env var, default false on both, gates
  pure logging.

---

## Recommended fix sequence

Order by leverage / dependency:

1. **Fix H1 (`ResolveAnchorsForWarmup` double-feed)**. Three options:
   - **(a)** Drop the live-only call at `warmup.go:416` and let the
     date-rollover branch in `resolveSessionAnchors` (which fires on
     first runtime bar, like backtest) handle anchor seeding.
     Smallest diff. Risk: behavior change for mid-session boots
     where the runtime trigger may not fire on the same bar warmup
     ended on. Needs test coverage.
   - **(b)** Make `UpdateSingleAnchor` consult `Calc.lastBarTime` so
     post-`Calc.Update` replays no-op. Low diff, internal to
     `anchored_vwap.go`. Risk: changes replay semantics for callers
     that legitimately want to seed bars older than `lastBarTime`.
   - **(c)** Add a corresponding `ResolveAnchorsForWarmup` call to
     backtest paths after `pipeline.Runner.WarmUp` and before
     `InitAggregators`. Symmetric but doubles the wrong work in
     backtest too.

   Recommend **(a)** with explicit tests for both cold-boot and
   mid-session-boot paths.

2. **Fix H4 (EntryGatedWriter not wired in HTTP backtest)**. Wire it
   in `backtest/runner.go:574+` so in-process HTTP backtests persist
   EntryGated rows. Otherwise Phase 2 measurement is impossible
   without falling back to omo-replay + `--emit-gated-diag`.

3. **Audit M9 (DP / whale lookup seeding)**. Live and backtest must
   agree on confluence inputs. Either streaming-source-equivalent
   for backtest (replay DP bars) or static-map-equivalent for live
   (pre-load DP at boot).

4. **Backfill H5 / H6 awareness in the v2 plan**. The trade-list
   divergence isn't pure indicator drift — broker-fill differences
   and AI-on vs AI-off independently produce different trades.
   Validate with both knobs aligned (live AI-off + backtest with
   same SimBroker config) before claiming Phase 4 success.

5. **H3 monitor `directDispatch` and M4 sanitized-order**: low
   priority — affects telemetry pipelines, not gates. Address when
   parity-observations hypertable lands (separate plan).

## Status of the v2 plan

The v2 plan's Phase 2 (`entry_specific` gate alignment) targets a
downstream symptom of H1+H2. Recommend **PAUSING Phase 2** until H1
is fixed and re-measured. After H1 fix, the entry_specific bucket
distribution should rebalance significantly — perhaps obviating
Phase 2 entirely.

Phase 3 (option-strike selection) remains independent and can ship
alongside or after H1.

## Files referenced

- `backend/cmd/omo-core/warmup.go`
- `backend/cmd/omo-core/services.go`
- `backend/cmd/omo-replay/main.go`
- `backend/internal/app/backtest/runner.go`
- `backend/internal/app/backtest/pipeline.go`
- `backend/internal/app/strategy/runner.go`
- `backend/internal/app/strategy/instance.go`
- `backend/internal/app/strategy/builtin/avwap_v1.go`
- `backend/internal/app/strategy/builtin/macd_v1.go`
- `backend/internal/domain/strategy/anchored_vwap.go`
- `backend/internal/app/monitor/service.go`
- `backend/internal/app/ingestion/service.go`
- `backend/internal/app/bootstrap/strategy.go`
- `backend/internal/app/bootstrap/ingestion.go`
- `backend/internal/observability/parity/parity.go`
- `backend/internal/adapters/eventbus/memory/bus.go`
