# Copytrade exit race fix plan (rev 2)

Rev 1 reviewed: qa-inspector BLOCKING_ISSUES (re-entrant deadlock + omo-replay subscription order), code-reviewer APPROVE_WITH_REVISIONS (test coverage, success criteria tolerance, re-entrancy precondition). This rev addresses every flagged item.

Follow-up to PR #101. The HTTP backtest now produces SELL fills (3 of 7 BUYs closed) but 4 dispatched `copytrade_exit_request` events still warn "no matching position" because position_monitor has not registered the BUY by the time the strategy drains its queued STCs.

## Evidence (PR #101, BT `bt-624af21f71290adb`)

Microsecond timeline for TSLA260204C00445000:

```
22:54:55.214082  fill record decision (TSLA fill at SimBroker)
22:54:55.214106  ledger writer: fill recorded (perf ledger_writer via async sub)
22:54:55.214229  copytrade_exit_request: no matching position (strategy drained queued STC)
22:54:55.214262  order filled -- trade persisted and FillReceived emitted
22:54:55.217984  registering monitored position (position_monitor processed channel item)
```

Strategy drain fires 156us BEFORE FillReceived is published, ~3.9ms BEFORE position_monitor registers. 4 of 5 STC dispatches in iter 6 hit this race.

## Root cause (verified)

Two-level async on the position_monitor side:

1. `positionmonitor/service.go:438` -- `SubscribeAsync` for FillReceived. Sync handlers like `runner.handleFill` (`strategy/runner.go:985`) run in the publisher's goroutine BEFORE the async pool delivers.
2. `handleFillEvent` (handlers.go:16-77) only parses the payload and pushes a `fillMsg` onto `s.fills`. Actual registration happens in `processFill`, called from `runTickLoop` (live) or `EvalExitRules -> drainFills` (backtest, once per bar).

In backtest, the BTO fill happens mid-bar; the strategy mutates Pending=false inline and on the NEXT OnEvent drains queued STCs. `drainFills` for this bar has not yet run, so `s.positions` is missing the entry.

## Fix approach: synchronous processFill in backtest mode

When `s.disableTickLoop` is true, bypass the channel entirely. Subscribe `handleFillEvent` synchronously and have it call `processFill` inline. `processFill` already takes `s.mu` (service.go:673-674), so the bus subscriber must NOT re-lock -- just call directly. Live mode unchanged: async + channel + tick-loop preserved.

### Why not the other options

- Tick-boundary drain callback: more runner machinery; bigger surface for one race.
- Execution-service pre-register: tight coupling, breaks layering.
- Per-position sim-time gate: hacky; only bounds, doesn't fix.

## Pre-conditions (hard gates, must verify before Phase 2 lands)

These were soft "audit" items in rev 1. Promoted to hard gates per reviewers.

### PC-1 -- processFill takes no re-entrant lock and emits no events

Audit `processFill` (service.go:672-906) and its transitive callees. Required properties:

- Takes `s.mu` exactly once (already does: line 673-674).
- Does NOT call `s.emit` (would re-enter bus.Publish while we hold s.mu on the publisher goroutine).
- Does NOT call `s.triggerExit` (which emits) directly.
- Does NOT block on a channel that the actor goroutine is supposed to drain.

QA already spot-checked: processFill mutates `s.positions`, calls `priceCache.UpdatePrice`, `optionsPricePort.GetOptionPrices` (500ms timeout), `specStore.GetLatest`, `earningsCalendar.GetNextEarnings`. None of these emit events. The 500ms options-price port timeout is the only blocking concern -- acceptable for backtest, but document the worst-case publisher stall.

If audit fails (any transitive emit or self-lock found), abort Phase 1/2 and switch to the deferred-drain alternative.

### PC-2 -- omo-replay subscription order

QA found this critical: in `backend/cmd/omo-replay/main.go`, per-shard `Runner().Start()` (~L708) happens BEFORE `posMonBundle.Service.Start(ctx)` (~L728). Strategy runner subscribes FillReceived FIRST, so even with the Phase 1 gate change, `runner.handleFill` still fires before `positionmonitor.handleFillEvent` on the CLI path. Success criterion #5 (omo-replay parity) fails until reordered.

