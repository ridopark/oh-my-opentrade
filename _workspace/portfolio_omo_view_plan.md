# Portfolio page: merged broker + OMO view in Open Positions panel

## Why

Today (2026-05-04) we shipped a fix for fast-poll `FillReceived` emit. Prior
to that fix, 11 broker-held positions were invisible to OMO's position
monitor (no automated stops, no EOD flatten, no strategy-side exits). The
broker had them; OMO didn't know.

The dashboard's `/portfolio` page reads from `broker.GetFreshPositions()`
only. There is no UI surface that exposes OMO's view, so divergences like
today's are silent until something fails.

Goal: in the existing Open Positions panel, render both views in one
table so an operator can spot orphan/drift state at a glance.

## Scope

Single panel, single table. No new page. Outer-join broker positions and
OMO monitored positions on `symbol`. For options, both sides emit raw OCC
(broker via `domain.FormatOCCSymbol` in `ibkr/broker.go:707-758`; OMO
inserts OCC at fill time) — direct string equality is the join key, no
normalization. Each row carries a status badge:

- **SYNCED** — broker and OMO both have it; qty AND side match.
- **ORPHAN_BROKER** — broker has it, OMO doesn't. Today's bug class.
- **ORPHAN_OMO** — OMO tracks it, broker doesn't. Indicates ledger drift
  (expired OCC auto-close, manual broker close, reconciliation gap).
- **QTY_DRIFT** — both sides know it but quantities differ (partial fills
  mid-flight, repeg races).
- **SIDE_DRIFT** — symbol matches, qty matches, but `Side` differs (e.g.
  broker SELL vs OMO BUY). Most dangerous false-negative if compared on
  qty alone — must be its own status, not folded into SYNCED.

A banner above the table summarizes non-SYNCED rows when any exist.

## Files

1. **backend/internal/app/positionmonitor/service.go** — no change.
   `ListPositions()` already exists at line 984 (RLock + slice copy).
2. **backend/internal/adapters/http/portfolio_handler.go** — add a new
   sub-case `path == "monitored" && r.Method == GET` to the existing
   `ServeHTTP` switch (handler is prefix-mounted at `/api/portfolio/`,
   no mux edit needed). Add a `SetPositionMonitor` wiring setter and a
   private `monitoredJSON` response struct with explicit snake_case
   tags. ~80 LOC.
3. **backend/cmd/omo-core/http.go** (lines 115-128, next to existing
   `SetOptionQuoteProvider` / `SetLastPriceFn` / `SetDailyPnLFn` /
   `SetRepo` calls) — call
   `portfolioHandler.SetPositionMonitor(svc.posMonitor)`. ~1 LOC.
4. **apps/dashboard/app/(dashboard)/portfolio/page.tsx** — extend the
   existing `fetchData` callback (uses plain `useState` + `useEffect`
   + `setInterval(fetchData, 5000)`, NOT TanStack Query — do not
   introduce React Query). Add a third parallel fetch inside the same
   `Promise.all`, a `monitored` `useState`, the merge logic, the new
   status column, and the banner. TS interface for `MonitoredPosition`
   inline next to existing `Position` interface (page.tsx:34-51) —
   matches existing placement. ~140 LOC.

No new test files for this iteration. The backend handler is a thin
serializer; frontend has no Jest harness in this repo. Manual smoke after
the next restart will verify (ironically a great test: at startup the
bootstrap log already proves `ListPositions()` returns the 11 we see).

## Backend

### Endpoint

```
GET /api/portfolio/monitored
```

Returns:

```json
{
  "bootstrap_complete": true,
  "monitored": [
    {
      "symbol": "MSFT260518P00420000",
      "strategy": "macd_only_v1",
      "side": "BUY",
      "quantity": 2,
      "entry_price": 12.404,
      "high_water_mark": 414.485,
      "low_water_mark": 410.20,
      "entry_time": "2026-05-04T17:00:04Z",
      "exit_rules": ["PREMIUM_TARGET", "CHANDELIER_TRAIL", "STAGNATION_EXIT", "EOD_FLATTEN", "MAX_HOLDING_TIME"],
      "instrument_type": "OPTION",
      "underlying": "MSFT",
      "strike": 420,
      "option_right": "PUT",
      "expiry": "2026-05-18",
      "iv_at_entry": 0.298,
      "asset_class": "",
      "custom_state": {
        "option_premium": 12.404,
        "iv_at_entry": 0.298,
        "strike": 420
      }
    }
  ]
}
```

