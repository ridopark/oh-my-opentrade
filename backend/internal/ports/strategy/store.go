package strategy

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// SpecStore defines the persistence interface for strategy specifications.
// Implementations may be filesystem-based (TOML files), database-backed, etc.
type SpecStore interface {
	// List returns all known strategy specs, optionally filtered by lifecycle state.
	List(ctx context.Context, filter *SpecFilter) ([]Spec, error)

	// Get returns a specific strategy spec by ID and version.
	// Returns ErrStrategyNotFound if not found.
	Get(ctx context.Context, id domstrategy.StrategyID, version domstrategy.Version) (*Spec, error)

	// GetLatest returns the latest version of a strategy spec by ID.
	// Returns ErrStrategyNotFound if not found.
	GetLatest(ctx context.Context, id domstrategy.StrategyID) (*Spec, error)

	// Save persists a strategy spec. Creates or updates.
	Save(ctx context.Context, spec Spec) error

	// Watch returns a channel that emits spec IDs whenever a spec file changes.
	// Implementations should detect filesystem changes or database updates.
	// The channel is closed when the context is canceled.
	Watch(ctx context.Context) (<-chan domstrategy.StrategyID, error)
}

// SpecFilter constrains which specs are returned by List.
type SpecFilter struct {
	LifecycleState *domstrategy.LifecycleState
	Author         string
}

// Spec represents the full specification of a strategy version, loaded from TOML.
// This is the "StrategySpec" concept from the architecture plan — metadata + params + routing + hooks.
type Spec struct {
	SchemaVersion int
	ID            domstrategy.StrategyID
	Version       domstrategy.Version
	Name          string
	Description   string
	Author        string

	Lifecycle LifecycleConfig
	Routing   RoutingConfig
	Params    map[string]any
	Hooks     map[string]HookRef
	ExitRules []domain.ExitRule
}

// LifecycleConfig holds lifecycle-related settings from the TOML spec.
type LifecycleConfig struct {
	State     domstrategy.LifecycleState
	PaperOnly bool
}

// RoutingConfig holds symbol/timeframe routing from the TOML spec.
type RoutingConfig struct {
	Symbols            []string
	Timeframes         []string
	AssetClasses       []string
	AllowedDirections  []string
	Priority           int
	ConflictPolicy     domstrategy.ConflictPolicy
	ExclusivePerSymbol bool
	WatchlistMode      string // "static"|"replace"|"intersection"|"union" — default "intersection"
}

// HookRef identifies a hook implementation.
type HookRef struct {
	Engine     domstrategy.HookEngine
	Name       string // for builtin hooks
	Entrypoint string // for yaegi hooks
	Source     string // file path for yaegi scripts
}
