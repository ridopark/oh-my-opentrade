package backtest

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// SliceBar is an input row for RunSliceToCompletion: the raw market
// bar together with the tick time (minTime under the single-threaded
// dispatch loop) that groups it with other symbols, and the ET
// session-open timestamp the single-threaded dispatch loop computes
// (via time.Date in the replay loc) to drive day-boundary aggregator
// resets.
//
// Events are NOT pre-built: the shard worker and replay loop each
// construct a domain.Event lazily via toEvent(). Building events
// up-front on a 30 sym / 1 yr run allocated ~6 GB of event structs
// in main.main — the largest single source of GC pressure on the
// run. Lazy construction drops that slice's retained memory from
// ~6 GB to ~400 MB of raw MarketBar values.
type SliceBar struct {
	TickTime    time.Time
	SessionOpen time.Time
	Bar         domain.MarketBar
	TenantID    string
	EnvMode     domain.EnvMode
}

// toEvent builds a MarketBarReceived domain.Event from this row.
// Called once per bar during Phase A and once again during replay —
// short-lived allocations the young-gen GC reclaims cheaply,
// compared to the retained-for-the-full-run giant slice the old
// design kept in main.main.
func (sb SliceBar) toEvent() domain.Event {
	idemKey := strconv.FormatInt(sb.Bar.Time.UnixNano(), 36) + string(sb.Bar.Symbol)
	return domain.NewBacktestEvent(
		domain.EventMarketBarReceived,
		sb.TenantID,
		sb.EnvMode,
		idemKey,
		sb.Bar,
		sb.Bar.Time,
	)
}

// SliceCoordinator is the callback surface RunSliceToCompletion uses
// to let the caller run per-tick pre- and post-processing (clock
// advance, day rollover, exit rule evaluation) without the backtest
// package knowing about position monitor or execution service
// internals.
type SliceCoordinator interface {
	// OnTickBegin is invoked once just before the first event for
	// tick t is replayed. Implementations typically advance the
	// replay clock (atomic store that clockFn reads) and run new-day
	// aggregator resets so downstream handlers see the correct
	// bar-time context when they fire.
	OnTickBegin(ctx context.Context, tickTime time.Time) error

	// OnTickEnd is invoked once after every event for tick t has
	// been replayed through the event bus. Implementations typically
	// call positionmonitor.Service.EvalExitRules(tickTime), drain
	// pending bus handlers, and advance simbroker price state.
	OnTickEnd(ctx context.Context, tickTime time.Time) error

	// OnBar is invoked once per replayed bar, just before the bar's
	// collector / priceCache side effects run in the replay loop.
	// Implementations typically call simbroker.UpdatePrice so fills
	// triggered by signals from this bar use the correct close
	// price. Receives the raw MarketBarReceived event (not the
	// sanitized one) so callers can extract the original bar.
	OnBar(ctx context.Context, raw domain.Event) error

	// PosLookup is the live position-lookup function the replay loop
	// uses to rerun ReconcileSignals against fresh positions before
	// publishing each SignalCreated event. Typically
	// positionmonitor.Service.LookupPosition.
	PosLookup(symbol string) (domain.MonitoredPosition, bool)

	// Logger returns the *slog.Logger ReconcileSignals uses to emit
	// reconciliation notices during replay. May return nil — the
	// coordinator should use slog.Default() if it has no specific
	// logger to hand over.
	Logger() *slog.Logger
}

// shardEmission is a deferred event (SignalCreated, or a monitor
// pending event like RegimeShifted / SetupDetected) that a shard
// buffered during its slice-to-completion pass. Signals dominate in
// practice (most other events fire only on rare regime transitions).
//
// barIdx is the index into the caller's flat bars slice the emission
// corresponds to — the emission was produced by shard.ProcessBarPhaseA
// for bars[barIdx]. The replay loop iterates the flat bars slice in
// order and, right after running each bar's side effects, drains any
// emissions tagged with that index from the owning shard's buffer.
//
// Keeping only emissions in per-shard buffers (instead of one event
// per bar) keeps the memory footprint near-zero — a typical 30 sym /
// 1 yr run produces ~3 386 signals, i.e. ~850 KB total vs the ~2 GB
// the naive bar-carrying design allocated. Less allocation means less
// GC pressure, which is what blocked the naive implementation from
// scaling to the 30 sym workload.
type shardEmission struct {
	barIdx int
	event  domain.Event
}

