# Backtest vs Live Trading Parity

Purpose: track the investigation of why AVWAP backtest results don't match
live trading, and the staged work required to close the gap.

Owner: ridopark
Started: 2026-04-25
Status: Phases 0/1/2/5b shipped; Phase 6 scaffolding + Phase 4 parts A/C
shipped (data-source-agnostic prep); Phase 3 gated on Monday 2026-04-27
RTH probe; Phase 4 parts B/D/E/F (~250 LOC) ship same-day after the
probe; Phase 5(d) audit deferred until Phase 4 ships.

## Problem statement

On 2026-04-24 with avwap_v4, no_ai=true, same 34 symbols, same params:
- Backtest run after EOD: 17 entries, +$6,036 P&L, PF 2.45
- Live (paper, IBKR): 1 entry (BA call, -$475)

Backtest cannot be trusted as a basis for tuning or shipping decisions until
its outputs predict live behaviour.

## Findings

### Confirmed NOT the cause

AVWAP slope computation. Reproduced AnchoredVWAPCalc.Slope() from
backend/internal/domain/strategy/anchored_vwap.go:244 in Python against the
1m market_bars feed. Live blocked-row slope values match the simulation to
within 0.005 bps for the early afternoon (live -0.05 = sim -0.0517). Both
paths feed 1m bars to the AVWAP calculator via Runner.UpdateAVWAPCalc
(runner.go:633). The earlier "5m vs 1m feed" hypothesis was wrong.

### Confirmed causes, by impact

1. Dark-pool confluence factor is wired only in the backtest path

   SetDarkPoolLookup is called only from internal/app/backtest/runner.go (3
   sites). The live omo-core process never calls it. r.dpLookup is therefore
   empty in live, and the overlay branches at runner.go:918, 1007, 1353,
   1590 are dead. ScoreDarkPool (confluence.go:280) short-circuits to score=0
   when DPRatio<=0, so the +10 dark-pool factor never contributes in live.

   Empirical proof: re-ran 2026-04-24 backtest with dp_confluence_enabled=false.
   - DP enabled: 17 trades, +$6,036, PF 2.454, WR 70.6%, DD 1.20%
   - DP disabled: 7 trades, +$1,967, PF 2.180, WR 71.4%, DD 0.82%
   - 12 of 17 entries depend on DP confluence to fire (JPM, META, PLTR,
     RIVN, TSLA, AFRM, HIMS, COIN put, SNOW, TSLA second leg, COIN call, NET).
   - DP-off path picks up 2 different entries (MRVL, MRNA).

2. AI is force-on in live with LLM provider returning HTTP 402

   omo-core hardcodes DisableAI:false at backend/cmd/omo-core/services.go:420.
   .env has LLM_ENABLED=true which overrides ai.enabled:false in config.yaml
   via config.go:721. OpenRouter returned 402 (out of credits) on every AI
   call we observed in the 2026-04-24 logs (META, QQQ, SPY).

   Effect on signals: when AI errors, signal_debate_enricher.go:329 emits
   Status=EnrichmentError with Confidence=fallbackConfidence (0.65 if no
   news, sig.Strength if news present). The 0.65 sits below dynamic_risk
   thresholds and the aiDirectionMinConfidence=0.5 direction gate. Backtest
   with no_ai=true skips this entirely and emits Confidence=sig.Strength=1.00.

3. omo-core mid-session restart loses in-memory state

   On 2026-04-24 omo-core restarted at 12:56 CDT (13:56 ET). The previous
   instance produced the only live entry of the day (BA at 12:21 ET) before
   we have logs. After the restart, AVWAP calc, inducement detector swing
   buffers, and dp_rolling z-score state were all reset.

### NOT explained yet

DP-off backtest produces 7 entries on 2026-04-24. Live produces 1. There is
a residual 6-entry gap that is not caused by DP wiring.

Single clean diagnostic case: AVGO call entry at 11:54 ET on 2026-04-24.
- DP-off backtest: fired the entry (entry_breakout setup based on bar 15:50-15:55
  UTC, which had high 421.36, low 414.14, volume 751k = 5x normal).
