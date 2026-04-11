# P1-2 BacktestPipeline Direct-Dispatch Design

## Goal

Replace the `eventBus.Publish → handler fan-out` pattern in the omo-replay
per-bar loop with direct function calls, for serial-chain events that have a
single real consumer. Live trading (`omo-core`) is unchanged.

## Constraints

- **No behavioural change**: same bar input → same signals/trades/equity output
  as the current bus-based path. P1-3 A/B verifies this.
- **Live path untouched**: `omo-core` continues to wire handlers via
  `eventBus.Subscribe`. This work adds a parallel direct-call path used only
  by omo-replay.
- **Minimal surgery to existing services**: prefer adding new methods over
  rewriting `HandleMarketBar`. The new methods delegate to the existing core
  logic; the only difference is "return the would-publish event" instead of
  actually publishing it.
- **Bus still used for**: signal/order/fill events (multi-consumer, rare),
  `WaitPending`, exit rule emissions from `posMonSvc.EvalExitRules`.

## Shape

```go
// New file: backend/internal/app/backtest/pipeline.go
package backtest

// Pipeline executes the per-bar hot path without going through the event bus.
// Construct via NewPipeline(infra) in omo-replay after services are wired.
type Pipeline struct {
    ingestion  *ingestion.Service
    monitor    *monitor.Service
    runner     *strategy.Runner
    priceCache *positionmonitor.PriceCache
    collector  *backtest.Collector // observer (optional)
    logger     *slog.Logger
}

// ProcessBar runs one MarketBarReceived event through the per-bar hot chain.
// Events that used to be published to the bus are delivered by direct call.
// Returns ctx.Err() immediately if the context is done.
func (p *Pipeline) ProcessBar(ctx context.Context, evt domain.Event) error {
    if err := ctx.Err(); err != nil { return err }

    // collector.onBar is an observer; keep it cheap and before ingestion so
    // the totals reflect every MarketBarReceived.
    if p.collector != nil {
        _ = p.collector.OnBarDirect(evt)
    }

    // stage 1: ingestion (spike filter, z-score gate, repair)
    sanitizedEvt, ok, err := p.ingestion.ProcessBar(ctx, evt)
    if err != nil { return err }
    if !ok { return nil } // dropped by filter

    // stage 2: monitor (indicator calc + HTF aggregator push)
    stateEvt, err := p.monitor.ProcessSanitizedBar(ctx, sanitizedEvt)
    if err != nil { return err }

    // stage 3: strategy runner handles the 1m bar (runs strategies, HTF
    //          aggregation, signal emission). HTF MarketBarSanitized events
    //          that monitor USED to publish are NOT needed — strategy.Runner
    //          does its own 1m→HTF aggregation.
    if err := p.runner.HandleBarDirect(ctx, sanitizedEvt); err != nil {
        return err
    }

    // stage 4: state updated fan-out
    if stateEvt != nil {
        if err := p.runner.HandleStateUpdatedDirect(ctx, *stateEvt); err != nil {
            return err
        }
        // Revaluator was async in the bus path; in backtest, call directly.
        if p.revaluator != nil {
            _ = p.revaluator.HandleStateUpdatedDirect(ctx, *stateEvt)
        }
    }

    // stage 5: position monitor price cache
    if p.priceCache != nil {
        _ = p.priceCache.HandleBarDirect(ctx, sanitizedEvt)
    }

    // Dropped: publishEnrichedBar has no subscribers in backtest.
    return nil
}
```

## New public methods on existing services

Each method does exactly what the existing `Handle*` does MINUS the `Publish`
calls. Where a Publish used to happen, the method returns the event (or a
slice of events) so the Pipeline can decide whether to dispatch directly.

### ingestion.Service.ProcessBar
```go
// ProcessBar runs the spike filter / adaptive filter over the market bar
// event and returns the sanitized event (if the bar was accepted) without
// touching the event bus. Mirrors HandleMarketBar minus the Publish call.
func (s *Service) ProcessBar(ctx context.Context, evt domain.Event) (sanitized domain.Event, ok bool, err error) { ... }
```

### monitor.Service.ProcessSanitizedBar
```go
// ProcessSanitizedBar feeds a 1m bar through the indicator calculator,
// aggregators, regime detector, and ORB tracker, and returns the
// StateUpdated event. HTF MarketBarSanitized events are NOT emitted —
// strategy.Runner does its own HTF aggregation from the 1m stream. The
// ORBRangeSet and SetupDetected events are still published to the bus
// internally because they have multi-consumer fan-out.
func (s *Service) ProcessSanitizedBar(ctx context.Context, evt domain.Event) (stateUpdated *domain.Event, err error) { ... }
```

### strategy.Runner.HandleBarDirect / HandleStateUpdatedDirect
These are thin wrappers around the existing private `handleBar` /
`handleStateUpdated` that expose them as public methods. No behavioural
change; they currently are only called via bus Subscribe. Zero-cost refactor.

### positionmonitor.PriceCache.HandleBarDirect
Same — expose the internal `handleBar` as public.

