# SimBroker fill-timestamp fix plan (rev 2)

Rev 1 reviewed: qa-inspector NEEDS_REVISION (BLOCKING: rev-1 swap at broker.go:470 shifts IV/DTE/BarVolume math to the wrong clock; informational correction: collector pairs by FIFO, not FilledAt). code-reviewer APPROVE_WITH_REVISIONS (rename to `DecidedAt`, make fallback transitional, tighten test list + success criteria, drop defensive bloat). This rev addresses every item.

`SimBroker.SubmitOrder` stamps `FilledAt` with `b.barTimes[underlying_symbol]` (broker.go:470, 666, 748) -- the latest bar SimBroker cached for the option's underlying ticker. Under sharded backtest execution this cache lags the actual order-submission sim time, producing fill timestamps that are days or weeks earlier than the signal that triggered them. Example: an NIO option BTO posted by Edtrader at sim **2026-02-26 20:48 UTC** fills with `FilledAt=2026-02-13 19:03` because that's the most recent NIO equity bar in the broker's cache when the order arrives.

Strategy state (`author_stated.csv` `opened_at`) is correct. The wrong `FilledAt` leaks into:

- `fills.csv` `filled_at` column (via `copytradereplay/ledger.go:85` reading `payload["filled_at"]`)
- `backtest/collector.go:867` trade-log SORT order (`FilledAt.Before(...)`). NOT entry/exit pairing -- collector pairs FIFO via `opens[0]` at collector.go:299-318, so pairing itself is unaffected.
- `positionmonitor` MFE/MAE windowing (handlers.go:30 reads `payload["filled_at"]`)
- Dashboard entry-time column

Live path (IBKR `order_stream.go:293`) is unaffected -- the live broker reports its own fill time.

## Verified context (qa-inspector)

- 3 SimBroker fill-time sites use the buggy `barTime`:
  - `broker.go:470` -- `barTime := b.barTimes[priceSymbol]` (source of the lag)
  - `broker.go:666` -- `simOrder.filledAt = barTime`
  - `broker.go:748` -- `OrderUpdate{FilledAt: barTime}`
- `barTime` is ALSO reused for option pricing math and we MUST keep that:
  - `computeOptionExitPrice(intent, lastPrice, barTime)` at line 480 (DTE years, IV time-of-day adj, historical DoltHub bid date key)
  - `applyParticipationImpact(ctx, intent, barTime, ...)` at lines 488, 514 (BarVolume cache key)
  - `FillContext{SubmitTime: barTime, ...}` at line 575 (equity fill model)
  These all need the underlying-price bar's clock, NOT the orchestrator's clock. Rev 1 would have broken them.
- 2 fill-time sites already correct: `broker.go:1119` (`ExpireOptions`), `broker.go:1207` (`AccrueFunding`).
- The right "now" is already captured at `execution.Service.handleIntent` (service.go:1169) as `submitStart := s.nowFn()`. In backtest, `s.nowFn` is the closure at backtest/runner.go:359 returning the latest bar tick time (via atomic). In live, default `time.Now`. Verified.
- `domain.OrderIntent` (entity.go:72) has NO timestamp field today. Not DB-serialized as a struct -- the timescaledb repo at order_intent_repo.go:126 enumerates 22 explicit columns. Adding a field does not require a migration.
- Live adapters (`ibkr/broker.go`, `alpaca/rest.go`) read only specific intent fields; neither references a timestamp. Safe to add. The IBKR/Alpaca "SubmittedAt" terms refer to broker-side ack -- naming collision risk if we use that term.
- Existing tests don't assert on `barTime`-derived `FilledAt` values, so no test churn.

## Fix approach: add `DecidedAt` to `OrderIntent`; new local in SimBroker

Add `DecidedAt time.Time` to `domain.OrderIntent` -- "the moment the orchestrator decided to submit this intent." Name avoids collision with IBKR/Alpaca's "submitted" (broker-ack). `execution.Service` stamps it from the existing `submitStart := s.nowFn()`. SimBroker derives a NEW local `decidedAt` from `intent.DecidedAt` with fallback to `barTime`, and uses `decidedAt` ONLY at the two FilledAt write sites (666, 748). The original `barTime` local stays untouched for option pricing (480), market impact (488, 514), and the equity fill model (575).

Fallback is **transitional**: it preserves existing behavior for any caller that doesn't set the field (paired-strategy submitter, dust-sweep, internal recursion, tests). After one release with a `log.Debug("simbroker: DecidedAt fallback to barTime")` line counted in production, the fallback gets removed in a follow-up PR.

## Implementation

