# Sprint 3.5 Implementation Plan: Flag Removal

> Date: 2026-04-11
> Goal: Flip `OMO_ORDER_JOURNAL_ENABLED` to always-on and delete the legacy cancel-all branches so the Sprint 2 journal becomes the default, unconditional assumption everywhere.
> Estimated effort: 30 minutes
> Branch: same as Sprint 2 (`feat/robustness-sprint-1`), or a new `feat/robustness-sprint-3.5` branch — your choice

---

## Trigger condition

**Do NOT start this sprint until:**
- [ ] Sprint 2 has been deployed to production with `OMO_ORDER_JOURNAL_ENABLED=true`
- [ ] At least **24 hours of market hours** have elapsed with the journal path active
- [ ] `SELECT status, COUNT(*) FROM order_intents GROUP BY status ORDER BY 1;` shows the expected distribution:
  - `submitted` and `filled` are the majority
  - `rejected` rows are investigated (each one = a journal-write failure or broker rejection)
  - Zero unexpected `lost` rows
  - Zero unexpected `pending_submit` rows older than a few minutes (would indicate a crash mid-submit)
- [ ] No new errors in Loki tagged `journal write failed` or `OrderIntentRejected` with reason `journal_write_failed`
- [ ] No Discord alerts about unmanaged broker orders (would indicate the reconciliation logic has a blind spot)

Until all boxes check, keep the flag as an emergency off-switch.

---

## Problem statement

Sprint 2 shipped the write-ahead order journal + journal-aware startup reconciliation behind a feature flag (`OMO_ORDER_JOURNAL_ENABLED`, default false). The flag exists to de-risk the initial deploy: if the journal code path has a latent bug, operators can flip `OMO_ORDER_JOURNAL_ENABLED=false` in the env and restart to get the old behavior back, with zero code changes.

Once the journal has proven itself under real load, the flag becomes a liability:

1. **Two code paths to test, review, and maintain.** Every future change to execution or positionmonitor has to reason about both branches.
2. **Legacy branch can rot.** If nobody runs the flag-off path, it's only tested when someone accidentally unsets the env var — at which point they get the old bug (cancelling protective stops on restart) without warning.
3. **Cognitive load in documentation.** Every comment and log line that mentions the flag is a reader slowdown.
4. **The `intentJournal != nil` guard is load-bearing-by-accident.** Other code might grow to rely on "nil means off" semantics, which entangles the feature flag with the rest of the code.

Remove the flag → delete the legacy branches → the journal becomes the only path.

---

## Scope

**In scope:**
- Flip the default behavior to always-on
- Delete the `intentJournal == nil` branches in execution service and position monitor
- Delete the env var read from config.go
- Delete the `OrderJournalEnabled` field from `config.Config`
- Delete tests that only existed to cover the flag-disabled legacy path
- Update comments and docstrings that reference the flag

**Out of scope:**
- Removing the ability to pass a `nil` journal to execution's `WithIntentJournal` or positionmonitor's `WithIntentJournal` — backtests still rely on that nil-default to skip journaling (backtest runner does not pass `IntentJournal` in `ExecutionDeps`). The **runtime** default changes; the **constructor** option stays.
- Changing the journal schema
- Changing the reconciliation logic
- Adding new journal fields or events

The distinction matters: backtests remain journal-less (correct isolation). Only the production path (`cmd/omo-core`) loses the flag gate.

---

## Surface area

### Files to modify

| File | Change |
|------|--------|
| `backend/internal/config/config.go` | Delete `OrderJournalEnabled bool` field + env var read |
| `backend/cmd/omo-core/services.go` | Delete the `if cfg.OrderJournalEnabled { ... }` wrapping around `intentJournal := infra.orderIntentRepo`. Unconditionally assign. |
| `backend/cmd/omo-core/infra.go` | Verify `orderIntentRepo` is constructed unconditionally (it already is, per the earlier scan — no change needed, just confirm) |
| `backend/internal/app/positionmonitor/order_reconcile.go` | Delete the `if s.intentJournal == nil` fallback-to-cancel-all branch. Assume non-nil. Delete the comment explaining "legacy path". |
| `backend/internal/app/positionmonitor/order_reconcile_test.go` | Delete `TestReconcileOpenOrdersOnBoot_FlagDisabled_PreservesLegacyCancelAll` — this test asserts the legacy behavior we're deleting. |
| `backend/internal/app/execution/service.go` | **Do NOT touch the `if s.intentJournal != nil` guards at lines 698, 735, 776, 1170, 1244.** Those must remain because backtests pass nil. The production entry point will always pass a non-nil journal post-Sprint-3.5, but the execution service must still support nil for backtest isolation. |
| `backend/internal/adapters/ibkr/broker.go` | Verify no flag reference (likely none) |
| `backend/internal/ports/order_intent_journal.go` | Remove the paragraph in the interface doc comment that says "Feature-flagged: when OMO_ORDER_JOURNAL_ENABLED is unset/false, the execution pipeline bypasses this interface entirely" |