Field provenance — read carefully, since several response fields are
NOT direct struct fields on `domain.MonitoredPosition`:

- `symbol`, `strategy`, `side`, `quantity`, `entry_price`,
  `high_water_mark`, `low_water_mark`, `entry_time`, `instrument_type`,
  `option_right`, `expiry` (from `OptionExpiry`), `asset_class` —
  direct fields on `MonitoredPosition` (see `domain/exit_rule.go:156-235`).
- `underlying` — derived via `domain.UnderlyingFromOCC(pos.Symbol)`
  (already used at `service.go:1015`). For non-OPTION rows, equals
  `pos.Symbol`.
- `strike` — read from `CustomState["strike"]` (set at fill time; see
  `exit_rule.go:398`). Omit for non-OPTION rows.
- `iv_at_entry` — read from `CustomState["iv_at_entry"]`. Omit if absent.
- `entry_price` — for OCC positions this is the option premium at
  entry (NOT underlying); the field's semantic matches what
  `NewMonitoredPosition` was given. The plan's previous claim of
  "underlying for OCC" was wrong — verified at the call site.
- `exit_rules` — `[]string` produced by mapping
  `for _, r := range pos.ExitRules { append(out, string(r.Type)) }`.
  Do NOT serialize the full `ExitRule` struct (no json tags, would
  emit PascalCase + nested params).
- `custom_state` — pass-through of selected `CustomState` keys for
  debugging tooltips. Whitelist (`option_premium`, `iv_at_entry`,
  `strike`) only — do NOT include `pending_close` / internal flags.
- `bootstrap_complete` — top-level envelope flag from
  `positionmonitor.Service.BootstrapReady()`. Frontend suppresses the
  drift banner for one poll cycle when false. (Service exposes the
  flag via a getter; the underlying state already exists — bootstrap
  reconcile completion. ~5 LOC change to service.go for the getter.)

### Handler shape

Define a private `monitoredJSON` struct with explicit snake_case json
tags, and serialize that — DO NOT `json.Encode(monitoredPosition)`
directly. Reasons:

1. `MonitoredPosition` has inconsistent json tags (camelCase like
   `optionExpiry` plus untagged fields that default to PascalCase),
   so direct serialization ships mixed-case garbage that the frontend
   cannot consume.
2. `MonitoredPosition.CustomState` (`map[string]float64`) and
   `PendingExitOrderIDs` (`map[string]struct{}`) are headers copied
   from the live monitor under `RLock`, but the underlying maps are
   shared. JSON encoder's reflective iteration over those maps races
   with monitor writes → concurrent-map-iteration panic. Defining
   `monitoredJSON` with a whitelist of CustomState keys (read inside
   the lock by the handler) sidesteps the share entirely.

```go
type monitoredJSON struct {
    Symbol         string             `json:"symbol"`
    Strategy       string             `json:"strategy"`
    Side           string             `json:"side"`
    Quantity       float64            `json:"quantity"`
    EntryPrice     float64            `json:"entry_price"`
    HighWaterMark  float64            `json:"high_water_mark"`
    LowWaterMark   float64            `json:"low_water_mark"`
    EntryTime      string             `json:"entry_time"` // RFC3339 UTC
    ExitRules      []string           `json:"exit_rules"`
    InstrumentType string             `json:"instrument_type"`
    Underlying     string             `json:"underlying"`
    Strike         *float64           `json:"strike,omitempty"`
    OptionRight    string             `json:"option_right,omitempty"`
    Expiry         string             `json:"expiry,omitempty"`
    IVAtEntry      *float64           `json:"iv_at_entry,omitempty"`
    AssetClass     string             `json:"asset_class"`
    CustomState    map[string]float64 `json:"custom_state,omitempty"`
}
```

