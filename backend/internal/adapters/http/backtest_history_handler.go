package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

// handleHistory routes GET/POST requests under /backtest/history/*.
//
//	GET  /backtest/history           — list runs (filters via query string)
//	GET  /backtest/history/{id}      — full detail
//	POST /backtest/history/{id}/tags — replace tags array
//	POST /backtest/history/{id}/pin  — set pinned flag
func (h *BacktestHandler) handleHistory(w http.ResponseWriter, r *http.Request, rest []string) {
	if h.historyRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "backtest history disabled")
		return
	}

	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		h.handleHistoryList(w, r)
	case len(rest) == 1 && r.Method == http.MethodGet:
		h.handleHistoryGet(w, r, rest[0])
	case len(rest) == 2 && rest[1] == "tags" && r.Method == http.MethodPost:
		h.handleHistoryTags(w, r, rest[0])
	case len(rest) == 2 && rest[1] == "pin" && r.Method == http.MethodPost:
		h.handleHistoryPin(w, r, rest[0])
	default:
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}
}

func (h *BacktestHandler) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := ports.BacktestListFilter{
		Strategies: splitCSV(q.Get("strategy")),
		Symbols:    splitCSV(q.Get("symbol")),
		Tags:       splitCSV(q.Get("tags")),
		Search:     q.Get("q"),
		PinnedOnly: q.Get("pinned") == "true",
		OrderBy:    q.Get("order_by"),
		OrderDir:   q.Get("order_dir"),
	}
	if s := q.Get("from"); s != "" {
		if t, err := parseTimeParam(s); err == nil {
			filter.From = t
		}
	}
	if s := q.Get("to"); s != "" {
		if t, err := parseTimeParam(s); err == nil {
			filter.To = t
		}
	}
	if s := q.Get("min_pf"); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			filter.MinPF = f
		}
	}
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			filter.Limit = n
		}
	}
	if s := q.Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			filter.Offset = n
		}
	}

	runs, total, err := h.historyRepo.List(r.Context(), filter)
	if err != nil {
		h.log.Error().Err(err).Msg("history list failed")
		jsonError(w, http.StatusInternalServerError, "list failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"runs":  runs,
		"total": total,
	})
}

func (h *BacktestHandler) handleHistoryGet(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := h.historyRepo.Get(r.Context(), id)
	if err != nil {
		h.log.Error().Err(err).Str("id", id).Msg("history get failed")
		jsonError(w, http.StatusInternalServerError, "get failed: "+err.Error())
		return
	}
	if detail == nil {
		jsonError(w, http.StatusNotFound, "backtest run not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(detail)
}

func (h *BacktestHandler) handleHistoryTags(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := h.historyRepo.SetTags(r.Context(), id, body.Tags); err != nil {
		jsonError(w, http.StatusInternalServerError, "set tags failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *BacktestHandler) handleHistoryPin(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Pinned bool `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := h.historyRepo.SetPinned(r.Context(), id, body.Pinned); err != nil {
		jsonError(w, http.StatusInternalServerError, "set pinned failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

