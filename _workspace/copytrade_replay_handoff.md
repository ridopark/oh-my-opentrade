# copytrade replay backtest — session handoff (2026-04-23, end of day)

Pick up here. The goal is to backtest the live `copytrade_v1` strategy against
90 days of scraped Discord messages, using the same code paths as paper trading
(Chandelier trail, partial STCs, Pending-flag semantics, max_positions gate) so
the result reflects what a real copier would have experienced.

This session produced: (a) a Discord history scraper, (b) the 90d JSONL, and
(c) design agreement with go-architect + Explore on how the replay should
hook in. Zero code yet toward the replay itself.

## What's already in place

- `services/discord-copytrade/state/history_90d.jsonl` — 261 raw Discord
  messages, Jan 27 → Apr 23, sorted oldest-first. 5 authors, 79 BTO / 106 STC /
  16 AVG action-bearing lines.
- `services/discord-copytrade/scrape_history.py` — one-shot Playwright scraper
  reusing `state/storage_state.json`, wired as compose profile `scrape`. Run
  again anytime with `docker compose --profile scrape run --rm scrape --days N
  --out /app/state/history_Nd.jsonl`.
- Live copytrade is already correct end-to-end (commit `72da48b` landed this
  session, covering the sentinel-symbol leak, ghost-position flag, and
  in-flight partial rejection). The strategy, runner subscription, and
  positionmonitor handler all work in backtest mode too because they subscribe
  to the same `ports.EventBusPort`.

## What the Discord data looks like (coverage against DoltHub)

Probing 12 random BTOs from "covered" symbols against the local
`historical_option_chain` table (against the exact ticker+date+expiration+
strike+right tuple the author specified):

- **2/12 exact hits.** Only INTC 3/20 55c (a standard monthly expiry) matched.
- **Root cause is expiration sparsity, not ticker coverage.** DoltHub stores
  2–4 expirations per `(symbol, date)` snapshot, skewed mid-to-long-dated.
  Near-term weeklies (0–9 DTE, which is where 46 of 79 BTOs live) are
  systematically absent.
- **EOD-only snapshots.** Schema has `date`, not `timestamp`. The author posts
  at 10:20 ET; the best we can get is the 16:00 close bid/ask — a 6-hour
  pricing gap per trade.
- **37% of tickers not covered at all.** NIO, BIDU, KWEB, BABA, PDD, TSM, SLV,
  GLD, ENPH, RKLB — 24 of 79 BTOs.

Upshot: a naive replay that only uses DoltHub drops ~70% of BTOs. The
fallback plan is to wire the existing synthetic-BSM generator (used elsewhere
in the backtest stack) as a secondary source when DoltHub misses, and report
coverage stats at end-of-run.

## Design decisions (from go-architect + Explore consultation)

### 1. Injector placement

New app-layer service: `backend/internal/app/copytradereplay/`.

    service.go      — Load(path), AdvanceTo(ctx, clockTime)
    parser.go       — Go port of the Python regex parser
    parser_test.go  — golden test against all 261 history rows
    ledger.go       — per-trade CSV writer subscribing to FillReceived

