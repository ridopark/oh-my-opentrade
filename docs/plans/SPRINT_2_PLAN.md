# Sprint 2 Implementation Plan: Persistent Order Journal + Startup Reconciliation

> Date: 2026-04-11
> Goal: Close the biggest remaining robustness gap — currently, a crash mid-exit causes omo-core to cancel legitimate protective stops on restart.
> Estimated total effort: 1.5–2 days of focused work
> Branch: `feat/robustness-sprint-1` (continuing; can rename after Sprint 2 ships)

---

## Problem Statement

**Current failure mode:** `positionmonitor/bootstrap.go:27` calls `broker.CancelAllOpenOrders(ctx)` on every startup. That means:

1. omo-core submits a stop-loss at $150 protecting a long position
2. omo-core crashes (or is restarted for a deploy)
3. On startup, bootstrap blindly cancels the $150 stop-loss
4. Position is now naked. If price drops, we eat the full loss.

**Secondary failure mode:** OrderIntent exists only in memory until *after* broker submission succeeds. If we crash between "signal produced" and "broker.SubmitOrder returns", there is no trace of the attempt. The intent is gone, the operator has no audit trail of "why didn't we trade X?".

**Reference patterns:** NautilusTrader `ExecutionMassStatus` + ThetaGang SQLite `order_intents` table with upsert dedup. Both documented in `ROBUSTNESS.md` and `nautilus_trader.md`.

---

## Target End State

1. Every `OrderIntent` is persisted to a new `order_intents` table **before** `broker.SubmitOrder` is called. If we crash between write and submit, on restart we see the intent in the journal with status `pending_submit` and can retry or abandon.
2. After broker submission, the journal row is updated with `broker_order_id` and status `submitted`.
3. Terminal events (fill, cancel, reject) update the journal row via the existing `handleOrderUpdate` path.
4. On startup, `bootstrap.go` stops blindly cancelling broker open orders. Instead it:
   - Queries broker for open orders
   - Cross-references each against the journal
   - **Journal match** → resume tracking (do not cancel)
   - **Broker has it, journal doesn't** → log as "unmanaged order", alert operator, DO NOT auto-cancel
   - **Journal has it, broker doesn't** → mark journal as `lost` and alert (order was filled/cancelled while we were down — reconciler figures out which)
5. Unit tests cover the three reconciliation branches plus the write-ahead write-failure case.

---

## Surface Area (from the pre-plan scan)

### Files to create
- `migrations/NNN_create_order_intents.up.sql` + `.down.sql`
- `backend/internal/adapters/timescaledb/order_intent_repo.go`
- `backend/internal/adapters/timescaledb/order_intent_repo_test.go`
- `backend/internal/app/positionmonitor/order_reconcile.go` (new, separate from position reconcile for clarity)
- `backend/internal/app/positionmonitor/order_reconcile_test.go`

### Files to modify
- `backend/internal/ports/broker.go` — add `GetOpenOrders(ctx) ([]OpenOrder, error)` to `BrokerPort`
- `backend/internal/ports/repository.go` — add order-intent journal methods to `RepositoryPort`
- `backend/internal/adapters/ibkr/broker.go` (or new file in the ibkr adapter) — implement `GetOpenOrders`
- `backend/internal/adapters/alpaca/adapter.go` — implement `GetOpenOrders`
- `backend/internal/adapters/simbroker/broker.go` — implement (return empty slice)
- `backend/internal/app/execution/service.go` — `handleIntent` path: write-ahead journal, then submit, then update
- `backend/internal/app/execution/service.go` — `handleOrderUpdate` path: update journal row on terminal events
- `backend/internal/app/positionmonitor/bootstrap.go` — replace `CancelAllOpenOrders` call with journal-aware reconciliation
- `backend/cmd/omo-core/services.go` — wire the new repository methods into execution + positionmonitor

### Files I will NOT touch
- `orders` table — leave it as the post-submission broker-acked view. `order_intents` is upstream of it.
- `trades` table — unchanged.
- Risk sizer / signal enricher — they emit OrderIntent onto the event bus; execution still consumes from there.

---

## Phase A: Journal Write-Ahead

### New migration

