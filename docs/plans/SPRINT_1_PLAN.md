# Sprint 1 Implementation Plan: Robustness Quick Wins

> Date: 2026-04-11
> Goal: Close 5 current bleeding edges before next live session
> Estimated total effort: 1–2 days of focused work
> Branch: `feat/robustness-sprint-1`

---

## Overview

Five low-effort changes that prevent the most common failure modes we identified in `ROBUSTNESS.md`. None require schema changes or new dependencies. All are independently shippable — each item can be committed and merged separately if needed.

**Ordering rationale:** start with items that have zero risk of introducing regressions (items 1, 3) before touching reconnect logic (5) which is more subtle. Items 2 and 4 are at the boundary and benefit from being tested together.

---

## Item 1: Panic Recovery in Strategy `OnBar`

### Problem
A single `nil` pointer dereference or unexpected panic in any strategy instance crashes the entire `omo-core` process. There is no `recover()` in the strategy invocation path.

### Target
- **File:** `backend/internal/app/strategy/runner.go`
- **Call sites to wrap:** 4 total
  - Line 1014: primary 1m instance `OnBar`
  - Line 1123: HTF instance `OnBar`
  - Line 1239: secondary `OnBar` path (replay?)
  - Line 1308: `WarmupOnBar`

### Implementation

Add a helper in `runner.go`:

```go
// safeOnBar invokes inst.OnBar with panic recovery. Returns (signals, err).
// A panic is captured, logged with the strategy id and stack, and converted to an error.
// This isolates one faulty strategy from crashing the entire runner.
func (r *Runner) safeOnBar(
    instCtx *instanceContext,
    inst strategy.Instance,
    symbol string,
    bar domain.MarketBar,
    indicators *domain.Indicators,
) (signals []domain.Signal, err error) {
    defer func() {
        if rec := recover(); rec != nil {
            stack := debug.Stack()
            r.logger.Error("instance OnBar panicked",
                "instance_id", inst.ID().String(),
                "symbol", symbol,
                "panic", rec,
                "stack", string(stack),
            )
            r.metrics.StrategyPanics.WithLabelValues(inst.ID().String(), symbol).Inc()
            r.notify.Errorf("strategy %s panicked on %s: %v", inst.ID().String(), symbol, rec)
            signals = nil
            err = fmt.Errorf("strategy %s panicked: %v", inst.ID().String(), rec)
        }
    }()
    return inst.OnBar(instCtx, symbol, bar, indicators)
}
```

Replace the 4 call sites:

```go
// Before:
signals, err := inst.OnBar(instCtx, symbol, sBar, indicators)

// After:
signals, err := r.safeOnBar(instCtx, inst, symbol, sBar, indicators)
```

For `WarmupOnBar` (line 1308), add an analogous `safeWarmupOnBar` helper.

### New dependencies
- `runtime/debug` (stdlib)
- Add `StrategyPanics` Prometheus counter to existing metrics struct

### Testing
- **Unit test** (`runner_test.go`): inject a strategy that panics on `OnBar`, assert runner continues and emits no signals from that instance but processes subsequent bars normally
- **Integration**: none needed

### Rollback
Revert the helper + 4 call-site edits. No schema or config changes.

### Acceptance
- [ ] Panics logged with full stack trace
- [ ] Panicking strategy does not crash runner
- [ ] Other strategies continue processing
- [ ] Discord alert fires
- [ ] Unit test green

**Estimated effort:** 1 hour

---

## Item 2: Shutdown Drain for In-Flight Orders

### Problem
`waitForShutdown()` in `backend/cmd/omo-core/main.go:38` closes the IBKR broker immediately after stopping the orchestrator. If there are in-flight exit orders (submitted but not yet confirmed filled), their fills are lost. On next startup, the position appears open and may be double-exited.

### Target
- **File:** `backend/cmd/omo-core/main.go` — `waitForShutdown` function
- **File:** `backend/internal/adapters/ibkr/order_stream.go` — add `DrainPending(ctx)` method

