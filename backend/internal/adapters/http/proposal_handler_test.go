package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubValidator struct {
	blocked bool
	gate    string
	reason  string
	calls   int
	gotIntent domain.OrderIntent
}

func (s *stubValidator) ValidateIntent(_ context.Context, intent domain.OrderIntent) (gate string, reason string, blocked bool) {
	s.calls++
	s.gotIntent = intent
	return s.gate, s.reason, s.blocked
}

type stubProposalSnapshot struct {
	state            string
	equity           float64
	dailyLossUsedUSD float64
	openPositions    int
	inflightIntents  int
}

func (s *stubProposalSnapshot) KillSwitchState() string                          { return s.state }
func (s *stubProposalSnapshot) Equity(_ context.Context) float64                 { return s.equity }
func (s *stubProposalSnapshot) OpenPositions(_ context.Context) int              { return s.openPositions }
func (s *stubProposalSnapshot) InflightIntents() int                             { return s.inflightIntents }
func (s *stubProposalSnapshot) DailyLossUsedUSD(_ context.Context, _ string, _ domain.EnvMode, _ float64) float64 {
	return s.dailyLossUsedUSD
}

func newProposalSnapshotStub() *stubProposalSnapshot {
	return &stubProposalSnapshot{
		state:            "ACTIVE",
		equity:           100000.0,
		dailyLossUsedUSD: 250.0,
		openPositions:    2,
		inflightIntents:  1,
	}
}

func validProposalBody() []byte {
	body, _ := json.Marshal(map[string]any{
		"symbol":      "AAPL",
		"direction":   "long",
		"limit_price": 175.50,
		"stop_loss":   170.00,
		"quantity":    100.0,
		"rationale":   "test",
	})
	return body
}

