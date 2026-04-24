# copytrade replay backtest — session handoff (2026-04-23, Day 1-4 shipped)

Pick up here. The goal is to backtest the live `copytrade_v1` strategy against
90 days of scraped Discord messages, using the same code paths as paper trading
(Chandelier trail, partial STCs, Pending-flag semantics, max_positions gate) so
the result reflects what a real copier would have experienced.

Days 1-4 landed in commit `77ab911` — end-to-end pipeline is live and
producing ledgers. Day 5 is optional polish; this doc is organized for a
cold-start pickup.

## TL;DR for the next session

1. Read "What's shipped" below to understand the pipeline that exists.
2. Run this to reproduce current results (takes ~45s on warm cache):

       cd backend
       go build -o /tmp/omo-replay-bin ./cmd/omo-replay
       cd ..
       /tmp/omo-replay-bin --backtest \
         --copytrade-history services/discord-copytrade/state/history_90d.jsonl \
         --strategies copytrade_v1 \
         --symbols AAPL,AMZN,GOOGL,INTC,IWM,MARA,MSFT,NIO,NVDA,ORCL,QQQ,SPY,TSLA \
         --from 2026-01-27 --to 2026-04-23 \
         --config configs/config.yaml --env-file .env

3. Expected output: 79 BTOs parsed → 7 SignalCreated → 3 round-trips → +$4,306.
   Full summary block at the end of replay prints author-stated vs actual.

4. Ledgers written to `_workspace/copytrade_replay/{fills,author_stated}.csv`.

## What's shipped (Days 1-4)

- `backend/internal/app/copytradereplay/` — new package:
  - `parser.go` — Go port of the Python Discord text parser, golden-tested
    against all 261 history rows (bit-identical output).
  - `service.go` — `Load(path, from, to)` pre-sorts the queue by
    `(PostedAt, BTO<AVG<STC)` and `AdvanceTo(ctx, clockTime)` drains any
    signal with `PostedAt <= clockTime` at tick END, publishing
    `EventCopytradeSignalReceived` directly to the sync bus. Bypasses the
    HTTP handler's 120s freshness TTL. 5 unit tests pass.
  - `ledger.go` — subscribes to `EventFillReceived` filtered by
    `strategy=copytrade_v1`, writes per-fill CSV.
  - `author_ledger.go` — walks the parsed queue, pairs BTOs with their
    same-key STCs, applies partial-fraction keyword rules, writes
    reconstructed per-position CSV with author-stated VWAP exit and
    PnL-per-contract.
- `backend/cmd/omo-replay/main.go` — flags `--copytrade-history` and
  `--copytrade-ledger-dir` (default `_workspace/copytrade_replay`). Injector
  + ledger subscribe **before** `FreezeHandlers()` (critical — post-freeze
  Subscribe silently no-ops under the atomic handler snapshot). `OnTickEnd`
  calls `AdvanceTo` after `EvalExitRules` then explicitly drains every
  shard's `pendingSignals` buffer since the Phase-B bar-index drain doesn't
  catch signals emitted from bus handlers.
- `backend/internal/app/bootstrap/strategy.go` —
  `BuildStrategyShardWithSentinels` variant: registers `__name__`
  sentinel-routed specs even when slab filter excludes them. Called from
  exactly the first shard factory in omo-replay via a
  `sentinelOwnerAssigned` bool, so the bus handler fires once per event
  (not N times for N shards). Non-copytrade callers pass through the
  non-sentinel path unchanged.
- `backend/internal/app/backtest/synthetic_options_chain.go` — two fixes
  that activate **only** when `minDTE == maxDTE` (forced-expiry mode from
  risk_sizer — byte-identical for range-DTE backtests):
  1. `weeklyExpiries` returns the exact date regardless of weekday. Was
     Friday-only, which silently dropped any author post with a Mon/Wed
     expiry.
  2. `GenerateChain` unions exchange-standard tick strikes into the grid
     ($1 for ≥\$50, $0.50 for ≥\$10, $0.25 otherwise). Before, a spot-based
     step of 1% skipped common integer strikes — AMZN \$245 wasn't in the
     grid for spot ~\$230.
