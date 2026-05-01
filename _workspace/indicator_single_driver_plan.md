# Indicator Single-Driver Refactor (Option D)

Status: DRAFT — pending acknowledgment
Drafted: 2026-05-01
Branch target: main
Owners: backend (`go-architect`), parity (`qa-inspector`)

## Goal

Make `indicator.Service` the sole driver of its own `Update` lifecycle. Both
`monitor.Service` and `strategy.Runner` become pure consumers via the existing
`Subscribe(...)` interface (extended to carry an event envelope) plus
`LastSnapshot(...)`.

## Why

PR 6a-2 (84f5a4bf) introduced an implicit invariant — "exactly one Update
driver per indicator instance" — but left two drivers wired in
(`monitor/service.go:928`, `strategy/runner.go:1677`). When monitor's handler
runs first on `EventMarketBarSanitized`, its `Update` fires the runner's HTF
Subscribe callback into `r.htfPending`. The runner's handler then runs,
clears `r.htfPending` at `runner.go:1675`, and its own `Update` is dedup'd at
the BarAggregator/calc layer so callbacks don't re-fire. Net effect: 0 HTF
strategy evaluations, 0 trades.

Confirmed by go-architect and qa-inspector independently.

The single-driver shape:
- restores SRP: one Update producer, two pure consumers
- removes implicit "first subscriber wins" coupling
- removes the `s.htfCallCtx` side-channel and `pendingHTFEvents` queue from
  monitor
- closes the door on the same regression recurring whenever someone adds a
  third Update caller

## Non-goals

- Backwards compat with the old Subscribe signature.
- Behavioural changes to indicator math, HTF aggregation, or RTH gating.
- Touching warmup code paths (`WarmUp`, `WarmUpCollect`, `PrimeAggregator`)
  beyond what the new Subscribe signature mechanically requires.
- AI strategist, copytrade, or any other event subscriber outside the
  monitor/runner indicator chain.

## Target architecture

```
EventMarketBarSanitized published by ingestion
    ↓
indicator.Service handler (subscribes FIRST, registered in Start)
    s.calc.Update(bar) → s.last[(sym, tf)] = snap
    if 1m: aggregators push → on close, fire Subscribe callbacks(closed, snap, env)
    ↓
monitor.Service handler (subscribes SECOND)
    snap := s.shadowIndicator.LastSnapshot(sym, tf)   // already populated
    runs AVWAP standalone, regime, ORB, etc.
    NO Update call
    ↓
strategy.Runner handler (subscribes THIRD)
    drains r.htfPending (populated by callbacks during indicator's Update)
    NO reset, NO Update call
```

For backtest direct dispatch (`pipeline.go:189-217`):
```
ingestion.ProcessBarTyped
  → indicator.HandleSanitizedDirect          (NEW step)
  → monitor.HandleMarketBarTyped
  → runner.HandleBarDirectTyped
```