### Files to NOT modify

- `backend/internal/app/execution/service.go` — the `intentJournal != nil` checks stay (backtest isolation). Only the doc comment should mention that nil means backtest.
- `backend/internal/app/positionmonitor/service.go` — the `WithIntentJournal` option stays (backtest isolation). Only the doc comment changes.
- Any SPRINT_1_PLAN.md / SPRINT_2_PLAN.md — historical records, leave as-is
- `tmp/others/*` — comments about the flag are historical context

---

## Execution steps

### Step 1: Delete the config field

```go
// backend/internal/config/config.go — remove these lines
OrderJournalEnabled bool `yaml:"-"`

// and the env read
if val := os.Getenv("OMO_ORDER_JOURNAL_ENABLED"); val == "true" {
    cfg.OrderJournalEnabled = true
}
```

### Step 2: Unconditional wiring in services.go

```go
// Before:
var intentJournal ports.OrderIntentJournal
if cfg.OrderJournalEnabled {
    intentJournal = infra.orderIntentRepo
    log.Info().Msg("order intent journal enabled — write-ahead audit active")
}

// After:
intentJournal := infra.orderIntentRepo
log.Info().Msg("order intent journal active — write-ahead audit on")
```

### Step 3: Remove the legacy fallback in positionmonitor/order_reconcile.go

```go
// Before:
func (s *Service) reconcileOpenOrdersOnBoot(ctx context.Context) {
    if s.intentJournal == nil {
        // Legacy cancel-all path
        if canceled, err := s.broker.CancelAllOpenOrders(ctx); err != nil {
            ...
        }
        return
    }
    // journal-aware path
    brokerOpen, err := s.broker.GetOpenOrders(ctx)
    ...
}

// After:
func (s *Service) reconcileOpenOrdersOnBoot(ctx context.Context) {
    // Backtest path: no journal wired → skip reconciliation entirely.
    // The backtest runner owns its own position bootstrap.
    if s.intentJournal == nil {
        return
    }
    // Production always has a journal. Any error in the queries falls
    // back to CancelAllOpenOrders for safety — same as before.
    brokerOpen, err := s.broker.GetOpenOrders(ctx)
    ...
}
```

**Critical:** the `if s.intentJournal == nil` check **stays**, but its semantics change from "flag-off production mode" to "backtest mode, skip reconciliation". Without this the backtest runner — which deliberately passes no journal — would start calling `GetOpenOrders` on its simbroker, which returns empty, which then calls `OpenIntents` on a nil journal, which is a panic.

### Step 4: Delete the FlagDisabled test

```go
// backend/internal/app/positionmonitor/order_reconcile_test.go
// DELETE TestReconcileOpenOrdersOnBoot_FlagDisabled_PreservesLegacyCancelAll
// It asserts the cancel-all fallback fires when journal is nil — no longer true.
```

Replace with a test that asserts the new nil-journal behavior:

```go
func TestReconcileOpenOrdersOnBoot_NilJournal_NoOp(t *testing.T) {
    // Backtest path: no journal → skip reconciliation entirely.
    // Must NOT call CancelAllOpenOrders, GetOpenOrders, or OpenIntents.
    broker := &mockBroker{}
    s := newReconcileService(t, broker, nil)

    s.reconcileOpenOrdersOnBoot(context.Background())

    assert.Equal(t, 0, broker.cancelAllCalls, "nil journal must not call CancelAllOpenOrders")
    assert.Equal(t, 0, broker.getOpenOrdersCalls, "nil journal must not call GetOpenOrders")
}
```

(Requires adding a `getOpenOrdersCalls` counter to `mockBroker` if not already present.)

### Step 5: Update doc comments

