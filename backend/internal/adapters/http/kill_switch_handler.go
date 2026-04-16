package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/rs/zerolog"
)

// KillSwitchController is the subset of DailyLossBreaker the handler needs.
// Declared here to keep the handler free of the risk package's full surface
// (and to make the handler trivially mockable in tests).
type KillSwitchController interface {
	State() risk.KillSwitchState
	SetState(s risk.KillSwitchState, reason, actor string) risk.KillSwitchState
}

// KillSwitchEventReader optionally surfaces the last persisted transition
// on the GET endpoint. The composition root adapts a repo to this signature.
type KillSwitchEventReader interface {
	LastKillSwitchEvent(ctx context.Context) (*KillSwitchEventDTO, error)
}

// KillSwitchEventDTO mirrors timescaledb.KillSwitchEventRow without the
// dependency so the http package stays adapter-agnostic. The repo adapter
// implements a thin wrapper to convert.
type KillSwitchEventDTO struct {
	At       time.Time
	OldState string
	NewState string
	Reason   string
	Actor    string
}

// KillSwitchHandler serves GET/POST /api/v1/admin/kill-switch.
type KillSwitchHandler struct {
	ctrl   KillSwitchController
	events KillSwitchEventReader // may be nil
	log    zerolog.Logger
}

func NewKillSwitchHandler(ctrl KillSwitchController, events KillSwitchEventReader, log zerolog.Logger) *KillSwitchHandler {
	return &KillSwitchHandler{ctrl: ctrl, events: events, log: log}
}

func (h *KillSwitchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodPost:
		h.handlePost(w, r)
	default:
		h.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type killSwitchPostRequest struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	Actor  string `json:"actor"`
}

type killSwitchResponse struct {
	State    string `json:"state"`
	Previous string `json:"previous,omitempty"`
	At       string `json:"at"`
}

type killSwitchGetResponse struct {
	State     string              `json:"state"`
	LastEvent *killSwitchEventDTO `json:"lastEvent,omitempty"`
}

type killSwitchEventDTO struct {
	At       string `json:"at"`
	OldState string `json:"oldState"`
	NewState string `json:"newState"`
	Reason   string `json:"reason"`
	Actor    string `json:"actor"`
}

func (h *KillSwitchHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	resp := killSwitchGetResponse{State: h.ctrl.State().String()}
	if h.events != nil {
		ev, err := h.events.LastKillSwitchEvent(r.Context())
		if err != nil {
			h.log.Error().Err(err).Msg("kill switch: LastEvent failed")
		} else if ev != nil {
			resp.LastEvent = &killSwitchEventDTO{
				At:       ev.At.UTC().Format(time.RFC3339Nano),
				OldState: ev.OldState,
				NewState: ev.NewState,
				Reason:   ev.Reason,
				Actor:    ev.Actor,
			}
		}
	}
	h.writeJSON(w, http.StatusOK, resp)
}

func (h *KillSwitchHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	var req killSwitchPostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	stateStr := strings.TrimSpace(req.State)
	if stateStr == "" {
		h.jsonError(w, http.StatusBadRequest, "state is required")
		return
	}
	newState, err := risk.ParseKillSwitchState(stateStr)
	if err != nil {
		h.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		h.jsonError(w, http.StatusBadRequest, "reason is required")
		return
	}
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = "operator"
	}
	prev := h.ctrl.SetState(newState, reason, actor)
	h.writeJSON(w, http.StatusOK, killSwitchResponse{
		State:    newState.String(),
		Previous: prev.String(),
		At:       time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (h *KillSwitchHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *KillSwitchHandler) jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
