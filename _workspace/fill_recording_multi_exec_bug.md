# Fill-recording: only first execution persisted on multi-fill orders — handoff (2026-04-24)

## Symptom

Large orders that IBKR splits into multiple executions only land the first fill
in the `trades` table. `orders.filled_qty` is stuck at the first exec's qty
even when the order is fully filled on the broker side. Realized P&L in the DB
is consequently garbage for any split fill.

Additionally: manual closes from the Portfolio `/portfolio` page bypassed the
`orders` table entirely, so nothing about the close appeared on `/execution`.
That part is already fixed — this handoff is about the remaining multi-fill bug.

## Concrete evidence from 2026-04-24

SPY260424C00712000 (short-dated SPY call, copytrade_v1 entry, manual exit):

IBKR `ReqFills` returned 7 fills for this contract today:

    BUY  orderId=3222, permId=241708493
      11:32:32 ET  1  @ $1.00   execId ...4a09
      11:32:32 ET  5  @ $1.00   execId ...4a0b
      11:32:41 ET  25 @ $1.00   execId ...4a08
      11:32:43 ET  3  @ $0.99   execId ...4a1a
      total 34 contracts

    SELL orderId=3247, permId=241708563  (manual close via Portfolio UI)
      11:51:18 ET  19 @ $2.39   execId ...6065
      11:51:18 ET  5  @ $2.39   execId ...6066
      11:51:18 ET  10 @ $2.39   execId ...6067
      total 34 contracts @ flat $2.39

What was in the DB *before* the backfill:

    orders table:
      3222 BUY  34 filled filled_qty=1  filled_price=1.00   strategy=copytrade_v1
      (no row for 3247)

    trades table:
      1 row: BUY 1 @ $1.00 execId ...4a09

Net recorded position = 1 BUY, net realized = 0. Broker net = 0, realized ≈ $4,729.

## Root cause hypothesis (NOT yet verified)

`repository.go` query:

    queryUpdateOrderFill = `UPDATE orders SET status = 'filled',
        filled_at = $2, filled_price = $3, filled_qty = $4 WHERE broker_order_id = $1`

This **overwrites** filled_qty on every call rather than accumulating. So even
if the adapter forwards every exec event, the second+ call would replace filled_qty
= 5 with 25, then 25 with 3, ending at 3. But the DB ended at 1 — which suggests
subsequent execs aren't forwarded to the repo at all, not just that aggregation
is wrong.

Two things to verify:
1. Does the IBKR adapter emit one event per exec, or only per order completion?
2. Does `UpdateOrderFill` actually get called once per exec, or once per order?

Start reading: `backend/internal/adapters/ibkr/broker.go` `watchTradeDone` and
any `execDetails` / `commissionReport` handlers. The ibsync Trade struct has a
`Fills` slice that grows as execs arrive.

## Fix already shipped (scope: manual-close visibility only)

Commit `905efebc feat(execution): surface manual position closes on Execution Monitor`

`backend/internal/adapters/http/portfolio_handler.go`:
- New helper `recordManualClose` inserts a synthetic `BrokerOrder` row with
  `strategy="manual"` when `CloseAtMarket` succeeds.
- Both `handleClosePosition` and `handleCloseAll` capture signed qty via
  `broker.GetPosition` before the close, then call the helper on success.
- Uses `uuid.New()` for `intent_id` (DB requires NOT NULL UUID). OCC symbols
  get InstrumentType=OPTION plus parsed underlying/strike/expiry/right via
  `domain.ParseOCC`.
- `SaveOrder` failure is logged, not fatal — the broker close already succeeded.

When the fill lands, the existing fill-recording path updates this row in place
via `broker_order_id` (`queryInsertOrder` has `ON CONFLICT DO UPDATE`).

**But** that update path has the very bug described in this handoff, so manual
closes will also show only the first fill on multi-fill closes until the
underlying bug is fixed.

## DB backfill applied 2026-04-24

One-shot SQL backfilled the missing rows for SPY260424C00712000 so `/execution`
and PnL show the real round trip. Details inline in that conversation; no script
committed. Running `_tmp/ibkr_fills_lookup` provides the data source.

## Tooling

`_tmp/ibkr_fills_lookup/main.go` — connects to IB Gateway with `ClientID=99`
(non-colliding with omo-core's `ClientID=2`) and dumps every fill today via
`ib.ReqFills(NewExecutionFilter())`. Use this to reconstruct fill detail for
future backfills. Default port 4002 (paper). Usage:

    cd _tmp/ibkr_fills_lookup && go build ./...
    ./ibkr_fills_lookup -symbol SPY

## Task list

Tracked as tasks 1-5 in the current session task list:

1. **Investigate** the fill path — determine whether subsequent execs are
   dropped at the adapter layer or at the repo layer. Focus on
   `backend/internal/adapters/ibkr/broker.go watchTradeDone` and any
   `ExecDetails` / `CommissionReport` handling.

2. **Fix trades insertion** — every execution must produce a distinct
   `trades` row, keyed on `execution_id`. The UNIQUE index on
   `(execution_id, time)` already prevents dupes.   (blocked by #1)

3. **Fix orders aggregation** — `orders.filled_qty` must reflect cumulative
   qty; `orders.filled_price` must be the VWAP across all execs. Cleanest
   approach is to compute at end-of-order (on Trade.Done or last
   CommissionReport) from `SUM(trades.quantity)` for that
   `broker_order_id`.   (blocked by #1)

4. **Startup reconciliation via ReqFills** — on bootstrap, call
   `ib.ReqFills(NewExecutionFilter())`, diff against `trades.execution_id`,
   insert the missing rows. Catches fills dropped during crashes,
   disconnects, or today's bug. Independent of #1-#3. Can ship standalone
   and would have auto-healed today's incident. Consider also adding
   `POST /api/admin/reconcile-fills` to trigger without restart.

5. **Regression test** — simbroker sequence that emits 1+5+25+3 partial
   fills at different prices, asserting all 4 trades persist, `filled_qty=34`,
   `filled_price` = VWAP, and dedup via execution_id works.   (blocked by #2, #3)

## Recommended sequence

Ship #4 first. It's independent, has the highest operational leverage (auto-heal
on restart), and would have made today's data-repair invisible to the user.
Then do #1 → (#2, #3 in parallel) → #5.

## Open related bug (out of scope for this handoff)

The manual-close path currently writes only the `orders` row; the `trades`
rows for the close fills arrive via the same broken path as #1-#3. Once #2 is
in, manual closes will produce proper trade rows automatically.
