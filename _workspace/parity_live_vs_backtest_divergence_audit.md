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

## Post-fix verification (2026-04-30 17:30 UTC)

PR #27 ([`fix(parity): drop live-only ResolveAnchorsForWarmup`](https://github.com/ridopark/oh-my-opentrade/pull/27))
landed the fix at `cmd/omo-core/warmup.go:416`. PR #28
([`feat(parity): emit_gated_diag flag for HTTP backtest`](https://github.com/ridopark/oh-my-opentrade/pull/28))
unblocked the verification path by wiring `EntryGatedWriter` into
`backtest.Runner.Run()` when `RunConfig.EmitGatedDiag = true`.

Both deployed via `/rebuild-commit-restart` at 12:18 CDT and 12:30 CDT
respectively. A backtest of today's date (`bt-eb813ee1ad12ae22`,
`emit_gated_diag = true`, full 34-symbol universe, both strategies)
ran successfully and persisted 2,809 rows to `strategy_signal_events`.

Bar-keyed parity SQL on POST-FIX rows only (live `ts >= 17:18 UTC`,
backtest = `bt-eb813ee1ad12ae22`), 10 symbols × 2 bar.Times each:

| Symbol | bar_time | live_bars | bt_bars | vwap_delta | live_slope | bt_slope |
|--------|----------|----------:|--------:|-----------:|-----------:|---------:|
| AAPL   | 13:15 ET | 409       | 409     | 0.0000     | 0.106727   | 0.106727 |
| AAPL   | 13:20 ET | 414       | 414     | 0.0000     | 0.097817   | 0.097817 |
| AMZN   | 13:15 ET | 551       | 551     | 0.0000     | -0.069800  | -0.069800 |
| AMZN   | 13:20 ET | 556       | 556     | 0.0000     | -0.067721  | -0.067721 |
| GOOGL  | 13:15 ET | 548       | 548     | 0.0000     | 1.409761   | 1.409761 |
| GOOGL  | 13:20 ET | 553       | 553     | 0.0000     | 1.285726   | 1.285726 |
| IWM    | 13:15 ET | 615       | 615     | 0.0000     | 0.074043   | 0.074043 |
| IWM    | 13:20 ET | 620       | 620     | 0.0000     | 0.083116   | 0.083116 |
| META   | 13:15 ET | 264       | 264     | 0.0000     | -0.052521  | -0.052521 |
| META   | 13:20 ET | 269       | 269     | 0.0000     | -0.082263  | -0.082263 |
| MSFT   | 13:15 ET | 590       | 590     | 0.0000     | -0.407287  | -0.407287 |
| MSFT   | 13:20 ET | 595       | 595     | 0.0000     | -0.403768  | -0.403768 |
| NVDA   | 13:15 ET | 615       | 615     | 0.0000     | -0.314709  | -0.314709 |
| NVDA   | 13:20 ET | 620       | 620     | 0.0000     | -0.286324  | -0.286324 |
| QQQ    | 13:15 ET | 271       | 271     | 0.0000     | 0.099447   | 0.099447 |
| QQQ    | 13:20 ET | 276       | 276     | 0.0000     | 0.078632   | 0.078632 |
| SPY    | 13:15 ET | 548       | 548     | 0.0000     | 0.049573   | 0.049573 |
| SPY    | 13:20 ET | 553       | 553     | 0.0000     | 0.047007   | 0.047007 |
| TSLA   | 13:15 ET | 613       | 613     | 0.0000     | 0.083817   | 0.083817 |
| TSLA   | 13:20 ET | 618       | 618     | 0.0000     | 0.086128   | 0.086128 |

**Result: byte-identical pd_high state across all 20 (sym, bar.Time)
pairs.** `live_bars == bt_bars`, `vwap_delta = 0.0000` (six decimals
of agreement), `live_slope == bt_slope` (six decimals).

Pre-fix divergence (800-bar barCount delta, 0.9-3.4% VWAP drift,
3-7x slope drift) is closed. This implies invariance of:
- Bias gate (price vs AVWAP) — same VWAP → same decision.
- Slope gate (`MinSlopeBPS` threshold) — same slope value → same
  pass/fail.
- SD bands — `barCount`, `M2`, `vwapCount` all identical → same
  readiness, same band offsets.
- Confluence-via-AVWAP — score components that read AVWAP state
  produce identical outputs.

The reference query for any future regression check:

```sql
WITH classified AS (
  SELECT
    e.symbol,
    e.payload->'bar'->>'time' AS bar_time,
    CASE WHEN e.payload->>'tag' LIKE 'backtest_%' THEN 'backtest' ELSE 'live' END AS env,
    (e.payload->'avwapState'->'anchors'->'pd_high'->>'slopeBPS')::numeric AS slope_bps,
    (e.payload->'avwapState'->'anchors'->'pd_high'->>'vwap')::numeric    AS vwap,
    (e.payload->'avwapState'->'anchors'->'pd_high'->>'barCount')::int    AS bars
  FROM strategy_signal_events e
  WHERE e.payload->'avwapState'->'anchors'->'pd_high' IS NOT NULL
    AND e.payload->'bar'->>'time' >= '<window-start>'
    AND e.payload->'bar'->>'time' <  '<window-end>'
    AND (
      (e.payload->>'tag' IS NULL AND e.ts >= '<live-restart-utc>')
      OR e.payload->>'tag' = 'backtest_<bt-id>'
    )
)
SELECT
  symbol, bar_time,
  MAX(CASE WHEN env='live' THEN bars END) AS live_bars,
  MAX(CASE WHEN env='backtest' THEN bars END) AS bt_bars,
  ROUND(MAX(CASE WHEN env='live' THEN vwap END) - MAX(CASE WHEN env='backtest' THEN vwap END), 4) AS vwap_delta,
  ROUND(MAX(CASE WHEN env='live' THEN slope_bps END), 6) AS live_slope,
  ROUND(MAX(CASE WHEN env='backtest' THEN slope_bps END), 6) AS bt_slope
FROM classified
GROUP BY symbol, bar_time
HAVING COUNT(DISTINCT env) = 2
ORDER BY symbol, bar_time;
```

Day +1 cron at `08:35 CDT 2026-05-01` (= `13:35 UTC`) will post a
Discord notification with AAPL `pd_high.barCount` at first 5m close,
classified green/yellow/red against the same thresholds. Expected:
PASS, `barCount` in `[200, 410]` depending on Apr 30's HighTime
placement.

---

## HIGH severity (change gate decisions)

### H1. `ResolveAnchorsForWarmup` is live-only ✅ FIXED (PR #27)

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

### H4. EntryGatedWriter is NEVER subscribed in the in-process HTTP backtest ✅ FIXED (PR #28, opt-in via `emit_gated_diag` flag)

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

---

## Addendum: post-fix re-audit (2026-04-30, parallel sub-agent verification)

PR #27 (`fc9d4f80`, dropped the live-only `ResolveAnchorsForWarmup` at
`warmup.go:416`) and PR #28 (`59d63236`, wired `EntryGatedWriter` in
the HTTP backtest path conditional on `RunConfig.EmitGatedDiag`) closed
H1 and H4 respectively. Branch `parity-audit-postfix-update` carries
the empirical follow-up showing byte-identical `pd_high` state across
20 (sym, bar.Time) pairs at 13:15 ET / 13:20 ET on 2026-04-30. Four
parallel sub-agents re-read the codebase against this document on that
branch and returned the additions below. The original audit holds on
every claim it makes against the unfixed items. The findings are: one
new issue (default-knob mismatch between two backtest paths), three
reclassifications, and four gaps in the post-fix verification that
need closing before the parity claim is settled.

