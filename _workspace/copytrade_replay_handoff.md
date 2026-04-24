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

> Resuming copytrade replay backtest. Days 1-4 shipped in commit 77ab911;
> read `_workspace/copytrade_replay_handoff.md`. The pipeline is live and
> producing ledgers in `_workspace/copytrade_replay/`. Day 5 is optional
> polish — top candidates: (1) backfill missing-symbol bars to unlock the
> OOU half of the sample, (2) STC fill attribution via `sig_*` tag
> propagation, (3) ghost-position auto-expire. Ask before starting; the
> analytical value of #1 depends on whether more replay iterations are
> planned.
