package http

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// DecayHandler serves the decay telemetry API endpoints.
//
//	GET /api/decay/rolling?strategy=macd_only_v1     → []RollingDecayPoint
//	GET /api/decay/attribution?strategy=macd_only_v1 → []ComponentAttribution
type DecayHandler struct {
	repo ports.DecayTelemetryPort
	log  zerolog.Logger
}

// NewDecayHandler creates a new DecayHandler.
func NewDecayHandler(repo ports.DecayTelemetryPort, log zerolog.Logger) *DecayHandler {
	return &DecayHandler{repo: repo, log: log}
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

	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/rolling"):
		h.serveRolling(w, r)
	case strings.HasSuffix(path, "/attribution"):
		h.serveAttribution(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *DecayHandler) serveRolling(w http.ResponseWriter, r *http.Request) {
	strategy := r.URL.Query().Get("strategy")
	if strategy == "" {
		h.jsonError(w, http.StatusBadRequest, "strategy query parameter is required")
		return
	}

	points, err := h.repo.GetRollingDecayStats(r.Context(), strategy)
	if err != nil {
		h.log.Error().Err(err).Str("strategy", strategy).Msg("failed to get rolling decay stats")
		h.jsonError(w, http.StatusInternalServerError, "rolling decay query failed")
		return
	}

	if points == nil {
		points = []domain.RollingDecayPoint{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(points); err != nil {
		h.log.Error().Err(err).Msg("failed to encode rolling decay response")
	}
}

func (h *DecayHandler) serveAttribution(w http.ResponseWriter, r *http.Request) {
	strategy := r.URL.Query().Get("strategy")
	if strategy == "" {
		h.jsonError(w, http.StatusBadRequest, "strategy query parameter is required")
		return
	}

	attrs, err := h.repo.GetComponentAttribution(r.Context(), strategy)
	if err != nil {
		h.log.Error().Err(err).Str("strategy", strategy).Msg("failed to get component attribution")
		h.jsonError(w, http.StatusInternalServerError, "component attribution query failed")
		return
	}

	if attrs == nil {
		attrs = []domain.ComponentAttribution{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(attrs); err != nil {
		h.log.Error().Err(err).Msg("failed to encode component attribution response")
	}
}

func (h *DecayHandler) jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		h.log.Error().Err(err).Msg("failed to encode error response")
	}
}