### backtest.Collector.OnBarDirect
Same — expose `onBar` as public.

## Method-renaming strategy

I want to avoid the existing `handleBar` methods being callable both by the
bus (via `Subscribe`) and directly. Two options:

**Option A: Keep the bus-facing handler, add a direct shim.**
```go
func (r *Runner) handleBar(ctx context.Context, evt domain.Event) error { /* existing */ }
func (r *Runner) HandleBarDirect(ctx context.Context, evt domain.Event) error {
    return r.handleBar(ctx, evt)
}
```
Pros: zero risk of skipping a subscriber. The bus still works in live.
Cons: two methods that do the same thing.

**Option B: Promote the private method to public and have omo-replay not call Subscribe for the hot path at all.**
Cleaner but means live and backtest wire things differently, which makes
future live-path changes riskier (easy to break backtest wiring).

**Decision: Option A.** Minimal surgery, no risk to live.

## Pipeline construction in omo-replay

In `omo-replay/main.go` after `eventBus.FreezeHandlers()` and before the
replay loop:

```go
pipeline := backtest.NewPipeline(backtest.PipelineInfra{
    Ingestion:  ingSvc,
    Monitor:    monitorSvc,
    Runner:     pipelineRunner,
    PriceCache: posMonPriceCache,
    Revaluator: revaluator,
    Collector:  collector,
    Logger:     log.With("component", "backtest_pipeline"),
})
```

Then in the bar loop, **replace**:

```go
evt := domain.NewBacktestEvent(domain.EventMarketBarReceived, ...)
if err := eventBus.Publish(ctx, evt); err != nil { ... }
```

**with**:

```go
evt := domain.NewBacktestEvent(domain.EventMarketBarReceived, ...)
if err := pipeline.ProcessBar(ctx, evt); err != nil { ... }
```

## Subscribe call side-effects

The services currently auto-subscribe to the bus in their Start methods. In
the direct path, those subscriptions are still made (they're harmless — the
bus just never publishes the events they subscribe to because the direct
pipeline handles it). But to be safe we should skip the subscribes for events
the pipeline handles directly. Two options:

- Add a `BackTestDirect bool` flag on the bootstrap config; services skip the
  hot-path subscribes when set.
- Leave subscribes in; use `eventBus.Unsubscribe` after `FreezeHandlers()` for
  the dead events.

**Decision: skip subscribes via bootstrap flag.** Cleaner and avoids rebuilding
the frozen snapshot. The flag flows through the existing `bootstrap` package
to each service constructor.

## What stays on the bus

Not touched by this work — still pub/sub:

- `SignalCreated` → risk sizer, signal tracker, debate enricher, emitter
- `SignalEnriched` → risk sizer, position monitor revaluator
- `OrderIntentCreated` → execution service
- `OrderIntentValidated` / `OrderIntentRejected` → signal tracker, strategy
  runner, position monitor
- `FillReceived` → position monitor, collector, strategy runner, risk sizer,
  signal tracker, ledger writer
- `OrderSubmitted` / `ExitOrderTerminal` → position monitor
- `RegimeShifted` → debate (rare)
- `SetupDetected` → debate, strategy runner (rare, but multi-consumer)
- `ORBRangeSet` / `ORBPhaseUpdate` → SSE notifier (even though not wired in
  replay — if we ever wire it, we want it to work)

Exit-rule evaluations in `posMonSvc.EvalExitRules(minTime)` already run
post-tick and publish exit intents through the bus. Unchanged.

## Risk + test plan for P1-3

- **Behaviour parity**: run the existing 8/16/30 symbol 1-year benches on
  baseline + current HEAD + direct-dispatch branch. Compare signal counts,
  order intent counts, trade counts, equity curve. All must match bit-for-bit.
- **Unit tests**: the new `Process*` methods delegate to the existing
  `handle*` methods, so the existing tests still exercise the logic. Add one
  smoke test that constructs a `Pipeline` and pushes a few bars through,
  asserting no panic and non-nil snap outputs.
- **Benchmark**: `go test -bench=BenchmarkBacktestPipeline_ProcessBar` on a
  synthetic bar stream for before/after numbers independent of DB load.

## Files touched (estimated)

- `backend/internal/app/backtest/pipeline.go` — new file (~200 lines)
- `backend/internal/app/ingestion/service.go` — add `ProcessBar`
- `backend/internal/app/monitor/service.go` — add `ProcessSanitizedBar`
- `backend/internal/app/strategy/runner.go` — add `HandleBarDirect` + `HandleStateUpdatedDirect`
- `backend/internal/app/positionmonitor/price_cache.go` — add `HandleBarDirect`
- `backend/internal/app/positionmonitor/revaluator.go` — add `HandleStateUpdatedDirect`
- `backend/internal/app/backtest/collector.go` — add `OnBarDirect`
- `backend/cmd/omo-replay/main.go` — construct pipeline, swap the publish
- `backend/internal/app/bootstrap/*.go` — add `DirectDispatch bool` flag
  (maybe)

Rough line count: ~400 lines net added, mostly thin wrappers.