// RunSliceToCompletion runs every bar in bars to completion across
// Nworkers shard goroutines, collects each shard's emitted events into
// a per-shard ordered buffer, then performs a k-way merge on the main
// goroutine and replays the merged stream through the shared event bus
// in tick order. Exit rule evaluation runs between ticks via
// coord.OnTick.
//
// Parity with the single-threaded path is preserved because:
//
//  1. Each shard owns its own monitor + runner state — shards never
//     touch foreign state during the parallel phase.
//  2. Signal publication is deferred (runner.SetDeferSignalPublish)
//     and ReconcileSignals is re-run during replay against live
//     positions returned by coord.PosLookup.
//  3. Collector + price cache updates happen in the replay loop on
//     the main goroutine, in tick-and-dispatch order.
//  4. The k-way merge is stable on (tickTime, shardIdx, seq) so the
//     event order is deterministic across runs.
//
// Nworkers is clamped to [1, len(bars)-1+1] — len(symbols) is the
// practical upper bound but RunSliceToCompletion only sees bars, not
// symbols.
func (sp *ShardedPipeline) RunSliceToCompletion(
	ctx context.Context,
	bars []SliceBar,
	initialSessionOpen time.Time,
	coord SliceCoordinator,
) error {
	if coord == nil {
		return fmt.Errorf("backtest: RunSliceToCompletion requires a non-nil SliceCoordinator")
	}
	if len(bars) == 0 {
		return nil
	}

	// Partition bars into per-shard index slabs. Each slab entry is
	// just the int index into the caller-owned bars slice — no event
	// copies. Keeping the slabs thin (4 or 8 bytes per entry instead
	// of a full domain.Event) removes ~500 MB of allocations on a
	// 30 sym / 1 yr run and drops GC cost from ~24 % of CPU to
	// near-zero.
	perShardBars := make([][]int, sp.nworkers)
	for i := range bars {
		idx, known := sp.symbolToShard[bars[i].Bar.Symbol.String()]
		if !known {
			continue
		}
		perShardBars[idx] = append(perShardBars[idx], i)
	}

	// Each worker writes its signal emissions + monitor deferred
	// events into the corresponding perShard* slice. Tagged with the
	// flat barIdx. Single-writer per shard.
	perShardSignals := make([][]shardEmission, sp.nworkers)
	perShardDeferred := make([][]shardEmission, sp.nworkers)

	// initialDayOpen seeds each shard worker's currentDayOpen cursor
	// to the SessionOpen the caller used when calling
	// InitAggregators at warmup. That way a shard's first bar
	// correctly fires a day-boundary reset IF it falls on a later
	// ET session open than the warmup one (which is the normal case
	// — fromTime is e.g. 2025-04-01 and the first bar is e.g.
	// 2025-04-01 09:31 or 2025-04-02 09:31), but doesn't fire a
	// spurious reset when the first bar's session matches warmup.
	initialDayOpen := initialSessionOpen

	var wg sync.WaitGroup
	errs := make([]error, sp.nworkers)
	for i := 0; i < sp.nworkers; i++ {
		slab := perShardBars[i]
		if len(slab) == 0 {
			continue
		}
		wg.Add(1)
		go func(idx int, slab []int) {
			defer wg.Done()
			shard := sp.shards[idx]
			slabSymbols := sp.slabs[idx]
			var sigs, def []shardEmission
			currentDayOpen := initialDayOpen
			for _, flatIdx := range slab {
				if cerr := ctx.Err(); cerr != nil {
					errs[idx] = cerr
					return
				}
				dayOpen := bars[flatIdx].SessionOpen
				// Day-boundary reset: in single-threaded mode the
				// main loop calls ResetAggregators +
				// ResetSessionIndicators on each shard at the start
				// of a new trading day (before ANY bars for that day
				// hit the monitor). We must replicate that here so
				// HTF aggregators don't carry state across days —
				// otherwise regime events drift from the baseline by
				// ~100 k events on a 1-yr run and signal counts
				// diverge by ~20 RTH signals. initialDayOpen tracks
				// the session open that matches the InitAggregators
				// call the caller ran at startup, so we don't
				// double-reset on the very first bar of the run.
				if dayOpen.After(currentDayOpen) {
					if m := shard.Monitor(); m != nil {
						m.ResetAggregators(dayOpen)
						for _, sym := range slabSymbols {
							m.ResetSessionIndicators(sym.String())
						}
					}
					currentDayOpen = dayOpen
				}

				_, dropped, err := shard.ProcessBarPhaseA(ctx, bars[flatIdx].toEvent())
				if err != nil {
					errs[idx] = err
					return
				}
				if dropped {
					continue
				}
				if r := shard.Runner(); r != nil {
					ps := r.DrainPendingSignals()
					for j := range ps {
						sigs = append(sigs, shardEmission{barIdx: flatIdx, event: ps[j]})
					}
				}
				strict, bestEffort := shard.TakeDeferred()
				for j := range strict {
					def = append(def, shardEmission{barIdx: flatIdx, event: strict[j]})
				}
				for j := range bestEffort {
					def = append(def, shardEmission{barIdx: flatIdx, event: bestEffort[j]})
				}
			}
			perShardSignals[idx] = sigs
			perShardDeferred[idx] = def
		}(i, slab)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("backtest: shard %d slice execution: %w", i, err)
		}
	}

	return sp.replayFlat(ctx, bars, perShardSignals, perShardDeferred, coord)
}

