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
	Screening ScreeningConfig
	Params    map[string]any
	// SymbolOverrides contains per-symbol param overrides merged on top of Params/ExitRules.
	SymbolOverrides map[string]SymbolOverride // keyed by symbol (e.g. "NVDA")
	Hooks           map[string]HookRef
	ExitRules       []domain.ExitRule
	Options         *domain.OptionsConfig
}

// SymbolOverride holds per-symbol parameter and exit rule overrides.
type SymbolOverride struct {
	Params         map[string]any     // overrides for Spec.Params
	ExitRuleParams map[string]float64 // overrides for exit rule params, keyed by param key or "RULE_TYPE.param"
}

// ParamsForSymbol returns strategy params merged with symbol-level overrides.
// If no override exists, a copy of the default params is returned.
func (s Spec) ParamsForSymbol(symbol string) map[string]any {
	merged := make(map[string]any, len(s.Params))
	for k, v := range s.Params {
		merged[k] = v
	}
	override, ok := s.SymbolOverrides[symbol]
	if !ok || len(override.Params) == 0 {
		return merged
	}
	for k, v := range override.Params {
		merged[k] = v
	}
	return merged
}

// ExitRulesForSymbol returns exit rules merged with symbol-level overrides.
// If no override exists, a deep-copied default rule set is returned.
func (s Spec) ExitRulesForSymbol(symbol string) []domain.ExitRule {
	cloned := cloneExitRules(s.ExitRules)
	override, ok := s.SymbolOverrides[symbol]
	if !ok {
		return cloned
	}

	for i := range cloned {
		rule := &cloned[i]
		for paramKey := range rule.Params {
			if v, ok := parseOverrideFloat(override.Params[paramKey]); ok {
				rule.Params[paramKey] = v
			}
			if v, ok := override.ExitRuleParams[paramKey]; ok {
				rule.Params[paramKey] = v
			}
			typedKey := rule.Type.String() + "." + paramKey
			if v, ok := parseOverrideFloat(override.Params[typedKey]); ok {
				rule.Params[paramKey] = v
			}
			if v, ok := override.ExitRuleParams[typedKey]; ok {
				rule.Params[paramKey] = v
			}
		}
	}

	return cloned
}

func cloneExitRules(src []domain.ExitRule) []domain.ExitRule {
	if len(src) == 0 {
		return nil
	}
	out := make([]domain.ExitRule, len(src))
	for i, r := range src {
		params := make(map[string]float64, len(r.Params))
		for k, v := range r.Params {
			params[k] = v
		}
		out[i] = domain.ExitRule{Type: r.Type, Params: params}
	}
	return out
}

func parseOverrideFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int8:
		return float64(t), true
	case int16:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint8:
		return float64(t), true
	case uint16:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	default:
		return 0, false
	}
}

type ScreeningConfig struct {
	Description string // Level 2: what kind of setup this strategy looks for (sent to LLM)
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
