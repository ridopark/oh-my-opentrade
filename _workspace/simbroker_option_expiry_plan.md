# SimBroker option-expiry auto-close plan (rev 2)

Rev 1 reviewed: qa-inspector NEEDS_REVISION (BLOCKING: synthetic fills on `fillCh` get dropped at `execution.Service.handleStreamFill` because no `pendingOrder` exists; HIGH: lock-pattern claim was wrong; MEDIUM: underlying-price coverage gap). code-reviewer APPROVE_WITH_REVISIONS (4 missing tests, tighter success criteria, mandatory EOD hook, doc gaps). This rev addresses every item.

Make SimBroker simulate the OCC auto-exercise that happens for US equity options at expiry. Today, options that expire during a backtest stay "open" in the dashboard because SimBroker passively holds the position forever and the strategy never fires an STC.

## Why broker-level, not strategy-level

`EXPIRY_WATCH` / `DTE_FLOOR` rules in positionmonitor/evaluators.go are *warnings* that depend on the strategy to react. They do not match OCC behavior (which auto-exercises ITM contracts at expiry with no strategy input). Modeling at the broker layer:

- Invariant across strategies (copytrade, tradingthetrend, future option strategies).
- Matches the live-path observation in `positionmonitor/bootstrap.go:123-137` ("expired OCC absent on broker -- assuming auto-close at expiry"). Live broker handles this; SimBroker needs to too.

## Verified context (from qa-inspector)

- `position` struct (broker.go:98-106) holds symbol/qty/avgCost but NOT expiry/right/strike. We derive from OCC via `domain.ParseOCC` (domain/options.go:251-304) -- returns `(underlying, expiry-at-16:00-ET, strike, right, ok bool)`, rejects non-OCC cleanly. No struct churn.
- Underlying price: `b.prices[underlyingSym]` populated by per-bar `UpdatePrice` (broker.go:418-423). Resolved via `domain.UnderlyingFromOCC`.
- `b.positions[]` is only mutated via SubmitOrder (broker.go:661-664). No other seed paths -- OCC-on-demand parsing covers every option position.
- `EventBus.Flush()` (eventbus/memory/bus.go:235-264) is already used at runner.go:2081/2095/2164/2180 after EvalExitRules. Correct primitive.
- copytrade_v1 `handleFillConfirmation` early-returns on SELL fills (copytrade_v1.go:585-587), so a synthetic SELL won't corrupt `cst.Positions`. positionMonitor revaluator + trade collector consume FillReceived directly. So the strategy stays in sync only if FillReceived actually fires.

## **CRITICAL FIX from rev 1: bypass fillCh; publish FillReceived directly**

Rev 1 said "emit on `fillCh`." That's the wrong surface. The sole consumer of `fillCh` is `execution.Service.runFillListener` (execution/service.go:1476) -> `handleStreamFill` (execution/service.go:1602), which does `s.pendingOrders.Load(update.BrokerOrderID)` and warns "fill received for unknown order" then returns at line 1615-1618 when no pending entry exists. Synthetic expiry fills bypass `SubmitOrder` entirely so there is no pending order. Result on rev-1 path: warn-and-drop, no FillReceived event, no Trade row, no collector update, no position_monitor close. Success criterion would silently fail.

**Rev 2 approach**: `ExpireOptions` publishes `domain.EventFillReceived` directly to the EventBus, mirroring how `execution.Service.recordSweepFill` (execution/service.go:2533-2567) emits synthetic close fills today. Payload structure follows the existing FillReceived schema (the same map[string]any payload `runner.handleFill` already decodes at runner.go:2492-2516). Marker: `payload["exit_reason"] = "OPTION_EXPIRY"`. No SubmitOrder, no pending-order shim required.

## Scope

For each option position in `b.positions` whose OCC expiry has just been crossed by the bar clock, emit a SELL FillReceived at intrinsic value via the EventBus and clear the position.

## Behavior

1. Parse OCC -> (underlying, expiry, right, strike). Skip non-OCC.
2. Look up `underlyingPrice = b.prices[underlyingSym]`. If missing, intrinsic=0 with a warn log AND increment a counter surfaced in the data-quality summary (runner.go:2189).
3. Compute intrinsic per share:
   - CALL: `max(0, underlyingPrice - strike)`
   - PUT: `max(0, strike - underlyingPrice)`
4. Publish `domain.EventFillReceived` with payload mirroring `recordSweepFill`'s structure. Side=SELL, qty=position.quantity, price=intrinsic, filled_at=bar time, broker_order_id synthesized as `expiry:<symbol>:<unix_nano>`, exit_reason="OPTION_EXPIRY".
5. Apply zero commission/fees. (Future: IBKR $0.55 exercise fee if accounting needs it -- not in scope.)
6. Remove the position from `b.positions` (clear qty + strategy, matching existing close pattern at broker.go:711).

