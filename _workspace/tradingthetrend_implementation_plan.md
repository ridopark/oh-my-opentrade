# tradingthetrend_v1 - Implementation Plan

Companion doc to `tradingthetrend_prereg.md`. The prereg locks the rules of
*evaluation*; this doc describes the rules of *construction* - what we build,
in what order, and how we know each piece works.

This is NOT the place to lock backtest pass criteria - those live in the
prereg and override anything here on conflict.

---

## 1. Goal

Stand up a Discord-author-following options strategy `tradingthetrend_v1` that:

- Ingests morning watchlist posts of grammar `TICKER STRIKE[c|p] > TRIGGER`
  from a logged-in Discord channel via a Playwright sidecar.
- Validates entries with a 3-phase break-and-retest state machine (mirrors
  `break_retest_v1.go`) on the underlying.
- Buys the locked OCC contract at next-Friday weekly expiry (configurable).
- Manages mechanical exits: tiered premium max-stop, EOD flatten, time-stop,
  chandelier trail on the underlying.
- Shares a paper account with `copytrade_v1` under a combined author-mirror
  budget bucket.
- Is gated on a backtest that meets the prereg pass criteria before any live
  paper deployment.

Non-goals (this iteration):
- Mirroring author exits (TTT does not post STC; exits are mechanical).
- Real-money deployment (paper only; real money is a separate decision after
  60 paper sessions).
- A second strategy generalization (`EventDiscordSignalReceived` etc.).

---

## 2. Architecture (key decisions from go-architect review)

These decisions are load-bearing. They differ from the obvious-but-wrong
default; record them so a future maintainer doesn't re-litigate.

**Clone, don't generalize the sidecar.** Two services with different parser
grammars. Generalizing the sidecar plumbing on n=2 buys nothing; revisit at
n=3.

**Extract `services/discord_common/`.** The DOM helpers
(`discord_dom.py`) and bootstrap login flow (`bootstrap.py`) are invariant
across both sidecars. Pull them into a shared Python package; keep
watcher/parser/emit per-service.

**No new strike-locked selector.** `copytrade_v1` already shows the right
pattern: build the OCC symbol via `domain.FormatOCCSymbol(ticker, expiry,
right, strike)` and skip `ContractSelectionService` entirely. TTT has the same
shape (strike + right are explicit). Resolve `expiry := nearestFriday(today,
dteOffset)` inline (~5 lines). Adding a "strike-locked selector" to
`contract_selection.go` would mix two unrelated concerns under one filename.

**Bar-driven, not tick-stream.** Existing strategies (avwap_v1, copytrade_v1,
break_retest_v1, orb_v1) are all bar-driven. Going tick would force a new
ingestion path for one strategy. Use `bar.High` vs `prevBar.Close` for cross
detection. The retest state machine works on bar grain.

**Distinct event type `EventTradingTheTrendSignalReceived`.** Per-strategy
event namespaces are the existing precedent (`event.go:109-135` has 5
copytrade events). YAGNI on a generalized `EventDiscordSignalReceived` with
`source` field.

**Namespace `signal_id` with source prefix.** Sidecar emits
`tradingthetrend:{message_id}:{line_index}` to prevent dedupe collisions if a
future operator points two sidecars at the same channel.

**Backtest reuses the live state machine.** The break-and-retest evaluator
must be a function callable from both the live strategy and the backtest cmd.
Different signal sources (live event vs scraped JSONL); identical decision
logic.

---

## 3. Phase 1 - Sidecar

### 3a. Extract shared package

New: `services/discord_common/`
- `__init__.py`
- `discord_dom.py` (moved from `services/discord-copytrade/`)
- `bootstrap.py` (moved from `services/discord-copytrade/`)
- `requirements.txt` (Playwright pin shared across services)

Edit: `services/discord-copytrade/`
- `watcher.py`: change `from discord_dom import ...` to `from discord_common.discord_dom import ...`
- `bootstrap.py`: delete (becomes a thin shim or removed entirely; compose entry calls `python -m discord_common.bootstrap`)
- `Dockerfile`: add `services/discord_common/` to image build context

Verify: `pytest services/discord-copytrade/test_parser.py` still passes; existing watcher container builds and runs.

### 3b. New sidecar `services/discord-tradingthetrend/`

Files:
- `parser.py` - new grammar:
  ```
  ^\s*
  (?P<ticker>[A-Z]{1,6})\s+
  (?P<strike>\d+(?:\.\d+)?)
  (?P<right>[CP])
  \s*>\s*
  (?P<trigger>\d+(?:\.\d+)?)
  \s*$
  ```
  Returns `ParsedSignal{ticker, right (upper), strike, trigger, raw_line}`.
  Exits/AVG do not appear in this channel - parser only handles entries.