```sql
-- migrations/NNN_create_order_intents.up.sql
CREATE TABLE order_intents (
    id                UUID PRIMARY KEY,
    idempotency_key   TEXT NOT NULL,
    tenant_id         TEXT NOT NULL,
    env_mode          TEXT NOT NULL,
    symbol            TEXT NOT NULL,
    direction         TEXT NOT NULL,
    asset_class       TEXT NOT NULL,
    order_type        TEXT NOT NULL,
    time_in_force     TEXT NOT NULL,
    quantity          DOUBLE PRECISION NOT NULL,
    limit_price       DOUBLE PRECISION,
    stop_loss         DOUBLE PRECISION,
    max_slippage_bps  INTEGER,
    strategy          TEXT,
    confidence        DOUBLE PRECISION,
    max_loss_usd      DOUBLE PRECISION,

    -- Options metadata (null for equity)
    instrument_kind   TEXT,
    instrument_json   JSONB,

    -- Lifecycle state
    status            TEXT NOT NULL,  -- pending_submit | submitted | filled | canceled | rejected | lost
    broker_order_id   TEXT,           -- populated after successful broker.SubmitOrder
    submit_error      TEXT,           -- populated if broker.SubmitOrder returned an error
    filled_qty        DOUBLE PRECISION DEFAULT 0,
    filled_avg_price  DOUBLE PRECISION DEFAULT 0,

    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    submitted_at      TIMESTAMPTZ,
    terminal_at       TIMESTAMPTZ,

    -- Free-form metadata for rationale, gate decisions, etc.
    meta              JSONB
);

CREATE INDEX idx_order_intents_status_created   ON order_intents(status, created_at);
CREATE INDEX idx_order_intents_broker_order_id  ON order_intents(broker_order_id) WHERE broker_order_id IS NOT NULL;
CREATE UNIQUE INDEX uq_order_intents_idempotency ON order_intents(tenant_id, env_mode, idempotency_key);
```

**Design decisions:**
- `id` is the domain `OrderIntent.ID` (UUID). That's our primary key.
- `idempotency_key` + tenant + env is a uniqueness constraint so we catch duplicate submissions from a resubmit retry loop.
- `status` is a plain TEXT column (not enum) so migrations stay easy. App-side constants.
- `instrument_json` captures the options-specific `Instrument` struct without denormalizing every field — it's only read on reconciliation, not in hot queries.
- The `orders` table's `intent_id` FK already exists; it now points into this table. No schema change to `orders`.

### Repository port additions

```go
// internal/ports/repository.go

type OrderIntentJournal interface {
    // SaveOrderIntent writes a new intent row with status=pending_submit.
    // Returns ErrDuplicateIntent if idempotency_key already exists.
    SaveOrderIntent(ctx context.Context, intent domain.OrderIntent) error

    // MarkIntentSubmitted records that broker.SubmitOrder returned successfully
    // with the given brokerOrderID. Transitions status pending_submit -> submitted.
    MarkIntentSubmitted(ctx context.Context, intentID uuid.UUID, brokerOrderID string, at time.Time) error

    // MarkIntentSubmitFailed records a broker-rejected submission. Transitions
    // pending_submit -> rejected and stores the error for post-mortem.
    MarkIntentSubmitFailed(ctx context.Context, intentID uuid.UUID, errMsg string, at time.Time) error

    // MarkIntentTerminal records a terminal lifecycle event (fill, cancel, reject)
    // from the broker order stream. Looks up by brokerOrderID since that is what
    // the broker stream carries.
    MarkIntentTerminal(ctx context.Context, brokerOrderID string, status string, filledQty, filledAvgPrice float64, at time.Time) error

    // OpenIntents returns all journal rows in non-terminal status. Used by
    // startup reconciliation. Bounded to rows created within the last lookback
    // to avoid loading stale rows from previous sessions during dev work.
    OpenIntents(ctx context.Context, tenantID string, envMode domain.EnvMode, lookback time.Duration) ([]domain.OrderIntentJournalRow, error)

    // MarkIntentLost records that startup reconciliation could not find a
    // broker match for a journal row in submitted state. Transitions
    // submitted -> lost.
    MarkIntentLost(ctx context.Context, intentID uuid.UUID, at time.Time) error
}
```

`RepositoryPort` embeds `OrderIntentJournal` (or we add the methods directly — depends on existing interface layering; agent to decide).

### Execution service: write-ahead

In `execution/service.go handleIntent`, the critical change is ordering:

```go
// BEFORE (current):
//   validate -> risk check -> gate chain -> broker.SubmitOrder -> save BrokerOrder

// AFTER:
//   validate -> risk check -> gate chain
//   -> journal.SaveOrderIntent(intent) [status=pending_submit]
//   -> broker.SubmitOrder(intent)
//   -> on success: journal.MarkIntentSubmitted(id, brokerOrderID)
//   -> on failure: journal.MarkIntentSubmitFailed(id, err)
//   -> save BrokerOrder (existing code)
```

**Important:** The write-ahead save must succeed before we call `broker.SubmitOrder`. If the journal write fails (DB is down), we do NOT submit the order — the whole point is to never submit without a durable record. Log, alert, and skip.

### Fill handling: terminal update

In `execution/service.go handleOrderUpdate` (already routes to `handleFillWithPrice` for fills, `cleanupPendingOrder` for cancels/rejects), add a journal update:

```go
// On fill event:
journal.MarkIntentTerminal(ctx, brokerOrderID, "filled", filledQty, filledAvgPrice, at)

// On canceled/expired/rejected:
journal.MarkIntentTerminal(ctx, brokerOrderID, update.Event, filledQty, filledAvgPrice, at)
```

The existing `orders` table writes continue unchanged — they are the broker-view source of truth for completed orders. The `order_intents` journal is the upstream intent-view.

---

## Phase B: Startup Reconciliation

### New broker port method

```go
// internal/ports/broker.go

// OpenOrder is the broker's view of a working order that existed
// before this process started. Used for startup reconciliation against
// the intent journal.
type OpenOrder struct {
    BrokerOrderID string
    Symbol        string
    Side          string  // "buy" / "sell"
    Quantity      float64
    OrderType     string  // "limit" / "market" / "stop"
    LimitPrice    float64
    StopPrice     float64
    Status        string  // "submitted" / "accepted" / "working"
    CreatedAt     time.Time
}

type BrokerPort interface {
    // ... existing methods
    GetOpenOrders(ctx context.Context) ([]OpenOrder, error)
}
```

### IBKR implementation

The ibsync `ib.OpenTrades()` API is already used by `drain.go` (Sprint 1 Phase B) — reuse the same pattern. Convert each `ibsync.Trade` into an `OpenOrder`, filtering to working states (Submitted, PreSubmitted, Accepted).

### Alpaca implementation

Use `alpaca.GetOrders(alpaca.GetOrdersRequest{Status: "open"})` or equivalent — check their SDK.

### SimBroker implementation

Return `[]OpenOrder{}, nil`. SimBroker is in-process; there is nothing to reconcile.

### Bootstrap rewrite

Current `positionmonitor/bootstrap.go:27`:

```go
// BEFORE
if canceled, err := s.broker.CancelAllOpenOrders(ctx); err != nil {
    s.log.Error().Err(err).Msg("failed to cancel pre-existing open orders on bootstrap")
}
```

New flow (replace):

```go
// AFTER
brokerOpen, err := s.broker.GetOpenOrders(ctx)
if err != nil {
    s.log.Error().Err(err).Msg("failed to query broker open orders on bootstrap; falling back to cancel-all for safety")
    _, _ = s.broker.CancelAllOpenOrders(ctx)
    return
}

journalRows, err := s.intentJournal.OpenIntents(ctx, s.tenantID, s.envMode, 48*time.Hour)
if err != nil {
    s.log.Error().Err(err).Msg("failed to load intent journal on bootstrap; falling back to cancel-all for safety")
    _, _ = s.broker.CancelAllOpenOrders(ctx)
    return
}

// Index the journal by broker_order_id for O(1) lookup.
journalByBrokerID := make(map[string]domain.OrderIntentJournalRow, len(journalRows))
for _, row := range journalRows {
    if row.BrokerOrderID != "" {
        journalByBrokerID[row.BrokerOrderID] = row
    }
}
brokerByID := make(map[string]ports.OpenOrder, len(brokerOpen))
for _, o := range brokerOpen {
    brokerByID[o.BrokerOrderID] = o
}

// Case 1: broker has it, journal has it -> resume tracking
// Case 2: broker has it, journal does NOT -> unmanaged; log + alert; DO NOT cancel
// Case 3: journal has it (submitted), broker does NOT -> lost; mark + alert

var unmanaged []ports.OpenOrder
for _, o := range brokerOpen {
    if _, ok := journalByBrokerID[o.BrokerOrderID]; ok {
        // Case 1: hand off to execution service's pendingOrders map so fills land correctly.
        s.resumeTracking(o)
        continue
    }
    unmanaged = append(unmanaged, o)
}
if len(unmanaged) > 0 {
    s.log.Warn().Int("count", len(unmanaged)).Msg("broker has open orders with no journal entry — leaving in place for operator review")
    s.notifier.NotifySync(fmt.Sprintf("⚠️ Startup found %d unmanaged broker orders (not in journal). Review manually.", len(unmanaged)))
}

for _, row := range journalRows {
    if row.Status != "submitted" || row.BrokerOrderID == "" {
        continue
    }
    if _, ok := brokerByID[row.BrokerOrderID]; ok {
        continue
    }
    // Case 3: journal has an order the broker doesn't. Fill or cancel happened while we were down.
    // The fill reconciliation logic (reconcileExecutions in the existing order stream) will pick up
    // any completed fills; here we just mark the journal row.
    if err := s.intentJournal.MarkIntentLost(ctx, row.ID, time.Now()); err != nil {
        s.log.Error().Err(err).Str("intent_id", row.ID.String()).Msg("failed to mark lost intent")
    }
}
```