Edge cases:
- Qty 0 (already closed): skip silently.
- Non-option symbol: skip.
- Underlying price missing: emit at intrinsic=0 with warn + counter.
- Short option positions (qty < 0): same intrinsic calc, side=BUY-to-close. Out-of-scope mechanically for copytrade (long-only) but the code must not panic.
- Bar time `>=` expiry session close: inclusive boundary (>=, not >). Critical -- pinned by a unit test (item below).

## Implementation

### Phase 1 -- `ExpireOptions(barTime)` on `*Broker` + EventBus dependency

New exported method:

```go
// ExpireOptions closes any option positions whose expiry session-close has
// passed barTime. Emits a SELL FillReceived at intrinsic value on the
// EventBus (NOT fillCh -- see rev-2 note) and clears the position. Mimics
// OCC auto-exercise so backtest no longer leaves option positions open
// after expiry.
func (b *Broker) ExpireOptions(ctx context.Context, barTime time.Time) { ... }
```

Constructor change: `Broker` needs an `eventBus ports.EventBusPort` field (already has access via runner; verify whether SimBroker today holds a bus reference -- if not, add it through `Config` or a setter). If adding the bus to SimBroker is too invasive, a callback `OnExpiryFill func(payload map[string]any)` injected by the runner is the smaller alternative. The runner then publishes; broker just collects expired contracts and hands them up. Pick callback approach if SimBroker has no bus today -- it's strictly less coupling.

**Lock pattern (corrected from rev 1)**: Existing pattern at broker.go:728-744 emits while holding `b.mu.Lock`. ExpireOptions does the same: scan positions under lock, build a slice of expiry events, mutate position map, release lock, then publish each event. Publishing AFTER unlock matters here because EventBus.Publish to a sync subscriber (`runner.handleFill`) can call back into the broker via `b.GetPosition` or similar accessors; doing it under lock could re-enter. This DOES differ from `SubmitOrder` (which emits under lock via non-blocking `select`/`default`), but the difference is justified: SubmitOrder uses fillCh with backpressure default-drop semantics; we're using direct EventBus.Publish which is synchronous and re-entrant-vulnerable. Document this divergence inline.

Use `domain.CalendarFor(domain.AssetClassEquity).SessionClose(expiry)` to get the precise close time. Handles half-days.

Idempotent: positions with qty=0 are skipped, so calling ExpireOptions multiple times for the same bar is safe.

### Phase 2 -- wire into backtest runner

Two call sites:

(a) Per-bar after `EvalExitRules` and BEFORE the existing `EventBus.Flush()` at runner.go:2094-2095:

```go
if r.infra.SimBroker != nil {
    r.infra.SimBroker.ExpireOptions(ctx, minTime)
}
r.infra.EventBus.Flush()
```

ExpireOptions runs AFTER EvalExitRules so any same-bar strategy-driven exits (e.g., chandelier_trail giveback at session close) land first. Idempotency guarantees double-close safety: if STC closed the position to qty=0, ExpireOptions skips it.

(b) Mandatory inside the final EOD-flatten tick at runner.go:2178 (rev 1 had this as optional; reviewer requested mandatory):

```go
posMonBundle.Service.EvalExitRules(lastClose)
if r.infra.SimBroker != nil {
    r.infra.SimBroker.ExpireOptions(ctx, lastClose)
}
r.infra.EventBus.Flush()
```

This catches any options whose expiry == backtest end date.

### Phase 3 -- data-quality counter

Add `optionsExpiredMissingUnderlying int` to the broker's stats. Increment when underlying price is unavailable at expiry. Surface in the data-quality summary at runner.go:2189 alongside `options_synthetic_hits` etc. Without this counter, silent-zero intrinsic for ITM-but-unpriced contracts would be a known footgun.

### Phase 4 -- tests

`backend/internal/adapters/simbroker/expiry_test.go` (new):

1. `TestExpireOptions_ITMCall_EmitsSellAtIntrinsic`: long CALL, underlying above strike, advance past expiry, assert FillReceived at intrinsic, position cleared.
2. `TestExpireOptions_OTMCall_EmitsSellAtZero`.
3. `TestExpireOptions_ITMPut_EmitsSellAtIntrinsic`.
4. `TestExpireOptions_OTMPut_EmitsSellAtZero`.
5. `TestExpireOptions_PreExpiry_NoFill`.
6. `TestExpireOptions_Idempotent`: call twice for the same bar, only one fill.
7. `TestExpireOptions_UnknownUnderlyingPrice_FillsAtZero`: no priceCache entry, intrinsic=0 + counter incremented + warn log.
8. `TestExpireOptions_NonOption_Untouched`.
9. **NEW** (reviewer): `TestExpireOptions_HalfDaySession`: option dated 2026-11-27 (Black Friday early-close 13:00 ET). Bar time 13:00 ET fires the sweep; bar time 12:59 does not.
10. **NEW** (reviewer): `TestExpireOptions_BoundaryInclusive`: bar time exactly == expiry session close fires the sweep. Pins the >= semantics.
11. **NEW** (reviewer): `TestExpireOptions_DualLegSameDay`: same underlying, one CALL and one PUT, both expiring same day. Both close in one sweep, map-iteration-mutation safe.
12. **NEW** (reviewer): `TestExpireOptions_AfterSTC_NoDoubleClose`: position closed by an STC at 15:55 leaves qty=0. ExpireOptions at 16:00 sees qty=0 and emits no second fill. Pins the idempotency contract that lets order-of-operations work.