Subscribe callbacks fire inside indicator's Update, before monitor and
runner enter their handlers. `r.htfPending` writes from the callback now
happen on a different goroutine context than the runner's handler reads —
synchronous in practice (in-memory bus + direct dispatch are both serial in
the publisher's goroutine), but we add a `sync.Mutex` guard to make the
contract explicit and outlive any future async dispatch addition.

## File-by-file changes

### 1. `backend/internal/app/indicator/service.go`

Add envelope type (or reuse a domain one; new type keeps the package
self-contained):
```go
type Envelope struct {
    TenantID   string
    EnvMode    domain.EnvMode
    IdemKey    string
    OccurredAt time.Time
}
```

Change `Subscribe` callback signature:
```go
func (s *Service) Subscribe(
    sym domain.Symbol,
    tf domain.Timeframe,
    fn func(closed domain.MarketBar, snap domain.IndicatorSnapshot, env Envelope),
) func() { ... }
```

Add `Start(ctx, bus)`:
```go
func (s *Service) Start(ctx context.Context, bus ports.EventBusPort) error {
    return bus.Subscribe(ctx, domain.EventMarketBarSanitized, s.handleSanitized)
}

func (s *Service) handleSanitized(ctx context.Context, ev domain.Event) error {
    bar, ok := ev.Payload.(domain.MarketBar)
    if !ok {
        return fmt.Errorf("indicator: payload is not a MarketBar, got %T", ev.Payload)
    }
    env := Envelope{
        TenantID:   ev.TenantID,
        EnvMode:    ev.EnvMode,
        IdemKey:    ev.IdempotencyKey,
        OccurredAt: ev.OccurredAt,
    }
    s.UpdateWithEnv(bar, env)
    return nil
}
```

Add `HandleSanitizedDirect(ctx, ev)` — same body as `handleSanitized`, public
for backtest direct dispatch.

Add `UpdateWithEnv(bar, env)` — internal Update that propagates envelope to
callbacks. The existing `Update(bar)` becomes a thin wrapper passing
`Envelope{}`. Keeps WarmUp paths and the runner's native-HTF passthrough
working without forcing them to construct an envelope.

Internal pending-firings struct grows `env` so callbacks receive it after
the lock is released:
```go
type firing struct {
    closed domain.MarketBar
    snap   domain.IndicatorSnapshot
    env    Envelope
    subs   []subscription
}
```

### 2. `backend/internal/app/monitor/service.go`

`HandleMarketBarTyped` (and `HandleMarketBar`):
- Delete `s.htfCallCtx = htfCallEnvelope{...}` block at :920-:925.
- Delete `s.pendingHTFEvents = s.pendingHTFEvents[:0]` at :926.
- Delete `snap := s.shadowIndicator.Update(bar)` at :928.
- Replace `snap` with `snap, _ := s.shadowIndicator.LastSnapshot(bar.Symbol, bar.Timeframe)`.
- Delete the `if htfDispatch && len(s.pendingHTFEvents) > 0 { ... }` drain
  block at :929-:931.

HTF Subscribe callback (`s.shadowIndicator.Subscribe(sym, tf, cb)` at :653,
callback body at :683-:714):
- Adopt new `(closed, snap, env Envelope)` signature.
- Build the HTF `MarketBarSanitized` event using `env` directly instead of
  reading `s.htfCallCtx`.
- Build the `RegimeShifted` event using `env` directly.
- Publish both events to the bus inline (callback executes synchronously in
  indicator's goroutine; the publish is itself synchronous fan-out and does
  not re-enter monitor or runner Update paths). This removes the need for
  `pendingHTFEvents` entirely.
- Verify reentrancy is safe: HTF MarketBarSanitized fan-out reaches monitor's
  enriched-bar publisher, DB writers, SSE emitters — none feed back into
  Update. Document this in a comment.

Field cleanup:
- Delete `htfCallEnvelope` struct (:56).
- Delete `pendingHTFEvents` field (:81-:85).
- Delete `htfCallCtx` field (:86-:89).

### 3. `backend/internal/app/strategy/runner.go`

`handleBarCore`:
- Delete `r.htfPending = r.htfPending[:0]` at :1675.
- Delete the `r.indicator.Update(bar)` block at :1676-:1678.
- Native-HTF passthrough at :1717-:1723: change
  `nativeSnap := r.indicator.Update(bar)` to
  `nativeSnap, ok := r.indicator.LastSnapshot(bar.Symbol, bar.Timeframe)`
  with an `if !ok` guard.

HTF callback `makeHTFCallback` at :897-:901:
- Adopt new `(closed, snap, env)` signature.
- The runner doesn't need `env` (it doesn't publish HTF events from the
  callback — only buffers for handleBarCore drain). Accept and ignore.
- Add lock around the `r.htfPending` append: callback can fire on a
  different goroutine in the future. Use `r.htfPendingMu` (new field),
  separate from `r.mu` to avoid lock ordering issues with Update-time
  callbacks.
- Drain at :1725 takes `r.htfPendingMu`, copies, releases, processes.
  The pending slice is then reset under the lock.

Update the comment block at :893-:901 that documents the now-stale
"r.mu held during Update" assumption.

### 4. `backend/cmd/omo-core/services.go`

After `indicator.NewService("monitor_shadow")` at :139:
- Wire `svc.indicator.Start(ctx, infra.eventBus)` BEFORE `svc.monitor.Start(ctx)` (currently
  at :811). New call inserted at the same lifecycle phase.
- Order: indicator.Start → monitor.Start → strategyRunner.Start.

### 5. `backend/internal/app/backtest/runner.go`

Two construction sites:
- Single-pipeline path at :304 (`idx := indicator.NewService(...)`):
  Wire `idx.Start(ctx, r.infra.EventBus)` BEFORE `monitorSvc.Start(ctx)` at
  :1254.
- Per-shard factory at :1509: each `shardIdx` gets `Start(ctx, bus)` called
  before its monitor's start. The sharded pipeline runs its own dispatch
  via `pipeline.go`, so subscription order matters less for the direct path
  but we keep it consistent for any bus-mediated events that still flow.

### 6. `backend/internal/app/backtest/pipeline.go`

`ProcessBarPhaseATyped` at :189-:245:
- Insert `p.indicator.HandleSanitizedTyped(ctx, sanitizedBar, env)` between
  `p.ingestion.ProcessBarTyped` (:193) and `p.monitor.HandleMarketBarTyped`
  (:200). Pass envelope built from the original event metadata that
  `ProcessBarPhaseATyped` already has.

`ProcessBarPhaseA` at :247-:303 (Event-wrapped path, used only by the
single-shard fallback):
- Same insertion between ingestion's `ProcessBar` and monitor's
  `HandleMarketBar`.

Add `Indicator: ...` accessor and storage on `Pipeline` struct (already
present at :32, :103, :127). No struct change needed — just call its new
direct-dispatch entry.

### 7. `backend/cmd/omo-replay/main.go`

Two construction sites:
- :257 (`idx := indicator.NewService("omo_replay")`)
- :558 (`shardIdx := indicator.NewService(...)`)

Wire `Start(ctx, bus)` for each before the corresponding monitor's Start.
omo-replay's lifecycle is short-lived so this is mechanical.

### 8. `backend/internal/app/bootstrap/strategy.go`