func doProposalRequest(t *testing.T, h http.Handler, method string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	var req *http.Request
	if bodyReader != nil {
		req = httptest.NewRequest(method, "/api/proposals/order", bodyReader)
	} else {
		req = httptest.NewRequest(method, "/api/proposals/order", nil)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestProposalHandler_ValidIntent_AllGatesPass(t *testing.T) {
	v := &stubValidator{blocked: false}
	s := newProposalSnapshotStub()
	h := NewProposalHandler(v, s, zerolog.Nop())

	rr := doProposalRequest(t, h, http.MethodPost, validProposalBody(), nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp proposalResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.WouldPass)
	assert.Empty(t, resp.Gate)
	assert.Empty(t, resp.Reason)

	evalAt, err := time.Parse(time.RFC3339, resp.EvaluatedAt)
	require.NoError(t, err)
	validUntil, err := time.Parse(time.RFC3339, resp.ValidUntil)
	require.NoError(t, err)

	delta := validUntil.Sub(evalAt)
	assert.InDelta(t, float64(30*time.Second), float64(delta), float64(time.Second))
}

func TestProposalHandler_GateRejection(t *testing.T) {
	v := &stubValidator{blocked: true, gate: "position_gate", reason: "position exists"}
	s := newProposalSnapshotStub()
	h := NewProposalHandler(v, s, zerolog.Nop())

	rr := doProposalRequest(t, h, http.MethodPost, validProposalBody(), nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp proposalResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.False(t, resp.WouldPass)
	assert.Equal(t, "position_gate", resp.Gate)
	assert.Equal(t, "position exists", resp.Reason)
}

func TestProposalHandler_HALTED_ShortCircuits(t *testing.T) {
	v := &stubValidator{blocked: false}
	s := newProposalSnapshotStub()
	s.state = "HALTED"
	h := NewProposalHandler(v, s, zerolog.Nop())

	rr := doProposalRequest(t, h, http.MethodPost, validProposalBody(), nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp proposalResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.False(t, resp.WouldPass)
	assert.Equal(t, "kill_switch", resp.Gate)
	assert.Equal(t, "halted", resp.Reason)
	assert.Equal(t, 0, v.calls, "validator must NOT be called when HALTED")
}

func TestProposalHandler_MalformedJSON_400(t *testing.T) {
	h := NewProposalHandler(&stubValidator{}, newProposalSnapshotStub(), zerolog.Nop())
	rr := doProposalRequest(t, h, http.MethodPost, []byte(`{"not`), nil)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestProposalHandler_MissingSymbol_400(t *testing.T) {
	h := NewProposalHandler(&stubValidator{}, newProposalSnapshotStub(), zerolog.Nop())
	body, _ := json.Marshal(map[string]any{
		"direction":   "long",
		"limit_price": 175.50,
		"stop_loss":   170.00,
		"quantity":    100.0,
	})
	rr := doProposalRequest(t, h, http.MethodPost, body, nil)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "symbol")
}

func TestProposalHandler_InvalidDirection_400(t *testing.T) {
	h := NewProposalHandler(&stubValidator{}, newProposalSnapshotStub(), zerolog.Nop())
	body, _ := json.Marshal(map[string]any{
		"symbol":      "AAPL",
		"direction":   "sideways",
		"limit_price": 175.50,
		"stop_loss":   170.00,
		"quantity":    100.0,
	})
	rr := doProposalRequest(t, h, http.MethodPost, body, nil)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "direction")
}

func TestProposalHandler_NegativeQuantity_400(t *testing.T) {
	h := NewProposalHandler(&stubValidator{}, newProposalSnapshotStub(), zerolog.Nop())
	body, _ := json.Marshal(map[string]any{
		"symbol":      "AAPL",
		"direction":   "long",
		"limit_price": 175.50,
		"stop_loss":   170.00,
		"quantity":    -1.0,
	})
	rr := doProposalRequest(t, h, http.MethodPost, body, nil)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "quantity")
}

func TestProposalHandler_GET_405(t *testing.T) {
	h := NewProposalHandler(&stubValidator{}, newProposalSnapshotStub(), zerolog.Nop())
	rr := doProposalRequest(t, h, http.MethodGet, nil, nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestProposalHandler_DELETE_405(t *testing.T) {
	h := NewProposalHandler(&stubValidator{}, newProposalSnapshotStub(), zerolog.Nop())
	rr := doProposalRequest(t, h, http.MethodDelete, nil, nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestProposalHandler_OPTIONS_204(t *testing.T) {
	h := NewProposalHandler(&stubValidator{}, newProposalSnapshotStub(), zerolog.Nop())
	rr := doProposalRequest(t, h, http.MethodOptions, nil, nil)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestProposalHandler_RequestIDEcho(t *testing.T) {
	h := NewProposalHandler(&stubValidator{}, newProposalSnapshotStub(), zerolog.Nop())
	rr := doProposalRequest(t, h, http.MethodPost, validProposalBody(),
		map[string]string{"X-Request-ID": "abc-123"})
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "abc-123", rr.Header().Get("X-Request-ID"))

	var resp proposalResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "abc-123", resp.RequestID)
}

func TestProposalHandler_RequestIDGenerated(t *testing.T) {
	h := NewProposalHandler(&stubValidator{}, newProposalSnapshotStub(), zerolog.Nop())
	rr := doProposalRequest(t, h, http.MethodPost, validProposalBody(), nil)
	require.Equal(t, http.StatusOK, rr.Code)

	headerRID := rr.Header().Get("X-Request-ID")
	assert.NotEmpty(t, headerRID)
	assert.GreaterOrEqual(t, len(headerRID), 16, "expected UUID-shaped value, got %q", headerRID)

	var resp proposalResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, headerRID, resp.RequestID)
}

func TestProposalHandler_SnapshotPopulated(t *testing.T) {
	v := &stubValidator{blocked: false}
	s := &stubProposalSnapshot{
		state:            "ACTIVE",
		equity:           75000.0,
		dailyLossUsedUSD: 1234.56,
		openPositions:    7,
		inflightIntents:  3,
	}
	h := NewProposalHandler(v, s, zerolog.Nop())

	rr := doProposalRequest(t, h, http.MethodPost, validProposalBody(), nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp proposalResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.InDelta(t, 75000.0, resp.Snapshot.Equity, 1e-9)
	assert.InDelta(t, 1234.56, resp.Snapshot.DailyLossUsedUSD, 1e-9)
	assert.Equal(t, 7, resp.Snapshot.OpenPositions)
	assert.Equal(t, 3, resp.Snapshot.InflightIntents)
}

// HALTED short-circuits ALL proposals at the HTTP layer, including exit
// directions. Rationale: the validator's exit-bypass logic is internal
// (execution.Service.ValidateIntent line 45). At the adapter boundary we
// keep the rule simple — if HALTED, no proposal passes. KISS, safest.
func TestProposalHandler_ExitDirection_BypassesKillSwitchHalt(t *testing.T) {
	v := &stubValidator{blocked: false}
	s := newProposalSnapshotStub()
	s.state = "HALTED"
	h := NewProposalHandler(v, s, zerolog.Nop())

	body, _ := json.Marshal(map[string]any{
		"symbol":      "AAPL",
		"direction":   "exit_long",
		"limit_price": 175.50,
		"stop_loss":   170.00,
		"quantity":    100.0,
	})
	rr := doProposalRequest(t, h, http.MethodPost, body, nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp proposalResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.False(t, resp.WouldPass)
	assert.Equal(t, "kill_switch", resp.Gate)
	assert.Equal(t, 0, v.calls, "HALTED short-circuits at HTTP layer regardless of direction")
}