### Reclassifications

- **M3 -> HIGH.** Bridge warmup at `backtest/runner.go:1000` and
  `omo-replay/main.go:985` feeds 50 bars through `monitor.WarmUp`,
  then the runtime loop re-processes the same bars via
  `HandleMarketBar` -> `s.calculator.Update`.
  `IndicatorCalculator.Update` keys only on `(Symbol, Timeframe)` and
  has no `bar.Time` dedup, so the bridge bars feed indicators twice.
  Diverges from live (no bridge) and from the intended single-pass
  invariant. Tracked in #30.

- **M9 -> HIGH.** `omo-replay` never calls `SetDarkPoolLookup` /
  `SetWhaleLookup`. Confluence scoring runs without DP/whale signals
  in replay mode while backtest uses static maps keyed by exact
  `(symbol, time.UTC())` and live uses streaming with grace windows.
  Three-way asymmetry across paths that share the same scoring code.
  Tracked in #31.

- **M8 -> LOW.** The audit names `risk_sizer` and confluence-multiplier
  branches that key on `DisableAI` / `AIEnabled` beyond the enricher.
  Sub-agent grep finds no such branches. Only
  `signal_debate_enricher.go` (`WithSkipAI`), `instance.go:395`
  (payload stamp), and `ai_scalping_v1.go:266,295` (strategy-level AI
  gating) actually branch. M8 collapses into H6 -- there is no
  separate AI surface.

### New finding: HTTP backtest vs omo-replay disagree on AI default

The two backtest paths default `DisableAI` differently:

- HTTP `POST /backtest/run` (`http/backtest_handler.go:31`):
  `NoAI bool` zero-value `false` -> AI ON by default.
- `omo-replay` (`cmd/omo-replay/main.go:81`): `--no-ai` defaults to
  `true` -> AI OFF by default.

A user comparing measurements collected via HTTP backtest against
measurements collected via omo-replay is silently comparing AI-on
output against AI-off output. The original audit treats backtest as
uniformly `--no-ai=true` (per the v2 plan note); that is true for
omo-replay only. This is a Phase 2 measurement blocker until the two
paths align.

### Verification-table gaps (post-fix table on `parity-audit-postfix-update`)

