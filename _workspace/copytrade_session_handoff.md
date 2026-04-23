# copytrade_v1 — session handoff (2026-04-23)

Pick up here next session. The plan was signed off in the prior
conversation; this doc exists so a fresh session can continue without
re-deriving architecture.

## Decisions locked (do not re-ask)

- Full-stack option A: register as a normal strategy, integrate with
  risk_sizer, position monitor, PnL aggregator, lifecycle.
- TOML aligns with existing conventions (avwap_v4 shape): bps-of-equity
  sizing, `[lifecycle] paper_only = true`, `[options]` section, etc.
- All authors in the source channel are pre-vetted — whitelist optional.
- Partial-exit: mirror partials via keyword-table from TOML `[params]`.
- Sizing via existing risk_sizer; TOML-configurable bps.
- Expiry: mirror exactly what author posts (no DTE filter).
- Paper-only for v1.
- Runs on workstation in a Docker container (Playwright).
- Exits from STC keywords are primary; safety net is **CHANDELIER_TRAIL
  with external arm** (arm on first STC-partial). No STAGNATION_EXIT,
  no EOD_FLATTEN.
- Out-of-universe ticker → skip signal + notify via existing
  `DISCORD_WEBHOOK_URL` (reuses `notification.DiscordNotifier`).
- Login stays manual via one-time bootstrap to `storage_state.json` in a
  container volume. No credentials in .env.

## What's shipped (uncommitted)

All green, full test suites pass for touched packages.

### Python sidecar
- `services/discord-copytrade/parser.py` + `test_parser.py` (40 tests)
- `services/discord-copytrade/discord_dom.py`
- `services/discord-copytrade/emit.py`
- `services/discord-copytrade/watcher.py`
- `services/discord-copytrade/bootstrap.py`
- `services/discord-copytrade/Dockerfile`
- `services/discord-copytrade/docker-compose.yml`
- `services/discord-copytrade/requirements.txt`
- `services/discord-copytrade/.env.example`
- `services/discord-copytrade/README.md`
- `services/discord-copytrade/_demo_parse.py` — can delete after review

### Go domain
- `backend/internal/domain/event.go` — added `EventCopytradeSignalReceived`
  and `EventChandelierTrailArm` constants
- `backend/internal/domain/copytrade.go` (new): `CopytradeAction`,
  `CopytradeSignalPayload`, `ChandelierTrailArmPayload`
- `backend/internal/domain/strategy/contract.go` — appended
  `CopytradeSignal` strategy-layer event struct

### Go engine: force_contract tags
- `backend/internal/app/strategy/risk_sizer.go` —
  `handleOptionsSignal` branches on pinned-contract tags
- `backend/internal/app/strategy/risk_sizer_force_contract.go` (new):
  `extractForcedContract`, `findPinnedContract`, tag-key constants
  `TagForceExpiry`, `TagForceStrike`, `TagForceRight`, `TagForceRefPremium`
- `backend/internal/app/strategy/risk_sizer_force_contract_test.go` (new):
  8 tests

### Go engine: CHANDELIER_TRAIL external-arm
- `backend/internal/app/positionmonitor/evaluators.go` —
  `activate_mode=1` branch reads `chandelier_ext_armed` +
  `chandelier_ext_peak` CustomState; tracks running peak; fires on
  giveback. MFE-mode behavior unchanged.
- `backend/internal/app/positionmonitor/handlers.go` —
  `handleChandelierTrailArm` handler that finds the matching position by
  contract symbol and seeds the CustomState keys.
- `backend/internal/app/positionmonitor/service.go` — subscribes to
  `EventChandelierTrailArm` in `Start`.
- `backend/internal/app/positionmonitor/evaluators_test.go` — 6 new
  subtests under `TestEvaluateChandelierTrail_ExternalArm`.

## Remaining tasks

### Task #4: HTTP handler (~120 LOC)
- `backend/internal/adapters/http/copytrade_handler.go`
- POST `/internal/copytrade/signal`
- Auth via shared secret (env `OMO_COPYTRADE_SECRET`, header
  `X-Copytrade-Secret`)
- Unmarshal JSON to `CopytradeSignalPayload`, publish on event bus
- Dedup by `signal_id` (short-lived LRU or relay to strategy state)
- Unit tests: auth pass/fail, malformed payload, valid happy path

### Task #5: Copytrade strategy + state (biggest task)
- `backend/internal/app/strategy/builtin/copytrade_v1.go` (new)
- Implement the `strategy.Strategy` interface (Meta, WarmupBars, Init,
  OnBar, OnEvent)
- OnBar: no-op (copytrade is event-driven)
- OnEvent: dispatches on `CopytradeSignal` (strategy-layer type)
  - BTO → build `Signal{Type: SignalEntry, Side: SideBuy}` with tags:
    `ref_price`, `force_expiry`, `force_strike`, `force_right`,
    `force_ref_premium`. Let risk_sizer do sizing via the pinned-contract
    path added in Task #8.
  - STC → look up position by (author, ticker, expiry, strike, right,
    generation); apply keyword-table fraction to remaining contracts;
    build exit `Signal`. On FIRST STC-partial per position, also emit
    `domain.ChandelierTrailArmPayload` via `Context.EmitDomainEvent`.
  - AVG → skip if `skip_avg=true`; log only.
  - Out-of-universe → call `UniverseHistoryPort.WasTradable`; if false,
    post to `notification.DiscordNotifier` and drop.
- State: map keyed by (author, ticker, expiry, strike, right, generation)
  → `CopytradePosition{RemainingQty, Fills, TrailArmed}`. Marshal/
  Unmarshal for persistence.
