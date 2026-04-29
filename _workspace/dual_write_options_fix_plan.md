# Option-fill dual-write fix plan (2026-04-27)

## Motivation

Every option fill in live IBKR mode is recorded twice in `trades`:
- **Path B** (live): sub-second time, `instrument_type=OPTION`, full option metadata, `execution_id=""`, intent rationale.
- **Path A** (next boot): whole-second time, `instrument_type=EQUITY`, NULL option_symbol, real `0000e242.*` exec_id, rationale `reconcile_fills_on_boot: recovered leg ...`.

Today's `trades` rows for 2026-04-27: 31 total, 23 from `reconcile_fills_on_boot` (all EQUITY, no option_symbol) + 8 live OPTION rows. Confirmed via direct DB query — Path A's rationale always names `reconcile_fills_on_boot`, never `insertFillLeg`.

This is the structural cause of:
- **Bug #2c**: dual-write of every option fill.
- **Bug #3**: exit-SELL "missing option_right/expiry" — those rows ARE the boot-reconciled leg rows that lost their option metadata at scan time.
- **Bug #2b** (partial): the 2x trade_count inflation. (The 78x inflation observed on 04-09/10/13 needs the OTHER reconciler — call site #1 reconcileOnBoot — fixed separately; not in scope here.)

## Root cause (two stacked defects in `reconcileFillsOnBoot`)

### Defect A — dedup miss

`backend/internal/adapters/timescaledb/repository.go:1103` GetRecordedExecutionIDs filters out rows where `execution_id` is NULL or empty:

```sql
SELECT execution_id FROM trades
 WHERE account_id = $1 AND env_mode = $2 AND time >= $3
   AND execution_id IS NOT NULL AND execution_id <> ''
```

Live writers in IBKR mode go through `insertFillLeg` from either:
- `handleStreamFill` (line 1672) — sets `executionID = update.ExecutionID` (real)
- `handleFillWithPrice` from `recordFillFromDetails` (line 2634) — sets `executionID = ""`

In production today the WS `orderStream` is silent for IBKR option fills (consistent with the line-1241 comment "WS stream active but unreliable on IBKR paper"). So `fastPollPosition` is the live writer, and every live row has empty exec_id.

`reconcileFillsOnBoot` then calls `broker.GetAllFills()`, gets `0000e242.*` exec IDs, checks them against the recorded set (which is empty for these orders because the live row has no exec_id), and reinserts every leg as a duplicate.

### Defect B — option-column scan mismatch

`queryGetOrderByBrokerID` (repository.go:53) and `GetOrderByBrokerOrderID` (repository.go:1068) both ignore the option columns (`instrument_type`, `option_symbol`, `underlying`, `strike`, `expiry`, `option_right`). The orders table has them populated correctly, but they never reach the caller.

So `service.go:563` checks `existing.InstrumentType == domain.InstrumentTypeOption` — always false (zero value). The option-enrichment block is dead code, and reconciled rows fall through to default `EQUITY` with NULL fields.

## Fix

Two surgical changes, both in `backend/internal/adapters/timescaledb/repository.go`. No service-layer changes.

### Change 1 — load option columns in `GetOrderByBrokerOrderID`

`queryGetOrderByBrokerID`: add 6 columns to the SELECT.

```sql
SELECT time, account_id, env_mode, intent_id, broker_order_id, symbol, side,
       quantity, limit_price, stop_loss, status,
       COALESCE(filled_at, '0001-01-01'::timestamptz),
       COALESCE(filled_price, 0), COALESCE(filled_qty, 0),
       COALESCE(strategy, ''), COALESCE(rationale, ''), COALESCE(confidence, 0),
       COALESCE(instrument_type, ''),
       COALESCE(option_symbol, ''),
       COALESCE(underlying, ''),
       COALESCE(strike, 0),
       COALESCE(expiry, '0001-01-01'::timestamptz),
       COALESCE(option_right, '')
  FROM orders WHERE broker_order_id = $1 LIMIT 1
```

`GetOrderByBrokerOrderID`: extend Scan to read into `o.InstrumentType` (cast to `domain.InstrumentType`), `o.OptionSymbol`, `o.Underlying`, `o.Strike`, `o.Expiry`, `o.OptionRight`. Verify `domain.BrokerOrder` already has these fields (it does — used by `backfillFromBrokerHistory` at service.go:695-701). Use a local `var instType, optSym, optRight string` and `var expiry time.Time` in the Scan, then assign to typed fields after.

