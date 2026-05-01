package backtest

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// Pipeline runs the per-bar hot path for the backtest/replay binary without
// going through the event bus for serial-chain events. The event bus is
// still used for multi-consumer events (signal/order/fill fan-out) and for
// post-tick exit rule evaluation.
//
// One ProcessBar call replaces the chain of 3-4 Publish calls that the live
// path makes per bar:
//
//	MarketBarReceived → ingestion
//	  → MarketBarSanitized → monitor + strategy + priceCache + (dead) enrichedBar
//	    → StateUpdated → strategy.handleStateUpdated
//
// See docs/perf/p1-2-backtest-pipeline-design.md for rationale.
type Pipeline struct {
	ingestion  *ingestion.Service
	monitor    *monitor.Service
	runner     *strategy.Runner
	indicator  *indicator.Service
	priceCache *positionmonitor.PriceCache
	collector  *Collector
	eventBus   ports.EventBusPort

	// deferredStrict / deferredBestEffort hold monitor pending events that
	// Phase A couldn't dispatch directly (regime, setup, HTF events) —
	// they go to multi-consumer bus handlers and must publish in Phase B
	// in dispatch order. Populated once per bar by Phase A; drained by
	// Phase B. Single-writer per-pipeline because each shard owns exactly
	// one Pipeline and a shard's Phase A worker is the only goroutine
	// touching the fields until the barrier completes.
	deferredStrict     []domain.Event
	deferredBestEffort []domain.Event
}

// stashDeferredForPhaseB appends monitor drain events that Phase A
// couldn't handle directly to the pipeline's deferred buffers. Called at
// most once per Phase A invocation; the shard's worker goroutine is the
// sole writer, and Phase B is the sole reader (after the barrier).
func (p *Pipeline) stashDeferredForPhaseB(_ context.Context, strict, bestEffort []domain.Event) {
	if len(strict) > 0 {
		p.deferredStrict = append(p.deferredStrict, strict...)
	}
	if len(bestEffort) > 0 {
		p.deferredBestEffort = append(p.deferredBestEffort, bestEffort...)
	}
}

// TakeDeferred returns and clears the strict + best-effort deferred
// event slices populated by ProcessBarPhaseA. Used by slice-to-
// completion dispatch to relocate monitor pending events into a
// per-shard event stream for later merge + replay. The returned
// slices are fresh copies — safe for the caller to retain — and the
// pipeline's internal buffers are truncated so the next ProcessBar
// starts fresh.
func (p *Pipeline) TakeDeferred() (strict, bestEffort []domain.Event) {
	if len(p.deferredStrict) > 0 {
		strict = make([]domain.Event, len(p.deferredStrict))
		copy(strict, p.deferredStrict)
		p.deferredStrict = p.deferredStrict[:0]
	}
	if len(p.deferredBestEffort) > 0 {
		bestEffort = make([]domain.Event, len(p.deferredBestEffort))
		copy(bestEffort, p.deferredBestEffort)
		p.deferredBestEffort = p.deferredBestEffort[:0]
	}
	return strict, bestEffort
}

// drainDeferredForPhaseB publishes every stashed Phase A event through
// the shared event bus in dispatch order, then truncates the buffers.
func (p *Pipeline) drainDeferredForPhaseB(ctx context.Context) {
	if p.eventBus != nil {
		for i := range p.deferredStrict {
			_ = p.eventBus.Publish(ctx, p.deferredStrict[i])
		}
		for i := range p.deferredBestEffort {
			_ = p.eventBus.Publish(ctx, p.deferredBestEffort[i])
		}
	}
	p.deferredStrict = p.deferredStrict[:0]
	p.deferredBestEffort = p.deferredBestEffort[:0]
}

// PipelineInfra is the injection struct — construct one and pass it to
// NewPipeline. Nil fields are tolerated (optional observers).
type PipelineInfra struct {
	Ingestion  *ingestion.Service
	Monitor    *monitor.Service
	Runner     *strategy.Runner
	Indicator  *indicator.Service
	PriceCache *positionmonitor.PriceCache
	Collector  *Collector
	EventBus   ports.EventBusPort
}