- `watcher.py` - copy from copytrade, swap parser import + emit endpoint.
- `emit.py` - POST to `/internal/tradingthetrend/signal` with header
  `X-TradingTheTrend-Secret`. Payload schema:
  `{signal_id, message_id, author, posted_at, ticker, right, strike, trigger,
  raw_line}`. `signal_id` = `f"tradingthetrend:{message_id}:{line_index}"`.
- `test_parser.py` - cases:
  - `RKLB 90c > 88.00`
  - `MSFT    425c    >   423.00` (varied whitespace)
  - `NVDA 217.5c > 215.00` (decimal strike)
  - `TSLA 425p > 421.00` (puts)
  - Multi-line message (5 signals -> 5 ParsedSignals)
  - Commentary-only line (returns empty list)
  - Malformed lines: missing `>`, missing right letter, lowercase ticker,
    negative trigger, strike with letters.
- `Dockerfile`, `docker-compose.yml`, `bootstrap.py` shim, `README.md`,
  `.env.example`, `state/` dir.

Tests pass criteria (Phase 1 done): all parser tests green; both sidecars
build under docker compose; both can authenticate (manual bootstrap step).

---

## 4. Phase 2 - Backend HTTP and event type

### Domain
File: `backend/internal/domain/event.go`
- Add `EventTradingTheTrendSignalReceived` event constant.
- Add `TradingTheTrendSignalPayload` struct mirroring sidecar emission
  (ticker, right, strike, trigger, posted_at, signal_id, message_id, author,
  raw_line). Note: NO expiry field (resolved server-side), NO price field
  (this is a stop-on-breakout, not a BTO at quoted price).

### HTTP handler
File: `backend/internal/adapters/http/tradingthetrend_handler.go`
- Modeled on `copytrade_handler.go:1-263`.
- Constructor: `NewTradingTheTrendHandler(bus, secret, freshnessTTL, log)`.
- Auth: `X-TradingTheTrend-Secret` (constant-time compare).
- Dedupe: same `seenAndRecord` logic, separate `seen` map.
- Freshness TTL: 60 seconds (per prereg + risk requirements). Older signals
  rejected with 410 Gone.
- Validation: ticker non-empty, strike > 0, trigger > 0, right in {C,P},
  posted_at parses as RFC3339.
- Publishes `EventTradingTheTrendSignalReceived` payload to bus.

Tests file: `backend/internal/adapters/http/tradingthetrend_handler_test.go`
- Auth pass/fail.
- Dedupe.
- Stale signal rejection.
- Validation errors per field.
- Successful publish.

### Wire-up
File: `backend/internal/app/bootstrap/...` (route registration; check
existing copytrade route for the exact path).
- Route: `POST /internal/tradingthetrend/signal`.
- Env var: `OMO_TRADINGTHETREND_SECRET`. Document in `.env.example`.

Tests pass criteria (Phase 2 done): handler unit tests green; manual curl
with valid secret publishes; with bad secret returns 401; replay returns 200
deduped; stale returns 410.

---

## 5. Phase 3 - Strategy `tradingthetrend_v1`

The strategy is the bulk of the project. Split into sub-phases for review.

### 5a. Strategy core (parser port + arm + cross detection)

File: `backend/internal/app/strategy/builtin/tradingthetrend_v1.go`

Components:

- **Go-side parser** (port from Python). Lives in same file. ~30 lines plus
  ~30 lines of tests. Generated golden file from Python parser (`_demo_parse`
  pattern) verifies parity.
- **TTTPhase enum**: `Idle | Watching | WaitingRetest | Confirming`. Mirrors
  `BRPhase` from `break_retest_v1.go:128-134`.
- **TradingTheTrendState**: per-symbol state with Watchlist entries,
  PrevBar, current Phase, BreakoutLevel (= signal trigger), BreakoutSide
  (Buy for calls, Sell for puts), BarsSincePhaseEntry, ATR snapshot, Strike,
  Right, Expiry. Mirrors `BreakRetestState` shape.
- **OnSignal handler**: receives `EventTradingTheTrendSignalReceived`. Adds
  Watchlist entry keyed by ticker (one active arm per ticker per day).
  Sets Phase = Watching.
