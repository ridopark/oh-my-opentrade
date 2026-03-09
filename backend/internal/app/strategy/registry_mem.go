package strategy

import (
	"sync"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// MemRegistry is an in-memory implementation of the ports/strategy.Registry interface.
// It stores builtin strategy implementations keyed by their StrategyID.
type MemRegistry struct {
	mu         sync.RWMutex
	strategies map[start.StrategyID]start.Strategy
}

// NewMemRegistry creates a new in-memory strategy registry.
func NewMemRegistry() *MemRegistry {
	return &MemRegistry{
		strategies: make(map[start.StrategyID]start.Strategy),
	}
}

// Register adds a builtin strategy implementation.
// Returns ErrStrategyExists if already registered.
func (r *MemRegistry) Register(s start.Strategy) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := s.Meta().ID
	if _, exists := r.strategies[id]; exists {
		return start.ErrStrategyExists
	}
	r.strategies[id] = s
	return nil
}

// Get returns a builtin strategy by its ID.
// Returns ErrStrategyNotFound if not registered.
func (r *MemRegistry) Get(id start.StrategyID) (start.Strategy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.strategies[id]
	if !ok {
		return nil, start.ErrStrategyNotFound
	}
	return s, nil
}

// List returns all registered builtin strategy IDs.
func (r *MemRegistry) List() []start.StrategyID {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]start.StrategyID, 0, len(r.strategies))
	for id := range r.strategies {
		ids = append(ids, id)
	}
	return ids
}