- Live (previous omo-core instance): blocked at 15:54:59 UTC with
  "slope: -0.12 bps < 1.00 min". Same blocked reason for the surrounding bars.

The slope formula is identical between live and backtest. Two hypotheses:
(a) backtest fired via the pinch entry path (pinch_enabled=true) which has
its own gates and may not enforce min_slope_bps the same way as breakout;
the live blocked-row records only one gate failure per bar.
(b) the previous live instance had a 1m bar-feed gap before the 12:56 CDT
restart that left the AVWAP calc state different from backtest's complete
sequence. The volume divergence on the spike bar amplifies any missing-bar
effect.

We cannot distinguish (a) from (b) without per-entry-type evaluation logs.

## Plan

### Phase 0: Tick data logging [SHIPPED]

Shipped at f05aec7b. market_trades hypertable, AsyncTradeWriter, and the
Alpaca WS shim now preserves Exchange/Conditions/Tape on
domain.MarketTrade.

Foundation for the audit, restart-replay, and parity-diagnosis work that
follows. Tick data flows through the live event bus today (EventTradeReceived
in warmup.go:558) but is consumed in-flight by formingbar and runner and
discarded. No table, no rolling archive.

Add a market_trades hypertable + an async writer subscribed to
EventTradeReceived. Pattern: copy the existing async bar_writer (visible at
boot as "async bar writer started"). Per-trade writes are batched and flushed
on a small interval to keep the hot path latency-bound.

Schema sketch:
  market_trades(time, symbol, price, size, exchange, conditions text[],
                tape text, tenant_id, env_mode)
  hypertable on time, partition_by symbol, compression after 1 day,
  retention 30 days (downsampled to existing market_bars and darkpool_bars
  before drop).

Volume estimate: 34 symbols x ~6.5h RTH x ~1.7k trades/min/symbol = roughly
3-5M rows/day. ~250MB/day uncompressed; 25-50MB/day after TimescaleDB
compression. 30-day rolling window fits in ~1-2GB.

Why before everything else:
- Phase 5 (omo-data audit cross-check) becomes a single SQL diff against
  darkpool_bars instead of a REST refetch loop.
- Phase 6 (restart resilience) replays from local storage instead of paying
  REST latency on every boot.
- The Phase 1 AI fix and Phase 2 diagnostic instrumentation produce
  evidence; tick logging is the only way to prove what live actually saw at
  the wire level when those diagnostics fire on a future divergence.

Domain.MarketTrade extension also lands here (Exchange, Conditions, Tape).
The websocket adapter shim at alpaca/websocket.go:510 stops dropping those
fields. This change unblocks Phase 4 (live DP aggregation) but is owned by
this phase since the persistence path needs the same fields.

Estimated scope: 1 migration, ~200 LOC for the writer + adapter shim
plumbing + domain field additions. Pattern lifted from the existing async
bar writer.

### Phase 1: AI enable parity [SHIPPED]

Shipped at 11abff91. services.go now reads `DisableAI: !cfg.AI.Enabled`
so cfg.AI.Enabled is the single switch. Set LLM_ENABLED=false in .env to
match a no_ai=true backtest.

Live and backtest must read AI enable state from the same authoritative
config. Today they don't, which is why we see EnrichmentError + 0.65
fallback confidence in live versus EnrichmentSkipped + 1.00 in backtest
runs with no_ai=true.

Today:
- Live: services.go:420 hardcodes DisableAI: false, so the strategy
  pipeline always runs the LLM enricher regardless of cfg.AI.Enabled.
- Backtest: backtest/runner.go threads req.NoAI from the HTTP request
  body into DisableAI.
- Net: cfg.AI.Enabled in config.yaml has no effect on live's DisableAI;
  only the LLM_ENABLED env var indirectly enables the AI advisor service
  (config.go:721).

Change:
- services.go:420 reads `DisableAI: !cfg.AI.Enabled`. Single source of
  truth for live AI state.