// Ingestion returns the ingestion.Service this pipeline drives. Used by
// sharded setup paths that need to seed the adaptive filter per shard.
func (p *Pipeline) Ingestion() *ingestion.Service { return p.ingestion }

// Monitor returns the monitor.Service this pipeline drives. Used by
// sharded setup paths that need to reach per-shard monitor state
// (WarmUp, MarkReady, InitAggregators, ResetSessionIndicators).
func (p *Pipeline) Monitor() *monitor.Service { return p.monitor }

// Runner returns the strategy.Runner this pipeline drives. Used by
// sharded setup paths that need to reach per-shard runner state
// (WarmUp, InitAggregators, SetAIAnchorResolver, ClearAllPendingStates,
// SetSuppressProgressEvents).
func (p *Pipeline) Runner() *strategy.Runner { return p.runner }

// Indicator returns the per-shard indicator.Service that mirrors
// monitor.calculator. Sharded warmup uses it to drive snapshotFn closures
// against the same calc the live shadow path feeds.
func (p *Pipeline) Indicator() *indicator.Service { return p.indicator }

// NewPipeline wires up the per-bar direct-dispatch chain. The caller must
// already have constructed the services (typically via the bootstrap
// package) and called eventBus.FreezeHandlers afterward. NewPipeline also
// flips monitor.Service into direct-dispatch mode, which makes its
// HandleMarketBar route the StateUpdated/HTF/Regime events to a pending
// slice that ProcessBar drains below.
func NewPipeline(infra PipelineInfra) *Pipeline {
	if infra.Monitor != nil {
		infra.Monitor.SetDirectDispatch(true)
	}
	return &Pipeline{
		ingestion:  infra.Ingestion,
		monitor:    infra.Monitor,
		runner:     infra.Runner,
		indicator:  infra.Indicator,
		priceCache: infra.PriceCache,
		collector:  infra.Collector,
		eventBus:   infra.EventBus,
	}
}

// ProcessBar runs one MarketBarReceived event through the hot chain as a
// series of direct method calls. Returns the first non-nil error encountered.
// If the bar is dropped by the adaptive filter, ProcessBar returns nil with
// no downstream work performed.
//
// Sharded callers split the work into ProcessBarPhaseA (parallel-safe) and
// ProcessBarPhaseB (must run in dispatch order). Single-shard callers can
// still invoke ProcessBar directly — it chains both phases sequentially and
// preserves the pre-sharding semantics exactly.
func (p *Pipeline) ProcessBar(ctx context.Context, evt domain.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sanitized, dropped, err := p.ProcessBarPhaseA(ctx, evt)
	if err != nil || dropped {
		return err
	}
	return p.ProcessBarPhaseB(ctx, evt, sanitized)
}