`EntryTime` formatted via `t.UTC().Format(time.RFC3339)` to match the
existing `opened_at` field in `/api/portfolio/positions`
(portfolio_handler.go:242). `OptionExpiry` formatted as `"2006-01-02"`
or empty if zero.

`PortfolioHandler` gains:

```go
type PortfolioHandler struct {
    // existing fields...
    posMonitor PositionMonitorReader   // narrow interface, optional
}

type PositionMonitorReader interface {
    ListPositions() []domain.MonitoredPosition
    BootstrapReady() bool
}

func (h *PortfolioHandler) SetPositionMonitor(m PositionMonitorReader) {
    h.posMonitor = m
}
```

If `h.posMonitor == nil` when the route is hit (handler used before
the setter is called, or the monitor never wired), return
`503 Service Unavailable` with `{"error":"position monitor not configured"}`.
Matches the null-check pattern of sibling optional deps (`h.optQuoter`,
`h.repo` at lines 169 and 146). NO nil-deref panic.

Narrow interface (two methods) avoids importing the full
`positionmonitor` package into `adapters/http`. Keeps hexagonal direction
clean: HTTP adapter consumes a port, monitor implements it.

`ListPositions()` already on `*positionmonitor.Service`;
`BootstrapReady()` is a thin getter to add (~5 LOC, exposes state that
already exists internally for bootstrap reconcile).

### Tenant/env scoping

The position monitor service is per-tenant + per-env, so
`ListPositions()` already returns only this instance's positions. No
filter needed in the handler.

## Frontend

### Data merge

Three fetches in parallel inside the existing `Promise.all` in
`fetchData`: existing `/api/portfolio/positions`, existing balance/PnL
fetch, and new `/api/portfolio/monitored`. Merge in a `useMemo`:

```
mergedRows = symbol-keyed outer-join({ broker, omo }) -> []Row
where Row = {
  symbol,
  broker?:  Position,        // existing
  omo?:     MonitoredPosition,    // new (TS interface inline at page.tsx:34-51 area)
  status:   "SYNCED" | "ORPHAN_BROKER" | "ORPHAN_OMO" | "QTY_DRIFT" | "SIDE_DRIFT",
}
```

Status decision (in order):

```
if !broker && omo  -> ORPHAN_OMO
if broker && !omo  -> ORPHAN_BROKER
if broker.quantity != omo.quantity -> QTY_DRIFT
if broker.side != omo.side         -> SIDE_DRIFT
else SYNCED
```

Side comparison must NOT be skipped: a flipped position (broker SELL 2
vs OMO BUY 2) is identical in symbol+qty and renders as SYNCED if side
is ignored. This is the most dangerous false-negative.

When `bootstrap_complete === false` in the response envelope, suppress
the drift banner for that poll cycle (rows still render but the page-
level warning is muted — banner re-evaluates on next 5s tick).

### Visual treatment

Reuse existing primitives — DO NOT install shadcn `Alert` (none in
`apps/dashboard/components/ui/`):

- Status pill: existing `Badge` component (already imported in
  page.tsx:11; used at lines 113, 446, 471).
- Banner: inline pattern at page.tsx:301-309 (`div` +
  `AlertTriangle` icon + Tailwind `bg-*-500/10 border-*-500/30`).
  Match the existing error-banner shape.

Per-row decoration:

- New `Monitored` column shows: strategy badge + exit-rule count + a
  status pill.
- ORPHAN_BROKER row: red left-border + badge "Not monitored".
- ORPHAN_OMO row: amber left-border + badge "Broker missing".
- QTY_DRIFT row: amber + badge "Qty drift: broker=X omo=Y".
- SIDE_DRIFT row: red + badge "Side drift: broker=BUY omo=SELL".
- SYNCED: no decoration beyond a green-tick icon in the column.

For ORPHAN_OMO rows where `broker` is undefined, the renderer reads
`instrument_type` / `underlying` / `strike` / `option_right` / `expiry`
from `omo` instead. The `closing` flag is broker-only and is undefined
on ORPHAN_OMO rows — guard accordingly.

Banner above the panel (only when count > 0 AND `bootstrap_complete`):