- Backtest behaviour unchanged (still respects per-run no_ai=true override).
- To match a typical no_ai=true backtest in live, set LLM_ENABLED=false in
  .env (or ai.enabled: false in config.yaml). Both paths now produce
  EnrichmentSkipped with confidence = sig.Strength.

Acceptance: a live signal and the corresponding backtest signal on the same
symbol/bar produce the same Status (EnrichmentSkipped/Error/OK) and the
same Confidence value when both run with AI in the same on/off state.
Verify by adding the enrichment Status + Confidence to the diagnostic
payload (Phase 2) and querying for matching pairs.

Estimated scope: ~5 LOC + a doc note in .env.

### Phase 2: Diagnostic instrumentation [SHIPPED]

Shipped across:
- bee2cb75 — per-factor Components on EntryGatedConfluence + AVWAPState
  snapshot (anchors map, slope, bar count, last bar time).
- 22cc5268 — slope-lookback parameterization so the diagnostic slope
  matches the slope used by the gate; AVWAPState is a pointer field so
  MACD blocks (no AnchoredVWAPCalc) emit no avwapState key at all.
- 514533d7 — strategy column written as the spec id (e.g. avwap_v4)
  instead of the engine name (avwap), so SQL diffs join cleanly across
  live and backtest rows.
- 99ee6d56 — AIEnabled bool on EntryGatedPayload, stamped from the
  runner's disableAI flag at the instance-context emit boundary. Both
  bootstrap and the multi-account services.go path now plumb the flag
  in. EntryAttempts was already covered by the existing EntryChecks
  []EntryCheckResult field.

Departed from the original plan: the original spec called for an
Enrichment {Status, Confidence} field on the blocked-row payload. That
turned out to be structurally undefined — EntryGated fires from inside
strategy.Evaluate() before the enricher runs, the AI direction gate runs
post-enrichment in risk_sizer.go and emits OrderIntentRejected (not
EntryGated), and SignalEnriched events already carry Status+Confidence
for emitted signals. The minimum signal needed for blocked-row AI parity
is just the runner's AI mode at evaluation time, which is what AIEnabled
captures.

Acceptance: for any future live blocked row and the corresponding
backtest blocked row on the same symbol/bar, a SQL diff on payload jsonb
fields (confluence.components, avwapState.anchors, aiEnabled) tells us
which gate disagreed and why.

### Phase 3: Verify Alpaca SIP WS delivers TRF prints (Monday open)

Probe binary at _tmp/dp_ws_probe/dp_ws_probe (built, auth verified Saturday).
Run during RTH:
  cd _tmp/dp_ws_probe
  set -a && source ../../.env && set +a
  ./dp_ws_probe SPY 60

Decision rule:
- D >= 35% of WS share volume: proceed with Phase 4 in-process aggregation
  in omo-core (subscribe to EventTradeReceived, run DPAggregator, swap the
  Phase 6 LoggingSink for AddTrade).
- D < 35%: WS strips TRF prints. Escalate to Databento XNAS.BASIC + XNYS.TRADES
  ($825/mo) or Polygon Stocks Advanced ($199-$399/mo). Architecture downstream
  is unchanged; the data source becomes external.

REST baseline for comparison: SPY 09:30-09:35 ET on 2026-04-24 = 46.21% of
share volume came through X="D" via /v2/stocks/SPY/trades?feed=sip.

### Phase 4: Live DP aggregation in omo-core [PARTS A AND C SHIPPED]

Parts A and C of the integration plan landed ahead of the Monday probe —
both are data-source-agnostic and unlock Phase 4 to be a thin wiring
change once the WS feed is confirmed.

(A) Aggregator refactor — SHIPPED.
- backfill.DPAggregator now holds a sync.Mutex; AddTrade and Flush/
  FlushClosed are concurrency-safe, so the live event-bus subscriber
  goroutine and the 1-minute flush ticker can share one aggregator
  per symbol.
- New FlushClosed(now) []DarkPoolBar emits and removes only buckets
  whose 5m window has ended on or before now.Truncate(5m); the
  in-flight bucket stays in memory so subsequent ticker calls don't
  double-emit it. Existing Flush() unchanged for batch callers.