The 20-row "byte-identical" table demonstrates parity for a narrow
slice of state space. The slices it does not cover:

1. **Anchors.** Only `pd_high` is checked. Production runtime anchor
   set is `{session_open, pd_high, pd_low}` (`services.go:631`,
   `monitor/service.go:80`). `pd_low` was in the original empirical
   divergence (lines 19-21 of this doc: AMZN/GOOGL/IWM `pd_low` slope
   drift) and has no post-fix datapoint.
2. **Time window.** All rows are 13:15 ET / 13:20 ET, the first 10
   minutes of RTH. Pre-fix divergence was sampled at 15:20 ET, 800
   bars after RTH open. Late-session parity is unverified.
3. **Symbols.** 10 of 34 universe symbols. Mid-cap names where
   session-data quality is lower on either side could expose H2
   (SessionRefresher asymmetry) that mega-caps mask.
4. **Precision.** `vwap_delta = 0.0000` is rounded to 4 decimals, i.e.
   parity-to-1bp on a $250 price (~$0.025 of slack), not
   byte-identical.

Closing those gaps takes four edits to the reference SQL: loop the
JSON path over `('pd_high','pd_low','session_open')`, widen the
bar-time window to `[14:30, 16:00)`, drop the symbol filter (let
`HAVING COUNT(DISTINCT env) = 2` discover symbols), and round
`vwap_delta` to 6 decimals. Tracked in #32.

### Day +1 cron caveat

The cron at `08:35 CDT 2026-05-01` referenced in the post-fix section
is `scripts/verify-cross-day-fix.sh`, originally scheduled for PR #24
(cross-day-state fix). Its threshold logic happens to coincide with
PR #27's expected effect because both fixes target `pd_high.barCount`
inflation, but it was not reauthored for PR #27. The script also only
checks AAPL `pd_high` -- same anchor and same-symbol-only gaps as the
verification table. Tomorrow's signal is useful but narrow; the
broader regression query in #32 is the durable check.

### H1 fix-option (a) follow-up gap

The chosen fix relies on the runtime rollover at
`runner.go:1483-1485` (`if r.lastSessionDate[symbol] != barDate`)
firing on bar #1. For mid-session boots where
`lastSessionDate[symbol]` is already populated to today's date by
some other path, that branch will not fire and the seeding falls
through to the `hasMissingAnchor` safety net at `:1486`. There is no
unit test exercising the mid-session boot scenario. Add one before
the next mid-session restart.

### Follow-up issues opened

- #30 -- M3: bridge warmup 50-bar double-feed in backtest/omo-replay
- #31 -- M9: omo-replay does not seed DP/whale, three-way confluence
  asymmetry across live/backtest/omo-replay
- #32 -- broaden post-fix parity SQL to cover all anchors, late
  session, full universe, and 6-decimal precision

## #46 EMA50 / MACD precision drift -- CLOSED 2026-05-01

The EMA50/MACD drift between live and backtest reported in #46 is
closed by the IndicatorCalculator unification migration (PRs 1-7
merged 2026-05-01, PR 8 ships parity contract tests and this note).

Structural root cause: the codebase carried three independently-warmed
`monitor.IndicatorCalculator` instances (live monitor `L1`, runner
warmup boot `L2`, runner `htfCalcs` `L3`; mirrored as `B1`/`B2`/`B3`
in backtest). Each was fed by a different warmup path, so
`B1.states[(sym,5m)].ema50 != L1.states[(sym,5m)].ema50` after seed,
with the seed bias decaying at `(1 - 2/51)^N`. The broadened parity
SQL measured ~1.6e-3 EMA50 delta after 50 bars on a $200 stock.

Fix: collapse all three calcs into a single per-context
`indicator.Service` owned alongside `monitor.Service` and constructed
fresh per backtest. Every consumer (monitor enriched-bar emission,
strategy runner HTF gates, bootstrap activator) now reads through one
canonical state map. Bit-equality is verified at the read site (PR 4)
and at the aggregator-write site (PR 6a-2). Operator-side full-RTH
parity backtest across the trading universe is the final acceptance
gate documented in the PR 8 description.

Migration log:

- PR 1 (#58 → 31081a10): introduce `internal/app/indicator/` package
- PR 2 (#59 → 445035f4): wire shadow `indicator.Service` into monitor
- PR 3 (#60 → 370fabd7): unified warmup, delete `L2`/`B2` calcs
- PR 4 (#61 → 4cb5a987): migrate runner `htfCalcs` to indicator
- PR 5 (#62 → da61586e): migrate bootstrap activator closure
- PR 6a-1 (#63 → 22617880): introduce `Subscribe` API + aggregator
- PR 6a-2 (#64 → d2231000): migrate aggregator chains onto Subscribe
- PR 7 (#65 → d92640bd): collapse `monitor.calc` into indicator
- PR 8 (this branch): parity contract tests + audit closure

The structural roadmap and architectural decisions (D1-D5) are at
`_workspace/indicator_calculator_unification_plan.md`.