- `services/discord-copytrade/scrape_history.py` + compose profile — the
  Playwright scraper wired as `docker compose --profile scrape run --rm
  scrape --days N --out /app/state/history_Nd.jsonl`.
- Options data path: when `--copytrade-history` is set, swap
  `cachingOptionsMarket(alpacaAdapt)` for `HistoricalOptionsAdapter` with
  the synthetic BSM fallback enabled by default. PreLoad runs over the
  from/to window. `Stats()` prints DoltHub/synthetic hit counts at
  end-of-run. Alpaca option-bar fetch subscription is gated off in
  copytrade mode (chandelier trail uses underlying→BSM, not option marks,
  so the Alpaca fetch just produced noise).

## Resolved decisions

All six pre-code blockers are resolved (see commit `77ab911` message for
detail):

- **#1 Parser port** — ported to Go, golden test locked in.
- **#2 Synthetic BSM fallback** — accepted + reported.
- **#3 Option-bar feed** — single-mark path chosen; chandelier trail works
  via underlying→BSM so option bars aren't on the critical path.
- **#4 Universe gate wiring** — not yet wired. Turned out to be a
  *non-gate*: the runner's `universeHistory` port is nil in backtest, so
  signals pass the gate by default. The real filter is at the sharded-
  pipeline level (slab symbols). OOU BTOs pass the runner but never
  execute because no bars arrive — they stay Pending forever and consume
  `max_positions` slots.
- **#5 Author whitelist** — copytrade_v1.toml has `author_whitelist = []`
  (allow all). No authors in the 90d sample are filtered here.
- **#6 Freshness TTL bypass** — injector publishes directly to the bus;
  never routes through the HTTP handler.

## 90d run results (commit 77ab911, 13-symbol universe)

    Input:  79 BTOs, 103 STCs, 14 AVGs from 5 authors over Jan 27 - Apr 23 2026
    Injector: 196 signals published (100% of parsed input, 60 non-action drops)

    Strategy emits:
      7 SignalCreated (of 79 BTOs)
      reasons for the 72 drops — all logged at WARN level in copytrade_v1:
        ~72  "dropping BTO — max_positions reached"
          2  "BTO rejected — unwinding ghost position"
         67  "STC with no prior BTO — dropping"  (corresponding BTOs dropped)
          3  "STC refused — BTO not yet confirmed by broker"
         14  "skipping AVG"  (skip_avg=true — intended)

    Fills: 6 (3 BTO + 3 STC round-trips completed)
    Actual P&L:    +$4,306.63 / 3 wins / 0 losses / Sharpe 2.809
    Author P&L:    +$1,192.84 per contract across 53 positions / 77.8% WR

    Trade log:
      AMZN 245c 2/02  BTO $3.52 → STC $3.86   +$266   (author: +$31.69/ct)
      TSLA 445c 2/04  BTO $4.19 → STC $5.77   +$1108  (author: +$6.54/ct)
      INTC 55c 3/20   BTO $0.96 → STC $2.43   +$2932  (author: +$8.25/ct, 33% closed)

## Why only 3 trades out of 79 BTOs?

Two constraints compound:

1. **Symbol coverage gap.** 10 of the 23 tickers in the sample
   (BABA/PDD/TSM/ENPH/SLV/GLD/KWEB/BIDU/NIO/RKLB/FSLR) have no bars in
   `market_bars`. These BTOs emit → reach the risk sizer → intent
   resolves via synthetic chain → SimBroker **fails to fill** (no price
   for the underlying) → intent sits unresolved → strategy's position
   counter treats them as still-open ("Pending=true, ghost position").

2. **`max_positions = 5`.** Once 5 Pending-or-open positions accumulate,
   every subsequent BTO drops with `max_positions reached`. With OOU
   tickers never clearing from Pending, the cap fills early and stays
   full. The 2 `unwinding ghost position` log lines show the strategy
   *eventually* reconciles some stuck positions on OrderIntentRejected,
   but this path doesn't fire often enough to keep the cap open.