- Test coverage: 4 new cases including a concurrent stress test
  (8 writers x 125 trades + ticker flusher, race detector clean over
  5 runs) that asserts no lost or double-counted volume.

(C) Runner DPSource interface — SHIPPED.
- New backend/internal/app/strategy/dp_source.go defines the DPSource
  interface ({Lookup(sym, t) (DarkPoolBar, bool); HasData() bool})
  with two concrete impls: staticDPSource for the backtest path and
  noopDPSource as the always-non-nil default.
- Runner.dpLookup field replaced with Runner.dpSource. NewRunner
  installs noopDPSource so the field is never nil.
- SetDarkPoolLookup(map) preserved as a thin wrapper that builds a
  staticDPSource — backtest call sites unchanged.
- SetDarkPoolSource(DPSource) added for Phase 4: livedarkpool.Service
  will satisfy DPSource and plug in via this setter without going
  through the legacy map shape. nil fallback installs noopDPSource.
- All 5 r.dpLookup access sites in runner.go (handleStateUpdated DP
  overlay block, late-session Z-score block, the holiday-skip probe,
  the late-buy/sell aggregation, the handleBar HTF DP overlay, the
  HTF S/R level scan, and the closed-HTF-bar overlay) now go through
  r.dpSource.Lookup. The 4 outer guards switched from
  `len(r.dpLookup) > 0` to `r.dpSource.HasData()` — semantics
  identical for backtest, false for live pre-Phase-4 so all DP
  overlay blocks short-circuit cleanly.
- Test coverage: 8 unit tests covering noop default, static lookup
  hit/miss, HasData semantics for empty/nil maps, SetDarkPoolLookup
  wrapping, last-write-wins between the two setters, nil-source
  fallback to noop.

