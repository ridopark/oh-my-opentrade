package strategy

import (
	"sync"

	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// MemRegistry is an in-memory implementation of the ports/strategy.Registry interface.
// It stores builtin strategy implementations keyed by their StrategyID.
type MemRegistry struct {
	mu         sync.RWMutex
	strategies map[strat.StrategyID]strat.Strategy
}

// NewMemRegistry creates a new in-memory strategy registry.
func NewMemRegistry() *MemRegistry {
	return &MemRegistry{
		strategies: make(map[strat.StrategyID]strat.Strategy),
	}
}

// Register adds a builtin strategy implementation.
// Returns ErrStrategyExists if already registered.
func (r *MemRegistry) Register(s strat.Strategy) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := s.Meta().ID
	if _, exists := r.strategies[id]; exists {
		return strat.ErrStrategyExists
	}
	r.strategies[id] = s
	return nil
}

// Get returns a builtin strategy by its ID.
// Returns ErrStrategyNotFound if not registered.
func (r *MemRegistry) Get(id strat.StrategyID) (strat.Strategy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.strategies[id]
	if !ok {
		return nil, strat.ErrStrategyNotFound
	}
	return s, nil
}

// List returns all registered builtin strategy IDs.
func (r *MemRegistry) List() []strat.StrategyID {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]strat.StrategyID, 0, len(r.strategies))
	for id := range r.strategies {
		ids = append(ids, id)
	}
	return ids
}