### Implementation

**Step 1:** Add drain method to the order stream adapter. The drain waits for all orders currently in the "working" state to transition to a terminal state (filled / cancelled / rejected), with a hard deadline.

```go
// In backend/internal/adapters/ibkr/order_stream.go

// DrainPending blocks until all currently working orders reach a terminal state
// or ctx is cancelled. Used during graceful shutdown to avoid losing fill events.
func (s *OrderStream) DrainPending(ctx context.Context) error {
    s.mu.RLock()
    pending := len(s.workingOrders)
    s.mu.RUnlock()
    if pending == 0 {
        return nil
    }

    ticker := time.NewTicker(200 * time.Millisecond)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            s.mu.RLock()
            remaining := len(s.workingOrders)
            s.mu.RUnlock()
            return fmt.Errorf("drain timeout: %d orders still working", remaining)
        case <-ticker.C:
            s.mu.RLock()
            remaining := len(s.workingOrders)
            s.mu.RUnlock()
            if remaining == 0 {
                return nil
            }
        }
    }
}
```

Note: verify the exact field name for "working orders" map in `order_stream.go` — it may already exist under a different name.

**Step 2:** Call drain in shutdown sequence *between* stopping the orchestrator and closing the broker:

```go
// backend/cmd/omo-core/main.go, inside waitForShutdown()

// 2. Stop orchestrator (no new trades).
if svc.orchestrator != nil {
    svc.orchestrator.Stop()
}

// 2b. NEW: Drain in-flight orders (wait for fills/cancels to land).
drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
if err := infra.ibkrBroker.DrainPending(drainCtx); err != nil {
    log.Warn().Err(err).Msg("order drain incomplete, proceeding with shutdown")
}
drainCancel()

// 3. Close broker and data connections.
if err := infra.ibkrBroker.Close(); err != nil {
    ...
}
```

The `DrainPending` method on the broker delegates to the underlying `OrderStream.DrainPending`.

### New dependencies
None.

### Testing
- **Unit test** for `OrderStream.DrainPending`:
  - Returns nil when no orders pending
  - Blocks while orders are pending, unblocks when all terminate
  - Returns error on context deadline
- **Manual test**: place a limit order at a price far from market, send SIGTERM, verify shutdown waits ~30s before exiting (or exits cleanly if the order is cancelled during drain)

### Rollback
Remove the drain call from main.go. The new method on OrderStream is additive and harmless if unused.

### Acceptance
- [ ] Drain method exists and is covered by unit tests
- [ ] Shutdown sequence waits for pending orders
- [ ] 30s deadline prevents indefinite hang
- [ ] Manual SIGTERM test confirms fills arrive before process exits

**Estimated effort:** 2–3 hours

---

## Item 3: Shutdown Flag in Reconciliation Loops

### Problem
Position reconciliation runs on a ticker (30s per-position, 60s global). During shutdown, a reconciliation tick could fire between orchestrator stop and broker close, potentially inserting reconciliation trades or emitting stale events.

### Target
- **File:** `backend/internal/app/positionmonitor/service.go` — add `isShuttingDown` atomic flag
- **File:** `backend/internal/app/positionmonitor/reconcile.go` — check flag at start of each reconcile tick

### Implementation

```go
// In service.go, add to Service struct:
type Service struct {
    // ... existing fields
    isShuttingDown atomic.Bool
}

// Add method:
func (s *Service) SignalShutdown() {
    s.isShuttingDown.Store(true)
}
```

In `reconcile.go`, at the top of each reconciliation method:

```go
func (s *Service) reconcilePositions(ctx context.Context) {
    if s.isShuttingDown.Load() {
        return
    }
    // ... existing logic
}
```

Wire `SignalShutdown` into the main shutdown sequence in `main.go`:

```go
// backend/cmd/omo-core/main.go, right after orchestrator.Stop():
if svc.positionMonitor != nil {
    svc.positionMonitor.SignalShutdown()
}
```

### New dependencies
- `sync/atomic` (already imported in most files)

### Testing
- **Unit test** in `reconcile_test.go`: set `isShuttingDown`, call reconcile, assert no broker calls, no DB writes, no events emitted
- **No integration test needed**

### Rollback
Trivial — remove flag + call sites.

### Acceptance
- [ ] Flag added to Service
- [ ] All reconcile entry points early-return when set
- [ ] Unit test covers the guarded behavior
- [ ] Main.go wires SignalShutdown into shutdown sequence

**Estimated effort:** 30 minutes

---

## Item 4: systemd Watchdog Unit + sd_notify Heartbeat

### Problem
If omo-core crashes hard (OOM, unrecoverable panic that bypasses Item 1, etc.), it stays down until a human intervenes. Positions remain exposed without monitoring.

### Target
- **New file:** `deployments/systemd/omo-core.service`
- **File:** `backend/cmd/omo-core/main.go` — add sd_notify heartbeat goroutine

### Implementation

**Step 1:** systemd unit file (runs on staging/prod hosts; Docker deployments can adapt later):

```ini
# deployments/systemd/omo-core.service
[Unit]
Description=oh-my-opentrade core trading service
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=notify
ExecStart=/usr/local/bin/omo-core
Restart=on-failure
RestartSec=10
StartLimitBurst=5
StartLimitIntervalSec=600

# Watchdog: systemd will SIGKILL the process if no sd_notify arrives for 120s
WatchdogSec=120
NotifyAccess=main

# Resource limits
LimitNOFILE=65536
MemoryMax=2G

# Security
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

**Step 2:** Add sd_notify heartbeat in Go. Use `github.com/coreos/go-systemd/v22/daemon` (single-file dependency, already common in Go ecosystem).

```go
// Add to backend/cmd/omo-core/main.go, call from run() before waitForShutdown:

func startWatchdogNotify(ctx context.Context, log zerolog.Logger) {
    // Returns 0 if not running under systemd with WatchdogSec set.
    interval, err := daemon.SdWatchdogEnabled(false)
    if err != nil || interval == 0 {
        log.Info().Msg("systemd watchdog not active; skipping heartbeat")
        return
    }
    // Notify at half the watchdog interval (systemd recommendation).
    heartbeat := interval / 2
    log.Info().Dur("heartbeat_interval", heartbeat).Msg("systemd watchdog enabled")

    // Notify ready on startup.
    daemon.SdNotify(false, daemon.SdNotifyReady)

    go func() {
        ticker := time.NewTicker(heartbeat)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                daemon.SdNotify(false, daemon.SdNotifyStopping)
                return
            case <-ticker.C:
                daemon.SdNotify(false, daemon.SdNotifyWatchdog)
            }
        }
    }()
}
```

**Liveness criterion:** today the heartbeat is unconditional. A future improvement (Sprint 2+) is to make it conditional on feed health — only send heartbeat if the feed age is < 90s. For Sprint 1, unconditional is fine — it at least catches hard crashes and hangs.

### New dependencies
- `github.com/coreos/go-systemd/v22/daemon` — add to `go.mod`

### Testing
- **Local smoke test:** run under `systemd-run --user --unit=omo-test --property=WatchdogSec=10s ./omo-core`, observe heartbeats in journalctl
- **Unit test:** mock the daemon package or skip — systemd integration is inherently host-level

### Rollback
- Remove unit file (no deploy effect)
- Remove `startWatchdogNotify` call and import
- Heartbeat gracefully no-ops if `WatchdogSec` is not set, so the Go change is safe even on non-systemd hosts

### Acceptance
- [ ] systemd unit file committed
- [ ] sd_notify heartbeat implemented
- [ ] Manual test: kill -9 the process, confirm systemd restarts it
- [ ] Manual test: freeze the process (SIGSTOP), confirm systemd kills and restarts after WatchdogSec
- [ ] No-op safely when not running under systemd

**Estimated effort:** 2 hours

---

## Item 5: Reconnect Max Attempts + Escalation Alerts

### Problem
`connection.keepAlive()` in `backend/internal/adapters/ibkr/connection.go:124` retries indefinitely with exponential backoff. If IBKR is down for hours, omo-core silently retries forever with no alert. The system appears "running" but isn't trading. Positions are unmanaged.

### Target
- **File:** `backend/internal/adapters/ibkr/connection.go` — extend `keepAlive` with attempt counter + escalation

### Implementation

Extend the connection struct with a reconnect policy:

```go
const (
    reconnectInitialDelay  = 5 * time.Second
    reconnectMaxDelay      = 60 * time.Second
    reconnectEscalateAfter = 6   // ~3 minutes of failures -> Discord alert
    reconnectFatalAfter    = 60  // ~1 hour of failures   -> kill switch active
)