// ProcessBarPhaseA runs the parallel-safe portion of the bar hot chain:
// collector observation (stage 0), ingestion filter (stage 1), monitor
// indicator update (stage 2), price cache update, monitor pending drain
// for StateUpdated → runner, and strategy runner handleBar (stage 3).
// Signal publication is DEFERRED — runner.emitSignal buffers into
// pendingSignals when deferSignalPublish is true, which Phase B drains
// and publishes in dispatch order.
//
// No event bus publishes run on the hot path (monitor/runner pending
// events stay buffered until Phase B), so N shards can call this method
// concurrently from separate goroutines.
//
// dropped=true indicates the adaptive filter rejected the bar — the caller
// must skip Phase B for that job.
// ProcessBarPhaseATyped is the allocation-free fast path for
// slice-to-completion dispatch: it takes the raw bar + envelope
// metadata directly, avoiding the ~1.87 GB of Event allocations a
// 30 sym / 1 yr run incurs through toEvent(). Uses the typed
// ingestion / monitor / runner entry points added in Phase 4-5.
func (p *Pipeline) ProcessBarPhaseATyped(ctx context.Context, bar domain.MarketBar, tenantID string, envMode domain.EnvMode) (dropped bool, err error) {
	if cerr := ctx.Err(); cerr != nil {
		return false, cerr
	}
	sanitizedBar, ok, err := p.ingestion.ProcessBarTyped(bar)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	// Indicator is the SOLE driver of Update — drives state forward AND
	// fires monitor's + runner's HTF Subscribe callbacks. Must run BEFORE
	// monitor (which now reads via LastSnapshot) and runner (which drains
	// htfPending populated by callbacks during this call).
	if p.indicator != nil {
		if err := p.indicator.HandleSanitizedTyped(ctx, sanitizedBar, tenantID, envMode, bar.Time); err != nil {
			return false, err
		}
	}
	if p.monitor != nil {
		if err := p.monitor.HandleMarketBarTyped(ctx, sanitizedBar, tenantID, envMode, bar.Time); err != nil {
			return false, err
		}
	}
	if p.runner != nil {
		if err := p.runner.HandleBarDirectTyped(ctx, sanitizedBar, tenantID, envMode); err != nil {
			return false, err
		}
	}
	if p.monitor != nil && p.runner != nil {
		snaps := p.monitor.DrainPendingStateUpdates()
		for i := range snaps {
			if err := p.runner.HandleStateUpdatedSnap(ctx, snaps[i]); err != nil {
				return false, err
			}
		}
	}
	var deferredStrict, deferredBestEffort []domain.Event
	if p.monitor != nil {
		strict, bestEffort := p.monitor.DrainPending()
		for i := range strict {
			ev := &strict[i]
			if ev.Type == domain.EventStateUpdated {
				if p.runner != nil {
					if err := p.runner.HandleStateUpdatedDirect(ctx, *ev); err != nil {
						return false, err
					}
				}
				continue
			}
			deferredStrict = append(deferredStrict, *ev)
		}
		for i := range bestEffort {
			ev := &bestEffort[i]
			if ev.Type == domain.EventMarketBarSanitized {
				continue
			}
			deferredBestEffort = append(deferredBestEffort, *ev)
		}
	}
	if len(deferredStrict) > 0 || len(deferredBestEffort) > 0 {
		p.stashDeferredForPhaseB(ctx, deferredStrict, deferredBestEffort)
	}
	return false, nil
}

