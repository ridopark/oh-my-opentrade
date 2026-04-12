package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// mockDecayRepo implements ports.DecayTelemetryPort for testing.
type mockDecayRepo struct {
	rollingFn     func(ctx context.Context, strategy string) ([]domain.RollingDecayPoint, error)
	attributionFn func(ctx context.Context, strategy string) ([]domain.ComponentAttribution, error)
	insertFn      func(ctx context.Context, tradeID uuid.UUID, strategy, symbol string, pnl float64, regime, vixBucket string) error
}

func (m *mockDecayRepo) InsertTradeStats(ctx context.Context, tradeID uuid.UUID, strategy, symbol string, pnl float64, regime, vixBucket string) error {
	if m.insertFn != nil {
		return m.insertFn(ctx, tradeID, strategy, symbol, pnl, regime, vixBucket)
	}
	return nil
}

func (m *mockDecayRepo) GetRollingDecayStats(ctx context.Context, strategy string) ([]domain.RollingDecayPoint, error) {
	if m.rollingFn != nil {
		return m.rollingFn(ctx, strategy)
	}
	return nil, nil
}

func (m *mockDecayRepo) GetComponentAttribution(ctx context.Context, strategy string) ([]domain.ComponentAttribution, error) {
	if m.attributionFn != nil {
		return m.attributionFn(ctx, strategy)
	}
	return nil, nil
}

func TestDecayHandler_Rolling(t *testing.T) {
	pf20 := 1.5
	wr60 := 0.55
	mock := &mockDecayRepo{
		rollingFn: func(_ context.Context, strategy string) ([]domain.RollingDecayPoint, error) {
			if strategy != "macd_only_v1" {
				t.Fatalf("unexpected strategy=%q", strategy)
			}
			return []domain.RollingDecayPoint{
				{TradeSeq: 1, PnL: 50.0, RollingPF20: &pf20, RollingWR60: &wr60},
				{TradeSeq: 2, PnL: -20.0},
			}, nil
		},
	}

	h := NewDecayHandler(mock, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/rolling?strategy=macd_only_v1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var got []domain.RollingDecayPoint
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 points, got %d", len(got))
	}
	if got[0].TradeSeq != 1 || got[0].PnL != 50.0 {
		t.Fatalf("unexpected first point: %+v", got[0])
	}
	if got[0].RollingPF20 == nil || *got[0].RollingPF20 != 1.5 {
		t.Fatalf("unexpected rollingPf20: %v", got[0].RollingPF20)
	}
	if got[1].RollingPF20 != nil {
		t.Fatalf("expected nil rollingPf20 for second point, got %v", *got[1].RollingPF20)
	}
}

func TestDecayHandler_Rolling_MissingStrategy(t *testing.T) {
	h := NewDecayHandler(&mockDecayRepo{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/rolling", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDecayHandler_Rolling_Error(t *testing.T) {
	mock := &mockDecayRepo{
		rollingFn: func(_ context.Context, _ string) ([]domain.RollingDecayPoint, error) {
			return nil, errors.New("db error")
		},
	}
	h := NewDecayHandler(mock, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/rolling?strategy=test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDecayHandler_Rolling_EmptyResult(t *testing.T) {
	h := NewDecayHandler(&mockDecayRepo{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/rolling?strategy=test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// Should return [] not null
	if body := w.Body.String(); body != "[]\n" {
		t.Fatalf("expected empty array, got %q", body)
	}
}

func TestDecayHandler_Attribution(t *testing.T) {
	pfFired := 2.0
	pfNotFired := 1.2
	marginal := 0.8
	mock := &mockDecayRepo{
		attributionFn: func(_ context.Context, strategy string) ([]domain.ComponentAttribution, error) {
			if strategy != "orb_break_retest" {
				t.Fatalf("unexpected strategy=%q", strategy)
			}
			return []domain.ComponentAttribution{
				{
					Component:  "ema_stack",
					Group:      "trend",
					NFired:     80,
					NNotFired:  20,
					PFFired:    &pfFired,
					PFNotFired: &pfNotFired,
					Marginal:   &marginal,
				},
			}, nil
		},
	}

	h := NewDecayHandler(mock, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/attribution?strategy=orb_break_retest", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var got []domain.ComponentAttribution
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 attribution, got %d", len(got))
	}
	if got[0].Component != "ema_stack" {
		t.Fatalf("component=%q", got[0].Component)
	}
	if got[0].NFired != 80 || got[0].NNotFired != 20 {
		t.Fatalf("fired=%d notFired=%d", got[0].NFired, got[0].NNotFired)
	}
	if got[0].Marginal == nil || *got[0].Marginal != 0.8 {
		t.Fatalf("marginal=%v", got[0].Marginal)
	}
}

func TestDecayHandler_Attribution_MissingStrategy(t *testing.T) {
	h := NewDecayHandler(&mockDecayRepo{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/attribution", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDecayHandler_CORS(t *testing.T) {
	h := NewDecayHandler(&mockDecayRepo{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodOptions, "/api/decay/rolling", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow-origin=%q", got)
	}
}

func TestDecayHandler_MethodNotAllowed(t *testing.T) {
	h := NewDecayHandler(&mockDecayRepo{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodPost, "/api/decay/rolling", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestDecayHandler_NotFound(t *testing.T) {
	h := NewDecayHandler(&mockDecayRepo{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/decay/unknown", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestComponentAttribution_MarginalComputation(t *testing.T) {
	tests := []struct {
		name     string
		pfFired  *float64
		pfNot    *float64
		wantNil  bool
		wantVal  float64
	}{
		{
			name:    "both present",
			pfFired: ptrFloat(2.5),
			pfNot:   ptrFloat(1.0),
			wantVal: 1.5,
		},
		{
			name:    "fired nil",
			pfFired: nil,
			pfNot:   ptrFloat(1.0),
			wantNil: true,
		},
		{
			name:    "not-fired nil",
			pfFired: ptrFloat(2.5),
			pfNot:   nil,
			wantNil: true,
		},
		{
			name:    "both nil",
			pfFired: nil,
			pfNot:   nil,
			wantNil: true,
		},
		{
			name:    "negative marginal",
			pfFired: ptrFloat(0.8),
			pfNot:   ptrFloat(1.5),
			wantVal: -0.7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attr := domain.ComponentAttribution{
				PFFired:    tt.pfFired,
				PFNotFired: tt.pfNot,
			}
			// Simulate marginal computation same as the repo does
			if attr.PFFired != nil && attr.PFNotFired != nil {
				m := *attr.PFFired - *attr.PFNotFired
				attr.Marginal = &m
			}

			if tt.wantNil {
				if attr.Marginal != nil {
					t.Fatalf("expected nil marginal, got %v", *attr.Marginal)
				}
			} else {
				if attr.Marginal == nil {
					t.Fatalf("expected marginal=%v, got nil", tt.wantVal)
				}
				// Use tolerance for float comparison
				diff := *attr.Marginal - tt.wantVal
				if diff > 0.001 || diff < -0.001 {
					t.Fatalf("marginal=%v, want %v", *attr.Marginal, tt.wantVal)
				}
			}
		})
	}
}

func ptrFloat(v float64) *float64 { return &v }