type connection struct {
    // ... existing fields
    notify         notify.Service  // for Discord alerts
    killSwitch     execution.KillSwitchSetter
    reconnectAttempts atomic.Int64
    lastEscalation    atomic.Int64  // unix nanos, for rate-limiting alerts
}
```

Modify `keepAlive`:

```go
func (c *connection) keepAlive() {
    delay := reconnectInitialDelay
    ticker := time.NewTicker(delay)
    defer ticker.Stop()

    for {
        select {
        case <-c.ctx.Done():
            return
        case <-ticker.C:
            if c.isConnected() {
                continue
            }

            attempts := c.reconnectAttempts.Add(1)
            c.log.Warn().
                Dur("retry_in", delay).
                Int64("attempt", attempts).
                Msg("ibkr: connection lost, reconnecting")

            if err := c.connect(); err != nil {
                c.log.Error().Err(err).Int64("attempt", attempts).Msg("ibkr: reconnect failed")

                // Escalation: Discord alert after N failed attempts (rate-limited to once per 15min)
                if attempts == reconnectEscalateAfter {
                    c.notify.Errorf("⚠️ IBKR disconnected for %d attempts (~%s), last error: %v",
                        attempts, delay*time.Duration(attempts), err)
                }

                // Fatal: activate kill switch (REDUCING mode in the future, HALT for now)
                if attempts == reconnectFatalAfter {
                    c.notify.Errorf("🛑 IBKR down for %d attempts — activating kill switch", attempts)
                    if c.killSwitch != nil {
                        c.killSwitch.Activate("ibkr reconnect exhausted")
                    }
                }

                // Backoff
                delay *= 2
                if delay > reconnectMaxDelay {
                    delay = reconnectMaxDelay
                }
            } else {
                // Success: reset counter and delay
                total := c.reconnectAttempts.Swap(0)
                if total > 0 {
                    c.notify.Infof("✅ IBKR reconnected after %d attempts", total)
                }
                delay = reconnectInitialDelay
                c.fireReconnectCallbacks()
            }
            ticker.Reset(delay)
        }
    }
}
```

### New dependencies
- Existing `notify.Service` dependency (already injected elsewhere)
- `execution.KillSwitchSetter` interface — verify the exact type in `app/execution/service.go`; may need a small interface extraction

### Testing
- **Unit test** with a mock connector:
  - Assert attempt counter increments on each failure
  - Assert notify called at `reconnectEscalateAfter` (once, not repeatedly)
  - Assert killSwitch activated at `reconnectFatalAfter`
  - Assert counter resets after successful reconnect
  - Assert no escalation on clean reconnect
- **Backoff math sanity check**: attempts 1–6 take approximately `5 + 10 + 20 + 40 + 60 + 60 = 195s` ≈ 3 minutes to reach escalation

### Rollback
Revert the keepAlive changes. The constants and struct fields are additive.

### Acceptance
- [ ] Max attempts + escalation constants defined
- [ ] Discord alert fires once at escalation threshold
- [ ] Kill switch activates at fatal threshold
- [ ] Successful reconnect emits recovery notification
- [ ] Counter resets on success
- [ ] Unit tests green

**Estimated effort:** 2–3 hours

---

## Execution Order & Dependencies

```
Day 1:
  09:00 - Item 1 (panic recovery)         [1h]   -> commit, PR, merge
  10:00 - Item 3 (shutdown flag)          [30m]  -> commit, PR, merge
  11:00 - Item 2 (shutdown drain)         [3h]   -> commit, PR
  14:00 - Lunch
  15:00 - Item 5 (reconnect escalation)   [3h]   -> commit, PR

