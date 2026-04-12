package backtest

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// SliceBar is an input row for RunSliceToCompletion: the original
// MarketBarReceived event together with the tick time (minTime under
// the single-threaded dispatch loop) that groups it with other symbols.
type SliceBar struct {
	TickTime time.Time
	Event    domain.Event
}

// SliceCoordinator is the callback surface RunSliceToCompletion uses
// to let the caller run per-tick post-processing (exit rule
// evaluation, price cache drain, etc.) without the backtest package
// knowing about position monitor or execution service internals.
type SliceCoordinator interface {
	// OnTick is invoked exactly once per merged tick, after every
	// event tagged with that tick time has been replayed through
	// the shared event bus. Implementations typically call
	// positionmonitor.Service.EvalExitRules(tickTime) and let the bus
	// drain synchronously.
	OnTick(ctx context.Context, tickTime time.Time) error

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

// shardEventKind discriminates the payload carried by shardEvent.
// kindBar forwards (raw, sanitized) bar events through the collector
// and price cache during replay; kindSignal carries a pending
// SignalCreated event the shard buffered via runner.deferSignalPublish;
// kindRaw wraps any non-StateUpdated monitor deferred event.
type shardEventKind uint8

const (
	kindBar shardEventKind = iota
	kindSignal
	kindRaw
)

// sliceEvent is the unit of merge between shards. Stored in
// per-shard chronological buffers during the parallel phase, merged
// and replayed in (tickTime, shardIdx, seq) order afterwards.
type sliceEvent struct {
	tickTime time.Time
	shardIdx int
	seq      uint64
	kind     shardEventKind
	// kindBar payload — the original received bar event and its sanitized
	// form (sanitized is zero-valued if the adaptive filter dropped it,
	// but such bars are skipped before building the sliceEvent).
	rawBar       domain.Event
	sanitizedBar domain.Event
	// kindSignal / kindRaw payload.
	event domain.Event
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
	coord SliceCoordinator,
) error {
	if coord == nil {
		return fmt.Errorf("backtest: RunSliceToCompletion requires a non-nil SliceCoordinator")
	}
	if len(bars) == 0 {
		return nil
	}

	// Partition bars into per-shard slabs using the shard index for
	// each bar's symbol. Bars are already chronological because the
	// caller assembled them from per-symbol streams in time order;
	// appending preserves chronology within each shard.
	perShardBars := make([][]SliceBar, sp.nworkers)
	for i := range bars {
		bar, ok := bars[i].Event.Payload.(domain.MarketBar)
		if !ok {
			return fmt.Errorf("backtest: slice bar %d has non-MarketBar payload %T", i, bars[i].Event.Payload)
		}
		idx, known := sp.symbolToShard[bar.Symbol.String()]
		if !known {
			// Unknown symbol — drop, matching per-tick Dispatch semantics.
			continue
		}
		perShardBars[idx] = append(perShardBars[idx], bars[i])
	}

	// Each shard's worker goroutine writes its emitted events into
	// perShardEvents[idx]. Buffers are single-writer per shard so no
	// locking is needed.
	perShardEvents := make([][]sliceEvent, sp.nworkers)

	var wg sync.WaitGroup
	errs := make([]error, sp.nworkers)
	for i := 0; i < sp.nworkers; i++ {
		slab := perShardBars[i]
		if len(slab) == 0 {
			continue
		}
		wg.Add(1)
		go func(idx int, slab []SliceBar) {
			defer wg.Done()
			buf, err := sp.runShardSlice(ctx, idx, slab)
			perShardEvents[idx] = buf
			errs[idx] = err
		}(i, slab)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("backtest: shard %d slice execution: %w", i, err)
		}
	}

	// Serial k-way merge + replay on the main goroutine.
	return sp.mergeAndReplay(ctx, perShardEvents, coord)
}

// runShardSlice runs every bar in slab through shard[idx]'s Phase A
// and collects the emitted events into a chronological buffer. The
// returned slice is owned by the caller.
func (sp *ShardedPipeline) runShardSlice(ctx context.Context, idx int, slab []SliceBar) ([]sliceEvent, error) {
	shard := sp.shards[idx]
	out := make([]sliceEvent, 0, len(slab)*2)
	var seq uint64

	for _, sb := range slab {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sanitized, dropped, err := shard.ProcessBarPhaseA(ctx, sb.Event)
		if err != nil {
			return nil, err
		}
		if dropped {
			continue
		}

		// Carry the raw + sanitized bar through the merge so the
		// replay loop can feed collector + price cache in dispatch
		// order — those two services hold shared mutexes and must
		// run serially.
		out = append(out, sliceEvent{
			tickTime:     sb.TickTime,
			shardIdx:     idx,
			seq:          seq,
			kind:         kindBar,
			rawBar:       sb.Event,
			sanitizedBar: sanitized,
		})
		seq++

		// Drain deferred signals emitted by the runner during
		// handleBar. These carry SignalCreated events that must be
		// reconciled against live positions during replay.
		if r := shard.Runner(); r != nil {
			sigs := r.DrainPendingSignals()
			for i := range sigs {
				out = append(out, sliceEvent{
					tickTime: sb.TickTime,
					shardIdx: idx,
					seq:      seq,
					kind:     kindSignal,
					event:    sigs[i],
				})
				seq++
			}
		}

		// Drain monitor deferred events — regime shifts, setup
		// detections, HTF events. These go to multi-consumer bus
		// handlers that may share state; safe to replay serially.
		strict, bestEffort := shard.TakeDeferred()
		for i := range strict {
			out = append(out, sliceEvent{
				tickTime: sb.TickTime,
				shardIdx: idx,
				seq:      seq,
				kind:     kindRaw,
				event:    strict[i],
			})
			seq++
		}
		for i := range bestEffort {
			out = append(out, sliceEvent{
				tickTime: sb.TickTime,
				shardIdx: idx,
				seq:      seq,
				kind:     kindRaw,
				event:    bestEffort[i],
			})
			seq++
		}
	}

	return out, nil
}

