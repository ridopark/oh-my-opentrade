# Multi-fill recording bug — implementation plan

## Root cause (verified from code)

Two layers conspire:

### Adapter layer (IBKR + Alpaca)

- IBKR `order_stream.go` has **three** emission sources colliding:
  - `pollOrderUpdates` (every 2s): emits when filled qty changes or status changes
  - `watchTradeDone`: emits ONCE on `Trade.Done()` for terminal status
  - `execReconciler` (every 10s): walks `ib.Fills()` and emits per-ExecID — **but skips any order present in `emittedDone`**
- The `emittedDone` set is keyed by `OrderID`, not ExecID. Once `pollOrderUpdates` or `watchTradeDone` emits a single terminal "fill", `execReconciler` is permanently muzzled for that order. Subsequent ExecDetails arriving from IBKR get dropped.
- The terminal "fill" event reads `tradeToOrderUpdate(t)`, which exposes:
  - `Qty/Price/ExecutionID` from `t.Fills()[len-1]` (LAST cached fill, which is whichever fill was in ibsync's slice when status flipped to Filled)
  - `FilledQty/FilledAvgPrice` from `OrderStatus.Filled/AvgFillPrice` (cumulative)
  - When ibsync's `t.Fills()` slice is partially populated relative to OrderStatus (race), `ExecutionID` ends up pointing to fill #1 even though `FilledQty=34` would otherwise be correct. The 2026-04-24 incident shows the worse case: `FilledQty` itself was stale (=1).
- Alpaca `trade_stream.go` already emits one event per execution as `event="partial_fill"` per ExecID, with one final `event="fill"`. The bug exists there too because the execution layer ignores partials.

### Execution layer

- `handleStreamFill`:
  - For `partial_fill`: logs only, no DB write (`return`)
  - For `fill`: writes ONE trade row using `update.FilledQty` (cumulative) and `update.ExecutionID` (last fill)
- `RecordFill` in repo: `UPDATE orders SET status='filled', filled_at=$2, filled_price=$3, filled_qty=$4` — overwrites unconditionally

Net effect on multi-fill orders:
- Trades table gets **one** row per order with cumulative qty + last ExecID's price/time (or, on the IBKR race, first ExecID + first qty). Per-exec price granularity is lost.
- On the IBKR race, `filled_qty` ends up at the FIRST partial's qty rather than cumulative — exactly the 2026-04-24 symptom.

## Fix design

**Principle: one trade row per execution_id; orders.filled_qty is the monotonic broker-cumulative.**

### 1. Adapter (`backend/internal/adapters/ibkr/order_stream.go`)

- Remove `emittedDone` order-level dedup for fills. Switch to **per-ExecID dedup**, owned by `execReconciler`. The reconciler becomes the single source of fill truth.
- Drop `execReconcileInterval` to **2s** (cheap — `ib.Fills()` is local cache reads). Keep the 60s `ReqFills` background refresh as-is.
- `watchTradeDone` and `pollOrderUpdates` keep emitting `canceled`/`expired`/`rejected`/`new`/`accepted` lifecycle events but stop emitting `Event="fill"`. Order-level dedup (`emittedDone`) is preserved for those non-fill terminal events only.
- `fillToOrderUpdate(f)` already returns the right shape: `Qty=exec.Shares` (incremental), `Price=exec.Price`, `FilledQty=exec.CumQty` (cumulative as of THIS exec), `FilledAvgPrice=exec.AvgPrice`. Keep.

Net adapter behavior: every ExecDetails from IBKR yields exactly one `OrderUpdate{Event:"fill"}` carrying both incremental and cumulative info. Race-free because `exec.CumQty` is server-side ground truth, not derived from local OrderStatus snapshots.

### 2. Execution service (`backend/internal/app/execution/service.go`)

- `handleStreamFill`:
  - Treat `partial_fill` and `fill` identically: build a trade row using `update.Qty` and `update.Price` (incremental — what actually filled at that exec), `ExecutionID = update.ExecutionID`, then call new `repo.RecordFill` per exec.
  - Pass `update.FilledQty` (cumulative-at-this-exec) and `update.FilledAvgPrice` to `RecordFill` for the orders-row update.
  - **Pending-order lifecycle**: do not `LoadAndDelete` until the order is fully filled. Use `update.FilledQty + epsilon >= po.intent.Quantity` as the terminal-fill predicate. On non-final fills, just record + leave pending alive.
  - On the final fill: do current cleanup (clear inflight, position-gate transitions, dust-sweep launch, MarkIntentTerminal in journal, FillReceived event with cumulative qty).
  - On non-final fills: emit a smaller `FillReceived` carrying the per-exec qty/price (downstream PnL aggregator already keys off `execution_id`, so per-exec emissions de-dup naturally).
- `handleFill` (sync/poll path) remains unchanged — those paths hit a single fill, not multi-exec.

### 3. Repository (`backend/internal/adapters/timescaledb/repository.go`)

- Replace `queryUpdateOrderFill`:
  ```sql
  UPDATE orders
     SET filled_at    = GREATEST(COALESCE(filled_at, '0001-01-01'::timestamptz), $2),
         filled_price = $3,
         filled_qty   = GREATEST(COALESCE(filled_qty, 0), $4),
         status       = CASE
                          WHEN $4 + 1e-9 >= quantity THEN 'filled'
                          WHEN COALESCE(filled_qty, 0) < $4 THEN 'partially_filled'
                          ELSE status
                        END
   WHERE broker_order_id = $1
     AND COALESCE(filled_qty, 0) <= $4
  ```
  - `GREATEST` makes it safe to call out-of-order (a stale exec arriving later can't downgrade).
  - The `WHERE` clause makes a no-op when the row is already past this exec's cumulative.
  - Status promotion is monotonic: `submitted` → `partially_filled` → `filled`.
- `RecordFill` already wraps trade insert + order update in one tx. Trade INSERT already uses `ON CONFLICT (trade_id, time) DO NOTHING` and the `idx_trades_execution_id` UNIQUE index for ExecID dedup — both stay.
- Keep `UpdateOrderFill` as a thin wrapper over the new query so all four call sites (reconcileOnBoot, backfillFromBrokerHistory, handleFill sync, RecordFill tx) get the new semantics for free.

### 4. Boot reconciler via `ReqFills` (`backend/internal/adapters/ibkr/broker.go` + execution)

- New optional broker capability:
  ```go
  // In ports/broker.go
  type FillLister interface {
      GetAllFills(ctx context.Context) ([]FillRecord, error)
  }
  type FillRecord struct {
      BrokerOrderID  string
      ExecutionID    string
      Symbol         string
      Side           string
      Qty            float64
      Price          float64
      CumQty         float64
      AvgPrice       float64
      FilledAt       time.Time
  }
  ```
- IBKR `broker.go` implements via `ib.ReqFills(NewExecutionFilter())` (blocking; called once at boot).
- Execution `service.go`: new `reconcileFillsOnBoot` after `backfillFromBrokerHistory`. Diffs broker fills against a new `repo.GetExecutionIDsForOrders([]string)` and inserts missing trade rows + updates orders. Idempotent on re-run via execution_id UNIQUE.
- Skip `POST /api/admin/reconcile-fills` for v1 (handoff says "consider"; restart triggers it). Add follow-up if needed.

### 5. Regression test

`backend/internal/app/execution/multi_fill_test.go`:
- Mock `OrderStreamPort` emitting the 2026-04-24 sequence: `(1@$1.00, 5@$1.00, 25@$1.00, 3@$0.99)` with cumulative `(1, 6, 31, 34)` and avg-prices computed correctly.
- Mock repo records every `RecordFill` call.
- Assert:
  - 4 `RecordFill` calls
  - 4 distinct trade rows with correct ExecutionIDs
  - Final `orders.filled_qty == 34`, `filled_price ≈ 0.99117647` (VWAP), `status == 'filled'`
  - Replay the same sequence: dedup via execution_id holds, no double trades
  - Out-of-order delivery: arrive `(3, 25, 5, 1)` — final state still 34 / VWAP / filled (the GREATEST + WHERE guards make this work)

Plus a smaller unit test on the new repo query against a sqlmock.

## Files touched

| File | Change |
|---|---|
| `backend/internal/adapters/ibkr/order_stream.go` | Adapter dedup refactor; reconciler interval drop |
| `backend/internal/adapters/timescaledb/repository.go` | New `queryUpdateOrderFill` query; same callers |
| `backend/internal/app/execution/service.go` | `handleStreamFill` per-exec; partial_fill writes; pending lifecycle; new `reconcileFillsOnBoot` |
| `backend/internal/adapters/ibkr/broker.go` | `GetAllFills` implementation |
| `backend/internal/ports/broker.go` | `FillLister` interface, `FillRecord` struct |
| `backend/internal/ports/repository.go` | `GetExecutionIDsForOrders` method |
| `backend/internal/adapters/timescaledb/repository.go` | `GetExecutionIDsForOrders` impl |
| `backend/internal/app/execution/multi_fill_test.go` | New regression test |
| Mock repos in `*_test.go` files | Add `GetExecutionIDsForOrders` no-op stub to satisfy interface |

Estimated LOC: ~400 added, ~50 modified.

## Blast radius

- **Touches the live order/fill recording path**. Every order in production flows through this code.
- Mitigations:
  - Trades table: ExecID UNIQUE index prevents double-counting on retries / reconciler races
  - Orders table: monotonic GREATEST + WHERE guards prevent out-of-order downgrades
  - Pending-order lifecycle: keep alive until cumulative >= intent.Qty; existing terminal-event cleanup paths unchanged
- **Risk: dust-sweep timing**. Currently runs on terminal "fill". After the change, dust-sweep should run on the FINAL fill (cumulative >= intent qty). Same trigger point, just different code path.
- **Risk: SCALE_OUT exits**. These submit a SELL with `intent.Quantity` < broker position. Final detection by `cumulative >= intent.Quantity` still works — SCALE_OUT intents specify partial qty, fill matches that qty.
- **Risk: simbroker tests**. Simbroker still emits one fill per order with cumulative=full. The new code handles single-fill orders identically to old code (final fill IS the only fill).
- **Risk: schema migration**. None — no new columns.

## Test plan

- `go test ./backend/internal/adapters/timescaledb/...` (sqlmock query shape)
- `go test ./backend/internal/adapters/ibkr/...` (existing tests still pass; per-ExecID emit covered)
- `go test ./backend/internal/app/execution/...` (existing + new multi_fill_test.go)
- `go vet ./backend/...`
- Smoke: rebuild, restart in paper, submit a small order, observe single-fill path still works

## Sequence of commits

1. ports + repo query change + repo tests
2. adapter dedup refactor + adapter tests
3. execution service handleStreamFill rewrite + multi_fill_test
4. boot reconciler via ReqFills (Task #4)
5. backfill SPY260424C00712000 via existing ad-hoc script (already done; just leave note)

## Out of scope

- `POST /api/admin/reconcile-fills` (deferred; restart suffices)
- Manual-close path (already shipped; benefits from the fix automatically)
- Adding `broker_order_id` column to trades table (avoided — would require migration; broker-cumulative passed in is sufficient)
