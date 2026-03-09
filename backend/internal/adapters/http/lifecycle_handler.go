package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/rs/zerolog"
)

type LifecycleHandler struct {
	svc *strategy.LifecycleService
	log zerolog.Logger
}

func NewLifecycleHandler(svc *strategy.LifecycleService, log zerolog.Logger) *LifecycleHandler {
	return &LifecycleHandler{svc: svc, log: log}
}

func (h *LifecycleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "strategies" || parts[1] != "v2" {
		h.jsonError(w, http.StatusNotFound, "not found")
		return
	}

	if r.Method == http.MethodGet && len(parts) == 3 && parts[2] == "instances" {
		h.handleListInstances(w)
		return
	}

	if r.Method == http.MethodPost && len(parts) == 5 && parts[2] == "instances" {
		instanceID, err := start.NewInstanceID(parts[3])
		if err != nil {
			h.jsonError(w, http.StatusBadRequest, "invalid instance id")
			return
		}

		switch parts[4] {
		case "promote":
			h.handlePromote(w, r, instanceID)
			return
		case "deactivate":
			h.handleDeactivate(w, instanceID)
			return
		case "archive":
			h.handleArchive(w, instanceID)
			return
		default:
			h.jsonError(w, http.StatusNotFound, "not found")
			return
		}
	}

	h.jsonError(w, http.StatusNotFound, "not found")
}

func (h *LifecycleHandler) handleListInstances(w http.ResponseWriter) {
	instances := h.svc.ListInstances()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(instances); err != nil {
		h.log.Error().Err(err).Msg("failed to encode instances response")
	}
}

func (h *LifecycleHandler) handlePromote(w http.ResponseWriter, r *http.Request, instanceID start.InstanceID) {
	var req struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.Target) == "" {
		h.jsonError(w, http.StatusBadRequest, "target is required")
		return
	}

	target, err := start.NewLifecycleState(req.Target)
	if err != nil {
		h.jsonError(w, http.StatusBadRequest, "invalid target lifecycle")
		return
	}

	if err := h.svc.Promote(instanceID, target); err != nil {
		h.writeSvcErr(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *LifecycleHandler) handleDeactivate(w http.ResponseWriter, instanceID start.InstanceID) {
	if err := h.svc.Deactivate(instanceID); err != nil {
		h.writeSvcErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *LifecycleHandler) handleArchive(w http.ResponseWriter, instanceID start.InstanceID) {
	if err := h.svc.Archive(instanceID); err != nil {
		h.writeSvcErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *LifecycleHandler) writeSvcErr(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	msg := err.Error()
	if errors.Is(err, start.ErrInstanceNotFound) {
		status = http.StatusNotFound
		msg = "instance not found"
	}
	h.jsonError(w, status, msg)
}

func (h *LifecycleHandler) jsonError(w http.ResponseWriter, statusCode int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		h.log.Error().Err(err).Msg("failed to encode error response")
	}
}