**The `s.resumeTracking(o)` method is new** — it registers the order back into the execution service's `pendingOrders` map so that when the order stream sends us the eventual fill, the existing handler wires it through. Without this step, fills would land with no matching pending order and the `handleStreamFill` retry loop would time out.

### Safety fallback

If EITHER the broker query OR the journal query fails, we fall back to `CancelAllOpenOrders` — the current behavior. This preserves correctness at the cost of cancelling valid stops, which is no worse than today. We log loudly so operators know the fallback kicked in.

---

## Phase C: Tests

### Unit: journal repo

`order_intent_repo_test.go` — table-driven tests hitting a test Postgres (the same `TimescaleDB` fixture pattern the existing repo tests use):

- SaveOrderIntent inserts a row with status=pending_submit
- Duplicate idempotency_key is rejected with ErrDuplicateIntent
- MarkIntentSubmitted transitions pending_submit -> submitted and sets broker_order_id + submitted_at
- MarkIntentSubmitted on already-terminal row is a no-op
- MarkIntentSubmitFailed transitions pending_submit -> rejected with error
- MarkIntentTerminal looks up by broker_order_id and transitions -> filled/canceled/rejected
- OpenIntents returns only non-terminal rows within the lookback window
- MarkIntentLost transitions submitted -> lost

### Unit: bootstrap reconciliation

`order_reconcile_test.go` — pure logic test with a mock broker and mock journal:

- Empty broker + empty journal → no-op
- Broker has 2 orders, journal has both → both resumed, nothing cancelled, no alerts
- Broker has 1 order, journal empty → unmanaged alert fired, order NOT cancelled
- Broker empty, journal has 1 submitted → MarkIntentLost called, alert fired
- Mixed: 1 matched, 1 unmanaged, 1 lost → correct routing for each
- Broker query error → CancelAllOpenOrders fallback invoked
- Journal query error → CancelAllOpenOrders fallback invoked

### Integration: end-to-end

One test that stands up a real execution service with simbroker + sqlite journal:

- Submit an order intent
- Assert journal row is pending_submit
- Let broker submit succeed
- Assert journal row is submitted with broker_order_id
- Simulate a fill event from the order stream
- Assert journal row is filled

---

## Execution Order

```
Day 1:
  09:00 - Phase A.1: migration + repo skeleton           [2h]
  11:00 - Phase A.2: execution service write-ahead       [2h]
  14:00 - Phase A.3: terminal event update path          [1h]
  15:00 - Phase A.4: unit tests for journal repo         [2h]
  17:00 - commit Phase A: `feat(robustness): persist order intents before broker submission`

Day 2:
  09:00 - Phase B.1: GetOpenOrders port + IBKR impl      [2h]
  11:00 - Phase B.2: Alpaca + SimBroker impls            [1h]
  12:00 - Phase B.3: bootstrap rewrite                   [2h]
  15:00 - Phase B.4: reconciliation tests                [2h]
  17:00 - commit Phase B: `feat(robustness): resume broker open orders from journal on startup`

Day 3 (if needed):
  09:00 - Phase C: integration test + staging smoke      [2h]
  11:00 - bugfixes + polish
  14:00 - merge-ready
```