- **OnBar evaluator**: dispatches by Phase to:
  - `evaluateWatching`: if `bar.Close > trigger + breakout_buffer_atr * ATR`
    AND momentum filters (body/range, ATR mult, vol surge, max wick) pass,
    advance to WaitingRetest. Reuse logic from `checkMomentumBreakout` at
    `break_retest_v1.go:738-786`.
  - `evaluateWaitingRetest`: if `bar.Low` enters `[trigger - retest_band_atr*ATR,
    trigger + retest_band_atr*ATR]`, advance to Confirming. Invalidate on
    deep retrace (`bar.Close < trigger - invalidation_atr * ATR`).
    Expire after `retest_expiry_bars`. Reuse `isInRetestZone` pattern at
    `break_retest_v1.go:788-797`.
  - `evaluateConfirming`: next bar must close above trigger + buffer with
    bullish body. Optional retest-quality gate (locked ON per prereg).
    Arm pending entry on success.
- **Entry construction**: on confirm, compute
  `expiry := nearestFriday(today, expiry_dte)`, build
  `occSymbol := domain.FormatOCCSymbol(ticker, expiry, right, strike)`,
  emit market-buy via existing `armPendingEntry` from
  `pending_entry.go`.
- **Hard cutoff**: no entries after `entry_cutoff_et = 13:30 ET` regardless
  of phase. Phase machine resets at session close.

Tests:
- Parser parity (Go vs Python golden file).
- Each phase transition: Watching -> Confirmed, WaitingRetest hit zone,
  Confirming hold-confirm fires, Confirming hold-confirm fails (back to
  Watching? or expire), Invalidation, Retest expiry, Hard cutoff.
- 0DTE skip: signal arrives Friday with no >= 2 DTE expiry available -> arm
  becomes Idle, no entry.

### 5b. Mechanical exits

Same file, separate functions invoked by `OnBar` after entry has filled.

Order of evaluation (locked per prereg):
1. Hard max stop on premium (DTE-tiered): -25% (1 DTE), -30% (2-4), -40%
   (5-7).
2. EOD flatten: 15 min before close.
3. Time-stop: if breakout hasn't extended +0.5 * ATR within
   `time_stop_bars = 12`, exit at next bar open.
4. Chandelier on UNDERLYING: `HHV(underlying_high, chandelier_lookback) -
   chandelier_atr_mult * ATR`. Close option when underlying touches trail.

All closes route through existing position-monitor close path (same as
copytrade_v1).

Tests:
- Each exit type fires in isolation with all others disabled.
- Priority order: when hard-stop and EOD trip same bar, hard-stop wins.
- Chandelier trail tracks correctly across multi-bar moves.

### 5c. Combined author-mirror budget bucket

Goal: prevent copytrade_v1 + tradingthetrend_v1 from blowing the day's risk
on correlated entries.

New: `backend/internal/app/risk/author_mirror_bucket.go` (or similar; check
where existing per-strategy budgets live).

- Bucket name `author_mirror`. Cap = `combined_bucket_cap_mult * single_strategy_cap`
  (default 1.3).
- Per-symbol notional aggregation across both strategies (one bucket key
  per underlying ticker).
- Per-minute fire-rate cap: max 2 entries / 5 min across the combined
  bucket.
- Position-conflict policy: shared account, isolate by OCC symbol so the
  position-monitor close path does not cross-trigger between strategies.
  Verify via test that closing a copytrade position does not affect a
  tradingthetrend position on a different OCC symbol of the same
  underlying.

Wire-in: both strategies' entry emission must call the bucket's pre-trade
check; reject (with reason logged) on cap breach.

Tests:
- Two simultaneous entries on same underlying: pro-rata sized down.
- 3rd entry within 5 min: rejected.
- Position-conflict isolation: closes do not cascade.

### 5d. Pre-trade gates and kill switches

Pre-trade gates (apply identically in backtest fill model + live):
- Freshness: drop signal age > 60s in live (uses message Posted At from
  Discord). In backtest, use post timestamp from JSONL.
- Trigger drift: at confirm bar evaluation, if underlying is already
  `> trigger_drift_pct = 0.5%` past trigger relative to confirm bar's close,
  reject.
- Max spread: 5% of mid (3% on mega-cap underlyings).
- Min OI: 500.
- Halt / LULD: drop if underlying halted in prior 5 minutes.
- Order type: marketable limit (mid + N ticks), NOT market order.

Live-only kill switches (do not apply in backtest):
- Strategy DD > 15% from high-water in 5 trading days -> auto-disable.
- Sidecar heartbeat missing > 45s during RTH -> auto-disable.
- Parse error rate > 10% over last 20 messages -> auto-disable.
- Realized slippage > 3x modeled for 5 trades -> auto-disable.
- Account equity within 10% of PDT $30k floor -> auto-disable new entries.