`StrategyDeps.Indicator` (line 66) already exists. No change.

The throwaway `indicator.NewService("bootstrap_snapshot_fn")` at :482
doesn't subscribe to a bus and is internal-only — no Start call needed.

## Tests

### Unit / package tests to update for new Subscribe signature

- `backend/internal/app/indicator/subscribe_test.go` — signature change.
- `backend/internal/app/indicator/parallel_isolation_test.go` — likely uses
  Subscribe.
- `backend/internal/app/strategy/runner_indicator_htf_parity_test.go`.
- `backend/internal/app/monitor/mtfa_test.go` — uses
  `EventMarketBarSanitized`, may depend on subscribe order.
- `backend/internal/app/bootstrap/pipeline_integration_test.go`.

### New tests

1. **`indicator_sole_driver_test.go`** (in `internal/app/indicator/`)
   Asserts that a `*Service` started against a fake bus is the only thing
   that drives `Update` on a given bar. Uses a fake monitor/runner that
   call `Update` defensively and expects all but the first call to be
   detected as redundant via a counter on the wrapped calc.

2. **`backtest_trades_smoke_test.go`** (in `internal/app/backtest/`)
   End-to-end backtest over a 5-day window with avwap_v4 + macd_only_v1
   activated, asserting `trades > 0`. Locks the regression: any future PR
   that breaks the HTF dispatch chain fails this test before merge.

3. **`htf_subscribe_envelope_test.go`** (in `internal/app/indicator/`)
   Subscribes to a HTF (sym, tf), publishes an EventMarketBarSanitized via
   a fake bus, asserts the callback receives the envelope (TenantID,
   EnvMode, IdemKey, OccurredAt) matching the published event.

### Tests to remove / adapt

- Any test asserting monitor's `pendingHTFEvents` shape or `htfCallCtx`
  side-channel — replace with assertions on bus-published HTF events.

## Migration / rollout

Single PR (no flag gate):
1. Land all changes together — partial rollout would leave the dual-driver
   bug in place.
2. Pre-merge: run the existing `runner_indicator_htf_parity_test.go` and
   the new `backtest_trades_smoke_test.go` against the branch.
3. Post-merge: rebuild, restart live, watch first 1h for HTF strategy
   signals. avwap_v4 and macd_only_v1 should produce signals at expected
   cadence (compare to pre-PR-6a-2 baseline).

If the smoke test fails post-merge, revert is one commit (the whole PR).
No data migration, no schema change.

## Blast radius

| Component | Risk | Mitigation |
|-----------|------|------------|
| Live trading | High — both AVWAP and MACD strategies sit on this path | Smoke test gating + post-deploy live monitor for first hour |
| Backtest correctness | High — HTF strategy dispatch is the entire feature | Smoke test asserting trades > 0 |
| Warmup paths | Low — `WarmUp`, `WarmUpCollect`, `PrimeAggregator` use `s.calc.Update` directly, not `Service.Update` | No behavioural change; signature change is type-only |
| omo-replay | Medium — uses indicator.Service in the same pattern | Same Start wiring, same direct dispatch |
| Monitor enriched-bar publisher | Low — reads `LastSnapshot`, will see fresh snap because indicator handler runs before monitor | N/A |
| Tests | High mechanical churn — Subscribe signature change ripples | Land in same PR, no compatibility shim |

## Open questions

1. **Reentrancy of direct HTF event publish from monitor's callback.** The
   callback fires inside `indicator.Service.UpdateWithEnv` while the
   service holds `s.mu`. Publishing to the bus calls subscribers
   synchronously. Need to verify the HTF MarketBarSanitized subscribers
   (enriched-bar publisher, DB writers, SSE emitters) do NOT call back into
   `indicator.Service.Update` or its public mutators. Action: grep the
   subscribers, document the contract in a comment on `Subscribe`.

2. **Lock around `r.htfPending`.** Today the access is single-goroutine
   (synchronous bus + direct dispatch). Adding `r.htfPendingMu` is
   defensive against future async dispatch additions. Defer if it adds
   measurable overhead per the existing benchmark (`service_bench_test.go`).

3. **`Envelope` location.** Putting it in the `indicator` package keeps the
   service self-contained. Alternative: move to `domain` for reuse — but
   no current consumer outside indicator/monitor/runner needs it. Keep in
   `indicator` until a second consumer appears.

## Estimated effort

~5-7 hours of implementation + tests.

## Acceptance criteria

- [ ] HTTP backtest over avwap_v4+macd_only_v1, 4-month window, produces
      `trades > 0`.
- [ ] `runner_indicator_htf_parity_test.go` still passes byte-identically.
- [ ] Live restart shows avwap_v4 / macd_only_v1 signals within 1h of
      market open at expected cadence.
- [ ] No `monitor.Service.shadowIndicator.Update` call site survives.
- [ ] No `strategy.Runner.handleBarCore` `r.indicator.Update` call site
      survives for 1m bars.
- [ ] `monitor.Service` no longer carries `pendingHTFEvents` or
      `htfCallCtx` fields.
- [ ] New smoke test `backtest_trades_smoke_test.go` is wired into CI.