Integration: `backend/internal/app/backtest/runner_option_expiry_test.go`. Seed a copytrade-like scenario with three contracts whose expiries fall inside the backtest window. Run through expiry. Assert collector receives SELL fills with `exit_reason=OPTION_EXPIRY` and dashboard-visible state shows positions closed.

## Out of scope

- IBKR exercise commission ($0.55 / contract).
- ITM-call assignment into equity position (cash-settle instead). Rationale: downstream consumers (collector P&L, equity position map, buying-power calc) treat option P&L as cash-settled; assignment-into-equity would require mutating all three. Cash-settle is the smaller, correct-for-this-purpose model.
- Cash-settled index options (SPX). The intrinsic SELL we emit matches their settlement semantics; no special handling needed but verify OCC parser surfaces them.
- Dividend-driven early exercise of ITM calls (the day before ex-div). Out of scope.
- SHORT option positions (assignment risk pre-expiry). Mechanically supported, modeling out of scope.
- Live-path broker behavior. IBKR/Alpaca already handle expiry; `expired OCC absent` log at positionmonitor/bootstrap.go:125 remains valid.
- Replay-restart where backtest start > expiry: positions are seeded fresh per backtest run, not loaded from persisted state. ExpireOptions during bootstrap is NOT needed.
- Portfolio-scale (1000s of contracts): O(N) per bar is fine for copytrade (cap=50). TODO comment in code: maintain sorted-by-expiry index for portfolio backtests if N grows.

## Blast radius

- SimBroker: new method, new EventBus dependency (or callback), new stat counter.
- backtest/runner.go: two added call sites (per-bar + EOD).
- No changes to strategy code, position_monitor, execution service, or live broker adapters.
- Existing fills.csv schema reused; new rows carry `exit_reason=OPTION_EXPIRY` tag.
- Tests: 12 new unit tests + 1 new integration test.

## Success criteria

After all phases applied and rebuilt:

1. `go test ./backend/internal/adapters/simbroker/... ./backend/internal/app/backtest/...` green.
2. HTTP backtest of copytrade_v1 (2026-01-27 to 2026-04-23) completes successfully.
3. **TIGHTENED** (per reviewer): `_workspace/copytrade_replay/fills.csv` contains SELL rows with `exit_reason=OPTION_EXPIRY` for these specific OCC symbols whose expiry falls inside the window:
   - `INTC260320C00043000` (exp 2026-03-20)
   - `NVDA260213P00185000` (exp 2026-02-13)
   - `TSLA260213P00405000` (exp 2026-02-13)
   These three are the open-at-end-of-backtest positions from the user's dashboard screenshot. If a position never opens (e.g., risk_sizer rejection cascading from cap=50 reshuffle), substitute the next analogous open-and-expired contract; report the substitution.
4. Dashboard view shows zero "open" positions for option contracts whose expiry < backtest end.
5. `KWEB260821C00040000` (exp AFTER backtest end) still shows as open -- correct semantics.
6. No regression on race-fix (PR #102): zero `copytrade_exit_request: no matching position` warnings in the run.
7. omo-replay CLI run with the same date range produces the same expiry-close fills.
8. Data-quality summary at end of run includes `options_expired_missing_underlying` field (zero is good; non-zero surfaces a coverage gap).

## Execution order

1. Phase 1: implement `ExpireOptions` on `*Broker` + EventBus or callback dependency + 12 unit tests in TDD.
2. Phase 2: wire into backtest/runner.go (per-bar + EOD).
3. Phase 3: add the missing-underlying counter, surface in data-quality summary.
4. Build omo-core, restart, run HTTP backtest, verify success criteria 2-8.
5. Open PR.

## Risk register

- **OCC parser edge cases** (mini contracts, weeklies): rely on `domain.ParseOCC`. Skip with warn if it returns ok=false. Defensive test included.
- **Underlying price stale or missing**: counter + warn. Acceptable for cash-settle approximation.
- **STC + expiry double-close**: covered by idempotency; pinned by test #12.
- **Backtest end < expiry**: no fill, position correctly remains open (KWEB case).
- **Lock-pattern divergence from SubmitOrder**: documented inline. Necessary because direct EventBus.Publish is re-entrant; fillCh's non-blocking default-drop pattern doesn't apply.
- **No bus on SimBroker today**: if true, prefer callback injection over adding a bus dependency. Decide during Phase 1 implementation; document choice.
