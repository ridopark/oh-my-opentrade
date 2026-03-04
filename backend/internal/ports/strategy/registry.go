package strategy

import (
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// Registry provides lookup for builtin strategy implementations.
// Builtin strategies are compiled into the binary (e.g., ORBStrategy).
// User-defined strategies loaded via Yaegi or WASM use a different path.
type Registry interface {
	// Register adds a builtin strategy implementation.
	// Returns ErrStrategyExists if already registered.
	Register(strategy domstrategy.Strategy) error

	// Get returns a builtin strategy by its ID.
	// Returns ErrStrategyNotFound if not registered.
	Get(id domstrategy.StrategyID) (domstrategy.Strategy, error)

	// List returns all registered builtin strategy IDs.
	List() []domstrategy.StrategyID
}