What still needs to be built (gated on Monday's WS probe):

(B) New package backend/internal/app/livedarkpool with a Service that
    subscribes to EventTradeReceived, dispatches each tick to a
    per-symbol *backfill.DPAggregator, runs a 1-minute ticker that
    calls FlushClosed and persists/publishes the resulting bars, and
    exposes Lookup as a DPSource for the runner. The aggregator is
    already concurrency-safe (Part A); the Service is essentially a
    map[Symbol]*DPAggregator with a goroutine pool around it.

(D) Sink swap in warmup.go — replace tradereplay.LoggingSink() with
    a closure that calls livedarkpool.Service.AddTrade(...). One-line
    change once Service exists.

(E) Config flag cfg.LiveDarkPoolEnabled. Service is always constructed
    so /metrics gauges exist; subscription, ticker, and SetDarkPool-
    Source are gated on the flag.

(F) Persistence reconciliation: SaveDarkPoolBars upsert semantics must
    match omo-data's backfill so Phase 5(d) audit's SQL diff doesn't
    surface spurious drift. omo-data already uses ON CONFLICT DO
    UPDATE on (symbol, time); the live writer just needs to use the
    same DarkPoolRepo.

Decision unchanged from the previous update: ship Phase 4 prep before
Monday or wait for the probe. With (A) and (C) now in, the remaining
work (B/D/E/F) is ~150-200 LOC — it can land Monday morning in time
for the open. Recommended: wait for the probe, ship same-day.

Background context (still applicable):
- DP factor is load-bearing (12 of 17 entries on 2026-04-24 depend on it),
  the data is already on the WS we pay for, omo-data's DP backfill
  already has the aggregator we need (now concurrency-safe per Part A).
- Foundation already in place: domain.MarketTrade carries Exchange/
  Conditions/Tape (Phase 0); WS shim preserves those fields (Phase 0);
  AsyncTradeWriter persists every tick (Phase 0); tradereplay.Service
  reads market_trades and feeds a Sink (Phase 6).

Remaining work for Phase 4 (B/D/E/F):

(B) backend/internal/app/livedarkpool.Service:
    map[Symbol]*backfill.DPAggregator with lazy per-symbol init.
    Subscribes to EventTradeReceived, dispatches each trade to the
    right aggregator's AddTrade. 1-minute ticker calls FlushClosed
    per aggregator and for each emitted DarkPoolBar: publishes a
    DarkPoolBarReady event + upserts via DarkPoolRepo.SaveDarkPoolBars.
    Implements DPSource so it plugs straight into the runner via
    SetDarkPoolSource (Part C). Off-path when cfg.LiveDarkPoolEnabled
    is false — Service is still constructed so /metrics gauges exist,
    but doesn't subscribe and Lookup returns false.
    Estimated scope: ~250 LOC + ~150 LOC tests.

(D) Sink swap in warmup.go: replace tradereplay.LoggingSink() with a
    closure that calls livedarkpool.Service.AddTrade(ctx, trade). Boot
    replay rebuilds the in-memory aggregator state from market_trades
    rows since session_open. ~5 LOC change.

(E) Config flag cfg.LiveDarkPoolEnabled (default false). Subscription,
    ticker, and SetDarkPoolSource gated on the flag.

(F) Persistence reconciliation: live writes use the same ON CONFLICT
    DO UPDATE on (symbol, time) that omo-data's backfill already uses,
    so the 5(d) audit diff doesn't surface spurious drift. Existing
    DarkPoolRepo.SaveDarkPoolBars already does this — no schema work.

Trade-condition exclusion (Form-T late prints with X="D" miscounted as
fresh DP) is the one open behavioral gap. Today's backfill.DPAggregator
filters only on `exchange == "D"`. Phase 4 (B) should port the SIP
condition allowlist from omo-data's backfill before subscribing the
live event bus — otherwise the audit (5d) will alert on every late-
print bucket. Tracked as a sub-task of (B).

Risks remaining:
- WS may strip Exchange field. Mitigated: Phase 3 probe Monday;
  architecture downstream is unchanged regardless of source — Part C's
  DPSource hides the source choice from the runner entirely.
- Aggregator state for the partial bucket is reset on every omo-core
  restart. Mitigated by the boot replay (D) once the sink is swapped.
- Thread safety: handled in Part A — DPAggregator now has a sync.Mutex
  and is race-detector-clean under stress.

Decision: with Parts A and C in, the remaining work is small and
data-source-agnostic except for the WS subscription branch in (B).
Ship B/D/E/F same-day Monday after the probe, picking the right WS
source based on the probe result.

### Phase 5: omo-data role change

Decision: omo-data does NOT retire. Its job shifts from "always re-aggregate"
to "fill the gaps that omo-core's WS pipeline missed". Four sub-tracks; (a)
was already the case before this plan, (b) shipped this session, (c) is
unchanged, (d) is deferred.

(a) DP gap-fill — already covered by datarefresh.refreshDarkPoolBars. No
    change needed.

(b) Intraday market_bars gap-fill — SHIPPED at fc237d36.
    - gapdetect.BarBackfiller interface + concrete IntradayBarBackfiller
      that wraps RoutingFetcher.GetHistoricalBars (Coinbase for crypto,
      Alpaca for equity) and Repository.SaveMarketBars. Idempotent via
      the existing upsert. 1d timeframe short-circuits (owned by
      datarefresh).
    - gapdetect.Service.SetBackfiller hook is nil-safe; absence keeps
      the detect-only behavior for any other consumer of the package.
    - omo-data/main.go runs the scan every 6h via a new runEvery helper
      (generalization of runDaily). 6h is short enough that early-session
      outages get filled before the next trading day's backtests run,
      long enough to amortize Alpaca REST cost across the universe.
    - Prom counters: omo_gapfill_attempts_total{symbol,timeframe,
      outcome=ok|fetch_error|save_error|empty} and
      omo_gapfill_bars_saved_total.
    - Test coverage: 13 unit tests using fake fetcher/saver and a
      recording backfiller. Covers detect-only-without-backfiller,
      per-gap dispatch, fetch-error continuation, no-gaps short-circuit,
      empty broker response, partial-save count, nil-dep panics, 1d
      rejection.

    Why: omo-core's WS pipeline is the only writer for 1m/5m/15m/1h
    market_bars during RTH. If it's down for any portion of a session
    those minutes are silently missing — every backtest of that day
    then disagrees with what live actually saw. The 2026-04-24 mid-day
    restart is the canonical case; with this scan, that hole would have
    closed within 6h automatically.

(c) Pre-omo-core-feature historical backfill — unchanged, today's code.

(d) Audit cross-check — DEFERRED until Phase 4 ships. Nightly diff of
    omo-core's live DP aggregation against re-aggregating market_trades
    (Phase 0) or a fresh REST pull. Alert on dp_ratio drift > 5% or 5m
    bar volume drift > 1%. Phase 0's tick log already makes this a pure
    SQL query; Phase 4 produces the live aggregation that becomes the
    audit subject.

### Phase 6: omo-core restart resilience [SCAFFOLDING SHIPPED]

When omo-core boots during RTH the in-memory DPAggregator (Phase 4) will
have no state for the partial 5m bucket or any earlier bars of today.
Path (a) — replay from local market_trades — is the steady-state recovery
path. Shipped at 1952fe3e:

- `Repository.GetMarketTrades` mirrors `GetMarketBars`: ASC by time, half-
  open `[from, to)` window, covered by idx_market_trades_symbol_time from
  migration 045.
- New package `backend/internal/app/tradereplay` with a narrow `Reader`
  port (Repository satisfies it implicitly), a `Sink` callback type, and
  `Service.Replay(ctx, since, syms, sink)` that reads per-symbol and
  feeds the sink chronologically within each symbol. Best-effort failure
  model: read errors skip the symbol, sink errors stop the current
  symbol but continue with the next, all per-symbol errors joined in the
  return value. Stats reports per-symbol coverage even on error so the
  caller can decide whether to fall back to (b).
- Boot hook in warmup.go inside the existing intra-session block runs
  the replayer once with `tradereplay.LoggingSink()` (no-op) over equity
  symbols since `todayOpen.UTC()`. Telemetry-only today: the logged
  trade counts and time ranges are the wire-level smoke test that
  writer/reader/replayer holds together on production data, before any
  real consumer depends on it. Phase 4 swaps the sink for
  `DPAggregator.AddTrade` and the same scaffolding becomes the live
  recovery path.
- Test coverage: 8 unit tests in tradereplay/service_test.go using a
  fake Reader (no DB) — happy path ordering, empty window, nil sink,
  zero since, read-error continuation, sink-error continuation, context
  cancellation, window-args propagation.

Fallback paths (deferred):
- (b) REST-replay from Alpaca via RESTClient.GetHistoricalTrades. Used
  when (a) has no data for the boot window — typically because the
  AsyncTradeWriter itself just restarted and dropped the burst from its
  channel. Same code path omo-data uses for backfill. Add when (a)'s
  smoke-test reveals real-world boot windows we need to cover.
- (c) Accept that mid-day restarts dirty only the current 5m bucket.
  Prior buckets are persisted to darkpool_bars by Phase 4 as they close,
  so the strategy gets correct DP for all closed buckets after the first
  one post-restart. This is the floor we accept if both (a) and (b) fail
  for a given window.

## Acceptance criteria for "live matches backtest"

When all phases ship, the following should hold on a 5-day rolling window:

1. Trade count parity: live entries within +/-20% of EOD backtest run with
   identical params (including the same AI on/off state) on the same date
   range.

2. Per-bar gate parity: for any AVWAP-routed (symbol, bar) pair that produced
   different outcomes (entry vs blocked, or different blocked reasons)
   between live and backtest, the Phase 2 diagnostic payloads must explain
   the divergence as one of: (a) AVWAP state from a known restart,
   (b) a documented intentional difference. No silent discrepancies.
   Phase 1 (AI parity) eliminates AI fallback as a source.

3. DP factor parity: dp_ratio computed by the live aggregator matches dp_ratio
   computed by omo-data's REST aggregation (or by re-aggregating market_trades
   from Phase 0) for the same (symbol, 5m bucket) to within 1% on >= 95% of
   buckets, when both have data. Phase 5 audit step enforces this.

