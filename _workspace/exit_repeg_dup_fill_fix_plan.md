# Exit Re-Peg Duplicate-Fill Race — Execution Plan

Status: READY (no code written)
Drafted: 2026-04-29
Trigger incident: NFLX260515P00090000, 2026-04-28 18:30:12 UTC, account `default`
Prior incident: RIVN260508C00017500, 2026-04-27 (defensive guard `1c415107`)
Owners: backend (`go-architect`), live-ops verification (`live-ops`)

## TL;DR

Position monitor's exit re-peg flow today is `cancel orderA` + `place orderB`.
IBKR can fill both within ~60ms when the cancel loses the race to a fill —
producing duplicate SELL trade rows and a negative DB net position. Two
structural fixes:

- **Phase 1 (Fix B)**: backfill `execution_id` on the poll-path fill writer
  so the existing `idx_trades_execution_id` UNIQUE index can dedup any
  same-exec duplicate. Small, low-risk; closes a gap flagged in `1c415107`.
- **Phase 2 (Fix A)**: replace the cancel+place re-peg with an atomic
  IBKR modify (ibsync's `PlaceOrder` reuses the same `OrderID`). Eliminates
  the broker-side race entirely. Behind a config flag for safe rollout.

Phase 1 ships first as one PR. Phase 2 ships second as one PR plus a tiny
flag-flip follow-up.

## Root cause

Re-peg flow at `backend/internal/app/positionmonitor/exit_eval.go:324-498`:

```
handleExitTimeout
  -> MarkRepegCancel(orderA)           [tag for terminal suppression]
  -> CancelOrder(orderA)               [exit_eval.go:538]
  -> wait for terminal status (poll)   [exitCancelConfirm]
  -> triggerExit(...)                  [emits new intent → orderB]
```

If IBKR fills `orderA` after the cancel was sent but before it lands, both
fills come back through `execution.handleStreamFill` /
`execution.fastPollPosition` and both call `insertFillLeg`. Net DB qty goes
to `BUY 20 - SELL 20 - SELL 20 = -20`. Reconciler at
`positionmonitor/reconcile.go:172` correctly refuses to write a synthetic
trade and surfaces `negative DB net detected` for manual review.

Why the existing `shouldBlockExitFill` guard (`execution/service.go:1908`,
shipped in `1c415107`) doesn't catch it: it reads `pos.Quantity` from the
position monitor's in-memory state, which is updated asynchronously via
the FillReceived event bus. Both NFLX fills landed within 60ms — faster
than the monitor could drain the first event and decrement qty. Both pass
`legQty <= openQty + 1e-9`.

Why the `idx_trades_execution_id` UNIQUE index doesn't catch it: the
poll-path writer (`fastPollPosition` → `recordFillFromDetails` →
`handleFillWithPrice` → `insertFillLeg`) calls insert with `executionID=""`.
The partial UNIQUE index (`WHERE execution_id IS NOT NULL`,
migration `018`) skips NULL rows. Stream-path writes always carry
`execution_id`; poll-path never does.

## Architecture decision

Two fixes, sequenced. Each closes a different layer:

- **Fix B is a defensive ledger guard.** Catches "same broker exec
  recorded twice" — the dedup gap between stream and poll. Doesn't fix
  NFLX/RIVN by itself (those have two distinct execIDs from IBKR), but
  closes the empty-execution_id hole the prior commit explicitly flagged.
- **Fix A is the structural fix.** Eliminates the cancel-fill race at the
  broker — there is never a second order to race. NFLX/RIVN cannot recur
  on IBKR.

Ship Fix B first because:
- ~150 LOC vs. ~300-500 for Fix A.
- No state-machine changes; only the fill writer.
- Flips a strict superset of dedup defenses on without touching exit logic.
- Prerequisite for clean per-leg accounting on the poll path.

Fix A goes behind a flag because the position monitor exit state machine
inherits assumptions about orderID transitions on terminal events.

---

## Phase 1: Fix B — backfill `execution_id` on the poll path

### 1.1 Files

| File | Change |
| --- | --- |
| `backend/internal/app/execution/service.go` | New helper `recordFillsFromExecHistory(po, brokerOrderID, l)`. Wire from `fastPollPosition` (~line 1392, `recordFillFromDetails` call). Keep `recordFillFromDetails` as the fallback. |
| `backend/internal/app/execution/repeg_dup_fill_dedup_test.go` | New test file (4 cases). |
| `backend/internal/adapters/simbroker/broker.go` | Confirm or add `GetAllFills` returning empty slice (FillLister stub) so the helper's fallback path engages cleanly under tests. |

### 1.2 Implementation steps (TDD-first)

1. **RED — write failing tests** in `repeg_dup_fill_dedup_test.go`:
   - `TestPollPath_PopulatesExecutionID`: stub broker exposes `GetAllFills`
     returning one record with `ExecutionID="EX-1"` for the order;
     `fastPollPosition` detection path inserts a trade row with
     `ExecutionID=="EX-1"`.
   - `TestPollPath_MultiLegFill`: GetAllFills returns 2 legs for the same
     `BrokerOrderID` with distinct ExecIDs; verify 2 trade rows inserted
     with sum of `Quantity == intent.Quantity`.
   - `TestPollPath_FillListerUnsupported`: broker does not implement
     `FillLister`; falls back to today's single-leg `recordFillFromDetails`
     write. No panic.
   - `TestPollPath_GetAllFillsError`: broker returns error from
     `GetAllFills`; falls back to `recordFillFromDetails` and logs warn.
2. **GREEN — implement** the helper:
   ```go
   // recordFillsFromExecHistory queries broker for per-exec fill records
   // for brokerOrderID and inserts each leg with its real ExecutionID.
   // Falls back to recordFillFromDetails when the broker doesn't implement
   // FillLister, returns an error, or yields zero matching legs.
   func (s *Service) recordFillsFromExecHistory(po *pendingOrder, brokerOrderID string, l zerolog.Logger) {
       lister, ok := s.broker.(ports.FillLister)
       if !ok {
           s.recordFillFromDetails(po, brokerOrderID, ports.OrderDetails{...}, l)
           return
       }
       fills, err := lister.GetAllFills(context.Background())
       if err != nil {
           l.Warn().Err(err).Msg("recordFillsFromExecHistory: GetAllFills failed — falling back")
           s.recordFillFromDetails(po, brokerOrderID, ports.OrderDetails{...}, l)
           return
       }
       legs := filterByOrderID(fills, brokerOrderID)
       if len(legs) == 0 {
           l.Warn().Str("broker_order_id", brokerOrderID).Msg("recordFillsFromExecHistory: no legs found — falling back")
           s.recordFillFromDetails(po, brokerOrderID, ports.OrderDetails{...}, l)
           return
       }
       l.Info().Str("broker_order_id", brokerOrderID).Int("legs", len(legs)).Msg("recordFillsFromExecHistory")
       for _, leg := range legs {
           s.insertFillLeg(po, brokerOrderID, leg.ExecutionID, leg.FilledAt, leg.Price, leg.Qty, leg.CumQty, leg.AvgPrice, l)
       }
   }
   ```
3. Wire the call into `fastPollPosition` at `service.go:~1392`, replacing
   `recordFillFromDetails`. Behavior on the existing fallback paths is
   unchanged.
4. Confirm `insertFillLeg` already handles the UNIQUE-violation gracefully
   (it should — `RecordFill` returns the error, current code logs and
   continues at `service.go:1849`). If not, ensure a unique-violation
   error is logged at `Info` and skipped, not `Error`.
5. **REFACTOR** — extract `filterByOrderID` if cleaner; otherwise inline.

### 1.3 Tests

Run:
```
cd backend && go test ./internal/app/execution/... -run 'TestPollPath_'
cd backend && go test ./internal/app/execution/... ./internal/app/positionmonitor/... ./internal/adapters/simbroker/...
```

Existing tests that must still pass:
`multi_fill_test.go`, `reconcile_fills_test.go`, `dust_sweep_test.go`,
`backfill_test.go`, `repeg_dup_guard_test.go`, `reconcile_on_boot_test.go`.

### 1.4 Acceptance — Phase 1

Behavioral:
- Every poll-path trade row inserted post-deploy carries a non-empty
  `execution_id` matching the IBKR exec, except where today's reconciler
  intentionally writes empty (sweep/reconcile rows — unchanged).
- A duplicate INSERT against the same `(execution_id, time)` returns the
  expected UNIQUE violation and is logged-and-skipped without panicking
  or breaking the surrounding fill flow.
- Multi-leg fills for one IBKR `BrokerOrderID` produce N trade rows with
  distinct `ExecutionID`s and a sum of `quantity` equal to the order's
  `FilledQty`.

Observable (24-48h after deploy):
- SQL — should be near-zero (only sweep/reconcile rows):
  ```
  SELECT count(*) FROM trades
  WHERE execution_id IS NULL AND time >= '<deploy_ts>'
    AND env_mode='Paper' AND status='FILLED';
  ```
- Loki — at least one paired sequence on a real exit:
  ```
  fast position poll: fill detected via livePos
  recordFillsFromExecHistory ... legs=N
  ```
- Loki — zero new panics or `failed to record fill leg` errors above
  baseline.

Negative (must NOT happen):
- Fill-recording latency regression. The `GetAllFills` RPC must complete
  inside one 200ms `fastPollPosition` tick; if it doesn't, the next tick
  proceeds with the existing fallback (no new latency tail).
- New `broker-only position detected` or `DB orphan confirmed` events
  triggered by the change (would indicate misattributed legs).
- Backtest PF drift > ±1% on AVWAP / MACD baselines (sentinel only —
  simbroker is not on the IBKR path).

### 1.5 Phase 1 verification

```
# Unit + table-driven coverage
cd backend && go test ./...

# Build
cd backend && go build ./...

# Live spot-check after deploy (sample — adjust deploy_ts):
psql -h localhost -U opentrade -d opentrade -c "
  SELECT count(*) FROM trades
  WHERE execution_id IS NULL
    AND time >= NOW() - INTERVAL '24 hours'
    AND env_mode='Paper' AND status='FILLED';"
```

### 1.6 Phase 1 rollback

Single revert of the `fastPollPosition` call site restores
`recordFillFromDetails`. The fallback path remains in place throughout, so
no schema or wire-format change to back out.

---

## Phase 2: Fix A — modify-in-place re-peg

Status: gated behind `repeg.modify_in_place` config flag, default `false`
on first deploy.

### 2.1 Files

| File | Change |
| --- | --- |
| `backend/internal/ports/broker.go` | New `OrderModifier` interface: `ModifyOrder(ctx, brokerOrderID string, newLimit, newQty float64) error`. Sentinel `ErrUnsupportedModify`. |
| `backend/internal/adapters/ibkr/broker.go` | Implement `ModifyOrder` — find existing `Order` in `ib.OpenTrades()`, mutate `LmtPrice`, call `ib.PlaceOrder(contract, order)` reusing OrderID. |
| `backend/internal/adapters/simbroker/broker.go` | Implement `ModifyOrder` returning `ErrUnsupportedModify` (caller falls back). |
| `backend/internal/adapters/ibkr/broker_test.go` | New `TestModifyOrder_ReusesOrderID`. |
| `backend/internal/app/execution/service.go` | New `RepegOrderInPlace(brokerOrderID, newLimit float64) (modified bool, err error)` method on `Service` (satisfies `RepegNotifier` extension). |
| `backend/internal/app/positionmonitor/service.go` | Extend `RepegNotifier` with `RepegOrderInPlace`. |
| `backend/internal/app/positionmonitor/exit_eval.go` | New branch in `handleExitTimeout` (line 324): on `action=="repeg"` and flag-on, try `RepegOrderInPlace` first; fall through to existing cancel+place on failure. |
| `backend/internal/app/positionmonitor/repeg_in_place_test.go` | New test file (3 cases). |
| `configs/config.yaml` | Add `repeg.modify_in_place: false` block. Documented as "experimental — flip after one clean session per success criteria". |

### 2.2 Implementation steps (TDD-first)

1. **RED — write failing IBKR adapter test** `TestModifyOrder_ReusesOrderID`:
   - mockIB harness: prime an open trade for orderID 4118 at LmtPrice 1.595.
   - Call `adapter.ModifyOrder(ctx, "4118", 1.560, 0)`.
   - Assert mockIB recorded a `PlaceOrder` call with `Order.OrderID == 4118`
     and `Order.LmtPrice == 1.560` (i.e. ibsync's "modify" path, per
     `ibsync/ib.go:944-986`).
2. **RED — position monitor tests** in `repeg_in_place_test.go`:
   - `TestHandleExitTimeout_ModifySupported`: flag on, RepegNotifier reports
     modify-supported; verify no `triggerExit` call, `ExitOrderID` unchanged,
     `ExitRepegCount` bumped by 1, `ExitLastSentPrice` updated.
   - `TestHandleExitTimeout_ModifyUnsupported`: flag on but broker returns
     `ErrUnsupportedModify`; falls through to existing cancel+place flow
     (existing tests cover the resulting state).
   - `TestHandleExitTimeout_ModifyRacesFill`: flag on, broker reports the
     order is already terminal; verify `ExitRepegCount` not inflated and
     fall-through engages.
3. **GREEN — broker port + adapters**:
   - Add `OrderModifier` interface + `ErrUnsupportedModify` to
     `ports/broker.go`.
   - IBKR `ModifyOrder`: walk `ib.OpenTrades()`, find matching OrderID,
     mutate `LmtPrice` (and `TotalQuantity` if `newQty > 0`), call
     `ib.PlaceOrder(contract, order)`. Return `nil` if the trade is found;
     `ErrUnsupportedModify` if not (treat "already terminal" as
     unsupported so caller falls through to the existing cancel+place
     resubmit path which handles the now-filled state).
   - simbroker `ModifyOrder`: return `ErrUnsupportedModify`.
4. **GREEN — execution service**:
   - `RepegOrderInPlace(brokerOrderID, newLimit)`: look up `pendingOrder`
     by ID. If broker implements `OrderModifier`, call `ModifyOrder` and
     return `(true, nil)` on success. If `ErrUnsupportedModify` or any
     error, return `(false, err)`.
   - Do NOT update `pendingOrder.intent.LimitPrice` (treat as immutable —
     decision (a) from the prior plan: IBKR fills carry the actual print
     price; the LimitPrice fallback path is a degenerate case rarely hit).
5. **GREEN — position monitor flow**:
   - In `handleExitTimeout` at line 324, when `action == "repeg"`:
     - If flag on and `RepegOrderInPlace` is available, compute the new
       limit (existing repeg pricing logic in `exit_pricer.go`), call
       `RepegOrderInPlace`. On `(true, nil)`: bump
       `ExitRepegCount`, update `ExitLastSentPrice`, set
       `ExitManaging=false`, return without releasing the gate or
       calling `triggerExit`. Emit a structured log
       `repeg: modify-in-place sent` with `broker_order_id`, `old_limit`,
       `new_limit`, `repeg_count`.
     - On `(false, _)`: fall through to existing cancel+place path.
   - On the modify path, **do not** call `MarkRepegCancel` (no cancel
     fires, no terminal event suppression needed).
6. **GREEN — config flag wiring**:
   - Add `RepegModifyInPlace bool` to the relevant config struct, threaded
     into the position monitor on construction (matches existing flag
     pattern — search for one example: `omo-feature` skill or grep
     `bootstrap` for similar bool flags).
7. **REFACTOR** — confirm no test behavior depends on flag-off being the
   default once flag-on tests are added.

### 2.3 Tests

```
cd backend && go test ./internal/adapters/ibkr/... ./internal/app/execution/... ./internal/app/positionmonitor/...
```

Existing must-pass: `dust_sweep_test.go`, `reconcile_fills_test.go`,
`repeg_dup_guard_test.go`, `atr_trail_test.go`, all positionmonitor
service tests.

### 2.4 Flag rollout — off → on

Initial deploy: `repeg.modify_in_place: false`. Behavior is unchanged from
today; the new code paths are dormant.

After one full live session of Phase-1-only operation with zero negative
incidents (the Phase 1 success criteria), flip:

```
# In configs/config.yaml under repeg:
modify_in_place: true
```

Restart omo-core. Watch for `repeg: modify-in-place sent` events on the
first session repeg cycle.

### 2.5 Acceptance — Phase 2

Behavioral:
- With flag ON, a repeg cycle never produces a new IBKR `OrderID`.
  `ExitOrderID` on the position remains constant across repeg attempts;
  only `ExitRepegCount` and `ExitLastSentPrice` advance.
- With flag OFF (or on a broker without `OrderModifier`), today's
  cancel-and-resubmit flow runs unchanged. Every existing test passes;
  every existing log line still fires.
- A modify call that loses the race to a fill returns
  `ErrUnsupportedModify` and the position transitions to post-fill state
  via the existing FillReceived handler. `ExitRepegCount` not inflated.

Observable (one full live session post-flag-on, ~6.5h trading day):
- SQL drift sentinel — must return zero rows for the IBKR session:
  ```
  SELECT broker_order_id, count(*) FROM trades
  WHERE side='SELL' AND time >= '<flag_on_ts>' AND env_mode='Paper'
  GROUP BY broker_order_id
  HAVING count(*) > 1;
  ```
- Loki — zero new `negative DB net detected` alerts on options symbols
  in `Paper` env over the session.
- Loki — `repeg: modify-in-place sent` appears at least once on a session
  that historically would have repegged (any day with PREMIUM_STOP fires).
- Loki — `repeg notify: no pending order — likely already terminal` does
  NOT appear on the modify path (no cancel = no terminal-suppression
  lookup).
- IBKR gateway-side spot-check: same `permId` observed across the original
  and modified parameters.

Negative (must NOT happen):
- New `exit fill rejected` warnings from `shouldBlockExitFill` on the
  modify path (would indicate a duplicate is still reaching the persist
  layer — regression).
- Increase in dust-sweep launch rate. `cleanupPendingOrder` only fires on
  terminal events; a successful modify is not terminal — dust-sweep should
  trigger on the eventual fill exactly once.
- New `RecordExitFailure` trips. `ExitCircuitBroken` count must not exceed
  the prior-session baseline.
- `ExitManaging` stuck `true` across a modify cycle (state-transition bug).
- Backtest PF regression on AVWAP / MACD (simbroker still uses cancel+place
  fallback — sentinel only).

### 2.6 Phase 2 rollout gate (flag-default-off → flag-default-on)

All of:
- One full live session with `repeg.modify_in_place: true` AND ≥3
  observed repeg cycles, all of which:
  - Succeeded via the modify path (logged).
  - Produced exactly one fill row per filled order.
  - Did not trigger any negative signal in 2.5.
- Reconciler smoke test on staging: force a synthetic repeg, verify
  `reconcileGlobal` reports clean (`db_net == broker_net`).
- 24h post-flip without any rollback trigger.

If satisfied, change the config default to `true` in `configs/config.yaml`
and the bootstrap default; keep the flag wire so it can still be flipped
off in an emergency.

### 2.7 Phase 2 rollback

Set `repeg.modify_in_place: false` and restart omo-core. Cancel+place
flow resumes immediately on the next repeg. No data migration; no schema
change.

---

## Overall acceptance — both phases (30-day window)

The full effort is "done" when, over a 30-day live IBKR window after
Phase 2 goes flag-default-on:

- Zero `negative DB net detected` alerts on options symbols.
- Zero rows in:
  ```
  SELECT broker_order_id, count(*) FROM trades
  WHERE env_mode='Paper' AND time >= '<window_start>'
  GROUP BY broker_order_id
  HAVING count(*) > 1;
  ```
  attributable to repeg cycles (reconciler/sweep multi-row inserts
  excluded by their distinct rationale strings).
- Reconciler `position_monitor` component logs continue to fire on real
  drift — verified by injecting a synthetic broker-only mismatch in
  staging once during the window. Silence must be real, not a broken
  alarm.
- No regression in 30-day live PF for AVWAP and MACD strategies.
- `shouldBlockExitFill` guard at `execution/service.go:1908` observes
  zero block events. If it fires, treat as "fix incomplete" and
  investigate before declaring done.

If the window completes clean, retire the `repeg.modify_in_place` flag
and the cancel+place fallback for IBKR (keep cancel+place only for
brokers that don't implement `OrderModifier`).

## Open questions

- Does ibsync surface a distinct error when `PlaceOrder` is called on a
  done trade (`ib.go:964-967`), or does it silently return the original
  Trade? The implementation must distinguish "modify accepted" from
  "modify ignored because order is terminal" — verify with a unit test
  against the mock harness before relying on the ack.
- `OrderModifier.ModifyOrder` currently takes `newQty float64` for
  symmetry; do we have any production path that wants to modify quantity
  during a re-peg? Today's repeg keeps qty constant. If no near-term
  use, leave the parameter in the signature but assert `newQty == 0`
  (sentinel for "leave qty unchanged") in the IBKR adapter for ship-1.

## Out of scope

- `shouldBlockExitFill`'s fundamental raciness with the position monitor's
  in-memory state. Phase 2 makes it nearly unreachable; Phase 1 catches
  the same-exec-twice case at the DB. The remaining "broker actually
  double-filled" residue is a true broker failure mode the reconciler
  should continue to surface for manual review.
- The `MarkRepegCancel` race with `cleanupPendingOrder` (documented at
  `execution/service.go:2076-2092`). Phase 2's modify path removes the
  dependency entirely.
- ATR-override skip-repeg ("if underlying moved > 0.5*ATR(5m) against us,
  skip re-peg and go to market"). Existing TODO at
  `exit_eval.go:336`.
- Alpaca `PATCH /v2/orders` modify support. Alpaca paper is unreliable
  per memory `feedback_alpaca_paper_unreliable.md`; not on the live
  path. Implement when an Alpaca live deploy lands.