// mergeAndReplay performs a k-way merge of the per-shard buffers in
// (tickTime, shardIdx, seq) order and replays each event through the
// shared event bus. The SliceCoordinator is invoked once per tick
// boundary so the caller can run exit-rule evaluation between ticks.
func (sp *ShardedPipeline) mergeAndReplay(
	ctx context.Context,
	perShardEvents [][]sliceEvent,
	coord SliceCoordinator,
) error {
	heads := make([]int, len(perShardEvents))
	total := 0
	for _, ev := range perShardEvents {
		total += len(ev)
	}
	if total == 0 {
		return nil
	}

	var currentTick time.Time
	tickStarted := false
	logger := coord.Logger()
	if logger == nil {
		logger = slog.Default()
	}

	for done := 0; done < total; {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Linear scan for the smallest head across shards. Nworkers
		// is small (typically 8) so O(N) per step beats the overhead
		// of a heap.
		bestShard := -1
		for i, h := range heads {
			if h >= len(perShardEvents[i]) {
				continue
			}
			if bestShard < 0 || lessEvent(perShardEvents[i][h], perShardEvents[bestShard][heads[bestShard]]) {
				bestShard = i
			}
		}
		if bestShard < 0 {
			break
		}
		ev := perShardEvents[bestShard][heads[bestShard]]
		heads[bestShard]++
		done++

		if !tickStarted {
			currentTick = ev.tickTime
			tickStarted = true
		} else if !ev.tickTime.Equal(currentTick) {
			// Flush the previous tick's post-bar work.
			if err := coord.OnTick(ctx, currentTick); err != nil {
				return fmt.Errorf("backtest: slice coordinator OnTick(%s): %w", currentTick, err)
			}
			currentTick = ev.tickTime
		}

		if err := sp.replayEvent(ctx, ev, coord, logger); err != nil {
			return err
		}
	}

	if tickStarted {
		if err := coord.OnTick(ctx, currentTick); err != nil {
			return fmt.Errorf("backtest: slice coordinator OnTick(%s): %w", currentTick, err)
		}
	}
	return nil
}

// replayEvent handles one merged sliceEvent. kindBar feeds collector +
// price cache; kindSignal reconciles against live positions and then
// publishes; kindRaw goes straight to the bus.
func (sp *ShardedPipeline) replayEvent(
	ctx context.Context,
	ev sliceEvent,
	coord SliceCoordinator,
	logger *slog.Logger,
) error {
	switch ev.kind {
	case kindBar:
		if sp.collector != nil {
			_ = sp.collector.OnBarDirect(ctx, ev.rawBar)
		}
		if sp.priceCache != nil {
			_ = sp.priceCache.HandleBarDirect(ctx, ev.sanitizedBar)
		}
	case kindSignal:
		// Re-run reconciliation with live positions. This is the
		// whole reason deferReconcile exists: the shard pass ran
		// handleBar with empty positions, so any reversal-entry
		// signals came through as entries. In the replay we have
		// the real positions (since fills have replayed up to this
		// point via the bus) and can convert them correctly.
		sig, ok := ev.event.Payload.(start.Signal)
		if !ok {
			if sp.eventBus != nil {
				_ = sp.eventBus.Publish(ctx, ev.event)
			}
			return nil
		}
		reconciled := strategy.ReconcileSignals([]start.Signal{sig}, coord.PosLookup, logger)
		if len(reconciled) == 0 {
			return nil
		}
		outEvent := ev.event
		outEvent.Payload = reconciled[0]
		if sp.eventBus != nil {
			_ = sp.eventBus.Publish(ctx, outEvent)
		}
	case kindRaw:
		if sp.eventBus != nil {
			_ = sp.eventBus.Publish(ctx, ev.event)
		}
	}
	return nil
}

// lessEvent reports whether a orders before b in merge order. Stable
// on (tickTime, shardIdx, seq) — tied tickTimes fall back to shardIdx
// first so bars from the lower-numbered shard publish before higher
// shards, and ties within a shard use the append sequence.
func lessEvent(a, b sliceEvent) bool {
	if a.tickTime.Equal(b.tickTime) {
		if a.shardIdx == b.shardIdx {
			return a.seq < b.seq
		}
		return a.shardIdx < b.shardIdx
	}
	return a.tickTime.Before(b.tickTime)
}

// sortSliceEventsForTest exposes the merge ordering predicate for
// tests that construct synthetic sliceEvent slices and want to
// verify they land in the expected order.
func sortSliceEventsForTest(events []sliceEvent) {
	sort.SliceStable(events, func(i, j int) bool { return lessEvent(events[i], events[j]) })
}

