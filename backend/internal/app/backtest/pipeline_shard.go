package backtest

import (
	"context"
	"fmt"

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

// Dispatch routes a MarketBarReceived event to the shard that owns the
// bar's symbol and runs ProcessBar synchronously on that shard. Step 4
// replaces this body with worker-pool fan-out; Step 1 keeps it synchronous
// so parity testing can proceed without touching the hot loop's dispatch
// mechanics.
func (sp *ShardedPipeline) Dispatch(ctx context.Context, evt domain.Event) error {
	bar, ok := evt.Payload.(domain.MarketBar)
	if !ok {
		return fmt.Errorf("backtest: sharded dispatch expected MarketBar payload, got %T", evt.Payload)
	}
	idx, ok := sp.symbolToShard[bar.Symbol.String()]
	if !ok {
		// Unregistered symbol — drop (parity with single-pipeline case which
		// also has no handler for an unknown symbol).
		return nil
	}
	return sp.shards[idx].ProcessBar(ctx, evt)
}

// WaitTick is a no-op in Step 1 (Dispatch is synchronous). Step 4 will use
// it to barrier-sync worker goroutines at the end of a replay tick before
// the coordinator advances to exit-rule evaluation. Exposing the method
// now lets omo-replay's main loop commit to the final call shape in Step 2
// without another round of wiring changes when Step 4 lands.
func (sp *ShardedPipeline) WaitTick() {}

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

