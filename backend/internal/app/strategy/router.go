package strategy

import (
	"sort"
	"sync"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// Router maintains the mapping from symbols to strategy instances and
// handles conflict resolution when multiple instances target the same symbol.
type Router struct {
	mu        sync.RWMutex
	instances map[start.InstanceID]*Instance
	// symbolMap: symbol → sorted list of instance IDs (by priority descending)
	symbolMap map[string][]start.InstanceID
}

// NewRouter creates an empty Router.
func NewRouter() *Router {
	return &Router{
		instances: make(map[start.InstanceID]*Instance),
		symbolMap: make(map[string][]start.InstanceID),
	}
}

// Register adds a strategy instance and updates the symbol routing table.
func (r *Router) Register(inst *Instance) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.instances[inst.ID()] = inst

	for _, sym := range inst.Assignment().Symbols {
		ids := r.symbolMap[sym]
		// Avoid duplicates.
		found := false
		for _, id := range ids {
			if id == inst.ID() {
				found = true
				break
			}
		}
		if !found {
			ids = append(ids, inst.ID())
		}
		// Sort by priority descending (highest priority first).
		sort.Slice(ids, func(i, j int) bool {
			a := r.instances[ids[i]]
			b := r.instances[ids[j]]
			return a.Assignment().Priority > b.Assignment().Priority
		})
		r.symbolMap[sym] = ids
	}
}

// Unregister removes a strategy instance from the routing table.
func (r *Router) Unregister(id start.InstanceID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	inst, ok := r.instances[id]
	if !ok {
		return
	}

	for _, sym := range inst.Assignment().Symbols {
		ids := r.symbolMap[sym]
		for i, iid := range ids {
			if iid == id {
				r.symbolMap[sym] = append(ids[:i], ids[i+1:]...)
				break
			}
		}
		if len(r.symbolMap[sym]) == 0 {
			delete(r.symbolMap, sym)
		}
	}

	delete(r.instances, id)
}

// Replace atomically swaps an old instance for a new one in the routing table.
// Both operations happen under a single lock so no bar can be processed between
// unregister and register. Returns the old instance (or nil if not found).
func (r *Router) Replace(oldID start.InstanceID, newInst *Instance) *Instance {
	r.mu.Lock()
	defer r.mu.Unlock()

	old, hadOld := r.instances[oldID]

	if hadOld {
		for _, sym := range old.Assignment().Symbols {
			ids := r.symbolMap[sym]
			for i, iid := range ids {
				if iid == oldID {
					r.symbolMap[sym] = append(ids[:i], ids[i+1:]...)
					break
				}
			}
			if len(r.symbolMap[sym]) == 0 {
				delete(r.symbolMap, sym)
			}
		}
		delete(r.instances, oldID)
	}

	r.instances[newInst.ID()] = newInst
	for _, sym := range newInst.Assignment().Symbols {
		ids := r.symbolMap[sym]
		found := false
		for _, id := range ids {
			if id == newInst.ID() {
				found = true
				break
			}
		}
		if !found {
			ids = append(ids, newInst.ID())
		}
		sort.Slice(ids, func(i, j int) bool {
			a := r.instances[ids[i]]
			b := r.instances[ids[j]]
			return a.Assignment().Priority > b.Assignment().Priority
		})
		r.symbolMap[sym] = ids
	}

	return old
}

// AddSymbol attaches an additional symbol to an already-registered
// instance's routing entry. Used by sentinel-rooted dynamic-watchlist
// strategies (e.g. tradingthetrend_v1) that grow their per-shard symbol
// set as signals arrive — the bootstrap registers the instance under a
// sentinel symbol like "__tradingthetrend__", and on the first signal
// for ticker T the runner calls AddSymbol(instID, T) so subsequent bars
// for T dispatch to the same instance. Idempotent: repeat calls are
// no-ops. Returns false if the instance is not registered.
func (r *Router) AddSymbol(id start.InstanceID, symbol string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	inst, ok := r.instances[id]
	if !ok {
		return false
	}
	ids := r.symbolMap[symbol]
	for _, existing := range ids {
		if existing == id {
			return true // already routed
		}
	}
	ids = append(ids, id)
	sort.Slice(ids, func(i, j int) bool {
		a := r.instances[ids[i]]
		b := r.instances[ids[j]]
		return a.Assignment().Priority > b.Assignment().Priority
	})
	r.symbolMap[symbol] = ids
	_ = inst
	return true
}

// InstancesForSymbol returns active instances assigned to the given symbol,
// sorted by priority (highest first).
func (r *Router) InstancesForSymbol(symbol string) []*Instance {
	return r.InstancesForSymbolInto(symbol, nil)
}

// InstancesForSymbolInto is the scratch-buffer variant of InstancesForSymbol.
// Callers pass a reusable slice (usually dst[:0] of a previously returned
// slice) and receive it back populated. Used by the hot-path bar handler to
// avoid one slice allocation per bar.
func (r *Router) InstancesForSymbolInto(symbol string, dst []*Instance) []*Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids, ok := r.symbolMap[symbol]
	if !ok {
		return dst[:0]
	}

	if cap(dst) < len(ids) {
		dst = make([]*Instance, 0, len(ids))
	} else {
		dst = dst[:0]
	}
	for _, id := range ids {
		inst := r.instances[id]
		if inst != nil && inst.IsActive() {
			dst = append(dst, inst)
		}
	}
	return dst
}

// AllInstances returns all registered instances.
func (r *Router) AllInstances() []*Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Instance, 0, len(r.instances))
	for _, inst := range r.instances {
		result = append(result, inst)
	}
	return result
}

// Instance returns a specific instance by ID.
func (r *Router) Instance(id start.InstanceID) (*Instance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	inst, ok := r.instances[id]
	return inst, ok
}

// SymbolsForInstance returns every symbol that routes to the given instance,
// including both the instance's declared Assignment().Symbols and any symbols
// added via AddSymbol (sentinel-rooted dynamic-watchlist case). Sentinel
// routing keys (shaped __name__) are excluded — callers that need HTF
// callbacks want real tradable tickers, not routing placeholders.
func (r *Router) SymbolsForInstance(id start.InstanceID) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []string
	for sym, ids := range r.symbolMap {
		if len(sym) >= 4 && sym[:2] == "__" && sym[len(sym)-2:] == "__" {
			continue
		}
		for _, iid := range ids {
			if iid == id {
				out = append(out, sym)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// Symbols returns all symbols that have at least one instance assigned.
func (r *Router) Symbols() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.symbolMap))
	for sym := range r.symbolMap {
		result = append(result, sym)
	}
	sort.Strings(result)
	return result
}