Phase 3 must REORDER the omo-replay wiring (move posMon Start before per-shard Start), not just audit.

For HTTP backtest (`backend/internal/app/backtest/runner.go`), order is correct today: posMon Start at L1366 precedes runner Start at L1386/L1705. Phase 3 still adds a regression test to lock the order in (item 2 of reviewer feedback).

## Implementation

### Phase 1 -- conditional sync subscription in `positionmonitor.Service.Start`

File: `backend/internal/app/positionmonitor/service.go:437-440`.

Today:

```go
if err := s.eventBus.SubscribeAsync(ctx, domain.EventFillReceived, s.handleFillEvent); err != nil {
    return fmt.Errorf("position_monitor: failed to subscribe to FillReceived: %w", err)
}
```

Change to:

```go
// Backtest path requires sync delivery so processFill registers the
// position before runner.handleFill's drain pre-amble fires. Live path
// stays async to keep the bus publisher off the actor's hot path.
if s.disableTickLoop {
    if err := s.eventBus.Subscribe(ctx, domain.EventFillReceived, s.handleFillEvent); err != nil {
        return fmt.Errorf("position_monitor: failed to subscribe to FillReceived: %w", err)
    }
} else {
    if err := s.eventBus.SubscribeAsync(ctx, domain.EventFillReceived, s.handleFillEvent); err != nil {
        return fmt.Errorf("position_monitor: failed to subscribe to FillReceived: %w", err)
    }
}
```

Reviewer-2 suggested introducing `syncFillProcessing bool` to decouple the discriminator. Deferred: this is a one-site usage; an extra field is over-engineering. Add the comment above (intent is documented at the gate).

### Phase 2 -- inline processFill in `handleFillEvent` when tick loop disabled

File: `backend/internal/app/positionmonitor/handlers.go:55-75`.

Construction of `fillMsg` stays where it is. The branch is:

```go
msg := fillMsg{...} // construction unchanged

// Backtest mode: process inline so s.positions is populated before bus
// delivers FillReceived to other sync subscribers (strategy runner).
// processFill takes s.mu itself -- do NOT re-lock here, that would
// self-deadlock (sync.RWMutex is non-reentrant).
if s.disableTickLoop {
    s.processFill(msg)
    return nil
}

select {
case s.fills <- msg:
default:
    s.log.Warn().Str("symbol", symbol).Msg("position monitor: fill channel full, dropping fill")
}
return nil
```

The fix from rev 1: NO `s.mu.Lock()` wrapper. `processFill` already locks. Re-locking sync.RWMutex deadlocks.

### Phase 3 -- omo-replay subscription reorder (required) + HTTP backtest order regression test

#### 3a -- omo-replay reorder

File: `backend/cmd/omo-replay/main.go` around L705-728.

Today (verified by QA):
```
L705/L708: per-shard Monitor().Start() and Runner().Start()
L728: posMonBundle.Service.Start(ctx)
```

Move posMonBundle.Service.Start(ctx) BEFORE the per-shard Start loop (target: before L705). Strategy runner's FillReceived Subscribe must happen after position_monitor's Subscribe so sync delivery hits position_monitor first.

If posMonBundle has dependencies on shard state, refactor only what is needed to move its Start earlier (likely none -- posMon is independent of shards). If a real dependency surfaces, abort the reorder and instead defer drain via callback (fallback approach noted in rev 1 risks).

#### 3b -- HTTP backtest subscription-order regression test

New file: `backend/internal/app/strategy/runner_fill_subscription_order_test.go` (or extend existing wiring test).

Wire a real in-memory bus, register handlers in the same order the backtest runner does, publish a synthetic FillReceived, assert handler invocation order via a shared counter (handler N records counter.Add(1) and stores its position). Asserts: position_monitor.handleFillEvent index < strategy.runner.handleFill index.

This is the reviewer's "lock in" item. Without it, a future bootstrap refactor silently reintroduces the bug.

### Phase 4 -- tests

A. `positionmonitor/handlers_test.go` (extend or new file):