### Phase 1 -- `domain.OrderIntent` gets `DecidedAt`

File: `backend/internal/domain/entity.go` around line 72.

```go
type OrderIntent struct {
    ...
    // DecidedAt is the sim-time (backtest) or wall-time (live) at which
    // the orchestrator decided to submit this intent. SimBroker uses it
    // to stamp FilledAt on synchronous fills; live brokers ignore it
    // (their fill time comes from the broker's own confirmation). Zero
    // value is permitted during the transition window; SimBroker logs a
    // debug line and falls back to its cached bar time. Tracked for
    // mandatory non-zero enforcement in a follow-up PR.
    DecidedAt time.Time
}
```

No DB migration -- field is not in the SQL column enumeration at order_intent_repo.go:126.

### Phase 2 -- `execution.Service.handleIntent` stamps it

File: `backend/internal/app/execution/service.go` around line 1169.

```go
submitStart := s.nowFn()
intent.DecidedAt = submitStart
brokerOrderID, err := s.broker.SubmitOrder(ctx, intent)
```

`submitStart` is already captured for latency measurement. Same value, new use.

### Phase 3 -- `SimBroker.SubmitOrder` adds a new local

File: `backend/internal/adapters/simbroker/broker.go` at line 470.

Today:

```go
barTime := b.barTimes[priceSymbol]
```

After:

```go
barTime := b.barTimes[priceSymbol]
// FilledAt source: prefer intent.DecidedAt (set by execution.Service from
// s.nowFn()). Fall back to the cached underlying bar time for legacy
// callers (paired-leg submitter, dust-sweep, broker-internal recursion,
// tests). Fallback is transitional; tracked for removal once all upstream
// SubmitOrder callers stamp DecidedAt unconditionally.
decidedAt := intent.DecidedAt
if decidedAt.IsZero() {
    decidedAt = barTime
    b.log.Debug().Str("symbol", string(intent.Symbol)).Msg("simbroker: DecidedAt fallback to barTime")
}
```

Then use `decidedAt` ONLY at lines 666 and 748:

```go
// line 666 area:
simOrder.filledAt = decidedAt

// line 748 area:
OrderUpdate{ ..., FilledAt: decidedAt, ... }
```

`barTime` is unchanged for the option pricing path (line 480 `computeOptionExitPrice`), market impact path (lines 488, 514 `applyParticipationImpact`), and equity fill-model path (line 575 `FillContext.SubmitTime`).

### Phase 4 -- tests

`backend/internal/adapters/simbroker/broker_test.go` (extend existing):

1. `TestSubmitOrder_FilledAt_UsesIntentDecidedAt_Entry`: option BUY with `intent.DecidedAt = T1`; pre-stamp `b.barTimes[underlying] = T0` (T0 < T1); submit; assert returned fill's `FilledAt == T1`. Pins the entry path.
2. `TestSubmitOrder_FilledAt_UsesIntentDecidedAt_Exit`: option SELL (STC) with same setup; assert `FilledAt == T1`. Pins the exit path -- proves cascade to both 666 and 748 for both directions (code-reviewer item).
3. `TestSubmitOrder_FilledAt_FallbackToBarTime_Logs`: leave `intent.DecidedAt` zero; pre-stamp `b.barTimes[underlying] = T0`; capture logger output via a test seam; assert `FilledAt == T0` AND the debug log fired. Pins the fallback + operator visibility (code-reviewer item).

`backend/internal/app/execution/service_test.go` (extend):