func (p *Pipeline) ProcessBarPhaseA(ctx context.Context, evt domain.Event) (sanitized domain.Event, dropped bool, err error) {
	if cerr := ctx.Err(); cerr != nil {
		return domain.Event{}, false, cerr
	}

	// collector.OnBarDirect and priceCache.HandleBarDirect are deferred
	// to Phase B. Both services hold a single shared mutex, so calling
	// them from N shard goroutines in Phase A serializes the shards on
	// the mutex and erases any parallelism win. Phase B runs serially
	// anyway, so there's no correctness cost to moving them down.

	// stage 1: ingestion — spike filter + repair.
	var ok bool
	sanitized, ok, err = p.ingestion.ProcessBar(ctx, evt)
	if err != nil {
		return domain.Event{}, false, err
	}
	if !ok {
		return domain.Event{}, true, nil
	}

	// stage 1.5: indicator — sole driver of Update. Fires HTF Subscribe
	// callbacks for monitor (HTF MarketBarSanitized + RegimeShifted via
	// AppendPublish) and runner (htfPending) BEFORE monitor and runner
	// run their own logic. Single-driver contract: if monitor or runner
	// also called Update, the BarAggregator's bar.Time dedup would silently
	// no-op the second caller's callbacks (PR 6a-2 era regression).
	if p.indicator != nil {
		if err := p.indicator.HandleSanitizedDirect(ctx, sanitized); err != nil {
			return domain.Event{}, false, err
		}
	}

	// stage 2: monitor — indicator calc, HTF aggregation, regime, ORB.
	// The monitor is in direct-dispatch mode, so it collects the
	// StateUpdated / HTF / RegimeShifted / SetupDetected events in
	// pending slices instead of calling eventBus.Publish.
	if p.monitor != nil {
		if err := p.monitor.HandleMarketBar(ctx, sanitized); err != nil {
			return domain.Event{}, false, err
		}
	}

	// stage 3: strategy runner handles the 1m bar. With
	// deferSignalPublish set at shard construction time, any
	// SignalCreated emit lands in the runner's pendingSignals slice;
	// Phase B drains them.
	if p.runner != nil {
		if err := p.runner.HandleBarDirect(ctx, sanitized); err != nil {
			return domain.Event{}, false, err
		}
	}

	// stage 4a: typed StateUpdated drain — monitor pushes
	// IndicatorSnapshot values directly onto pendingStateUpdates in
	// direct-dispatch mode (no Event wrapping), and the runner
	// consumes them via HandleStateUpdatedSnap. This is the
	// single-largest GC win of Phase 4: the Event struct allocation
	// + IdempotencyKey concat + interface boxing accounted for
	// ~1.5 GB of allocations per 30 sym / 1 yr run.
	if p.monitor != nil && p.runner != nil {
		snaps := p.monitor.DrainPendingStateUpdates()
		for i := range snaps {
			if err := p.runner.HandleStateUpdatedSnap(ctx, snaps[i]); err != nil {
				return domain.Event{}, false, err
			}
		}
	}

	// stage 4b: drain monitor pending regime/setup/HTF events. These
	// go to multi-consumer bus handlers (debate enricher, etc.) and
	// must publish in dispatch order during Phase B.
	var deferredStrict, deferredBestEffort []domain.Event
	if p.monitor != nil {
		strict, bestEffort := p.monitor.DrainPending()
		for i := range strict {
			ev := &strict[i]
			if ev.Type == domain.EventStateUpdated {
				// Legacy bus-mode leftover — should be empty in
				// direct-dispatch, but guard just in case.
				if p.runner != nil {
					if err := p.runner.HandleStateUpdatedDirect(ctx, *ev); err != nil {
						return domain.Event{}, false, err
					}
				}
				continue
			}
			deferredStrict = append(deferredStrict, *ev)
		}
		for i := range bestEffort {
			ev := &bestEffort[i]
			if ev.Type == domain.EventMarketBarSanitized {
				continue
			}
			deferredBestEffort = append(deferredBestEffort, *ev)
		}
	}
	if len(deferredStrict) > 0 || len(deferredBestEffort) > 0 {
		p.stashDeferredForPhaseB(ctx, deferredStrict, deferredBestEffort)
	}

	return sanitized, false, nil
}

// ProcessBarPhaseB publishes a bar's deferred Phase A effects:
//   - collector mark-to-market observation
//   - price cache update
//   - runner SignalCreated buffer drain
//   - monitor pending regime/setup events
//
// Must be called in dispatch order so downstream bus handlers (risk
// sizer, execution service, sim broker, position monitor) see signals
// in the same order the single-threaded path would produce.
//
// The original evt (MarketBarReceived) and sanitized (MarketBarSanitized)
// events are both re-used here: collector keys off the received bar,
// priceCache keys off the sanitized bar. Passing both avoids making
// Phase B re-run ingestion.
func (p *Pipeline) ProcessBarPhaseB(ctx context.Context, evt domain.Event, sanitized domain.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// stage 0 (deferred): collector observes every MarketBarReceived for
	// mark-to-market equity tracking. Single shared mutex — running
	// serially here keeps it off the Phase A hot path.
	if p.collector != nil {
		_ = p.collector.OnBarDirect(ctx, evt)
	}

	// stage 5 (deferred): position monitor price cache. Single shared
	// mutex. Post-tick EvalExitRules reads this cache, so it needs to be
	// fresh by the time the barrier completes — which serial Phase B
	// guarantees.
	if p.priceCache != nil {
		_ = p.priceCache.HandleBarDirect(ctx, sanitized)
	}

	// Drain runner's buffered SignalCreated events — risk sizer,
	// enricher, etc. all subscribe to SignalCreated and must see them in
	// bar-dispatch order.
	if p.runner != nil {
		sigs := p.runner.DrainPendingSignals()
		for i := range sigs {
			if p.eventBus != nil {
				_ = p.eventBus.Publish(ctx, sigs[i])
			}
		}
	}
	// Drain any non-StateUpdated monitor pending events stashed during
	// Phase A (regime shifts, setup detections, HTF events).
	p.drainDeferredForPhaseB(ctx)
	return nil
}