- `backend/internal/ports/order_intent_journal.go` — drop the feature-flag paragraph
- `backend/internal/app/positionmonitor/service.go` — `WithIntentJournal` docstring should say "nil = backtest mode (no reconciliation)" instead of "nil = legacy cancel-all"
- `backend/internal/app/execution/service.go` — the comment on `intentJournal` field should say "Sprint 2 write-ahead journal. Nil in backtest mode only." (not "nil when flag is false")
- `backend/internal/app/bootstrap/execution.go` — comment on `ExecutionDeps.IntentJournal` field should say "optional — nil in backtest mode"

---

## Testing

### Unit tests
- Replace the FlagDisabled test with the NilJournal test as above
- Confirm all existing order_reconcile tests still pass
- Confirm all existing journal repo tests still pass
- Confirm backtest runner tests still pass (they pass nil; reconciliation should no-op correctly)

### Integration
- Run `journal-repo-smoke` — should still pass unchanged
- Run `submit-limit-order` + psql stage + restart → MATCHED reconciliation should still fire correctly
- Restart omo-core WITHOUT setting any env vars → the "order intent journal active" log line should appear unconditionally

### Regression
- `go build ./...` clean
- `go vet ./...` clean
- `go test ./internal/app/execution/... ./internal/app/positionmonitor/... ./internal/adapters/timescaledb/... ./cmd/omo-core/...` all green
- `go test ./internal/app/backtest/...` still green (backtest isolation preserved)

---

## Commit strategy

One commit:

```
refactor(robustness): remove OMO_ORDER_JOURNAL_ENABLED flag — journal is the default

Sprint 2 shipped the write-ahead order journal + journal-aware startup
reconciliation behind OMO_ORDER_JOURNAL_ENABLED so operators could flip
back to the legacy behavior if the journal had a latent bug. After
[N days] of real signals passing through the journal path in production
with zero failures — journal rows flowing through pending_submit ->
submitted -> filled as designed, no spurious lost rows, no Discord
alerts about unmanaged broker orders — the safety net is no longer
earning its cost.

Deletes:
  - OrderJournalEnabled from config.Config
  - OMO_ORDER_JOURNAL_ENABLED env var read in config.Load
  - The cfg.OrderJournalEnabled gate in cmd/omo-core/services.go
  - The nil-intent-journal fallback-to-cancel-all branch in
    positionmonitor/order_reconcile.go
  - TestReconcileOpenOrdersOnBoot_FlagDisabled_PreservesLegacyCancelAll

Keeps:
  - The intentJournal != nil guards in execution/service.go
  - The WithIntentJournal option on the position monitor
  Both remain because backtests deliberately pass no journal for
  isolation from production writes. The nil check is no longer a
  feature flag — it means "backtest mode".

Production now unconditionally:
  - Writes every OrderIntent to order_intents before broker submission
  - Updates the row with broker_order_id on submission
  - Updates the row with terminal state on fill/cancel/reject
  - Reconciles broker open orders against the journal on startup

Sprint 3.5 from tmp/others/ROADMAP.md, gated on Monday validation
plus N days of clean production logs per SPRINT_3_5_PLAN.md trigger
conditions.
```

---

## Risks

1. **Backtest runner regression.** If the new `if s.intentJournal == nil { return }` short-circuit is wrong, backtests would either skip necessary setup or crash. Mitigation: run the full backtest suite as part of the test gate.

2. **Log noise.** Removing the `cfg.OrderJournalEnabled` check means the "order intent journal active" log always fires. This is already the case post-flag — just confirming.

3. **Staging mismatch.** If staging is still running with the flag unset when this ships, staging breaks on restart. Mitigation: verify staging has the same journal migration applied and the flag set before merging.

4. **Rollback cost.** After this sprint, rolling back the journal requires a code revert (not an env var flip). Mitigation: that's exactly the cost the trigger conditions are designed to price in.

---

## Acceptance criteria

- [ ] `grep OMO_ORDER_JOURNAL_ENABLED backend/` returns zero matches
- [ ] `grep OrderJournalEnabled backend/` returns zero matches
- [ ] Unconditional "order intent journal active" log on startup (no flag gate)
- [ ] Backtest runner tests still pass
- [ ] All existing positionmonitor reconciliation tests still pass
- [ ] Fresh startup on production shows reconciliation running the journal-aware path unconditionally
- [ ] Commit landed on `feat/robustness-sprint-1` or a new branch, pushed to origin
- [ ] `ROADMAP.md` updated: Sprint 3.5 status → ✅ shipped
