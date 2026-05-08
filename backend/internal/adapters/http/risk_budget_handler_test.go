package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubBudgetReader struct {
	state        string
	equity       float64
	dailyLossUSD float64
	maxLossUSD   float64
	maxLossPct   float64
	maxRiskPct   float64
	openCount    int
	openCap      int
	inflight     int
}

func (s *stubBudgetReader) KillSwitchState() string         { return s.state }
func (s *stubBudgetReader) MaxLossUSD() float64             { return s.maxLossUSD }
func (s *stubBudgetReader) MaxLossPct() float64             { return s.maxLossPct }
func (s *stubBudgetReader) MaxRiskPctPerIntent() float64    { return s.maxRiskPct }
func (s *stubBudgetReader) InflightIntents() int            { return s.inflight }
func (s *stubBudgetReader) AccountEquity(_ context.Context) float64 { return s.equity }

func (s *stubBudgetReader) DailyLossUsedUSD(_ context.Context, _ string, _ domain.EnvMode, _ float64) float64 {
	return s.dailyLossUSD
}

func (s *stubBudgetReader) OpenPositionsCount(_ context.Context) (int, int) {
	return s.openCount, s.openCap
}

func newBudgetStub() *stubBudgetReader {
	return &stubBudgetReader{
		state:        "ACTIVE",
		equity:       100000.0,
		dailyLossUSD: 250.50,
		maxLossUSD:   5000.0,
		maxLossPct:   0.05,
		maxRiskPct:   0.02,
		openCount:    3,
		openCap:      10,
		inflight:     1,
	}
}

func doBudgetGet(t *testing.T, h http.Handler, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/risk/budget", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestRiskBudgetHandler_GET_ReturnsAllFields(t *testing.T) {
	stub := newBudgetStub()
	h := NewRiskBudgetHandler(stub, zerolog.Nop())

	rr := doBudgetGet(t, h, nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp budgetResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "ACTIVE", resp.KillSwitchState)
	assert.InDelta(t, 100000.0, resp.AccountEquity, 1e-9)
	assert.InDelta(t, 250.50, resp.DailyLossUsedUSD, 1e-9)
	assert.InDelta(t, 5000.0, resp.MaxLossUSD, 1e-9)
	assert.InDelta(t, 0.05, resp.MaxLossPct, 1e-9)
	assert.InDelta(t, 0.02, resp.MaxRiskPctPerIntent, 1e-9)
	assert.Equal(t, 3, resp.OpenPositionsCount)
	assert.Equal(t, 10, resp.OpenPositionsCap)
	assert.Equal(t, 1, resp.InflightIntents)
}

func TestRiskBudgetHandler_HALTED_StillReturnsBudget(t *testing.T) {
	stub := newBudgetStub()
	stub.state = "HALTED"
	h := NewRiskBudgetHandler(stub, zerolog.Nop())

	rr := doBudgetGet(t, h, nil)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp budgetResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "HALTED", resp.KillSwitchState)
	assert.InDelta(t, 100000.0, resp.AccountEquity, 1e-9)
	assert.Equal(t, 3, resp.OpenPositionsCount)
}

func TestRiskBudgetHandler_POST_405(t *testing.T) {
	h := NewRiskBudgetHandler(newBudgetStub(), zerolog.Nop())
	req := httptest.NewRequest(http.MethodPost, "/api/risk/budget", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestRiskBudgetHandler_DELETE_405(t *testing.T) {
	h := NewRiskBudgetHandler(newBudgetStub(), zerolog.Nop())
	req := httptest.NewRequest(http.MethodDelete, "/api/risk/budget", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestRiskBudgetHandler_OPTIONS_204(t *testing.T) {
	h := NewRiskBudgetHandler(newBudgetStub(), zerolog.Nop())
	req := httptest.NewRequest(http.MethodOptions, "/api/risk/budget", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestRiskBudgetHandler_RequestIDEcho(t *testing.T) {
	h := NewRiskBudgetHandler(newBudgetStub(), zerolog.Nop())
	rr := doBudgetGet(t, h, map[string]string{"X-Request-ID": "abc-123"})
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "abc-123", rr.Header().Get("X-Request-ID"))
}

func TestRiskBudgetHandler_RequestIDGenerated(t *testing.T) {
	h := NewRiskBudgetHandler(newBudgetStub(), zerolog.Nop())
	rr := doBudgetGet(t, h, nil)
	require.Equal(t, http.StatusOK, rr.Code)
	rid := rr.Header().Get("X-Request-ID")
	assert.NotEmpty(t, rid)
	assert.GreaterOrEqual(t, len(rid), 16, "expected UUID-shaped value, got %q", rid)
}
