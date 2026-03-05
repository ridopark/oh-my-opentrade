package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	appdna "github.com/oh-my-opentrade/backend/internal/app/dnaapproval"
	dnadomain "github.com/oh-my-opentrade/backend/internal/domain/dnaapproval"
	"github.com/rs/zerolog"
)

type DNAApprovalService interface {
	GetPendingApprovals(ctx context.Context) ([]appdna.ApprovalWithVersion, error)
	GetApproval(ctx context.Context, id string) (*appdna.ApprovalWithVersion, error)
	GetActiveDNA(ctx context.Context, strategyKey string) (*dnadomain.DNAVersion, error)
	Approve(ctx context.Context, approvalID, decidedBy, comment string) error
	Reject(ctx context.Context, approvalID, decidedBy, comment string) error
}

type DNAApprovalHandler struct {
	svc DNAApprovalService
	log zerolog.Logger
}

func NewDNAApprovalHandler(svc DNAApprovalService, log zerolog.Logger) *DNAApprovalHandler {
	return &DNAApprovalHandler{svc: svc, log: log}
}

func (h *DNAApprovalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "dna" {
		h.jsonError(w, http.StatusNotFound, "not found")
		return
	}
	parts = parts[2:]

	if r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "approvals" {
		h.handleListApprovals(w, r)
		return
	}
	if len(parts) >= 2 && parts[0] == "approvals" {
		approvalID := parts[1]
		if r.Method == http.MethodGet && len(parts) == 2 {
			h.handleGetApproval(w, r, approvalID)
			return
		}
		if r.Method == http.MethodGet && len(parts) == 3 && parts[2] == "diff" {
			h.handleDiff(w, r, approvalID)
			return
		}
		if r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "approve" {
			h.handleApprove(w, r, approvalID)
			return
		}
		if r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "reject" {
			h.handleReject(w, r, approvalID)
			return
		}
	}

	if r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "strategies" && parts[2] == "active" {
		h.handleGetActive(w, r, parts[1])
		return
	}

	h.jsonError(w, http.StatusNotFound, "not found")
}

type dnaVersionJSON struct {
	ID          string `json:"id"`
	StrategyKey string `json:"strategyKey"`
	ContentTOML string `json:"contentToml"`
	ContentHash string `json:"contentHash"`
	DetectedAt  string `json:"detectedAt"`
}

type dnaApprovalJSON struct {
	ID        string  `json:"id"`
	VersionID string  `json:"versionId"`
	Status    string  `json:"status"`
	DecidedBy *string `json:"decidedBy"`
	DecidedAt *string `json:"decidedAt"`
	Comment   *string `json:"comment"`
	CreatedAt string  `json:"createdAt"`
}

type approvalWithVersionJSON struct {
	Approval dnaApprovalJSON `json:"approval"`
	Version  dnaVersionJSON  `json:"version"`
}