1. `TestHandleFillEvent_BacktestPath_InlineProcessFill` -- `disableTickLoop=true`; publish a synthetic FillReceived via the gated handler call; assert `s.positions[key]` is populated synchronously, `s.fills` channel is empty (never used).
2. `TestHandleFillEvent_BacktestPath_TwoFillsSameTick` -- (NEW from reviewer-2 item 2). Publish two FillReceived back-to-back for different contracts; assert both positions registered before either bus.Publish returns. This catches the concurrent-fill race head-on.
3. `TestHandleFillEvent_LivePath_ChannelEnqueue` -- `disableTickLoop=false`; publish; assert `s.fills` has one message and `s.positions` is empty until the tick.
4. `TestHandleFillEvent_LivePath_TickProcesses` -- (NEW from reviewer-2 item 2). Confirm "positions stays empty until tick" then run a tick and verify registration -- locks in the live preserved behavior.

B. Integration via HTTP backtest end-to-end:

- POST /backtest/run, `strategies=["copytrade_v1"], from=2026-01-27, to=2026-04-23, speed=max`.
- Inspect fills.csv and the run-tagged log slice.

### Phase 5 -- DROPPED

Rev 1 had an optional defensive sentinel for subscription order. Dropped per reviewer-2 (item 5): replaced by the deterministic test in Phase 3b.

## Out of scope

- Live-mode async-to-sync conversion. The two-goroutine actor model in live mode is deliberate; do not touch.
- `revaluator.go:67` and `perf/ledger_writer.go:181` async subscriptions. Not on the copytrade exit path. QA confirmed revaluator's SetEntryThesis only writes into the already-registered position via s.mu -- under this fix the position is in the map before bus.Publish returns, so async revaluator finding it later is fine.

## Blast radius

- Phase 1+2: only backtest-mode behavior changes (gated on `disableTickLoop`). Live untouched.
- Phase 3a: omo-replay CLI wiring reorder. Live HTTP path unaffected (already ordered correctly).
- Phase 3b: test-only.
- Phase 4: tests only.

## Risk register

- **PC-1 fails** (processFill emits or self-locks): switch to deferred-drain alternative (strategy callback queue flushed at next tick boundary). Reviewer-2's lock-order note: holding s.mu across processFill on the publisher goroutine is safe only because processFill does not call back into the strategy. Confirmed by QA spot-check; recheck during Phase 2 implementation.
- **omo-replay reorder breaks shard wiring**: fallback is to keep current order in omo-replay and accept that the CLI parity check (success #5) only hits parity after a separate follow-up. Document in Phase 3a if encountered.
- **Channel-full warning becomes dead code in backtest**: leave the branch in place (it remains live behavior). Do not delete (per surgical-changes).
- **addPosition idempotency on burst FillReceived for same contract**: should be idempotent (positions map keyed by tenant:env:symbol). Add a unit test asserting two consecutive fills for the same symbol don't double-register (extension to A.2 above).

## Success criteria

After all phases applied and rebuilt:

1. `go test ./backend/internal/app/positionmonitor/... ./backend/internal/app/strategy/...` green.
2. HTTP backtest POST as Phase 4-B completes successfully.
3. **PRIMARY**: New backtest log slice contains zero `copytrade_exit_request: no matching position` warnings tagged with the new backtest_id. (Promoted from secondary per reviewer-2 item 3.)
4. **SECONDARY**: `_workspace/copytrade_replay/fills.csv` SELL fills equals strategy `partial close` + `position closed` event count (no tolerance for races -- tightened from rev 1's `-1`).
5. SELL/BUY unique-contract ratio >= 0.6 (PR #101 shipped 0.43).
6. omo-replay CLI run with the same date range and `--strategies copytrade_v1` produces the same fills.csv shape; zero "no matching position" warnings on the CLI path too.

If any criterion fails, capture evidence, return to investigation, draft a rev 3 plan, re-execute.

## Execution order

1. PC-1: read processFill + callees, confirm no transitive emit/triggerExit. If fails, switch to deferred-drain plan.
2. PC-2: reorder omo-replay (Phase 3a) -- or document fallback.
3. Phase 1: gate at service.go:437-440.
4. Phase 2: inline processFill at handlers.go:55-75 (NO s.mu.Lock wrapper).
5. Phase 3b: subscription-order regression test.
6. Phase 4: unit + integration tests.
7. Build omo-core, restart, POST /backtest/run, verify success criteria.
8. Open PR.
