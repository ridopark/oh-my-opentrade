package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

type ConfigHandler struct {
	specStore   portstrategy.SpecStore
	strategyDir string
	log         zerolog.Logger
}

func NewConfigHandler(specStore portstrategy.SpecStore, strategyDir string, log zerolog.Logger) *ConfigHandler {
	return &ConfigHandler{
		specStore:   specStore,
		strategyDir: strategyDir,
		log:         log.With().Str("component", "config_http").Logger(),
	}
}

func (h *ConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/strategies/config/")
	path = strings.TrimSuffix(path, "/")
	parts := strings.SplitN(path, "/", 2)

	if len(parts) < 2 || parts[1] != "config" || parts[0] == "" {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}

	strategyID := parts[0]

	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r, strategyID)
	case http.MethodPut:
		h.handlePut(w, r, strategyID)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type configResponse struct {
	Strategy        configStrategy                         `json:"strategy"`
	Lifecycle       configLifecycle                        `json:"lifecycle"`
	Routing         configRouting                          `json:"routing"`
	Params          map[string]any                         `json:"params"`
	ParamSchema     []domstrategy.ParamMeta                `json:"param_schema"`
	ExitRules       []configExitRule                       `json:"exit_rules"`
	SymbolOverrides map[string]portstrategy.SymbolOverride `json:"symbol_overrides"`
}

type configStrategy struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
}

type configLifecycle struct {
	State     string `json:"state"`
	PaperOnly bool   `json:"paper_only"`
}

type configRouting struct {
	Symbols            []string `json:"symbols"`
	Timeframes         []string `json:"timeframes"`
	AssetClasses       []string `json:"asset_classes"`
	AllowedDirections  []string `json:"allowed_directions"`
	Priority           int      `json:"priority"`
	ConflictPolicy     string   `json:"conflict_policy"`
	ExclusivePerSymbol bool     `json:"exclusive_per_symbol"`
	WatchlistMode      string   `json:"watchlist_mode"`
}

type configExitRule struct {
	Type   string             `json:"type"`
	Params map[string]float64 `json:"params"`
}