Day 2:
  09:00 - Item 4 (systemd watchdog)       [2h]   -> commit, PR, merge
  11:00 - Review PRs from Day 1, address feedback
  14:00 - Deploy to staging, observe for 24h
```

**Dependencies:**
- Items 1, 3, 4 are independent
- Item 2 needs the existing `OrderStream` state — no hard dependency, just context
- Item 5 depends on confirming `KillSwitchSetter` interface location

**Parallelizable:** yes, if multiple developers. As a solo developer, serial is cleaner for PR review.

---

## Testing Strategy

### Unit tests (per-item)
Each item has a dedicated unit test added in this sprint. All pass before merge.

### Integration test
One end-to-end shutdown test after all items land:
1. Start omo-core against paper IBKR
2. Open a position
3. Submit an exit order
4. Send SIGTERM
5. Verify:
   - Drain log message appears
   - Order reaches terminal state
   - Position journal is consistent
   - Reconciliation early-return log appears

### Chaos test (staging)
Once deployed to staging:
1. Kill -9 the process, verify systemd restart within 10s
2. Block IBKR port at firewall for 10 minutes, verify escalation alert at ~3min, verify recovery notification when unblocked
3. Inject a panic in a test strategy (build-flagged), verify isolation

---

## What This Sprint Does NOT Fix

These remain open after Sprint 1 and are scheduled for later sprints:

| Gap | Sprint |
|-----|--------|
| Persistent order journal (prevents cancelling legit stops) | Sprint 2 |
| Startup order reconciliation from broker | Sprint 2 |
| Portfolio heat / sector / directional limits | Sprint 3 |
| 3-state kill switch (currently binary) | Sprint 3 |
| BAG combo orders for spreads | Sprint 4 |
| Block-size filter on darkpool_bars | Sprint 5 |
| Pluggable fill model | Sprint 6 |

---

## Risks & Unknowns

1. **OrderStream internal structure** — I'm assuming `workingOrders` exists as a map. If it doesn't, Item 2 needs a small refactor to track in-flight orders explicitly. Verify before starting.
2. **KillSwitch interface surface** — Item 5 calls `killSwitch.Activate()`. Need to confirm the method exists and is accessible from the IBKR adapter package without causing import cycles. May need to extract a small interface in `ports/`.
3. **systemd on Docker** — If staging/prod uses Docker rather than bare systemd, Item 4 needs an adaptation (Docker health checks with `HEALTHCHECK` directive). Check deployment mode before Item 4.
4. **Discord notification spam** — Items 1 and 5 both send Discord alerts. Ensure there's no alert loop (e.g., a panic during notify that triggers another notify). Add rate limiting if needed.

---

## Success Criteria

Sprint 1 is done when:
- [ ] All 5 items merged to main
- [ ] Integration test green
- [ ] Deployed to staging for 24h with no new alerts attributable to the changes
- [ ] `ROBUSTNESS.md` updated to mark items 1, 2, 3, 5, 6 (by the numbering in that doc) as complete
- [ ] Operator runbook note: "systemd will auto-restart on crash; Discord escalation at ~3min IBKR outage"
