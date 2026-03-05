package strategy

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
)

type SpecLoader struct{}

func NewSpecLoader() *SpecLoader { return &SpecLoader{} }

func (l *SpecLoader) LoadFile(path string) (*portstrategy.Spec, error) {
	s, err := LoadSpecFile(path)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func LoadSpecFile(path string) (portstrategy.Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: read %q: %w", path, err)
	}

	var hdr struct {
		SchemaVersion *int `toml:"schema_version"`
	}
	if _, err := toml.Decode(string(data), &hdr); err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: parse TOML header %q: %w", path, err)
	}

	schema := 1
	if hdr.SchemaVersion != nil {
		schema = *hdr.SchemaVersion
	}

	switch schema {
	case 1:
		return loadV1(string(data), path)
	case 2:
		return loadV2(string(data), path)
	default:
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: unsupported schema_version=%d in %q", schema, path)
	}
}

type rawHookRef struct {
	Engine     string `toml:"engine"`
	Name       string `toml:"name"`
	Entrypoint string `toml:"entrypoint"`
	Source     string `toml:"source"`
}

func (r rawHookRef) toHookRef() (portstrategy.HookRef, error) {
	if strings.TrimSpace(r.Engine) == "" {
		return portstrategy.HookRef{}, errors.New("hook engine is required")
	}
	eng, err := domstrategy.NewHookEngine(r.Engine)
	if err != nil {
		return portstrategy.HookRef{}, err
	}
	return portstrategy.HookRef{
		Engine:     eng,
		Name:       r.Name,
		Entrypoint: r.Entrypoint,
		Source:     r.Source,
	}, nil
}

type rawStrategySection struct {
	ID          string `toml:"id"`
	Version     any    `toml:"version"`
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Author      string `toml:"author"`
}

type rawLifecycleSection struct {
	State     *string `toml:"state"`
	PaperOnly *bool   `toml:"paper_only"`
}

type rawRoutingSection struct {
	Symbols            []string `toml:"symbols"`
	Timeframes         []string `toml:"timeframes"`
	Priority           *int     `toml:"priority"`
	ConflictPolicy     *string  `toml:"conflict_policy"`
	ExclusivePerSymbol *bool    `toml:"exclusive_per_symbol"`
	WatchlistMode      *string  `toml:"watchlist_mode"`
}

func loadV1(content, path string) (portstrategy.Spec, error) {
	var raw struct {
		Strategy     rawStrategySection `toml:"strategy"`
		Parameters   map[string]any     `toml:"parameters"`
		Params       map[string]any     `toml:"params"`
		RegimeFilter struct {
			AllowedRegimes    []string `toml:"allowed_regimes"`
			MinRegimeStrength any      `toml:"min_regime_strength"`
		} `toml:"regime_filter"`
	}
	if _, err := toml.Decode(content, &raw); err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: parse v1 TOML %q: %w", path, err)
	}

	if strings.TrimSpace(raw.Strategy.ID) == "" {
		return portstrategy.Spec{}, errors.New("strategy spec: missing required field strategy.id")
	}
	id, err := domstrategy.NewStrategyID(raw.Strategy.ID)
	if err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: invalid strategy.id: %w", err)
	}
	ver, err := parseVersion(raw.Strategy.Version)
	if err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: invalid strategy.version: %w", err)
	}

	params := make(map[string]any)
	mergeInto(params, raw.Parameters)
	mergeInto(params, raw.Params)
	if raw.RegimeFilter.AllowedRegimes != nil {
		params["regime_filter.allowed_regimes"] = raw.RegimeFilter.AllowedRegimes
	}
	if raw.RegimeFilter.MinRegimeStrength != nil {
		params["regime_filter.min_regime_strength"] = raw.RegimeFilter.MinRegimeStrength
	}

	conflict, _ := domstrategy.NewConflictPolicy(domstrategy.ConflictPriorityWins.String())
	state := domstrategy.LifecycleLiveActive

	return portstrategy.Spec{
		SchemaVersion: 1,
		ID:            id,
		Version:       ver,
		Name:          raw.Strategy.Name,
		Description:   raw.Strategy.Description,
		Author:        raw.Strategy.Author,
		Lifecycle: portstrategy.LifecycleConfig{
			State:     state,
			PaperOnly: false,
		},
		Routing: portstrategy.RoutingConfig{
			Symbols:            nil,
			Timeframes:         []string{"1m"},
			Priority:           100,
			ConflictPolicy:     conflict,
			ExclusivePerSymbol: false,
			WatchlistMode:      "intersection",
		},
		Params: params,
		Hooks:  map[string]portstrategy.HookRef{},
	}, nil
}

