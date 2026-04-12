package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	omhttp "github.com/oh-my-opentrade/backend/internal/adapters/http"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDecayQuerier struct {
	rollingResult []domain.RollingDecayPoint
	rollingErr    error
	attrResult    []domain.ComponentAttribution
	attrErr       error
}

func (m *mockDecayQuerier) GetRollingDecayStats(_ context.Context, _ string) ([]domain.RollingDecayPoint, error) {
	return m.rollingResult, m.rollingErr
}

func (m *mockDecayQuerier) GetComponentAttribution(_ context.Context, _ string) ([]domain.ComponentAttribution, error) {
	return m.attrResult, m.attrErr
}

func TestDecayHandler_Rolling_Success(t *testing.T) {
	pf := 1.5
	mock := &mockDecayQuerier{
		rollingResult: []domain.RollingDecayPoint{
			{TradeSeq: 1, PnL: 100, RollingPF20: &pf},
		},
	}
	h := omhttp.NewDecayHandler(mock, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/rolling?strategy=macd_only_v1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var points []domain.RollingDecayPoint
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &points))
	assert.Len(t, points, 1)
	assert.Equal(t, 1, points[0].TradeSeq)
}

func TestDecayHandler_Rolling_MissingStrategy(t *testing.T) {
	h := omhttp.NewDecayHandler(&mockDecayQuerier{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/rolling", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDecayHandler_Rolling_RepoError(t *testing.T) {
	mock := &mockDecayQuerier{rollingErr: errors.New("db down")}
	h := omhttp.NewDecayHandler(mock, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/rolling?strategy=x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestDecayHandler_Rolling_EmptyResult(t *testing.T) {
	h := omhttp.NewDecayHandler(&mockDecayQuerier{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/rolling?strategy=x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]\n", w.Body.String())
}

func TestDecayHandler_Attribution_Success(t *testing.T) {
	pfF, pfNF := 2.0, 1.5
	marginal := 0.5
	mock := &mockDecayQuerier{
		attrResult: []domain.ComponentAttribution{
			{Component: "dp_buy", Group: "darkpool", NFired: 50, NNotFired: 30, PFFired: &pfF, PFNotFired: &pfNF, Marginal: &marginal},
		},
	}
	h := omhttp.NewDecayHandler(mock, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/attribution?strategy=macd_only_v1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var results []domain.ComponentAttribution
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &results))
	assert.Len(t, results, 1)
	assert.Equal(t, "dp_buy", results[0].Component)
	assert.InDelta(t, 0.5, *results[0].Marginal, 0.001)
}

func TestDecayHandler_CORS_Preflight(t *testing.T) {
	h := omhttp.NewDecayHandler(&mockDecayQuerier{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodOptions, "/api/decay/rolling", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestDecayHandler_MethodNotAllowed(t *testing.T) {
	h := omhttp.NewDecayHandler(&mockDecayQuerier{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodPost, "/api/decay/rolling", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestDecayHandler_UnknownPath(t *testing.T) {
	h := omhttp.NewDecayHandler(&mockDecayQuerier{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/unknown", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