**Two commits, not four** — Phase A is one atomic unit (write-ahead), Phase B is another (startup reconcile). Splitting further creates commits that either regress behavior (A without B still cancels all orders) or add dead code (B without A has an empty journal).

---

## Rollout Strategy

**Feature flag:** new env var `OMO_ORDER_JOURNAL_ENABLED` (default false for the first deploy). When false, execution skips the journal write and bootstrap uses the existing `CancelAllOpenOrders` path. When true, the full journal + reconcile flow runs.

**Why:** we can ship the code and the migration without immediately changing runtime behavior, validate on staging, then flip the flag. If something goes wrong we flip back without reverting commits.

**Removal:** flag gets removed in Sprint 3 once we're confident in the journal. Do not leave the flag permanent — it becomes a second code path that rots.

---

## Risks & Unknowns

1. **DB write latency on the hot path.** Adding a synchronous INSERT before every `broker.SubmitOrder` adds one round-trip (~1–3ms for TimescaleDB on localhost). For paper trading this is fine; for eventual live high-frequency we may need a write-ahead log or async queue. Acceptable for now; document as a future concern.

2. **Broker open order query on startup is slow.** IBKR `ib.OpenTrades()` is already cached and fast (Sprint 1 confirmed). Alpaca REST call is ~100–300ms. Cheap. Not a concern.

3. **Orphaned "unmanaged" orders accumulate.** If an operator manually submits an order through TWS and doesn't clean it up, every startup alerts about it. Mitigation: add a `--ack-unmanaged` admin CLI later, or a config file listing known-accepted external order IDs. Out of scope for this sprint.

4. **Journal row for a lost intent might actually be filled.** Case 3 above (journal says submitted, broker doesn't have it) can mean either "order filled while we were down" or "order cancelled while we were down". Marking as `lost` doesn't distinguish. The existing `reconcileExecutions` path (IBKR order stream) queries fill history and will catch the fill, which creates the correct Trade row — but the `order_intents` row will say lost even if the actual outcome was a fill. Accept this discrepancy for the first version; a later pass can query broker executions by broker_order_id and flip lost → filled retroactively.

5. **ibsync OrderID types.** IBKR uses int OrderID; broker_order_id in our schema is TEXT. The existing `orders` table already stores it as TEXT, so we just stringify. Non-issue but worth noting.

---

## Acceptance Criteria

Sprint 2 is done when:

- [ ] `order_intents` migration up+down clean on fresh DB and existing DB
- [ ] OrderIntent persists before every broker submission (verified by log + DB query)
- [ ] A deliberate crash mid-submit leaves a `pending_submit` row in the journal
- [ ] Bootstrap reconciliation resumes tracking of a matched broker order (no cancel)
- [ ] Bootstrap reconciliation alerts on unmanaged orders and does NOT cancel them
- [ ] Bootstrap reconciliation marks lost intents and alerts
- [ ] Feature flag gates the whole thing; default-off behavior is byte-identical to today
- [ ] All unit tests green
- [ ] Integration test green
- [ ] 24h staging run with flag enabled shows no new errors
- [ ] `ROBUSTNESS.md` Gap #2 and Gap #4 marked complete

---

## What This Sprint Does NOT Fix

- **Intent-level retry logic.** If broker submission fails transiently (rate limited), we mark rejected and give up. A retry queue is Sprint 3+.
- **Gate decision audit log.** Pre-gate rejects (intent was produced, killed by SpreadGuard before SubmitOrder) are logged but still not persisted. Could be added to the journal as `status=gated` but expands scope — defer to Sprint 3.
- **Cross-session order history reporting.** The journal exists but there's no UI for it. Dashboard integration is a frontend task.
- **Schema versioning of the journal itself.** If we later change the columns, there's no migration path for in-flight rows. Not a concern at current scale.

---

## Reference Patterns

- **NautilusTrader `ExecutionMassStatus`** — [`nautilus_trader.md`](../../tmp/others/nautilus_trader.md) section 2. Similar startup query → reconcile → resume pattern, but NautilusTrader also generates synthetic fill events for missing fills which we are deferring.
- **ThetaGang SQLite journal** — [`thetagang.md`](../../tmp/others/thetagang.md) section 6. Nine tables with upsert dedup; we're starting with a single `order_intents` table and will grow from there if needed.
- **Robustness analysis** — [`ROBUSTNESS.md`](../../tmp/others/ROBUSTNESS.md) gaps #2 and #4. This sprint closes both.