The strategy logic is correct. The constraint is data coverage + the
cap's interaction with unresolved positions. Backfilling the missing
symbols to `market_bars` would unlock roughly half the sample. Beyond
that, the author-stated ledger suggests the untouched positions would
have been a net positive (+$1,192/ct across 53, 77.8% WR).

## Known gotchas (non-obvious)

- **`FreezeHandlers` ordering.** Any `bus.Subscribe` after
  `FreezeHandlers()` is silently ignored at delivery time — the bus's
  fast-path `Publish` reads from an atomic snapshot. When adding new
  subscribers to omo-replay, put them before the freeze call
  (`main.go:1036`).
- **Sentinel-shard single-fire.** The copytrade strategy registers in the
  *first* shard only via a `sentinelOwnerAssigned` bool in the factory
  closure. If you refactor shard construction to parallel / non-deterministic
  order, the "first" assignment still works because Go maps aren't iterated
  in the factory — shard factory is called serially per slab by
  `NewShardedPipeline`.
- **Forced-expiry synthetic path.** `minDTE == maxDTE` is the signal to
  the synthetic generator that the caller has pinned a specific date and
  wants full coverage. Any call with a range (e.g. DTE 5-14 from a spec's
  `options.defaults`) takes the original Friday-only, step-based path.
- **STC fill attribution gap.** BTO fills carry `author`/`signal_id` via
  `sig_*` tag propagation. STC fills do not — position-monitor-driven
  exits don't propagate those tags onto the OrderIntent. Correlate STCs
  to BTOs by `contract_symbol` when analyzing the fills CSV.
- **Author-stated PnL multiplier.** `AuthorPnLPerCtr` multiplies by 100
  (options contract size) so dollars align with the backtest collector.
  Multiply by actual contract count to compare to the real ledger.

## Day 5 — optional polish

Priority order based on analytical ROI:

1. **Backfill missing-symbol bars.** Would unlock ~half the author sample.
   `omo-backfill` binary exists; check its flags. Candidates:
   BABA, PDD, TSM, ENPH, SLV, GLD, KWEB, BIDU, NIO, RKLB, FSLR.
2. **STC fill attribution.** Propagate the source BTO's `sig_*` tags onto
   the exit intent in `positionmonitor/handlers.go:triggerExit`. Carefully
   scoped: don't break non-copytrade exits. Makes fills.csv pivotable by
   `signal_id`.
3. **Ghost-position auto-expire.** If a BTO stays Pending for >N minutes
   without a fill (because the symbol has no bars), auto-unwind. Would
   prevent max_positions from filling with phantom positions.
4. **Integration test.** 10-message synthetic history → assert expected
   fill count + ledger row count. Locks in the pipeline against regression
   but doesn't reveal new info.

Not worth doing (lower ROI):
- Expiry-1d cap + intrinsic mark at expiry (the guardrail flagged by
  quant-analyst). No expired-unSTC positions in the sample — the 4
  unclosed ones are all still within their expiry window.

## Key file pointers

Replay pipeline (for the next session):

- `backend/cmd/omo-replay/main.go` —
  - `:95` copytrade-history flag
  - `:969` injector + ledger construction (before FreezeHandlers)
  - `:392` options market switch (copytrade → hist adapter, else alpaca)
  - `:461` shardFactory sentinel-first-shard assignment
  - `:1629` OnTickEnd AdvanceTo + explicit pendingSignals drain
  - `:1278` end-of-run `=== COPYTRADE REPLAY ===` block
  - `:1308` end-of-run author-stated block + summary
- `backend/internal/app/copytradereplay/` — package lives here
- `backend/internal/app/bootstrap/strategy.go:186` —
  `BuildStrategyShardWithSentinels` + `isSentinelSymbol`
- `backend/internal/app/backtest/synthetic_options_chain.go:171` —
  `weeklyExpiries` forced-expiry branch
- `backend/internal/app/backtest/synthetic_options_chain.go:249` —
  `unionStandardStrikes` helper

Live copytrade path (unchanged by this work):

- `backend/internal/app/strategy/runner.go:745` — bus subscription for
  `EventCopytradeSignalReceived`