4. `TestHandleIntent_StampsDecidedAtFromNowFn`: wire a fake nowFn returning T_test; capture the intent passed to a stub broker; assert `intent.DecidedAt == T_test`. Pins the stamping (rev-1 #4).

Tests #5+ (concurrency, both-zero defensive) dropped per code-reviewer: SubmitOrder is per-broker-instance serialized, no contention; Go zero-time doesn't panic in time comparisons or formatting.

Integration: existing copytrade backtest path. Manual verification after rebuild:

5. Run `POST /backtest/run` for copytrade_v1, 2026-01-27 to 2026-05-08. For every copytrade BUY row in `_workspace/copytrade_replay/fills.csv`, assert `ts_filled` falls in `[PostedAt, PostedAt + 1m]` where PostedAt is the Discord timestamp of the message whose snowflake is encoded in the row's `signal_id`. Causally consistent, no earlier-than-post drift.

### Phase 5 -- DB persistence audit

Already done by qa-inspector (informational): no migration needed. Verify one more time before merge by `grep -r "OrderIntent" backend/migrations/ backend/internal/adapters/sqlite/ backend/internal/adapters/timescaledb/` for completeness.

## Out of scope

- `paired.go:97` and `:151` paired-leg submitter (same shard-lag bug applies). Follow-up PR.
- Dust-sweep (service.go:2334, 2400) and internal SubmitGroup recursion (broker.go:1528): low-impact and rarely hit; fallback covers them.
- Backfill of historical Trade rows in TimescaleDB with bad `filled_at` from pre-fix backtests. No migration. **Operator note**: dashboard queries that ORDER BY `filled_at` may show pre-fix backtest runs with skewed ordering until rerun.
- Partial-fill emission via `execution.Service.emitPartialFillReceived` (service.go:1925). That path already takes `filled_at` from a parameter the caller provides at submission time -- unaffected.
- Live IBKR/Alpaca paths -- already correct.
- Rename of `FilledAt` field. Semantic stays.

## Blast radius

- 3 source files: `domain/entity.go`, `app/execution/service.go`, `adapters/simbroker/broker.go`.
- All other `SubmitOrder` call sites: zero behavioral change (fallback preserves current behavior; fallback log fires at DEBUG so it doesn't pollute INFO).
- Live adapters: untouched, ignore the new field.
- Tests: 4 new (3 simbroker, 1 execution). No existing tests broken.

## Retry / resubmit semantics

`DecidedAt` is stamped per-call in `execution.Service.handleIntent`. If a retry creates a fresh call to `s.broker.SubmitOrder`, it captures a fresh `s.nowFn()`. Each submission attempt reflects WHEN that attempt happened. The plan does not preserve the original-intent decision time across retries -- by design, because the FilledAt is meant to reflect when the broker filled, not when the strategy originally wanted to act. (For "intent-creation" time we'd add a separate field; out of scope.)

## Success criteria

After all phases applied and rebuilt:

1. `go test ./backend/internal/adapters/simbroker/... ./backend/internal/app/execution/...` green.
2. HTTP backtest POST `{"strategies":["copytrade_v1"],"from":"2026-01-27","to":"2026-05-08","speed":"max"}` completes.
3. **TIGHTENED**: For every copytrade BUY row in `_workspace/copytrade_replay/fills.csv`, `ts_filled` falls in `[PostedAt, PostedAt + 1m]` where PostedAt is the Discord timestamp from the signal_id snowflake. **Strict lower bound `>= PostedAt`** -- no fill earlier than the signal that triggered it. Specifically:
   - `NIO260618C00005500` BUY `ts_filled` in `[2026-02-26T20:48:31Z, 2026-02-26T20:49:31Z]`.
   - `BABA260717C00165000` BUY `ts_filled` in `[2026-03-16T14:23:33Z, 2026-03-16T14:24:33Z]`.
4. No regression PR #102: zero `copytrade_exit_request: no matching position` warnings.
5. No regression PR #103: 9 OPTION_EXPIRY rows still appear at correct expiry-day session close.
6. omo-replay CLI run produces causally-consistent fill timestamps (same assertion as #3).
7. Fallback-log count in the run summary is **zero** for copytrade orders (every copytrade order goes through `execution.Service.handleIntent`, which always stamps DecidedAt). Non-zero count from other paths (paired, dust-sweep) is acceptable for this PR and tracked for the follow-up.

## Execution order

1. Phase 1: add `DecidedAt` to OrderIntent.
2. Phase 2: stamp in `execution.Service.handleIntent` + unit test #4.
3. Phase 3: new local in SimBroker + unit tests #1-3.
4. Phase 5: DB audit (read-only confirmation).
5. Build omo-core, restart, run HTTP backtest, verify success criteria 2-7.
6. omo-replay parity (criterion 6).
7. Open PR.

## Risk register

- **Legacy callers don't set DecidedAt**: fallback preserves old behavior; debug-log line tracks usage. Tracked for removal.
- **`s.nowFn()` returns wrong time**: doesn't today; unit test #4 pins it.
- **OrderIntent persistence**: not serialized as a struct; no migration. Verified.
- **Live broker accidentally uses DecidedAt**: impossible -- live adapters don't read the field. Verified at ibkr/broker.go:22-100 and alpaca/rest.go:255-256.
- **IV/DTE/BarVolume drift from rev 1**: AVOIDED via the separate `decidedAt` local; `barTime` retained for those code paths. Pinned indirectly by existing simbroker tests for option exit pricing (they'd fail if barTime changed under them).
- **Historical Trade rows have bad filled_at**: not migrated; rerun backtests for clean data. Operator note above.
- **Dashboard zero-time rendering**: not a concern -- fallback emits non-zero (the cached barTime). Once mandatory enforcement lands, asserts non-zero pre-merge.