## Open questions

- Does Alpaca SIP WS actually deliver X="D" TRF prints? Phase 3 probe (Monday
  2026-04-27 RTH open) answers. If <35% the data source escalates to Databento
  or Polygon; downstream architecture is unchanged.
- Is the AVGO call divergence pinch vs breakout, or AVWAP state? Phase 2
  diagnostic payload now carries enough state (per-factor confluence, AVWAP
  anchor snapshot, AIEnabled) to decide via SQL diff. Re-evaluate after the
  next live/backtest pair we observe diverging.
- Does the Phase 6 boot replayer's market_trades coverage match what the
  Phase 4 DP aggregator will need? First answer comes from Monday's boot
  log (`market_trades boot replay complete` line in warmup output) — if any
  equity symbol shows trades=0 mid-session, either AsyncTradeWriter is
  dropping under burst or the WS shim from Phase 0 is filtering out the
  symbol's prints.
- Does the 6h cadence on the gap-fill scan close real holes inside one
  trading day? First answer comes from `omo_gapfill_attempts_total` after
  the next omo-core outage. If the `ok` outcome dominates `empty`/`fetch_error`
  on intraday timeframes, cadence is right; if `empty` dominates the cadence
  is too aggressive (Alpaca hasn't yet seen the bars we're asking for).