PDT enforcement: rolling 5d day-trade counter; reject 4th day-trade. Block
new entries below $30k account equity.

File: probably extends `backend/internal/app/strategy/builtin/tradingthetrend_v1.go`
plus shared kill-switch wiring in `backend/internal/app/risk/...` if not
already present.

Tests:
- Each gate rejects when expected.
- Each kill switch fires and auto-disables strategy.
- PDT counter decrements correctly across rolling 5d window.

### 5e. TOML spec and integration tests

File: `configs/strategies/tradingthetrend_v1.toml`

Locked knobs (per prereg defaults):
```
schema_version = 2
strategy_id = "tradingthetrend_v1"
asset_classes = ["OPTION"]

[entry]
expiry_dte = 0          # 0 = nearest weekly Friday; positive int = N days forward
min_dte = 2             # skip 0DTE
atr_breakout_mult = 1.5
breakout_buffer_atr = 0.2
body_range_ratio = 0.5
vol_surge_mult = 1.5
max_wick_ratio = 0.4
retest_band_atr = 0.15
retest_expiry_bars = 20
invalidation_atr = 0.5  # close past trigger by this many ATR -> invalidate
hold_confirm_required = true
retest_quality_gate = true   # LOCKED ON per prereg
entry_cutoff_et = "13:30"
trigger_drift_pct = 0.5
freshness_max_age_secs = 60

[exit]
max_stop_pct_dte_1 = 0.25
max_stop_pct_dte_2_4 = 0.30
max_stop_pct_dte_5_7 = 0.40
eod_flatten_minutes_before_close = 15
time_stop_bars = 12
time_stop_extension_atr = 0.5
chandelier_atr_period = 14
chandelier_atr_mult = 2.5
chandelier_lookback = 20

[options]
max_spread_pct = 0.05         # 0.03 for mega-caps via overrides
min_open_interest = 500
order_type = "marketable_limit"
limit_offset_ticks = 2

[risk]
combined_bucket_cap_mult = 1.3
account_equity_floor = 30000
pdt_max_day_trades = 3        # rolling 5 days; reject 4th

[sizing]
# TBD - copy structure from copytrade_v1
```

Integration tests:
- End-to-end: HTTP signal -> event -> Watching -> retest path -> entry ->
  chandelier exit -> P&L recorded.
- Same flow, hard-stop exit instead.
- Same flow, EOD exit instead.
- Same flow, signal rejected pre-entry by spread filter.

---

## 6. Phase 4 - Backtest harness

### 6a. Scrape historical channel

File: `services/discord-tradingthetrend/scrape_history.py`

- Mirror `services/discord-copytrade/scrape_history.py`.
- Output: JSONL with one row per parsed signal:
  `{message_id, posted_at (RFC3339), ticker, right, strike, trigger,
  raw_line, scrape_time}`.
- Run once at start of Phase 4; commit JSONL to `_workspace/` so the
  backtest is reproducible.
- Re-scrape 7 days later (per prereg confounder check); diff for deletions.

### 6b. Backtest cmd

File: `backend/cmd/omo-tradingthetrend-backtest/main.go`

- Reuse `backend/internal/app/backtest/runner.go` infrastructure.
- Inputs: scraped JSONL (TTT arm) + control basket generator (CONTROL arm).
- Both arms run the SAME break-and-retest state machine from
  `tradingthetrend_v1.go`. The strategy core must be callable as a
  library function from this cmd, not just from the runner.
- Control arm: per TTT-pick day, draw 4 random tickers from the trading
  universe matched on ADV decile + 20-day realized-vol decile. Same trigger
  logic (use prior session's high as the breakout level proxy or some
  deterministic rule TBD - resolve before run).
- Realistic fill model per prereg Section 5.
- Strike-availability filter per prereg Section 5.
- Output: per-trade ledger CSV + summary JSON with all prereg Section 6
  metrics computed (PF, day-bootstrap CI, Sharpe, max DD, concentration,
  holdout split, control-vs-TTT ratio, miss-rate metrics).

Tests:
- Unit test on a synthetic 5-day JSONL with known outcomes.
- Parity test: same scenario, live strategy vs backtest cmd produce
  identical entry/exit decisions.

### 6c. Run and evaluate

- Lock harness commit SHA before run (per prereg).
- Run TTT arm + CONTROL arm.
- Compare metrics against prereg Section 6 pass criteria.
- Document results in `_workspace/tradingthetrend_backtest_YYYYMMDD.md`.
- PASS -> append Results block to prereg Section 10, proceed to Phase 5.
- FAIL on any criterion -> shelve. Do NOT tune. Do NOT re-run with adjusted
  knobs.