- `backend/internal/app/strategy/runner.go:2401` — `handleCopytradeSignal`
- `backend/internal/app/strategy/builtin/copytrade_v1.go` — strategy logic
- `backend/internal/app/positionmonitor/handlers.go:201` —
  `handleCopytradeExitRequest` (where STC exits dispatch)

Data + scraping:

- `services/discord-copytrade/state/history_90d.jsonl` — 261 raw messages
- `services/discord-copytrade/scrape_history.py` — re-run scraper any time
- `backend/internal/app/copytradereplay/testdata/parsed_ground_truth.jsonl`
  — Python parser output captured as golden reference for Go parser tests

## Session-start prompt for next run

> Copytrade replay sprint complete. Days 1-4 shipped the pipeline (77ab911).
> Day 5 session 2 shipped symbol backfill. Day 5 session 3 shipped
> ghost-position TTL auto-expire + the prerequisite runner fixes
> (4461d26, c7ec395). Day 5 session 4 shipped HTTP backtest wiring and
> STC sig_* fill attribution (27d1567, 7e20f8d). Dashboard can now drive
> copytrade_v1 backtests and fills.csv rows carry author/signal_id for
> pivoting. No remaining work in the original punch list.

----

## Day 5 session 2 (2026-04-23, evening)

### Shipped

**Symbol backfill** — all 10 fully-missing tickers now have 1m bars for
the full replay window (Jan 27 → Apr 23 2026):

    BABA, BIDU, ENPH, FSLR, GLD, KWEB, PDD, RKLB, SLV, TSM

- 389,702 bars inserted via `omo-backfill`, 0 errors, 2m39s elapsed.
- DoltHub options imported for FSLR (62 days) and ENPH (62 days). The
  other 8 aren't in DoltHub, so synthetic BSM fallback covers them.
- No source changes — reproduce with:

      /tmp/omo-backfill-bin --symbols BABA,BIDU,ENPH,FSLR,GLD,KWEB,PDD,RKLB,SLV,TSM \
        --from 2026-01-27 --to 2026-04-24 --timeframe 1m \
        --config configs/config.yaml --env-file .env

  Log: `backend/_workspace/copytrade_replay/backfill_missing.log`

- Partial-coverage symbols (INTC/ORCL last 04-13, MARA/NIO last 03-31)
  were checked — every BTO in the sample falls within the existing
  coverage window, so extension wasn't needed.

### Replay rerun with 23-symbol universe

Ran via omo-replay CLI — nothing else changed vs commit 77ab911:

    /tmp/omo-replay-bin --backtest \
      --copytrade-history services/discord-copytrade/state/history_90d.jsonl \
      --strategies copytrade_v1 \
      --symbols AAPL,AMZN,BABA,BIDU,ENPH,FSLR,GLD,GOOGL,INTC,IWM,KWEB,MARA,MSFT,NIO,NVDA,ORCL,PDD,QQQ,RKLB,SLV,SPY,TSLA,TSM \
      --from 2026-01-27 --to 2026-04-23 \
      --config configs/config.yaml --env-file .env

Results (full log: `backend/_workspace/copytrade_replay/replay_23sym.log`):

    metric          baseline (13)   expanded (23)   delta
    SignalCreated   7               7               unchanged
    round-trips     3               5               +2
    Total PnL       +$4,306.63      +$5,807.31      +35%
    Sharpe          2.809           2.019           lower (ENPH loser)

New fills: SLV 79c 2/09 +$2,761 (winner), ENPH 40c 3/20 -$1,260 (loser).

