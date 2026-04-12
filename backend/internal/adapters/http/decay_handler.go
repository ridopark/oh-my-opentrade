package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// DecayQuerier abstracts the decay telemetry data access.
type DecayQuerier interface {
	GetRollingDecayStats(ctx context.Context, strategy string) ([]domain.RollingDecayPoint, error)
	GetComponentAttribution(ctx context.Context, strategy string) ([]domain.ComponentAttribution, error)
}

// DecayHandler serves the /api/decay/ endpoints.
type DecayHandler struct {
	querier DecayQuerier
	log     zerolog.Logger
}

// NewDecayHandler creates a new DecayHandler.
func NewDecayHandler(querier DecayQuerier, log zerolog.Logger) *DecayHandler {
	return &DecayHandler{querier: querier, log: log}
}

func (h *DecayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/decay/")
	switch path {
	case "rolling":
		h.handleRolling(w, r)
	case "attribution":
		h.handleAttribution(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *DecayHandler) handleRolling(w http.ResponseWriter, r *http.Request) {
	strategy := r.URL.Query().Get("strategy")
	if strategy == "" {
		http.Error(w, `{"error":"strategy parameter required"}`, http.StatusBadRequest)
		return
	}

	points, err := h.querier.GetRollingDecayStats(r.Context(), strategy)
	if err != nil {
		h.log.Error().Err(err).Str("strategy", strategy).Msg("decay: rolling query failed")
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	if points == nil {
		points = []domain.RollingDecayPoint{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(points)
}

func (h *DecayHandler) handleAttribution(w http.ResponseWriter, r *http.Request) {
	strategy := r.URL.Query().Get("strategy")
	if strategy == "" {
		http.Error(w, `{"error":"strategy parameter required"}`, http.StatusBadRequest)
		return
	}

	results, err := h.querier.GetComponentAttribution(r.Context(), strategy)
	if err != nil {
		h.log.Error().Err(err).Str("strategy", strategy).Msg("decay: attribution query failed")
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []domain.ComponentAttribution{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}