func toVersionJSON(v dnadomain.DNAVersion) dnaVersionJSON {
	return dnaVersionJSON{
		ID:          v.ID,
		StrategyKey: v.StrategyKey,
		ContentTOML: v.ContentTOML,
		ContentHash: v.ContentHash,
		DetectedAt:  v.DetectedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toApprovalJSON(a dnadomain.DNAApproval) dnaApprovalJSON {
	var decidedAt *string
	if a.DecidedAt != nil {
		s := a.DecidedAt.UTC().Format(time.RFC3339Nano)
		decidedAt = &s
	}
	return dnaApprovalJSON{
		ID:        a.ID,
		VersionID: a.VersionID,
		Status:    string(a.Status),
		DecidedBy: a.DecidedBy,
		DecidedAt: decidedAt,
		Comment:   a.Comment,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toApprovalWithVersionJSON(item appdna.ApprovalWithVersion) approvalWithVersionJSON {
	return approvalWithVersionJSON{Approval: toApprovalJSON(item.Approval), Version: toVersionJSON(item.Version)}
}

func (h *DNAApprovalHandler) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" && raw != "pending" {
		h.jsonError(w, http.StatusBadRequest, "unsupported status")
		return
	}
	items, err := h.svc.GetPendingApprovals(r.Context())
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list dna approvals")
		h.jsonError(w, http.StatusInternalServerError, "failed to list approvals")
		return
	}
	resp := make([]approvalWithVersionJSON, 0, len(items))
	for _, it := range items {
		resp = append(resp, toApprovalWithVersionJSON(it))
	}
	if resp == nil {
		resp = []approvalWithVersionJSON{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *DNAApprovalHandler) handleGetApproval(w http.ResponseWriter, r *http.Request, approvalID string) {
	item, err := h.svc.GetApproval(r.Context(), approvalID)
	if err != nil {
		h.log.Error().Err(err).Str("approval_id", approvalID).Msg("failed to get dna approval")
		h.jsonError(w, http.StatusInternalServerError, "failed to get approval")
		return
	}
	if item == nil {
		h.jsonError(w, http.StatusNotFound, "approval not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toApprovalWithVersionJSON(*item))
}

func (h *DNAApprovalHandler) handleDiff(w http.ResponseWriter, r *http.Request, approvalID string) {
	item, err := h.svc.GetApproval(r.Context(), approvalID)
	if err != nil {
		h.log.Error().Err(err).Str("approval_id", approvalID).Msg("failed to get dna approval for diff")
		h.jsonError(w, http.StatusInternalServerError, "failed to get approval")
		return
	}
	if item == nil {
		h.jsonError(w, http.StatusNotFound, "approval not found")
		return
	}

	active, err := h.svc.GetActiveDNA(r.Context(), item.Version.StrategyKey)
	if err != nil {
		h.log.Error().Err(err).Str("strategy_key", item.Version.StrategyKey).Msg("failed to get active dna")
		h.jsonError(w, http.StatusInternalServerError, "failed to get active dna")
		return
	}
	base := ""
	if active != nil {
		base = active.ContentTOML
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"baseToml": base,
		"newToml":  item.Version.ContentTOML,
	})
}

func (h *DNAApprovalHandler) handleApprove(w http.ResponseWriter, r *http.Request, approvalID string) {
	var req struct {
		DecidedBy string `json:"decidedBy"`
		Comment   string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := h.svc.Approve(r.Context(), approvalID, req.DecidedBy, req.Comment); err != nil {
		h.writeSvcErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *DNAApprovalHandler) handleReject(w http.ResponseWriter, r *http.Request, approvalID string) {
	var req struct {
		DecidedBy string `json:"decidedBy"`
		Comment   string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := h.svc.Reject(r.Context(), approvalID, req.DecidedBy, req.Comment); err != nil {
		h.writeSvcErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *DNAApprovalHandler) handleGetActive(w http.ResponseWriter, r *http.Request, strategyKey string) {
	v, err := h.svc.GetActiveDNA(r.Context(), strategyKey)
	if err != nil {
		h.log.Error().Err(err).Str("strategy_key", strategyKey).Msg("failed to get active dna")
		h.jsonError(w, http.StatusInternalServerError, "failed to get active dna")
		return
	}
	if v == nil {
		h.jsonError(w, http.StatusNotFound, "active dna not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toVersionJSON(*v))
}

func (h *DNAApprovalHandler) writeSvcErr(w http.ResponseWriter, err error) {
	msg := err.Error()
	status := http.StatusBadRequest
	switch {
	case strings.Contains(msg, "not found"):
		status = http.StatusNotFound
	case strings.Contains(msg, "not pending"):
		status = http.StatusConflict
	}
	h.jsonError(w, status, msg)
}

func (h *DNAApprovalHandler) jsonError(w http.ResponseWriter, statusCode int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		h.log.Error().Err(err).Msg("failed to encode error response")
	}
}