func (h *ConfigHandler) handleGet(w http.ResponseWriter, _ *http.Request, strategyID string) {
	id, err := domstrategy.NewStrategyID(strategyID)
	if err != nil {
		jsonError(w, "invalid strategy id: "+err.Error(), http.StatusBadRequest)
		return
	}

	spec, err := h.specStore.GetLatest(context.Background(), id)
	if err != nil {
		h.log.Warn().Err(err).Str("strategy_id", strategyID).Msg("strategy not found")
		jsonError(w, "strategy not found", http.StatusNotFound)
		return
	}

	exitRules := make([]configExitRule, len(spec.ExitRules))
	for i, er := range spec.ExitRules {
		exitRules[i] = configExitRule{
			Type:   er.Type.String(),
			Params: er.Params,
		}
	}

	symOverrides := spec.SymbolOverrides
	if symOverrides == nil {
		symOverrides = make(map[string]portstrategy.SymbolOverride)
	}

	resp := configResponse{
		Strategy: configStrategy{
			ID:          spec.ID.String(),
			Version:     spec.Version.String(),
			Name:        spec.Name,
			Description: spec.Description,
			Author:      spec.Author,
		},
		Lifecycle: configLifecycle{
			State:     spec.Lifecycle.State.String(),
			PaperOnly: spec.Lifecycle.PaperOnly,
		},
		Routing: configRouting{
			Symbols:            spec.Routing.Symbols,
			Timeframes:         spec.Routing.Timeframes,
			AssetClasses:       spec.Routing.AssetClasses,
			AllowedDirections:  spec.Routing.AllowedDirections,
			Priority:           spec.Routing.Priority,
			ConflictPolicy:     spec.Routing.ConflictPolicy.String(),
			ExclusivePerSymbol: spec.Routing.ExclusivePerSymbol,
			WatchlistMode:      spec.Routing.WatchlistMode,
		},
		Params:          spec.Params,
		ParamSchema:     domstrategy.InferParamSchema(spec.Params, nil),
		ExitRules:       exitRules,
		SymbolOverrides: symOverrides,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type configUpdateRequest struct {
	Strategy struct {
		ID          string `json:"id"`
		Version     string `json:"version"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Author      string `json:"author"`
	} `json:"strategy"`
	Lifecycle struct {
		State     string `json:"state"`
		PaperOnly bool   `json:"paper_only"`
	} `json:"lifecycle"`
	Routing struct {
		Symbols            []string `json:"symbols"`
		Timeframes         []string `json:"timeframes"`
		AssetClasses       []string `json:"asset_classes"`
		AllowedDirections  []string `json:"allowed_directions"`
		Priority           int      `json:"priority"`
		ConflictPolicy     string   `json:"conflict_policy"`
		ExclusivePerSymbol bool     `json:"exclusive_per_symbol"`
		WatchlistMode      string   `json:"watchlist_mode"`
	} `json:"routing"`
	Params          map[string]any            `json:"params"`
	ExitRules       []configExitRule          `json:"exit_rules"`
	SymbolOverrides map[string]map[string]any `json:"symbol_overrides"`
	RegimeFilter    map[string]any            `json:"regime_filter"`
	DynamicRisk     map[string]any            `json:"dynamic_risk"`
	Screening       struct {
		Description string `json:"description"`
	} `json:"screening"`
}

func (h *ConfigHandler) handlePut(w http.ResponseWriter, r *http.Request, strategyID string) {
	var req configUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Strategy.ID != strategyID {
		jsonError(w, fmt.Sprintf("strategy id mismatch: URL=%q body=%q", strategyID, req.Strategy.ID), http.StatusBadRequest)
		return
	}

	id, err := domstrategy.NewStrategyID(req.Strategy.ID)
	if err != nil {
		jsonError(w, "invalid strategy id: "+err.Error(), http.StatusBadRequest)
		return
	}
	ver, err := domstrategy.NewVersion(req.Strategy.Version)
	if err != nil {
		jsonError(w, "invalid version: "+err.Error(), http.StatusBadRequest)
		return
	}
	state, err := domstrategy.NewLifecycleState(req.Lifecycle.State)
	if err != nil {
		jsonError(w, "invalid lifecycle state: "+err.Error(), http.StatusBadRequest)
		return
	}
	conflict, err := domstrategy.NewConflictPolicy(req.Routing.ConflictPolicy)
	if err != nil {
		jsonError(w, "invalid conflict policy: "+err.Error(), http.StatusBadRequest)
		return
	}

	params := make(map[string]any)
	for k, v := range req.Params {
		params[k] = v
	}
	for k, v := range req.RegimeFilter {
		params["regime_filter."+k] = v
	}
	for k, v := range req.DynamicRisk {
		params["dynamic_risk."+k] = v
	}

	exitRules := make([]domain.ExitRule, len(req.ExitRules))
	for i, er := range req.ExitRules {
		rt, rtErr := domain.NewExitRuleType(er.Type)
		if rtErr != nil {
			jsonError(w, fmt.Sprintf("invalid exit_rules[%d].type: %s", i, rtErr), http.StatusBadRequest)
			return
		}
		rule, ruleErr := domain.NewExitRule(rt, er.Params)
		if ruleErr != nil {
			jsonError(w, fmt.Sprintf("invalid exit_rules[%d]: %s", i, ruleErr), http.StatusBadRequest)
			return
		}
		exitRules[i] = rule
	}

	if err := domain.ValidateExitRules(exitRules); err != nil {
		jsonError(w, "exit rules validation failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	symOverrides := make(map[string]portstrategy.SymbolOverride, len(req.SymbolOverrides))
	for sym, ov := range req.SymbolOverrides {
		symOverrides[sym] = portstrategy.SymbolOverride{
			Params:         ov,
			ExitRuleParams: make(map[string]float64),
		}
	}

	spec := portstrategy.Spec{
		SchemaVersion: 2,
		ID:            id,
		Version:       ver,
		Name:          req.Strategy.Name,
		Description:   req.Strategy.Description,
		Author:        req.Strategy.Author,
		Lifecycle: portstrategy.LifecycleConfig{
			State:     state,
			PaperOnly: req.Lifecycle.PaperOnly,
		},
		Routing: portstrategy.RoutingConfig{
			Symbols:            req.Routing.Symbols,
			Timeframes:         req.Routing.Timeframes,
			AssetClasses:       req.Routing.AssetClasses,
			AllowedDirections:  req.Routing.AllowedDirections,
			Priority:           req.Routing.Priority,
			ConflictPolicy:     conflict,
			ExclusivePerSymbol: req.Routing.ExclusivePerSymbol,
			WatchlistMode:      req.Routing.WatchlistMode,
		},
		Screening: portstrategy.ScreeningConfig{
			Description: req.Screening.Description,
		},
		Params:          params,
		Hooks:           make(map[string]portstrategy.HookRef),
		ExitRules:       exitRules,
		SymbolOverrides: symOverrides,
	}

	tomlBytes, err := store_fs.EncodeFullV2(spec)
	if err != nil {
		jsonError(w, "failed to encode TOML: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tomlPath := filepath.Join(h.strategyDir, strategyID+".toml")
	if _, testErr := strategy.LoadSpecFile(tomlPath); testErr == nil {
		bakPath := tomlPath + ".bak"
		if origData, readErr := os.ReadFile(tomlPath); readErr == nil {
			_ = os.WriteFile(bakPath, origData, 0o644)
		}
	}

	tmpPath := tomlPath + ".tmp"
	if err := os.WriteFile(tmpPath, tomlBytes, 0o644); err != nil {
		jsonError(w, "failed to write config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if _, err := strategy.LoadSpecFile(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		jsonError(w, "validation failed — config not saved: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	if err := os.Rename(tmpPath, tomlPath); err != nil {
		_ = os.Remove(tmpPath)
		jsonError(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info().Str("strategy_id", strategyID).Msg("strategy config saved")

	h.handleGet(w, nil, strategyID)
}