This change is sufficient to fix Bug #3 metadata corruption on Path A rows.

### Change 2 — broaden boot-reconcile dedup beyond exec_id

Add a new repo method (matches the existing port pattern):

```go
// GetReconciledOrderIDs returns the set of broker_order_ids that already
// have at least one trade row in the time window. Used by boot fill
// reconciliation to skip orders whose live fill events were already
// persisted (with or without execution_id).
GetReconciledOrderIDs(ctx, tenantID, envMode, since) (map[string]struct{}, error)
```

SQL:
```sql
SELECT DISTINCT t.execution_id, o.broker_order_id
  FROM trades t
  JOIN orders o ON o.broker_order_id = ... -- not directly joinable; trades has no broker_order_id column
```

Wait — `trades` has no `broker_order_id` column. The link from `trades` back to `orders` goes through the order-side update only. So we can't reliably ask "does broker_order_id X already have a trade row?" from `trades` alone.

Two options:

**Option 2a — match by (symbol, side, time-window, qty)**: in `reconcileFillsOnBoot`, before inserting a leg for `f.ExecutionID`, query `trades` for any row matching `(account_id, env_mode, symbol, side, time within ±2s of f.FilledAt, quantity = f.Qty)` and skip if found. Robust against missing exec_id, but races on rapid identical legs (same qty, same second). For multi-leg orders that's a real risk (NVDA today: 3 legs of 1+1+2 — the two qty=1 legs are indistinguishable by this match). NOT acceptable.

**Option 2b — add `broker_order_id` to trades, backfill, then dedup by it**: schema change. Larger blast radius but the right structural fix. The orders adapter already knows the broker_order_id at write time (`insertFillLeg` is called inside `handleStreamFill`/`handleFillWithPrice` which have it as a parameter). Trade column addition is additive (NULL-safe), no existing reader breaks.

**Option 2c — make the LIVE writer always set `execution_id`**: when WS isn't firing, fastPollPosition could call `broker.GetAllFills()` (already exists for IBKR via FillLister) at the moment of detection, find the exec IDs that match this brokerOrderID, and pick one to stamp on the Path B row. Doesn't require schema change. But adds a synchronous broker call in the hot path and is fragile if the broker hasn't recorded the fill yet at poll time.

**Recommended: 2b** (schema change). It's the only option that durably closes the structural duplication and stays correct on every variant: WS-only, fastPoll-only, partial-WS partial-fastPoll, and the boot-reconcile case.

Subtasks for 2b:
1. Migration: `ALTER TABLE trades ADD COLUMN broker_order_id TEXT;` Add to `tradeInsertArgs` query template. Both `SaveTrade` and `RecordFill` propagate `trade.BrokerOrderID`. Add to `domain.Trade`.
2. Live writers (`insertFillLeg`, `handleFill`, `recordSweepFill`, `reconcileOnBoot`, `backfillFromBrokerHistory`, `reconcileFilledOrder`) set `trade.BrokerOrderID` from their pendingOrder/order context.
3. `reconcileFillsOnBoot` adds a second dedup gate: build `recordedOrderIDs` from `SELECT DISTINCT broker_order_id FROM trades WHERE ... AND broker_order_id IS NOT NULL`. If `f.BrokerOrderID` is in that set AND we have at least one trade row for that order whose qty sums to >= broker's cumulative for that order, skip. Otherwise still allow the per-leg insert (so a partially-recorded multi-leg can be completed).
4. Backfill: one-time `UPDATE trades SET broker_order_id = orders.broker_order_id FROM orders WHERE trades.account_id = orders.account_id ...` — but the join key is hard. Defer; new column starts NULL for historical rows, fills going forward. Cleanup of historical duplicates is a separate sweep below.

### Change 3 (out of scope, recorded for follow-up)

