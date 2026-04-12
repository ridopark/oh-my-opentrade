package backtest

import (
	"context"
	"fmt"
	"sync"

	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// ShardedPipeline partitions a backtest's symbol universe across N worker
// pipelines, each owning its own ingestion.Service + monitor.Service +
// strategy.Runner triple. The price cache, collector, and event bus remain
// shared because their per-bar work is either lock-free (FreezeHandlers)
// or protected by their own internal synchronization.
//
// Ingestion is deliberately per-shard: ingestion.Service wraps an
// AdaptiveFilter whose per-symbol state maps (windows, atrState, volState,
// symbolDeviation) have zero locking. Under Step 4's concurrent dispatch
// two shards calling ProcessBar on different symbols would race on those
// maps. Per-shard ingestion keeps the filter single-writer by construction
// and is cheap — each instance only holds state for its own slab.
//
// Step 1 of the Phase 2 parallelisation plan only installs the partitioning
// + shard construction. Dispatch is still a synchronous direct call for
// parity-testing purposes. Step 4 layers worker-pool fan-out on top by
// replacing Dispatch's body with goroutine-based batch execution while
// preserving the same public surface.
//
// See docs/perf/phase2-restart-plan.md (Steps 1-4) for rationale.
type ShardedPipeline struct {
	shards        []*Pipeline
	slabs         [][]domain.Symbol
	symbolToShard map[string]int
	nworkers      int

	priceCache *positionmonitor.PriceCache
	collector  *Collector
	eventBus   ports.EventBusPort

	// tickJobs accumulates bars dispatched during the current tick. Kept
	// in dispatch order so WaitTick can replay Phase B sequentially —
	// parity with the single-threaded path requires signals to be
	// published in the same order the bars arrived.
	tickJobs []shardJob
	// perShardScratch is a reusable slab of per-shard job pointer slices
	// populated at WaitTick time from tickJobs. Cleared between ticks to
	// avoid per-tick allocations on the hot path.
	perShardScratch [][]*shardJob

	// Persistent worker pool: one goroutine per shard reading jobs from
	// its inbox channel. Spawned once by startWorkers and torn down by
	// Close. Per-tick goroutine spawn overhead (24 × 240k ticks = 5.8M
	// spawns on the 30sym/1yr run) was a noticeable tax in the naive
	// implementation; persistent workers replace it with a single chan
	// send + wg.Done per active shard per tick.
	workerInbox []chan []*shardJob
	workerWG    sync.WaitGroup
	workersOnce sync.Once
}

// shardJob captures one bar's trip through Phase A + Phase B. Fields are
// populated in two passes: Dispatch records (shardIdx, ctx, evt); the
// Phase A worker goroutine fills (sanitized, dropped, err); Phase B runs
// serially and consumes the stored values.
type shardJob struct {
	shardIdx  int
	ctx       context.Context
	evt       domain.Event
	sanitized domain.Event
	dropped   bool
	err       error
}

// ShardServices is the per-shard (ingestion, monitor, runner) triple
// produced by a ShardFactory. The factory may apply any additional
// configuration (ORB config, tide tracker, gate chain, warmup bars,
// spike-filter seeding) before returning — the ShardedPipeline treats the
// services as fully constructed.
type ShardServices struct {
	Ingestion *ingestion.Service
	Monitor   *monitor.Service
	Runner    *strategy.Runner
}

// ShardFactory builds a fresh ShardServices pair configured for the given
// slab of symbols. Called once per shard at construction. The caller
// (typically omo-replay's main) provides this closure so the sharded pipeline
// stays agnostic to the bootstrap wiring details (spec store, AI advisor,
// tide tracker, etc.).
type ShardFactory func(slab []domain.Symbol) (ShardServices, error)

// ShardedInfra is the injection struct for NewShardedPipeline. The shared
// services (PriceCache, Collector, EventBus) are installed on every
// per-shard Pipeline. Factory is invoked Nworkers times to construct the
// per-shard (ingestion, monitor, runner) triples.
type ShardedInfra struct {
	PriceCache *positionmonitor.PriceCache
	Collector  *Collector
	EventBus   ports.EventBusPort
	Factory    ShardFactory
}

// NewShardedPipeline partitions symbols into nworkers slabs via a stable
// hash and constructs one Pipeline per slab. Nworkers is clamped to
// [1, len(symbols)]. Empty-slab shards are allowed but will never receive
// bars — they still run setup paths for cheap determinism.
func NewShardedPipeline(nworkers int, symbols []domain.Symbol, infra ShardedInfra) (*ShardedPipeline, error) {
	if infra.Factory == nil {
		return nil, fmt.Errorf("backtest: sharded pipeline requires a ShardFactory")
	}
	if nworkers < 1 {
		nworkers = 1
	}
	if nworkers > len(symbols) && len(symbols) > 0 {
		nworkers = len(symbols)
	}

	sp := &ShardedPipeline{
		shards:        make([]*Pipeline, nworkers),
		slabs:         make([][]domain.Symbol, nworkers),
		symbolToShard: make(map[string]int, len(symbols)),
		nworkers:      nworkers,
		priceCache:    infra.PriceCache,
		collector:     infra.Collector,
		eventBus:      infra.EventBus,
	}

	for _, sym := range symbols {
		idx := shardIndex(sym.String(), nworkers)
		sp.slabs[idx] = append(sp.slabs[idx], sym)
		sp.symbolToShard[sym.String()] = idx
	}

	for i := 0; i < nworkers; i++ {
		svcs, err := infra.Factory(sp.slabs[i])
		if err != nil {
			return nil, fmt.Errorf("backtest: shard %d factory failed: %w", i, err)
		}
		if svcs.Ingestion == nil {
			return nil, fmt.Errorf("backtest: shard %d factory returned nil ingestion", i)
		}
		sp.shards[i] = NewPipeline(PipelineInfra{
			Ingestion:  svcs.Ingestion,
			Monitor:    svcs.Monitor,
			Runner:     svcs.Runner,
			PriceCache: infra.PriceCache,
			Collector:  infra.Collector,
			EventBus:   infra.EventBus,
		})
	}

	return sp, nil
}

// ShardCount returns the number of worker shards.
func (sp *ShardedPipeline) ShardCount() int { return sp.nworkers }

// Shards returns the underlying per-shard pipelines. The returned slice
// is not a copy — callers must not mutate it.
func (sp *ShardedPipeline) Shards() []*Pipeline { return sp.shards }

// Slab returns the symbol slab owned by shard i.
func (sp *ShardedPipeline) Slab(i int) []domain.Symbol { return sp.slabs[i] }

// ShardIndexFor returns the shard index for the given symbol, or -1 when
// the symbol wasn't registered.
func (sp *ShardedPipeline) ShardIndexFor(sym string) int {
	if idx, ok := sp.symbolToShard[sym]; ok {
		return idx
	}
	return -1
}

// ShardForSymbol returns the Pipeline that owns the given symbol, or nil
// when the symbol wasn't registered. Step 3 uses this to route per-symbol
// setup calls (MarkReady, ResetSessionIndicators, per-symbol WarmUp) to
// the correct shard.
func (sp *ShardedPipeline) ShardForSymbol(sym string) *Pipeline {
	idx, ok := sp.symbolToShard[sym]
	if !ok {
		return nil
	}
	return sp.shards[idx]
}

// ForEachShard iterates every shard with its owned symbol slab. Used by
// setup paths that must fan out a per-shard operation (InitAggregators,
// ResetAggregators, Start, ClearAllPendingStates, SetSuppressProgressEvents,
// SetAIAnchorResolver, SetBaseSymbols). Stops on the first error.
func (sp *ShardedPipeline) ForEachShard(fn func(p *Pipeline, slab []domain.Symbol) error) error {
	for i, p := range sp.shards {
		if err := fn(p, sp.slabs[i]); err != nil {
			return err
		}
	}
	return nil
}

// RouteSymbol invokes fn on the shard that owns sym. Used by per-symbol
// setup paths (MarkReady, ResetSessionIndicators, per-symbol WarmUp).
// Returns false when the symbol is unregistered.
func (sp *ShardedPipeline) RouteSymbol(sym string, fn func(p *Pipeline)) bool {
	p := sp.ShardForSymbol(sym)
	if p == nil {
		return false
	}
	fn(p)
	return true
}

// LookupSnapshot routes a GetLastSnapshot-style lookup to the shard that
// owns sym. Returns a zero snapshot and false when the symbol is unknown.
// Used to build a shard-aware MarketDataFn closure for enricher / posMon
// snapshot callbacks — the single-pipeline code hit one monitor.Service
// directly; sharded code needs a routing wrapper.
func (sp *ShardedPipeline) LookupSnapshot(sym string) (domain.IndicatorSnapshot, bool) {
	p := sp.ShardForSymbol(sym)
	if p == nil {
		return domain.IndicatorSnapshot{}, false
	}
	if m := p.Monitor(); m != nil {
		return m.GetLastSnapshot(sym)
	}
	return domain.IndicatorSnapshot{}, false
}

// Dispatch enqueues a MarketBarReceived bar on its owning shard's Phase B
// queue without running any work synchronously. The actual Phase A fan-out
// and Phase B replay happen in WaitTick at the end of the tick. Enqueueing
// preserves dispatch order — parity with the single-threaded path depends
// on Phase B replaying bars in arrival order so downstream bus handlers
// (risk sizer, execution, sim broker) see signals in a deterministic
// sequence.
func (sp *ShardedPipeline) Dispatch(ctx context.Context, evt domain.Event) error {
	bar, ok := evt.Payload.(domain.MarketBar)
	if !ok {
		return fmt.Errorf("backtest: sharded dispatch expected MarketBar payload, got %T", evt.Payload)
	}
	idx, ok := sp.symbolToShard[bar.Symbol.String()]
	if !ok {
		// Unregistered symbol — drop (parity with single-pipeline case
		// which also has no handler for an unknown symbol).
		return nil
	}
	sp.tickJobs = append(sp.tickJobs, shardJob{shardIdx: idx, ctx: ctx, evt: evt})
	return nil
}

// startWorkers spawns one persistent goroutine per shard. Each worker
// loops on its inbox channel, runs ProcessBarPhaseA on every job it
// receives, and calls workerWG.Done when the batch is complete.
// Idempotent — only the first call spawns; subsequent calls are no-ops.
func (sp *ShardedPipeline) startWorkers() {
	sp.workersOnce.Do(func() {
		sp.workerInbox = make([]chan []*shardJob, sp.nworkers)
		for i := 0; i < sp.nworkers; i++ {
			sp.workerInbox[i] = make(chan []*shardJob, 1)
			go func(idx int) {
				shard := sp.shards[idx]
				for jobs := range sp.workerInbox[idx] {
					for _, job := range jobs {
						job.sanitized, job.dropped, job.err = shard.ProcessBarPhaseA(job.ctx, job.evt)
					}
					sp.workerWG.Done()
				}
			}(i)
		}
	})
}

// Close tears down the persistent worker goroutines. Safe to call more
// than once; only the first call actually closes the inbox channels.
// Not required for correctness — process exit tears down goroutines too
// — but exposed so tests and long-lived hosts can avoid goroutine leaks.
func (sp *ShardedPipeline) Close() {
	for _, ch := range sp.workerInbox {
		if ch != nil {
			close(ch)
		}
	}
	sp.workerInbox = nil
}

// WaitTick runs Phase A for every tick-accumulated job across the worker
// pool and then replays Phase B serially in dispatch order. Returns after
// all work completes and clears the tick queue. Called once per replay
// tick, after the main loop has dispatched every bar for that tick.
func (sp *ShardedPipeline) WaitTick() {
	if len(sp.tickJobs) == 0 {
		return
	}
	sp.startWorkers()

	// Bucket job pointers by shard so each worker goroutine can iterate
	// only its own slice without touching foreign memory. Reuse the
	// scratch slab across ticks to avoid allocations on the hot path.
	if cap(sp.perShardScratch) < sp.nworkers {
		sp.perShardScratch = make([][]*shardJob, sp.nworkers)
	} else {
		sp.perShardScratch = sp.perShardScratch[:sp.nworkers]
		for i := range sp.perShardScratch {
			sp.perShardScratch[i] = sp.perShardScratch[i][:0]
		}
	}
	for j := range sp.tickJobs {
		idx := sp.tickJobs[j].shardIdx
		sp.perShardScratch[idx] = append(sp.perShardScratch[idx], &sp.tickJobs[j])
	}

	// Phase A: wake persistent workers for shards with jobs this tick.
	activeWorkers := 0
	for i := 0; i < sp.nworkers; i++ {
		if len(sp.perShardScratch[i]) == 0 {
			continue
		}
		activeWorkers++
	}
	if activeWorkers > 0 {
		sp.workerWG.Add(activeWorkers)
		for i := 0; i < sp.nworkers; i++ {
			jobs := sp.perShardScratch[i]
			if len(jobs) == 0 {
				continue
			}
			sp.workerInbox[i] <- jobs
		}
		sp.workerWG.Wait()
	}

	// Phase B: serial replay in dispatch order. Must stay serial because
	// every downstream bus handler (risk sizer, execution service, sim
	// broker, position monitor) mutates shared state and assumes
	// deterministic signal ordering. The cost is paid at signal-time,
	// which is the rare case — most bars emit zero signals.
	for j := range sp.tickJobs {
		job := &sp.tickJobs[j]
		if job.err != nil || job.dropped {
			continue
		}
		_ = sp.shards[job.shardIdx].ProcessBarPhaseB(job.ctx, job.evt, job.sanitized)
	}

	sp.tickJobs = sp.tickJobs[:0]
}

// shardIndex hashes sym to [0, n) using FNV-1a. Stable across runs and
// across platforms — the partition layout must be deterministic so that
// parity checks comparing sharded vs single-threaded output stay meaningful.
func shardIndex(sym string, n int) int {
	const (
		offset32 uint32 = 2166136261
		prime32  uint32 = 16777619
	)
	h := offset32
	for i := 0; i < len(sym); i++ {
		h ^= uint32(sym[i])
		h *= prime32
	}
	return int(h % uint32(n))
}