## Decision log

- 2026-04-25: User confirmed strategy was already tuned and validated on a
  1-year window with DP on. Skipping the 30-day DP-off walk-forward
  validation that quant-analyst recommended. Implication: the 17-trade
  DP-on profile is the target; DP-off is not a viable ship config.
- 2026-04-25: User chose to keep omo-data running rather than fully retire
  it. Resilience over efficiency.
- 2026-04-25: Decided diagnostic instrumentation (now Phase 2) must precede
  the live DP wiring (now Phase 4). Without diagnostics, post-DP residual
  gaps would be unattributable and the parity claim unprovable.
- 2026-04-25: Strategy_signal_events.strategy column inconsistency (avwap
  vs avwap_v4) was fixed at the runner boundary in instance.go and runner.go
  (commits pending). New blocked-event rows now write the spec id.
- 2026-04-25: Decided to add tick data logging as Phase 0. Foundation for
  audit (Phase 5), restart replay (Phase 6), and any future wire-level
  divergence diagnosis. Cost is small (~250MB/day uncompressed, 30-day
  rolling window). The MarketTrade Exchange/Conditions field plumbing also
  lives here, which Phase 4 (live DP) depends on.
- 2026-04-25: Decided AI/LLM enable state must be the same in both paths.
  Live's hardcoded DisableAI:false at services.go:420 will become
  DisableAI:!cfg.AI.Enabled, so cfg.AI.Enabled becomes the single switch.
  Backtest already respects per-run no_ai. To match a typical no_ai=true
  backtest, set LLM_ENABLED=false in .env. This eliminates the 0.65 AI
  fallback confidence as a confounding variable for all subsequent
  diagnostic work and removes one open question.
- 2026-04-25: Decided to extend Phase 5 (omo-data role) to also gap-fill
  intraday bars (1m / 5m / 15m / 1h), not just dark-pool bars. omo-core's
  WS pipeline is the only writer for intraday bars; if it's down for any
  part of a session, market_bars has silent holes that corrupt subsequent
  backtests. This is the same kind of resilience hole as DP, with the same
  fix shape (nightly gap-detect + REST backfill of only the missing ranges).
- 2026-04-25: Phase 2 Enrichment {Status, Confidence} field on the blocked-
  row payload was rejected as structurally undefined — EntryGated fires
  inside Evaluate before the enricher runs, the AI direction gate runs
  post-enrichment in risk_sizer and emits OrderIntentRejected (not
  EntryGated), and SignalEnriched events already carry Status+Confidence
  for emitted signals. Replaced with AIEnabled bool, the only AI-state
  signal available at gate-evaluation time. Diff-able per-bar; sufficient
  for acceptance criterion #2.
- 2026-04-25: Phase 6 scaffolding shipped ahead of Phase 4 with a no-op
  LoggingSink instead of waiting on the live DP aggregator. The sink swap
  is a one-line change at warmup.go; cost of shipping early is one
  unnecessary read pass per boot, benefit is a wire-level smoke test on
  Monday before any consumer depends on the path. (b) REST fallback and
  the dedicated replay-on-mid-day-restart hook are deferred until Monday's
  boot log shows whether (a) needs them.