```
⚠ 1 orphan position not monitored by OMO: MSFT 5/18 420P
```

### Existing grouped-by-underlying behavior

`/portfolio` groups positions by underlying ticker (today's group key:
`p.underlying || p.symbol` from the broker `Position`). The merge
replaces the existing `positions: Position[]` group field with
`rows: MergedRow[]`. For ORPHAN_OMO rows the group key derives from
`omo.underlying` (already populated by the backend handler via
`UnderlyingFromOCC`). Equity grouping unchanged.

## Edge cases

- **Pending close orders.** Existing `closing: true` flag on broker rows
  stays. OMO state is independent — a position can be `closing` and still
  monitored.
- **Dust positions.** Bootstrap skips notional < $1 dust. Those would
  show as ORPHAN_BROKER. Acceptable; the operator-action is "let it
  expire" anyway, and the banner tells the truth.
- **Expired OCC mid-day.** Bootstrap downgrades to INFO log when broker
  shows nothing for an OCC past expiry. Won't appear at all (no broker
  row, no OMO row).
- **Crypto / equities.** Out of scope for this PR's UI assertions, but
  the merge logic must not assume `instrument_type === "OPTION"`.

## Risks

- **Polling frequency.** The new endpoint reads a mutex-guarded slice
  copy. Cheap. No DB hit. Page polls every 5s today; same cadence works.
- **Concurrent map iteration.** `MonitoredPosition.CustomState` and
  `PendingExitOrderIDs` are maps shared with the live monitor (slice
  copy in `ListPositions` doesn't deep-copy maps). The `monitoredJSON`
  serialization struct (see Backend / Handler shape) reads CustomState
  values into a new map under the handler's reading scope, sidestepping
  the share. Do NOT serialize `MonitoredPosition` directly.
- **Stale OMO view during bootstrap.** During the ~5s bootstrap window
  on startup, OMO returns an incomplete list. Response envelope carries
  `bootstrap_complete: false` so the frontend suppresses the banner.
  Rows still render (and may show ORPHAN_BROKER); banner clears on the
  next poll once bootstrap finishes.
- **Multi-tenant assumption.** Plan assumes one `positionmonitor.Service`
  per process (current state: tenant=`default`, env=`paper`). If a
  future deployment instantiates multiple monitors per process, the
  "no filter needed" claim breaks silently. Out of scope to redesign;
  add a TODO at the wiring site.

## Success criteria

1. After restart, every position currently in
   `positionmonitor.ListPositions()` matches a broker row with status
   SYNCED, and the drift banner is hidden.
2. If we manually close one of those at IBKR (without OMO knowing), the
   banner appears within one poll cycle: "1 orphan at OMO".
3. If a future fast-poll regression returns, every fast-poll entry shows
   ORPHAN_BROKER until restart bootstraps it.
4. A position with broker SELL 2 vs OMO BUY 2 (forced via test
   manipulation) renders as SIDE_DRIFT, NOT SYNCED.
5. During the ~5s startup bootstrap, the page renders without a banner
   flash even if a poll lands inside that window.
6. No regressions on existing /portfolio rendering for the steady-state
   path.

## Out of scope

- Auto-reconciliation actions (e.g., a "Register with OMO" button).
- Historical drift report or dashboard widget.
- Cross-tenant view.

## Sign-off needed

Before first Edit/Write, confirm:
- Variant: outer-join in one panel (this plan) — confirmed by user.
- New endpoint name `/api/portfolio/monitored` — ok?
- Status taxonomy (SYNCED / ORPHAN_BROKER / ORPHAN_OMO / QTY_DRIFT /
  SIDE_DRIFT) — ok?
- Adding `BootstrapReady() bool` getter to `positionmonitor.Service`
  (~5 LOC, exposes existing internal state) — ok?
- Response field set, especially: `entry_price` is the option premium
  at entry for OCC (not underlying); `strike` / `iv_at_entry` come from
  `CustomState`; `underlying` derived via `UnderlyingFromOCC` — ok?
- No tests for this iteration — ok?
- Branch: `feat/portfolio-omo-view`.
