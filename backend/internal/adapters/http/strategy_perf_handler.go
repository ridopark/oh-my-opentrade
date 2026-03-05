package http

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type StrategyPerfHandler struct {
	runner  *strategy.Runner
	pnlRepo ports.PnLPort
	log     zerolog.Logger
}

func NewStrategyPerfHandler(runner *strategy.Runner, pnlRepo ports.PnLPort, log zerolog.Logger) *StrategyPerfHandler {
	return &StrategyPerfHandler{runner: runner, pnlRepo: pnlRepo, log: log}
}

func (h *StrategyPerfHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "api" || parts[1] != "strategies" {
		h.jsonError(w, http.StatusNotFound, "not found")
		return
	}

	if len(parts) == 2 || (len(parts) == 3 && parts[2] == "") {
		h.serveList(w)
		return
	}

	strategyID := parts[2]

	if len(parts) == 3 {
		h.jsonError(w, http.StatusNotFound, "not found")
		return
	}
	endpoint := parts[3]
	switch endpoint {
	case "dashboard":
		h.serveDashboard(w, r, strategyID)
		return
	case "state":
		if len(parts) == 4 {
			h.serveState(w, strategyID)
			return
		}
		if len(parts) == 5 {
			h.serveStateSymbol(w, strategyID, parts[4])
			return
		}
		h.jsonError(w, http.StatusNotFound, "not found")
		return
	case "signals":
		h.serveSignals(w, r, strategyID)
		return
	default:
		h.jsonError(w, http.StatusNotFound, "not found")
		return
	}
}

func (h *StrategyPerfHandler) serveList(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if h.runner == nil {
		_ = json.NewEncoder(w).Encode([]strategy.StrategyInfo{})
		return
	}
	if err := json.NewEncoder(w).Encode(h.runner.ListStrategies()); err != nil {
		h.log.Error().Err(err).Msg("failed to encode strategies list")
	}
}

func (h *StrategyPerfHandler) serveDashboard(w http.ResponseWriter, r *http.Request, strategyID string) {
	from, to := parseRange(r)
	ctx := r.Context()

	dash, err := h.pnlRepo.GetStrategyDashboard(ctx, "default", domain.EnvModePaper, strategyID, from, to)
	if err != nil {
		h.log.Error().Err(err).Str("strategy", strategyID).Msg("failed to get strategy dashboard")
		http.Error(w, `{"error":"strategy dashboard query failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(dash); err != nil {
		h.log.Error().Err(err).Msg("failed to encode strategy dashboard")
	}
}

func (h *StrategyPerfHandler) serveState(w http.ResponseWriter, strategyID string) {
	w.Header().Set("Content-Type", "application/json")
	if h.runner == nil {
		_ = json.NewEncoder(w).Encode([]domain.StateSnapshot{})
		return
	}
	snaps := h.runner.StrategySnapshots(strategyID)
	if snaps == nil {
		snaps = []domain.StateSnapshot{}
	}
	if err := json.NewEncoder(w).Encode(snaps); err != nil {
		h.log.Error().Err(err).Msg("failed to encode strategy state")
	}
}

func (h *StrategyPerfHandler) serveStateSymbol(w http.ResponseWriter, strategyID, symbol string) {
	if h.runner == nil {
		h.jsonError(w, http.StatusNotFound, "not found")
		return
	}
	snap, ok := h.runner.StrategySnapshot(strategyID, symbol)
	if !ok {
		h.jsonError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(snap); err != nil {
		h.log.Error().Err(err).Msg("failed to encode strategy state snapshot")
	}
}

type strategySignalsResponse struct {
	Items      []domain.StrategySignalEvent `json:"items"`
	NextCursor string                       `json:"next_cursor,omitempty"`
}

func (h *StrategyPerfHandler) serveSignals(w http.ResponseWriter, r *http.Request, strategyID string) {
	from, to := parseRange(r)
	q := r.URL.Query()

	limit := 50
	if raw := q.Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			if v > 200 {
				v = 200
			}
			limit = v
		}
	}

	query := ports.StrategySignalQuery{
		TenantID: "default",
		EnvMode:  domain.EnvModePaper,
		Strategy: strategyID,
		Symbol:   q.Get("symbol"),
		From:     from,
		To:       to,
		Limit:    limit,
	}

	if cursor := q.Get("cursor"); cursor != "" {
		raw, err := base64.URLEncoding.DecodeString(cursor)
		if err == nil {
			cparts := strings.SplitN(string(raw), "|", 2)
			if len(cparts) == 2 {
				if t, err := time.Parse(time.RFC3339Nano, cparts[0]); err == nil {
					query.CursorTime = &t
					query.CursorID = cparts[1]
				}
			}
		}
	}

	page, err := h.pnlRepo.GetStrategySignalEvents(r.Context(), query)
	if err != nil {
		h.log.Error().Err(err).Str("strategy", strategyID).Msg("failed to get strategy signals")
		http.Error(w, `{"error":"strategy signals query failed"}`, http.StatusInternalServerError)
		return
	}

	resp := strategySignalsResponse{Items: page.Items, NextCursor: page.NextCursor}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.log.Error().Err(err).Msg("failed to encode strategy signals")
	}
}

func (h *StrategyPerfHandler) jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		h.log.Error().Err(err).Msg("failed to encode error response")
	}
}