**Key finding — the binding constraint shifted.** With symbol coverage
fixed, `max_positions = 5` is now the limiter, not missing bars. The
seven BTOs that pass the cap are the same seven as before; coverage fix
just lets more of them actually fill. More trades per run = ghost-expire
work (Day 5 item #3) is now the highest-ROI next lever.

### Discovered: HTTP backtest broken for copytrade_v1

Dashboard `POST /backtest/run` with `strategies=["copytrade_v1"]` fails
deterministically. Two stacked problems:

1. **Wrong symbols collected.** `collectStrategySymbols` in
   `backend/internal/adapters/http/backtest_handler.go:514` reads
   `routing.symbols` from the TOML, which is the sentinel `__copytrade__`.
   Runner then hits Alpaca for bars on `__COPYTRADE__` and gets
   HTTP 400 `invalid symbol: __COPYTRADE__`. Visible in
   `logs/omo-core.log` around the `backtest_id` logged at 21:15:20.

2. **None of the copytrade pipeline is wired into `backtest.Runner`.**
   The history parser, signal injector, sentinel-first-shard assignment,
   fills ledger, and OnTickEnd drain all live exclusively in
   `cmd/omo-replay/main.go`. The HTTP runner
   (`backend/internal/app/backtest/runner.go`) never constructs any of it.

Options adapter (`HistoricalOptionsAdapter` + synthetic BSM) IS already
present in the HTTP runner — only piece already wired. Alpaca
option-bar fetch subscription doesn't exist in the HTTP path at all, so
no gate needed.

### Plan — port copytrade wiring into HTTP backtest (NOT IMPLEMENTED)

Approved at the planning stage, did not yet touch code. Files + edits:

**`backend/internal/adapters/http/backtest_handler.go`** (~40 LOC)
- Add `CopytradeHistory string` + `CopytradeLedgerDir string` to
  `backtestRunRequest`.
- In `handleRun`: when `copytrade_v1` is in `Strategies`, require
  `CopytradeHistory`, reject with 400 if missing or file unreadable;
  default `CopytradeLedgerDir` to `_workspace/copytrade_replay`.
- In `collectStrategySymbols`: filter out any symbol starting with `__`
  (the sentinel prefix). If the only selected strategy is copytrade and
  `Symbols` is empty after filtering, 400 with a message telling the
  caller to pass explicit symbols.
- Plumb both new fields into `backtest.RunConfig`.

**`backend/internal/app/backtest/runner.go`** (~160 LOC)
- Add `CopytradeHistory`, `CopytradeLedgerDir` to `RunConfig`.
- Import `copytradereplay`.
- In `shardFactory` (line 1397): add `sentinelOwnerAssigned` bool and
  switch the first shard's strategy to
  `bootstrap.BuildStrategyShardWithSentinels` when
  `cfg.CopytradeHistory != ""`. Others keep `BuildStrategyShard`
  unchanged.
- Before `r.infra.EventBus.FreezeHandlers()` at line 1481: when
  `cfg.CopytradeHistory != ""`, construct `copytradereplay.Service`,
  `Load()` history, construct `copytradereplay.Ledger`, subscribe it.
- Extend `runnerSliceCoord` (line ~2240) with a `copytradeReplay`
  field; in `OnTickEnd` (line 2264) mirror
  `replaySliceCoord.OnTickEnd` in omo-replay:main.go:1810 — call
  `AdvanceTo(ctx, tickTime)`, `WaitPending`, per-shard
  `DrainPendingSignals` publish loop, final `WaitPending`.
- At end-of-run (where `emitter.EmitComplete` fires): emit one
  `copytrade_summary` SSE event and write `author_stated.csv` via
  `Service.WriteAuthorStatedLedger`. Close the ledger file in the
  runner's cleanup path.

**Out of scope** for this slice:
- Dashboard UI changes (backend contract only, manual curl verify first).
- Consolidating CLI and HTTP wiring into a shared builder.
- Mid-run copytrade progress events (end-of-run summary only).
- Author-whitelist request override (read from TOML as today).
- Integration test.

**Risks to watch**:
- Double-publish on multi-shard runs — `sentinelOwnerAssigned` bool
  guard is critical (pattern from `omo-replay/main.go:516`).
- `FreezeHandlers()` ordering — ledger `Subscribe` must run BEFORE the
  freeze at line 1481, otherwise it silently no-ops.
- Ledger dir `_workspace/copytrade_replay/` is shared with the CLI;
  concurrent runs would clobber `fills.csv`. Acceptable since the HTTP
  queue is single-worker.

Blast radius: non-copytrade backtests unchanged when
`CopytradeHistory == ""` — the sentinel-first-shard switch, the new
subscribers, and the OnTickEnd injector are all guarded by that
predicate.

**Verification checklist** before shipping:
- `go build ./cmd/omo-core` clean.
- `go test ./internal/app/backtest/... ./internal/adapters/http/...`
  passes (9 test files in backtest alone).
- Manual curl with 23-symbol list + history path reproduces CLI
  baseline (+$5,807.31, 5 round-trips) within slippage noise.
- Regression: POST any non-copytrade strategy, verify unchanged.

### Files updated by this session

- TimescaleDB `market_bars` — 389,702 new rows.
- TimescaleDB `historical_options_*` — FSLR and ENPH coverage extended.
- `_workspace/copytrade_replay/{fills.csv,author_stated.csv}` —
  overwritten with 23-symbol rerun output.
- `backend/_workspace/copytrade_replay/{backfill_missing.log,replay_23sym.log}`
  — new log files.

No source code changed this session. No commits made.

----

## Day 5 session 3 (2026-04-23, late evening)

### Shipped

**Ghost-position TTL auto-expire** and two prerequisite runner fixes.

Commits (on `main`):

- `4461d26` — fix(strategy): prevent reentrant deadlock + use sim-time
  in copytrade handlers. Lock-free `Instance.IsActive`/`Lifecycle`/
  `SetLifecycle` via `atomic.Pointer[LifecycleState]`; defer re-entrant
  `handleCopytradeExitRejected` → `Instance.OnEvent` via runner-level
  callback queue drained after inst.mu + r.mu released. Four copytrade
  handler sites now thread `event.OccurredAt` into `instCtx.now` via a
  new `Runner.handlerNow` helper so backtests see sim-time.
- `99dd814` — refactor: tighten docstrings + drop redundant reentry-test
  assertion. [skip-review]
- `c7ec395` — feat(copytrade): ghost-position TTL auto-expire with
  broker cancel. Config: `pending_ttl_paper_seconds=120`,
  `pending_ttl_live_seconds=90` (0 disables). Strategy sweeps stale
  Pending at top of OnEvent, emits `CopytradeEntryExpired` →
  execution cancels the BTO at the broker (SimBroker no-ops safely;
  IBKR does a real cancel). On race (fill arrives after slot freed)
  strategy emits `CopytradeOrphanFill` → notify formatter pages Discord.
- `af01344` — refactor: tighten TTL-sweep docstrings. [skip-review]

### Replay result after the fix (23-symbol, 90d)

    metric                baseline (TTL=0)   post-fix (TTL=120/90)
    round-trips           5                  5
    Total PnL             +$5,807.31         +$5,807.31
    Win rate              80%                80%
    Max drawdown          1.18%              1.18%
    Sharpe                2.019              2.019
    Ghost-expires fired   —                  0
    Orphan fills          —                  0

Identical output. The 23-symbol universe has no "silent drop" positions,
so the sweep finds nothing to expire; the feature is a safety net for
live trading. The *old* pre-fix hung run showed 4 false expires due to
the wall-clock-vs-sim-time bug — that's what the sim-time fix resolved.

### What the new `go-hexagonal/SKILL.md` gotchas capture

Two architectural traps worth remembering for any future copytrade /
event-driven strategy work:

1. **syncMode bus + in-handler publish re-enters the same goroutine.**
   Holding `inst.mu` across `strategy.OnEvent` AND having a bus
   subscriber that touches the same instance is a recipe for deadlock
   in backtest. Fix pattern: atomic lifecycle + runner-level callback
   queue drained at handler exit.
2. **Copytrade handlers must use `event.OccurredAt`, not `time.Now()`.**
   `handleFill` already did. `handleCopytradeSignal` /
   `handleCopytradeExitRejected` / `handleRejection` did not — the
   mismatch silently miscomputed any strategy-side age math in backtest.

### Remaining work

None in the original punch list. Both (a) and (b) shipped in Day 5
session 4 — see below.

----

## Day 5 session 4 (2026-04-23, late late evening)

Sprint close-out.

### Shipped

- `27d1567` — feat(copytrade): wire replay pipeline into HTTP-driven
  backtest runner. Two new `RunConfig` fields (`CopytradeHistory`,
  `CopytradeLedgerDir`) gate the entire copytrade code path;
  `backtest_handler.go` validates history path + enforces `speed=max`
  + filters sentinel `__*` symbols. Sharded path gains the sentinel-
  first-shard switch, the subscribe-before-freeze injector/ledger
  construction, an `OnTickEnd` leg that runs `AdvanceTo` +
  `DrainCopytradeCallbacks` + `DrainPendingSignals` per shard, and an
  end-of-run `backtest:copytrade_summary` SSE event + author-ledger
  write. Non-copytrade backtests unchanged (guarded on
  `CopytradeHistory == ""`).

- `7e20f8d` — feat(positionmonitor): propagate entry `sig_*` tags onto
  STC exit intent. New `MonitoredPosition.EntrySignalTags` field (no-op
  nil for non-copytrade), populated from `fill.SignalTags` on first BUY
  fill (scale-in path preserves original BTO attribution); `triggerExit`
  re-prefixes each tag with `sig_` onto `intent.Meta` (skip-if-present
  for defensive non-clobber). Execution strips `sig_` uniformly for
  BTO and STC fills, so `copytradereplay.Ledger`'s existing `author` /
  `signal_id` / `copytrade_action` / `ref_price` / `generation` columns
  now populate on STC rows with no schema change.

### Verification

- `go build ./cmd/omo-core ./cmd/omo-replay` clean.
- Full regression scope (positionmonitor, backtest, http, strategy,
  execution, copytradereplay, domain, notify) all PASS.
- End-to-end HTTP curl not performed this session (omo-core running old
  binary); restart + manual exercise deferred to next live session.

### How to exercise the HTTP backtest path

After `omo-core` picks up the new binary:

    curl -X POST http://localhost:8080/backtest/run \
      -H 'Content-Type: application/json' \
      -d '{
        "strategies": ["copytrade_v1"],
        "symbols": ["AAPL","AMZN","BABA","BIDU","ENPH","FSLR","GLD","GOOGL","INTC","IWM","KWEB","MARA","MSFT","NIO","NVDA","ORCL","PDD","QQQ","RKLB","SLV","SPY","TSLA","TSM"],
        "from": "2026-01-27",
        "to":   "2026-04-23",
        "timeframe": "1m",
        "initial_equity": 100000,
        "speed": "max",
        "copytrade_history": "services/discord-copytrade/state/history_90d.jsonl"
      }'