- 2026-04-25: Phase 5(b) cadence set to 6h, not nightly. Nightly would let
  an early-session omo-core outage sit unfilled until the next-day's run,
  meaning a Monday backtest of "today" reads incomplete data. 6h closes
  most holes within the same trading day while still amortizing Alpaca
  REST cost across the universe.
- 2026-04-25: Phase 4 parts A and C shipped Saturday ahead of the Monday
  probe. A (aggregator concurrency-safety + FlushClosed) and C (DPSource
  interface + runner integration) are data-source-agnostic — the runner
  no longer cares whether DP data comes from a backtest map or a live
  aggregator. Cost of shipping early: ~30 LOC of dead code in
  staticDPSource if the probe forces a switch to Databento (it doesn't —
  the wrapper is reused). Benefit: Monday's Phase 4 wiring shrinks from
  ~500 LOC to ~250 LOC (just the livedarkpool.Service body + sink swap).

## Related files

Strategy / domain:
- Strategy: backend/internal/app/strategy/builtin/avwap_v1.go
- Confluence math: backend/internal/domain/strategy/confluence.go
- AVWAP calc: backend/internal/domain/strategy/anchored_vwap.go
- Live runner: backend/internal/app/strategy/runner.go (DP overlay at :918)
- Backtest runner: backend/internal/app/backtest/runner.go
- AI enricher: backend/internal/app/strategy/signal_debate_enricher.go
- Risk sizer (AI direction gate): backend/internal/app/strategy/risk_sizer.go
- Strategy TOML: configs/strategies/avwap_v4.toml

Diagnostic payload (Phase 2):
- EntryGated payload type: backend/internal/domain/event.go (EntryGatedPayload)
- Stamp site: backend/internal/app/strategy/instance.go (EmitDomainEvent)
- AVWAP snapshot accessor: backend/internal/domain/strategy/anchored_vwap.go (Snapshot)

Tick logging (Phase 0):
- Migration: backend/migrations/045_create_market_trades.up.sql
- Writer: backend/internal/app/ingestion/trade_writer.go
- Repo write: backend/internal/adapters/timescaledb/repository.go (SaveMarketTrades)
- WS shim: backend/internal/adapters/alpaca/websocket.go (Exchange/Conditions/Tape preserved at :510)

Boot replay (Phase 6 scaffolding):
- Repo read: backend/internal/adapters/timescaledb/repository.go (GetMarketTrades)
- Service: backend/internal/app/tradereplay/service.go
- Boot hook: backend/cmd/omo-core/warmup.go (inside intra-session block)

Gap fill (Phase 5b):
- Backfiller: backend/internal/app/gapdetect/backfill.go (IntradayBarBackfiller)
- Service hook: backend/internal/app/gapdetect/metrics.go (Service.SetBackfiller)
- omo-data wire-up: backend/cmd/omo-data/main.go (gapSvc, runEvery)

DP aggregation (Phase 4 — A/C shipped, B/D/E/F pending):
- Aggregator (concurrency-safe, FlushClosed): backend/internal/app/backfill/darkpool_aggregator.go
- DP repo: backend/internal/adapters/timescaledb/darkpool_repo.go
- DPSource interface + static/noop impls: backend/internal/app/strategy/dp_source.go
- Runner integration: backend/internal/app/strategy/runner.go (SetDarkPoolLookup wraps in staticDPSource; SetDarkPoolSource for Phase 4 B)

Probe (Phase 3):
- Alpaca REST historical trades: backend/internal/adapters/alpaca/rest.go (HistoricalTrade.X at :633)
- WS probe binary: _tmp/dp_ws_probe/

## Sub-investigations referenced

- Quant-analyst review of DP-off backtest (agent run 2026-04-25, ID a4ef3d8ade104a1a9)
- DP sourcing research (agent run 2026-04-25, ID a0c7a7dac983a04f3)
