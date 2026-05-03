# Plan: Eliminate dual-write race in fill recording

## Problem

Two paths can persist trade rows for the same broker fill:

1. Per-exec stream (`handleStreamFill` -> `insertFillLeg(execID=real)`) -- one row per execution leg, real IBKR exec ID.
2. Aggregate finalization (`handleFillWithPrice("")` -> `insertFillLeg(execID="")`) -- one row per order, empty exec ID.

The unique index `idx_trades_execution_id` only catches duplicates that share an exec ID; rows with `""` bypass it. When both paths fire for the same `broker_order_id`, the trade ledger ends up with `(per-exec legs) + (aggregate row)` and net qty drifts.

We can't drop the aggregate path entirely -- per-exec stream is unreliable on IBKR (only 1 of 3 SOXL legs delivered, so the aggregate is a needed safety net).

Recent live evidence:
- `SOXL260508C00125000`: BUY 1 (real exec_id) + BUY 3 (empty exec_id) -> net +1 phantom long
- `MRVL260508C00162500`: BUY 4 + SELL 4 (real exec_id) + SELL 4 (empty exec_id) -> net -4 phantom short

## Fix approach

Make `broker_order_id` the dedup unit. Aggregate writes get a synthesized exec ID `agg:<broker_order_id>`. Per-exec writes are authoritative; aggregate only persists when no per-exec rows exist for that `broker_order_id`. Atomic via repo-level transactions.

Semantics:
- Per-exec write arrives first, then aggregate -> aggregate sees per-exec exists, no-ops.
- Aggregate write arrives first, then per-exec -> per-exec deletes the `agg:<bo_id>` row, inserts itself.
- Aggregate fires twice -> second is a no-op (unique index on synthesized ID).
- Per-exec fires twice with same exec_id -> second is no-op (existing dedup).

## Files to change

### backend/internal/adapters/timescaledb/repository.go

Split `RecordFill` (line 766) into two transactional variants:

**`RecordFillPerExec(ctx, brokerOrderID, filledAt, filledPrice, filledQty, trade)`**
1. BEGIN tx
2. `DELETE FROM trades WHERE broker_order_id = $1 AND execution_id LIKE 'agg:%'`
3. `UPDATE orders ...` (existing queryUpdateOrderFill)
4. `INSERT INTO trades ...` (existing queryInsertTrade with real exec_id)
5. COMMIT

**`RecordFillAggregate(ctx, brokerOrderID, filledAt, filledPrice, filledQty, trade)`**
1. Caller has already set `trade.ExecutionID = "agg:" + brokerOrderID`
2. BEGIN tx
3. `INSERT INTO trades (...) SELECT ... WHERE NOT EXISTS (SELECT 1 FROM trades WHERE broker_order_id = $X AND execution_id NOT LIKE 'agg:%')` -- conditional insert
4. `UPDATE orders ...` (existing queryUpdateOrderFill)
5. COMMIT

The aggregate path uses conditional INSERT to remain idempotent under concurrent per-exec arrival. The per-exec path always wins.