---

## 7. Phase 5 - Live wiring (gated on Phase 4 pass)

### 7a. Deployment

- Add `discord-tradingthetrend` service to `deployments/` compose file.
- Discord login: same account as discord-copytrade. Reuse storage_state if
  channel access overlaps; otherwise bootstrap fresh.
- Wire `OMO_TRADINGTHETREND_SECRET` env var.

### 7b. Initial paper deployment

- Sizing at 25% of pre-reg-passed size for first 20 sessions.
- Daily monitoring: realized vs modeled slippage, kill-switch trigger
  counts, freshness-fail counts, retest-confirm-rate.
- Ratchet to 50% size only after 20 clean sessions; full size after another
  20.

### 7c. Real-money decision

Out of scope for this plan. Separate decision after sustained paper
performance. Risk-manager has named 7 HIGH items that must be in place
before that decision (capital/concurrency, kill switches, PDT, monitoring,
sunset rule, independent validator, size ladder).

---

## 8. Test strategy summary

| Layer | Where | What |
|---|---|---|
| Parser parity | `tradingthetrend_v1_test.go` + `test_parser.py` | Golden file fixtures, identical inputs produce identical outputs |
| HTTP handler | `tradingthetrend_handler_test.go` | Auth, dedupe, freshness, validation, publish |
| Strategy state machine | `tradingthetrend_v1_test.go` | Each phase transition, invalidation, expiry, hard cutoff |
| Mechanical exits | `tradingthetrend_v1_test.go` | Each exit in isolation, priority order |
| Budget bucket | `author_mirror_bucket_test.go` | Cap enforcement, fire-rate, position isolation |
| Integration | `runner_tradingthetrend_test.go` | End-to-end signal -> entry -> exit |
| Backtest parity | `omo-tradingthetrend-backtest` unit test | Live strategy vs backtest cmd produce identical decisions |

---

## 9. Out of scope

- Mirroring author exits (no STC channel).
- Generalized DiscordSignalReceived event.
- Cross-strategy author-following hub (handle case-by-case until n=3).
- Real-money deployment (separate decision).
- Tuning the parameters in Section 5e (locked by prereg).
- A second author from a different channel (would be a separate strategy
  with its own prereg).

---

## 10. Open decisions / risks

These are NOT prereg pass criteria - just things that need a call before or
during implementation.

1. **Control arm trigger rule.** Section 6b: "use prior session's high as the
   breakout level proxy or some deterministic rule TBD." Resolve before
   running the backtest. Candidate: use yesterday's RTH high as the
   "trigger" for each control symbol on a given day.
2. **Phase 5b sizing details.** "25% of pre-reg-passed size" is a placeholder.
   Actual dollar amounts and contract counts depend on account equity at
   that time.
3. **DoltHub coverage on small-cap weeklies.** Memory says coverage is
   uneven on OTM strikes at 0-7 DTE. Phase 4 will surface miss-rate; if
   > 25%, prereg says inadmissible and we may need to backfill from a
   different vendor.
4. **Same Discord account for both sidecars.** User confirmed yes. Single
   storage_state shared across both containers, or one per container with
   identical credentials? Investigate session-conflict risk during Phase 5a.
5. **Kill-switch implementation surface.** Section 5d adds 5 kill switches.
   Some may already exist for other strategies; check before duplicating.

---

## 11. Implementation status

Tracked in TaskList (tasks 1-13). Phase ordering:

```
Phase 1a (extract discord_common)
   |
   +-- Phase 1b (TTT sidecar) ---+
   |                              |
   +-- Phase 2 (HTTP + event) ---+--- Phase 3a (strategy core)
                                                 |
                                                 +--- Phase 3b (exits)
                                                 |
                                                 +--- Phase 3c (budget bucket)
                                                 |
                                                 +--- Phase 3d (gates / kill switches)
                                                 |
                                                 +--- Phase 3e (TOML + integration tests)
                                                              |
                                                              +--- Phase 4a (scrape)
                                                                       |
                                                                       +--- Phase 4b (backtest cmd)
                                                                                |
                                                                                +--- Phase 4c (run + evaluate)
                                                                                         |
                                                                                         +--- Phase 5 (live wire, gated on pass)
```

Phase 1a unblocks 1b. Phase 2 + 3a can run in parallel after 1b. Phases
3b-3e are sequential within Phase 3 but the strategy file accumulates as we
go. Phase 4 cannot start until Phase 3e integration tests pass.