**Not** under `cmd/omo-replay/` (that's orchestration, not logic) and **not**
an adapter (adapters satisfy external-world ports; this one injects domain
events into the bus). `cmd/omo-replay/main.go` constructs the service
alongside the other `bootstrap.Build*` calls under a new `--copytrade-history
<path>` flag, gated on `--backtest`.

### 2. Timing model

Pre-sorted priority queue, drained at **tick END** (not start). Reasoning:

- The copytrade strategy sets `Pending=true` on BTO emit and refuses STCs
  against pending positions. Draining signals at tick start would inject
  same-minute BTO+STC pairs before SimBroker has a chance to fill the BTO →
  the STC gets refused → state desync.
- Drain-at-tick-end order: bar publish → fills post back → positionmonitor
  processes → strategy handleFillConfirmation clears Pending → injector
  drains any signals with `ts <= tickTime+1min`.
- Same-minute BTO/STC pairs (rare in the 90d sample — median gap is 7+ min)
  need deterministic within-tick ordering: BTO first, STC second. Achieved
  by stable-sorting the queue with a secondary key on action (BTO < AVG <
  STC) when timestamps tie at the bar boundary.

The replay event bus is `memory.NewSyncBus` (synchronous), so `eventBus.
Publish` from a single-threaded dispatcher is the clean path. No goroutines.

### 3. Options data path

Hybrid: historical DoltHub primary + synthetic BSM fallback. The current
replay wires `alpacaAdapt.GetOptionChain` (LIVE Alpaca, not historical) via
`cachingOptionsMarket` at `cmd/omo-replay/main.go:386` — this is wrong for a
historical replay and must be swapped.

The project already has the right adapter:
`backend/internal/app/backtest/historical_options_adapter.go` —
`GetOptionChain` at :208, `hasExpiryInDTERange` at :258, `generateSynthetic`
at :283. It's currently only used by the batch `omo-backfill` path. Wiring it
into omo-replay when `--copytrade-history` is set is the change.

Copytrade doesn't let risk_sizer pick from a chain — the Discord message
pins strike+expiry+right via `force_*` tags in handleBTO. So the adapter
just needs to answer "does this exact contract have a sane quote at this
date?" and return a synthetic OCC contract at PostedAt-spot when DoltHub
misses. `HistoricalOptionsAdapter.Stats()` (:195) already exists — print at
end of run.

### 4. Exit pricing feed (option bars during replay)

Currently, `cmd/omo-replay/main.go:350` lazily fetches `alpacaAdapt.
GetHistoricalOptionBars` per-contract on FillReceived and stuffs minute bars
into `optionBarsCache` for positionmonitor to mark exits. For copytrade
replay, Alpaca historical options bars only go back ~30 days and cost per
request; DoltHub has EOD only.

Two options the next session must pick from:

  (a) DoltHub-backed bar reader that interpolates EOD-mid across the trading
      day (repeats EOD bid/ask per minute bar). ~3 days of work. Inaccurate
      but doesn't pretend otherwise.
  (b) Single-mark pricing: hold the contract mark at entry premium until
      STC, then mark at EOD bid. ~1 day of work. Accurate at STC time;
      meaningless for any mid-day trail that needs running mark.

Recommendation: ship (b) first for a fast end-to-end run, then upgrade to
(a) if the initial numbers look worth polishing. Chandelier trail will be
noise under (b), so report trail behavior separately (did it arm? did it
fire? was it stale?).

### 5. Exit order execution

Already works in `--backtest`. Verified chain:

  positionmonitor.Start subscribes EventCopytradeExitRequest (service.go:413)
  handleCopytradeExitRequest → triggerExit (handlers.go:201 → :268)
  triggerExit emits OrderIntent on outbox (drains via drainOutbox in backtest)
  SimBroker handles InstrumentTypeOption (broker.go:273), supports partials
  FillReceived → runner.handleFill → strategy.handleFillConfirmation

No wiring change needed. Just pass `--strategies copytrade_v1` (or whatever
the current strategy filter flag is) when invoking omo-replay.

### 6. Reporting

Reuse `backtest.Collector` (already wired at `cmd/omo-replay/main.go:425`)
for PnL / Sharpe / drawdown / trade count. Add a copytrade-specific ledger
under `copytradereplay/ledger.go`:

  one CSV row per FillReceived event filtered by strategy=copytrade_v1:
  signal_id, author, ts_posted, ts_filled, contract_symbol, action,
  qty, fill_price, remaining_frac_after, generation, keyword

Post-process to pair BTO → STC sequences by position base key and compute
round-trip PnL. Parallel CSV for "author-stated" PnL (parsed directly from
the 90d history's BTO premium and each STC premium weighted by keyword
fraction) gives us the cheap eval for free — comparison of the two columns
tells us how much of the author's edge survives real copier execution.

End-of-run summary should print:
- Messages ingested, parsed, dropped (by reason)
- BTOs issued, filled, rejected (by reason)
- STCs issued, refused (by reason — pending, no-position, in-flight)
- HistoricalOptionsAdapter.Stats() — DoltHub hits vs synthetic
- backtest.Collector report
- Copytrade ledger vs author-stated ledger deltas

## Blockers requiring human decision before code

Priority order:

1. **Parser port**. Python regex in `services/discord-copytrade/parser.py` is
   ~20 LOC of straightforward regex plus `_resolve_expiry`. Reimplement in
   Go (`copytradereplay/parser.go`) vs. run sidecar as pre-processor
   dumping parsed JSONL. **Decision: port to Go.** Avoids Python dep in the
   replay binary, simpler CI, golden test against all 261 messages.

2. **Synthetic fallback acceptance**. **RESOLVED 2026-04-23: accept + report.**
   `HistoricalOptionsAdapter.Stats()` prints DoltHub hits vs synthetic
   coverage in end-of-run summary.

3. **Option-bar feed for running mark**. **RESOLVED 2026-04-23: single-mark
   (option b).** Rationale from agent consultation:
   - quant-analyst: option (a) EOD-repeat is *worse* than (b) for 0-9 DTE
     weeklies. Staircase input creates false-precision trail fires on stale
     data. (b)'s bias is small and symmetric for the sample's hold-time
     distribution. If signal survives (b), upgrade to real intraday (Polygon
     flat files), not synthetic interpolation.
   - Explore revealed: chandelier trail does NOT consume option bars. It
     reprices via `pos.EstimatedPremium(currentUnderlyingPrice, now)` (BSM)
     on every tick from `PriceCache`, fed by underlying bars (already
     publishing in replay). So (b) does NOT disable the trail — the
     handoff's earlier "trail will be noise" note is wrong.
   - go-architect: (b) is ~0.5 day (not 1) — 30-line swap on FillReceived
     handler. Formal port exists at `ports/options_historical_bars.go:13`.
   - Guardrail: cap holding at author STC or (expiry - 1 day), mark
     expired-unSTC'd contracts at intrinsic. Otherwise stale-mark bugs
     dominate any bias discussion.
   - Instrument chandelier arm + would-have-fire events separately from
     STC-driven exits so we can measure trail contribution.

4. **Universe gate wiring**. `universeHistory` is optional — nil skips the
   gate. Wiring it in replay makes out-of-universe drops early and
   countable, but requires constructing the history port for the replay
   context. **Default: wire it.** Low risk.

5. **Author whitelist match**. Replay must use the same copytrade TOML spec
   as production. If production's `author_whitelist` excludes authors in
   the 90d sample (TradingTheTrend, Edtrader, TB22, beendoubleyou), those
   BTOs silently drop. Confirm the spec before kicking off a full run.

6. **Freshness TTL bypass**. The HTTP handler rejects messages older than
   120s. The injector MUST publish `CopytradeSignalPayload` directly to
   the bus, bypassing the HTTP handler. (This is why the injector is an
   app-service, not a fake HTTP client.) Already baked into the design;
   flagged here so nobody "helpfully" routes through the HTTP layer.

## Work sequence — 5 days

**Day 1. Parser + domain plumbing.**
- Port Discord text parser to Go: `copytradereplay/parser.go`.
- Golden test against all 261 history rows — require 100% parse success or
  explicit drop reasons (non-action text, malformed strikes).
- Sanity check: parsed output matches live sidecar's HTTP payload shape
  (compare to `copytrade_handler.go:151 buildPayload`).

**Day 2. Injector service + replay wiring.**
- Implement `copytradereplay.Service` with `Load(path)`, `Pending() int`,
  `AdvanceTo(ctx, clockTime) error`.
- Wire into `cmd/omo-replay/main.go` under `--copytrade-history <path>` flag.
- Integrate with both the slice-to-completion path (`replaySliceCoord.
  OnTickEnd` ≈ `main.go:1618`) and the legacy single-shard loop (just before
  the next-bar iteration ≈ `main.go:1087`).
- Dry-run: confirm signals reach `handleCopytradeSignal` and produce
  `SignalCreated` events. Tracer count check.

**Day 3. Options data path.**
- Replace `cachingOptionsMarket(alpacaAdapt)` (at `main.go:386`) with
  `HistoricalOptionsAdapter` + synthetic generator when
  `--copytrade-history` is set.
- Pick option-bar feed strategy (see Blocker #3). Implement.
- Run end-to-end, verify positions open and close across the 90d window.

**Day 4. Reporting + ledger.**
- Add `copytradereplay.Ledger` subscribing to `EventFillReceived`, emitting
  per-trade CSV.
- Add parallel author-stated CSV generator from the parsed history.
- End-of-run summary block (see Reporting above).

**Day 5. Validation + tests.**
- Unit tests for parser, injector queue, ledger.
- Integration test: small synthetic history (10 messages, covered
  tickers only) through the full pipeline, asserting expected fill counts
  and ledger rows.
- Run against full 90d; triage drops (expiry misses, universe gate,
  parser failures).
- Update this doc with observed coverage + decisions for the next iteration.

## Key file pointers

Replay pipeline:
- `backend/cmd/omo-replay/main.go` — lines 188 (SyncBus), 213 (clockFn),
  294-582 backtest block, 350 option bar fetch, 386 options market wrap,
  1020 slice coord, 1577 OnTickBegin clock advance, 1618 OnTickEnd
- `backend/cmd/omo-replay/options_cache.go` — to replace/supplement

Copytrade live path (already correct):
- `backend/internal/app/strategy/runner.go` — handleCopytradeSignal :2401,
  handleCopytradeExitRejected :2522, subscriptions :745
- `backend/internal/app/strategy/builtin/copytrade_v1.go` — Init, OnEvent,
  handleBTO :272, handleSTC, handleFillConfirmation, handleEntryRejection,
  handleExitRejection
- `backend/internal/app/positionmonitor/handlers.go:201` —
  handleCopytradeExitRequest
- `backend/internal/app/positionmonitor/service.go:413` — subscription
- `backend/internal/adapters/http/copytrade_handler.go` — production entry;
  replay injector BYPASSES this

Data adapters:
- `backend/internal/app/backtest/historical_options_adapter.go` — full
  DoltHub+synthetic adapter (:208 GetOptionChain, :258 hasExpiryInDTERange,
  :283 generateSynthetic, :195 Stats)
- `backend/internal/adapters/timescaledb/historical_options_repo.go` —
  repo under the adapter
- `backend/internal/ports/options_market_data.go` — OptionsMarketDataPort
- `backend/internal/ports/historical_options.go` — HistoricalOptionsPort
- `backend/internal/adapters/simbroker/broker.go:273` — option fill path

Data + scraping:
- `services/discord-copytrade/state/history_90d.jsonl` — 261 raw messages
- `services/discord-copytrade/scrape_history.py` — re-run scraper
- `services/discord-copytrade/parser.py` — Python parser to port

Architecture reference:
- `.claude/skills/go-hexagonal/SKILL.md` — rules on placement (ports vs.
  adapters vs. app services)

## Session-start prompt for next run

> Resuming copytrade replay backtest. Read
> `_workspace/copytrade_replay_handoff.md`. Start with Day 1 — port the
> Discord parser to Go and golden-test against the 261 history rows. Before
> Day 3, get a decision from the user on Blockers #2 and #3 (synthetic
> fallback acceptance and option-bar feed strategy). The 90d history file
> is at `services/discord-copytrade/state/history_90d.jsonl`.