func loadV2(content, path string) (portstrategy.Spec, error) {
	var raw struct {
		SchemaVersion int                `toml:"schema_version"`
		Strategy      rawStrategySection `toml:"strategy"`
		Lifecycle     rawLifecycleSection
		Routing       rawRoutingSection
		Params        map[string]any        `toml:"params"`
		Parameters    map[string]any        `toml:"parameters"` // allow old key
		RegimeFilter  map[string]any        `toml:"regime_filter"`
		Hooks         map[string]rawHookRef `toml:"hooks"`
	}

	if _, err := toml.Decode(content, &raw); err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: parse v2 TOML %q: %w", path, err)
	}

	if raw.SchemaVersion != 2 {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: expected schema_version=2, got %d", raw.SchemaVersion)
	}
	if strings.TrimSpace(raw.Strategy.ID) == "" {
		return portstrategy.Spec{}, errors.New("strategy spec: missing required field strategy.id")
	}

	id, err := domstrategy.NewStrategyID(raw.Strategy.ID)
	if err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: invalid strategy.id: %w", err)
	}
	ver, err := parseVersion(raw.Strategy.Version)
	if err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: invalid strategy.version: %w", err)
	}

	stateStr := domstrategy.LifecycleLiveActive.String()
	if raw.Lifecycle.State != nil {
		stateStr = *raw.Lifecycle.State
	}
	state, err := domstrategy.NewLifecycleState(stateStr)
	if err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: invalid lifecycle.state: %w", err)
	}
	paperOnly := false
	if raw.Lifecycle.PaperOnly != nil {
		paperOnly = *raw.Lifecycle.PaperOnly
	}

	timeframes := raw.Routing.Timeframes
	if len(timeframes) == 0 {
		timeframes = []string{"1m"}
	}
	priority := 100
	if raw.Routing.Priority != nil {
		priority = *raw.Routing.Priority
	}
	conflictStr := domstrategy.ConflictPriorityWins.String()
	if raw.Routing.ConflictPolicy != nil && strings.TrimSpace(*raw.Routing.ConflictPolicy) != "" {
		conflictStr = *raw.Routing.ConflictPolicy
	}
	conflict, err := domstrategy.NewConflictPolicy(conflictStr)
	if err != nil {
		return portstrategy.Spec{}, fmt.Errorf("strategy spec: invalid routing.conflict_policy: %w", err)
	}
	exclusive := false
	if raw.Routing.ExclusivePerSymbol != nil {
		exclusive = *raw.Routing.ExclusivePerSymbol
	}

	watchlistMode := "intersection"
	if raw.Routing.WatchlistMode != nil && strings.TrimSpace(*raw.Routing.WatchlistMode) != "" {
		watchlistMode = *raw.Routing.WatchlistMode
	}

	params := make(map[string]any)
	mergeInto(params, raw.Parameters)
	mergeInto(params, raw.Params)
	for k, v := range raw.RegimeFilter {
		params["regime_filter."+k] = v
	}

	hooks := make(map[string]portstrategy.HookRef)
	for name, href := range raw.Hooks {
		ref, err := href.toHookRef()
		if err != nil {
			return portstrategy.Spec{}, fmt.Errorf("strategy spec: invalid hooks.%s: %w", name, err)
		}
		hooks[name] = ref
	}

	return portstrategy.Spec{
		SchemaVersion: 2,
		ID:            id,
		Version:       ver,
		Name:          raw.Strategy.Name,
		Description:   raw.Strategy.Description,
		Author:        raw.Strategy.Author,
		Lifecycle: portstrategy.LifecycleConfig{
			State:     state,
			PaperOnly: paperOnly,
		},
		Routing: portstrategy.RoutingConfig{
			Symbols:            raw.Routing.Symbols,
			Timeframes:         timeframes,
			Priority:           priority,
			ConflictPolicy:     conflict,
			ExclusivePerSymbol: exclusive,
			WatchlistMode:      watchlistMode,
		},
		Params: params,
		Hooks:  hooks,
	}, nil
}

func mergeInto(dst, src map[string]any) {
	if len(src) == 0 {
		return
	}
	for k, v := range src {
		dst[k] = v
	}
}

func parseVersion(v any) (domstrategy.Version, error) {
	if v == nil {
		return "", errors.New("version is required")
	}

	switch t := v.(type) {
	case string:
		return domstrategy.NewVersion(t)
	case int:
		return domstrategy.NewVersion(fmt.Sprintf("%d.0.0", t))
	case int64:
		return domstrategy.NewVersion(fmt.Sprintf("%d.0.0", t))
	case uint64:
		return domstrategy.NewVersion(fmt.Sprintf("%d.0.0", t))
	case float64:
		if t == float64(int64(t)) {
			return domstrategy.NewVersion(fmt.Sprintf("%d.0.0", int64(t)))
		}
		return "", fmt.Errorf("version must be semver string or integer major, got %v", t)
	default:
		rv := reflect.ValueOf(v)
		if rv.IsValid() {
			switch rv.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				return domstrategy.NewVersion(fmt.Sprintf("%d.0.0", rv.Int()))
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				return domstrategy.NewVersion(fmt.Sprintf("%d.0.0", rv.Uint()))
			}
		}
		return "", fmt.Errorf("unsupported version type %T", v)
	}
}