// replayFlat iterates the caller's flat bars slice in chronological
// order, advances the replay clock at tick boundaries, runs serial
// per-bar side effects (simbroker update, collector, price cache),
// and drains the owning shard's emission buffers whose barIdx matches
// the current flat index — publishing signals through ReconcileSignals
// against live positions and raw deferred events straight to the bus.
//
// Memory-light compared to the naive "event per bar" design: only
// real emissions sit in the per-shard buffers (~thousands on a
// year-long run) so we avoid the multi-GB slab the naive slice-event
// pool would allocate.
func (sp *ShardedPipeline) replayFlat(
	ctx context.Context,
	bars []SliceBar,
	perShardSignals [][]shardEmission,
	perShardDeferred [][]shardEmission,
	coord SliceCoordinator,
) error {
	logger := coord.Logger()
	if logger == nil {
		logger = slog.Default()
	}

	signalHeads := make([]int, sp.nworkers)
	deferredHeads := make([]int, sp.nworkers)

	var currentTick time.Time
	tickStarted := false

	for i := range bars {
		if err := ctx.Err(); err != nil {
			return err
		}
		tickTime := bars[i].TickTime

		if !tickStarted {
			currentTick = tickTime
			tickStarted = true
			if err := coord.OnTickBegin(ctx, currentTick); err != nil {
				return fmt.Errorf("backtest: slice coordinator OnTickBegin(%s): %w", currentTick, err)
			}
		} else if !tickTime.Equal(currentTick) {
			if err := coord.OnTickEnd(ctx, currentTick); err != nil {
				return fmt.Errorf("backtest: slice coordinator OnTickEnd(%s): %w", currentTick, err)
			}
			currentTick = tickTime
			if err := coord.OnTickBegin(ctx, currentTick); err != nil {
				return fmt.Errorf("backtest: slice coordinator OnTickBegin(%s): %w", currentTick, err)
			}
		}

		shardIdx, known := sp.symbolToShard[bars[i].Bar.Symbol.String()]
		if !known {
			continue
		}

		// Build the bar event lazily — reused by coord.OnBar,
		// collector, and priceCache below, then discarded so the
		// young-gen GC reclaims it after the iteration. Avoids
		// retaining 6 GB of pre-built events in the flat slab for
		// the full run.
		evt := bars[i].toEvent()

		// Per-bar serial side effects.
		if err := coord.OnBar(ctx, evt); err != nil {
			return fmt.Errorf("backtest: slice coordinator OnBar: %w", err)
		}
		if sp.collector != nil {
			_ = sp.collector.OnBarDirect(ctx, evt)
		}
		if sp.priceCache != nil {
			// Use the raw bar event — adaptive filter runs in
			// backtest PassThrough mode so sanitized == raw.
			_ = sp.priceCache.HandleBarDirect(ctx, evt)
		}

		// Drain any signals this shard emitted while processing
		// this bar (tagged with the same flat index). Publish them
		// through ReconcileSignals against live positions so the
		// reversal-entry ↔ close-position transform runs against
		// fresh posMon state.
		for signalHeads[shardIdx] < len(perShardSignals[shardIdx]) &&
			perShardSignals[shardIdx][signalHeads[shardIdx]].barIdx == i {
			em := perShardSignals[shardIdx][signalHeads[shardIdx]]
			signalHeads[shardIdx]++
			sig, ok := em.event.Payload.(start.Signal)
			if !ok {
				if sp.eventBus != nil {
					_ = sp.eventBus.Publish(ctx, em.event)
				}
				continue
			}
			reconciled := strategy.ReconcileSignals([]start.Signal{sig}, coord.PosLookup, logger)
			if len(reconciled) == 0 {
				continue
			}
			out := em.event
			out.Payload = reconciled[0]
			if sp.eventBus != nil {
				_ = sp.eventBus.Publish(ctx, out)
			}
		}

		// Drain deferred monitor events (regime, setup, HTF) for
		// this bar — straight to the bus, no reconciliation.
		for deferredHeads[shardIdx] < len(perShardDeferred[shardIdx]) &&
			perShardDeferred[shardIdx][deferredHeads[shardIdx]].barIdx == i {
			em := perShardDeferred[shardIdx][deferredHeads[shardIdx]]
			deferredHeads[shardIdx]++
			if sp.eventBus != nil {
				_ = sp.eventBus.Publish(ctx, em.event)
			}
		}
	}

	if tickStarted {
		if err := coord.OnTickEnd(ctx, currentTick); err != nil {
			return fmt.Errorf("backtest: slice coordinator OnTickEnd(%s): %w", currentTick, err)
		}
	}
	return nil
}