- Runner plumbing: new `handleCopytradeSignal` in `runner.go` that
  subscribes to `EventCopytradeSignalReceived`, finds the copytrade
  instance, calls `inst.OnEvent(instCtx, ticker, CopytradeSignal{...})`.
  Strategy is single-instance, catch-all — instance lookup cannot use
  symbol routing. Simplest: iterate `r.instances` looking for
  `strategy.ID == "copytrade_v1"`.

### Task #6: Config + wiring (~80 LOC)
- `configs/strategies/copytrade_v1.toml` (see signed-off shape below)
- `backend/cmd/omo-core/services.go` — register
  `NewCopytradeStrategy()` in the registry (line 406-427 area).
- Register HTTP handler in whatever wires
  `backend/internal/adapters/http/handler.go` routes.
- Env: `OMO_COPYTRADE_SECRET` in `.env` + `.env.example`.

### Task #7: Dry run
- Fill `.env` for sidecar; run bootstrap to save storage_state
- `docker compose up -d` the sidecar
- Start omo-core; tail logs for `CopytradeSignalReceived` events
- Cross-check Discord post log vs paper order tape

## Signed-off TOML shape (task #6)

Drop `STAGNATION_EXIT` and `EOD_FLATTEN` from the earlier draft.
CHANDELIER_TRAIL with `activate_mode = 1` is the only exit rule; author
STC posts are the primary exit path.

```toml
schema_version = 2

[strategy]
id = "copytrade_v1"
version = "1.0.0"
name = "Copytrade v1 - Discord Signal Mirror"
description = "Mirrors options trades posted to a vetted Discord channel."
author = "system"
created_at = "2026-04-23T00:00:00Z"

[lifecycle]
state = "PaperActive"
paper_only = true

[routing]
symbols = []
timeframes = ["1m"]
priority = 90
conflict_policy = "priority_wins"
exclusive_per_symbol = false
watchlist_mode = "dynamic"
asset_classes = ["OPTION"]
allowed_directions = ["LONG"]

[screening]
description = "No screening. Channel membership is the vetting layer."

[params]
author_whitelist = []
freshness_ttl_secs = 120
skip_avg = true
require_universe_membership = true

# Trailing stop armed externally on first STC-partial per position.
trail_on_partial_enabled = true
trail_giveback_pct = 0.15

# Partial-exit keyword table. First match wins. Fraction applies to
# REMAINING contracts.
[[params.partial_fractions]]
keyword = "all out"
fraction = 1.00
[[params.partial_fractions]]
keyword = "stop hit"
fraction = 1.00
[[params.partial_fractions]]
keyword = "stopped"
fraction = 1.00
[[params.partial_fractions]]
keyword = "mostly out"
fraction = 0.75
[[params.partial_fractions]]
keyword = "half out"
fraction = 0.50
[[params.partial_fractions]]
keyword = "taking more"
fraction = 0.33
[[params.partial_fractions]]
keyword = "partial"
fraction = 0.33
[[params.partial_fractions]]
keyword = "a few"
fraction = 0.25
[[params.partial_fractions]]
keyword = "trim"
fraction = 0.25

default_stc_fraction = 0.33

risk_per_trade_bps = 300
max_position_bps = 1000
max_positions = 5

[hooks]
signals = { engine = "builtin", name = "copytrade_v1" }

[dynamic_risk]
enabled = false

# --- Exit rules (safety only; primary exits driven by STC Discord posts) ---

[[exit_rules]]
type = "CHANDELIER_TRAIL"
[exit_rules.params]
activate_mode = 1.0
giveback_pct = 0.15

# --- Options ---

[options]
enabled = true
max_contracts = 20
limit_spread_pct = 1.00
limit_buffer_bps = 1000
stale_cancel_secs = 60

[options.defaults]
min_open_interest = 25
max_spread_pct = 0.25
max_iv = 2.0
```

## Critical behavioral notes for the next session

1. **Strategy is single-instance, not per-symbol.** `symbols = []` +
   `watchlist_mode = "dynamic"` means the runner must not try to
   instantiate per symbol. The strategy owns its own per-ticker state
   internally. Runner dispatch is by strategy ID, not symbol.

2. **BTO entry signals must set `force_contract` tags.** The pinned-
   contract path added in Task #8 is a no-op unless all of
   `TagForceExpiry` + `TagForceStrike` + `TagForceRight` are set. Code
   all three in the OnEvent BTO branch.

3. **First-partial detection.** A STC whose keyword maps to a
   fraction < 1.0 (i.e., anything except "all out"/"stop hit"/"stopped")
   is the trigger to emit `ChandelierTrailArmPayload`. Track with a
   bool flag in `CopytradePosition` so we don't re-emit on subsequent
   partials.

4. **Re-entry generations.** After "all out" the position is closed.
   If the same author posts a new BTO on the same ticker/expiry/strike
   later, that's a new position; bump the generation counter in the
   state key.

5. **Freshness TTL.** Compare `PostedAt` to `time.Now()` at the HTTP
   handler. Reject signals older than `freshness_ttl_secs` to avoid
   executing on stale messages during a sidecar catch-up after a
   restart.

6. **Universe check timestamp.** Call
   `UniverseHistoryPort.WasTradable(ctx, ticker, PostedAt)` — NOT
   `time.Now()` — so backfilled/replayed messages check the state at
   the time the author posted.

## Session handoff prompt suggestion

Paste this into the next session to continue:

> Continuing copytrade_v1 implementation. Read
> `_workspace/copytrade_session_handoff.md` for full context and
> decisions. Pick up at Task #4 (HTTP handler) and work through to
> Task #7 (dry run).