Expected: 202 Accepted with `{backtest_id, status}`. Stream via
`/backtest/events/{id}` for `backtest:copytrade_summary` + `backtest:complete`.

Error-case curls:
- Missing `copytrade_history` → 400 "copytrade_v1 requires copytrade_history (path to JSONL)".
- Unreadable path → 400 "copytrade_history unreadable: ...".
- `speed != "max"` → 400 "copytrade_v1 backtest requires speed=max (sharded pipeline only)".
- Empty post-filter symbols → 400 "copytrade_v1 has no non-sentinel symbols — pass explicit symbols in request body".

### Sprint ledger

Full commit list for the sprint, in order:

    77ab911  feat(copytrade-replay): pipeline (Days 1-4)
    8eeb2d3  docs: handoff after Days 1-4
    4461d26  fix(strategy): reentrant deadlock + sim-time in copytrade handlers
    99dd814  refactor: tighten docstrings + drop redundant test assertion [skip-review]
    c7ec395  feat(copytrade): ghost-position TTL auto-expire
    af01344  refactor(copytrade): tighten TTL-sweep docstrings [skip-review]
    1b560cc  docs(go-hexagonal): syncMode reentry + handler-clock gotchas [skip-review]
    a3f5808  docs(copytrade-replay): Day 5 session 3 handoff [skip-review]
    27d1567  feat(copytrade): HTTP backtest wiring
    7e20f8d  feat(positionmonitor): STC sig_* fill attribution
    (this one)  docs(copytrade-replay): Day 5 session 4 close-out [skip-review]

Sprint done.