`reconcileOnBoot` (call site #1, the 5-min ticker) uses `s.nowFn()` when `details.FilledAt` is zero, defeating its own `(trade_id, time)` PK dedup. That is the source of the 78x trade_count inflation seen on 04-09. Tracked separately as Bug #2b-ii.

## Tests

In `backend/internal/adapters/timescaledb/repository_test.go`:
- Insert an orders row with `instrument_type='OPTION'` + option metadata. Call `GetOrderByBrokerOrderID`. Assert all option fields round-trip. (Catches Defect B regression.)
- Insert a trade row with `broker_order_id='ABC'`. Call new `GetReconciledOrderIDs`. Assert `ABC` appears.

In `backend/internal/app/execution/service_test.go` (or new file if it doesn't exist for this layer):
- Fixture: orders row for an option BUY, broker GetAllFills returns 3 legs for that order, trades already has one Path B row (no exec_id, broker_order_id=ABC). Call `reconcileFillsOnBoot`. Assert: zero new rows inserted (already-recorded by broker_order_id).
- Same fixture but trades is empty. Assert: 3 legs inserted with full option metadata enriched from existing.

Won't write `reconcileOnBoot`-specific tests in this PR — that's Change 3 territory.

## Historical-data cleanup (post-deploy, separate one-shot)

Don't include in this PR. Once the fix is live and producing single-row writes:

1. Identify duplicate pairs: for each `(account_id, symbol|option_symbol, time::date)`, find live OPTION rows + boot-reconciled EQUITY rows where qty matches the order's filled_qty.
2. Decide canonical: prefer Path A's real `execution_id` (one row per leg, broker-authoritative) but copy Path B's option metadata onto it. Then DELETE Path B.
3. Run as a one-shot SQL script. Memory cleanup constraint applies: never naive-dedupe by `(symbol, time, side, qty)` alone.

## Blast radius

- **Schema migration**: `ADD COLUMN broker_order_id TEXT` is non-blocking on TimescaleDB. New column NULLs out for historical rows. No reader breaks (column is additive).
- **Live trading impact**: the Change-1 SQL/Scan extension affects only `reconcileFillsOnBoot` (boot-only path). Cannot affect live order routing or fill recording.
- **Change-2 risk**: a too-aggressive dedup could DROP legitimate fills. Mitigation: dedup only when `broker_order_id` matches AND existing trade rows' qty sum ≥ broker's cumulative for that order. False-skip impossible if broker is the source of truth.
- **Risk to open positions**: zero. No write paths in pos-monitor or risk gate are touched.

## Rollback

- Change 1 is a single-file edit; revert the file.
- Change 2's migration is additive — no rollback needed for the column. Code-side rollback restores the prior dedup-by-exec_id behavior; the dual-write resumes (status quo).

## Verification (post-deploy)

After 24h of live trading + at least one engine restart, query:
```sql
SELECT time::date AS d,
       SUM(CASE WHEN execution_id IS NULL OR execution_id = '' THEN 1 ELSE 0 END) AS no_exec,
       SUM(CASE WHEN execution_id IS NOT NULL AND execution_id <> '' THEN 1 ELSE 0 END) AS has_exec,
       SUM(CASE WHEN rationale LIKE 'reconcile_fills_on_boot%' THEN 1 ELSE 0 END) AS reconciled,
       SUM(CASE WHEN instrument_type='OPTION' THEN 1 ELSE 0 END) AS opt_typed
  FROM trades WHERE time >= NOW() - INTERVAL '7 days'
 GROUP BY d ORDER BY d DESC;
```

Expect: post-fix days show `reconciled` count drops to ~0 (only true gap-filling cases) AND every reconciled row has `instrument_type=OPTION` when the order was an option order.

## Files touched

1. `backend/internal/adapters/timescaledb/repository.go` (queryGetOrderByBrokerID + Scan + new GetReconciledOrderIDs + tradeInsertArgs broker_order_id)
2. `backend/internal/ports/repository.go` (add GetReconciledOrderIDs to interface)
3. `backend/internal/domain/entity.go` (add `BrokerOrderID string` to Trade)
4. `backend/internal/app/execution/service.go` (set trade.BrokerOrderID at all 5 SaveTrade/RecordFill writers; add second dedup gate in reconcileFillsOnBoot)
5. `backend/internal/app/positionmonitor/exit_eval.go` (set trade.BrokerOrderID at site #7)
6. New migration file under `backend/migrations/` (or wherever schema lives)
7. Tests as above

Approximate diff: ~120 LOC across 5 production files + 1 migration + 1 test file. Plan-required threshold met.
