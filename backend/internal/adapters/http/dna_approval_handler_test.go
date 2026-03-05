package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appdna "github.com/oh-my-opentrade/backend/internal/app/dnaapproval"
	dnadomain "github.com/oh-my-opentrade/backend/internal/domain/dnaapproval"
	"github.com/rs/zerolog"
)

type fakeDNAApprovalService struct {
	pending  []appdna.ApprovalWithVersion
	approval map[string]*appdna.ApprovalWithVersion
	active   map[string]*dnadomain.DNAVersion

	approveErr error
	rejectErr  error
}

func (f *fakeDNAApprovalService) GetPendingApprovals(_ context.Context) ([]appdna.ApprovalWithVersion, error) {
	return f.pending, nil
}

func (f *fakeDNAApprovalService) GetApproval(_ context.Context, id string) (*appdna.ApprovalWithVersion, error) {
	if f.approval == nil {
		return nil, nil
	}
	return f.approval[id], nil
}

func (f *fakeDNAApprovalService) GetActiveDNA(_ context.Context, strategyKey string) (*dnadomain.DNAVersion, error) {
	if f.active == nil {
		return nil, nil
	}
	return f.active[strategyKey], nil
}

func (f *fakeDNAApprovalService) Approve(_ context.Context, _, _, _ string) error {
	return f.approveErr
}
func (f *fakeDNAApprovalService) Reject(_ context.Context, _, _, _ string) error { return f.rejectErr }

func TestDNAApprovalHandler_ListApprovals(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	v := dnadomain.DNAVersion{ID: "v1", StrategyKey: "s1", ContentTOML: "[a]\n", ContentHash: strings.Repeat("a", 64), DetectedAt: now}
	a := dnadomain.DNAApproval{ID: "a1", VersionID: "v1", Status: dnadomain.DNAStatusPending, CreatedAt: now}

	fake := &fakeDNAApprovalService{pending: []appdna.ApprovalWithVersion{{Approval: a, Version: v}}}
	h := NewDNAApprovalHandler(fake, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/dna/approvals", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("cors header=%q", got)
	}

	var resp []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp))
	}
}

func TestDNAApprovalHandler_ListApprovals_StatusFilter(t *testing.T) {
	fake := &fakeDNAApprovalService{}
	h := NewDNAApprovalHandler(fake, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/dna/approvals?status=approved", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDNAApprovalHandler_GetApproval_NotFound(t *testing.T) {
	fake := &fakeDNAApprovalService{approval: map[string]*appdna.ApprovalWithVersion{}}
	h := NewDNAApprovalHandler(fake, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/dna/approvals/missing", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDNAApprovalHandler_Diff_WithActive(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	v := dnadomain.DNAVersion{ID: "v2", StrategyKey: "s1", ContentTOML: "new", ContentHash: strings.Repeat("b", 64), DetectedAt: now}
	a := dnadomain.DNAApproval{ID: "a2", VersionID: "v2", Status: dnadomain.DNAStatusPending, CreatedAt: now}
	aw := &appdna.ApprovalWithVersion{Approval: a, Version: v}
	active := &dnadomain.DNAVersion{ID: "v1", StrategyKey: "s1", ContentTOML: "base", ContentHash: strings.Repeat("a", 64), DetectedAt: now}

	fake := &fakeDNAApprovalService{
		approval: map[string]*appdna.ApprovalWithVersion{"a2": aw},
		active:   map[string]*dnadomain.DNAVersion{"s1": active},
	}
	h := NewDNAApprovalHandler(fake, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/dna/approvals/a2/diff", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["baseToml"] != "base" || resp["newToml"] != "new" {
		t.Fatalf("unexpected diff: %#v", resp)
	}
}

func TestDNAApprovalHandler_Approve_InvalidJSON(t *testing.T) {
	fake := &fakeDNAApprovalService{}
	h := NewDNAApprovalHandler(fake, zerolog.Nop())

	req := httptest.NewRequest(http.MethodPost, "/api/dna/approvals/a1/approve", strings.NewReader("{"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDNAApprovalHandler_Approve_NotFound(t *testing.T) {
	fake := &fakeDNAApprovalService{approveErr: errors.New("approval not found")}
	h := NewDNAApprovalHandler(fake, zerolog.Nop())

	req := httptest.NewRequest(http.MethodPost, "/api/dna/approvals/a1/approve", strings.NewReader(`{"decidedBy":"me","comment":"ok"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDNAApprovalHandler_Reject_Conflict(t *testing.T) {
	fake := &fakeDNAApprovalService{rejectErr: errors.New("approval is not pending: approved")}
	h := NewDNAApprovalHandler(fake, zerolog.Nop())

	req := httptest.NewRequest(http.MethodPost, "/api/dna/approvals/a1/reject", strings.NewReader(`{"decidedBy":"me","comment":"no"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDNAApprovalHandler_GetActive(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	active := &dnadomain.DNAVersion{ID: "v1", StrategyKey: "s1", ContentTOML: "base", ContentHash: strings.Repeat("a", 64), DetectedAt: now}
	fake := &fakeDNAApprovalService{active: map[string]*dnadomain.DNAVersion{"s1": active}}
	h := NewDNAApprovalHandler(fake, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/dna/strategies/s1/active", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDNAApprovalHandler_Routes_NotFound(t *testing.T) {
	fake := &fakeDNAApprovalService{}
	h := NewDNAApprovalHandler(fake, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/dna/unknown", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
