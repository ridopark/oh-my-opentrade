package backtest

import (
	"context"

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
	priceCache *positionmonitor.PriceCache
	collector  *Collector
	eventBus   ports.EventBusPort
}

// PipelineInfra is the injection struct — construct one and pass it to
// NewPipeline. Nil fields are tolerated (optional observers).
type PipelineInfra struct {
	Ingestion  *ingestion.Service
	Monitor    *monitor.Service
	Runner     *strategy.Runner
	PriceCache *positionmonitor.PriceCache
	Collector  *Collector
	EventBus   ports.EventBusPort
}

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
		priceCache: infra.PriceCache,
		collector:  infra.Collector,
		eventBus:   infra.EventBus,
	}
}

// ProcessBar runs one MarketBarReceived event through the hot chain as a
// series of direct method calls. Returns the first non-nil error encountered.
// If the bar is dropped by the adaptive filter, ProcessBar returns nil with
// no downstream work performed.
func (p *Pipeline) ProcessBar(ctx context.Context, evt domain.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// stage 0: collector observes every MarketBarReceived for mark-to-market
	// tracking. Before ingestion so the count matches the old bus path.
	if p.collector != nil {
		_ = p.collector.OnBarDirect(ctx, evt)
	}

	// stage 1: ingestion — spike filter + repair.
	sanitized, ok, err := p.ingestion.ProcessBar(ctx, evt)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	// stage 2: monitor — indicator calc, HTF aggregation, regime, ORB.
	// The monitor is in direct-dispatch mode, so it collects the
	// StateUpdated / HTF / RegimeShifted / SetupDetected events in
	// pending slices instead of calling eventBus.Publish.
	if p.monitor != nil {
		if err := p.monitor.HandleMarketBar(ctx, sanitized); err != nil {
			return err
		}
	}

	// stage 3: strategy runner handles the 1m bar (runs strategies, its
	// own HTF aggregation, emits SignalCreated via the bus).
	if p.runner != nil {
		if err := p.runner.HandleBarDirect(ctx, sanitized); err != nil {
			return err
		}
	}

	// stage 4: drain the monitor's pending events and dispatch them by
	// type. StateUpdated goes to strategy.Runner; HTF Sanitized events are
	// dropped (strategy.Runner does its own HTF aggregation); Regime and
	// Setup events still go to the bus because they have multi-consumer
	// fan-out (debate enricher, etc.).
	if p.monitor != nil {
		strict, bestEffort := p.monitor.DrainPending()
		for i := range strict {
			ev := &strict[i]
			switch ev.Type {
			case domain.EventStateUpdated:
				if p.runner != nil {
					if err := p.runner.HandleStateUpdatedDirect(ctx, *ev); err != nil {
						return err
					}
				}
			default:
				// Multi-consumer fan-out — keep on the bus.
				if p.eventBus != nil {
					_ = p.eventBus.Publish(ctx, *ev)
				}
			}
		}
		for i := range bestEffort {
			ev := &bestEffort[i]
			switch ev.Type {
			case domain.EventMarketBarSanitized:
				// HTF self-publishes; strategy.Runner has already
				// aggregated 1m → HTF internally. Drop.
			default:
				if p.eventBus != nil {
					_ = p.eventBus.Publish(ctx, *ev)
				}
			}
		}
	}

	// stage 5: position monitor price cache.
	if p.priceCache != nil {
		_ = p.priceCache.HandleBarDirect(ctx, sanitized)
	}

	return nil
}