Keep existing `RecordFill` as a thin shim that routes to `RecordFillPerExec` (it's only called with non-empty exec_id today).

### backend/internal/ports/repository.go

Extend the FillRecorder interface (line 31):
```go
RecordFillPerExec(ctx, brokerOrderID, filledAt, filledPrice, filledQty, trade) error
RecordFillAggregate(ctx, brokerOrderID, filledAt, filledPrice, filledQty, trade) error
RecordFill(...) error  // deprecated: routes to RecordFillPerExec
```

### backend/internal/app/execution/service.go

Modify `insertFillLeg` (line 1794) -- the single fan-in point for all fill writes. Branch on executionID:
```go
if executionID == "" {
    trade.ExecutionID = "agg:" + brokerOrderID
    s.repo.RecordFillAggregate(ctx, brokerOrderID, filledAt, cumAvgPrice, cumQty, trade)
} else {
    trade.ExecutionID = executionID
    s.repo.RecordFillPerExec(ctx, brokerOrderID, filledAt, cumAvgPrice, cumQty, trade)
}
```

Add a structured log on every dedup decision (DEBUG level): "fill recorded: per-exec replaced agg row" / "fill recorded: aggregate inserted (no per-exec)" / "fill recorded: aggregate skipped (per-exec exists)".

The 3 callers (`handleFillWithPrice` 1955, `recordFillFromDetails` 2748, syncFill branch 1263) need no signature change -- they already pass executionID through.

### Mock repos in test files

Update all `mockRepository` / `mockRepo` implementations to satisfy the new interface methods:
- `backend/internal/adapters/noop/repo.go`
- `backend/internal/app/execution/*_test.go` (multiple)
- `backend/internal/app/positionmonitor/service_test.go`
- `backend/internal/app/perf/ledger_writer_test.go`
- `backend/internal/app/recap/service_test.go`
- `backend/internal/ports/ports_test.go`

Most are simple stubs that delegate to existing RecordFill behavior or no-op.

## Tests

New file `backend/internal/app/execution/dual_write_test.go`:

1. **TestInsertFillLeg_PerExecThenAggregate_AggregateNoOp**
   Per-exec write arrives, then aggregate. Assert: 1 trade row, exec_id == real exec ID, no `agg:` prefix.

2. **TestInsertFillLeg_AggregateThenPerExec_PerExecReplaces**
   Aggregate writes first (creates `agg:<bo_id>` row), then per-exec arrives. Assert: 1 trade row, exec_id == real exec ID (the agg row was deleted).

3. **TestInsertFillLeg_AggregateOnly_OneRow**
   Only aggregate path fires (no per-exec stream). Assert: 1 row with exec_id `agg:<bo_id>`.

4. **TestInsertFillLeg_AggregateTwice_StillOneRow**
   Aggregate fires twice (e.g., race in finalization). Assert: 1 row (unique index on synthesized exec_id catches it).

5. **TestInsertFillLeg_MultiLegPerExec_ThenAggregate_AggregateNoOp**
   3 per-exec rows arrive (cum legs of 1, 2, 3), then aggregate fires. Assert: 3 per-exec rows, no aggregate row.

6. **TestInsertFillLeg_PerExecMultiLeg_NoDup**
   Same exec_id sent twice (stream replay). Assert: 1 row (existing dedup).

Tests use the real `Repository` against a test database OR a sufficiently rich mock. Existing `repository_test.go` uses an in-memory pattern; follow that convention.

## Blast radius

Touches the core fill-recording path -- every trade goes through here. A bug here either drops real fills (no insert) or duplicates them (no dedup) -- both worse than current state.

Mitigations:
- Structured DEBUG log on every dedup decision (skipped / deleted / first-write) -- first day's traffic is auditable.
- Paper soak ~24h before promoting; behavior identical between paper and live (same code path).
- Easy revert: single commit, reverting restores current code.
- Pre-push hook runs `go test ./...` -- must be green before push.

## Out of scope

- Historical-duplicate cleanup (already handled per-incident via `reconciliation_phantom` writes; can do a one-shot SQL backfill afterward if useful).
- Cleanup of the `RecordFill` shim (mark deprecated; remove in a follow-up after callers migrate).
- Discord/SSE notification changes -- existing FillReceived events unchanged.

## Success criteria

- All existing tests still pass (`go test ./...`).
- 6 new tests in `dual_write_test.go` pass.
- `golangci-lint` clean.
- Manual integration check: simulate via `simbroker` adapter -- entry order with split fills, verify trade ledger has only per-exec rows.
- Net position query (`queryGetNetPositions`) returns 0 for symbols where broker is flat, even after a dual-pipeline trigger condition.

## Estimate

~150 LOC across 4 files + ~120 LOC tests. ~2-3 hours focused work.
