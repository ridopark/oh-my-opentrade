package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestDataSourceHealthHandler_AllHealthy(t *testing.T) {
	now := time.Now().UTC()
	h := NewDataSourceHealthHandler(zerolog.Nop(),
		FeedDataSource("ibkr", "IBKR", 60*time.Second, func(ctx context.Context) (time.Time, string) {
			return now.Add(-1 * time.Second), ""
		}),
		FeedDataSource("alpaca", "Alpaca SIP", 60*time.Second, func(ctx context.Context) (time.Time, string) {
			return now.Add(-5 * time.Second), ""
		}),
		StaticDataSource("omo-data", "omo-data"),
		DBDataSource("db", "Database", func(ctx context.Context) error { return nil }),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/health/datasources", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got DataSourceHealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Sources) != 4 {
		t.Fatalf("sources len=%d want 4", len(got.Sources))
	}
	wantIDs := []string{"ibkr", "alpaca", "omo-data", "db"}
	wantLabels := []string{"IBKR", "Alpaca SIP", "omo-data", "Database"}
	for i, s := range got.Sources {
		if s.ID != wantIDs[i] || s.Label != wantLabels[i] {
			t.Fatalf("row[%d]=%+v", i, s)
		}
		if !s.Healthy {
			t.Fatalf("row[%d] id=%s expected healthy", i, s.ID)
		}
	}
	if got.AsOf.IsZero() {
		t.Fatalf("asOf should be set")
	}
}

func TestDataSourceHealthHandler_StaleSourceUnhealthy(t *testing.T) {
	stale := time.Now().Add(-10 * time.Minute)
	h := NewDataSourceHealthHandler(zerolog.Nop(),
		FeedDataSource("alpaca", "Alpaca SIP", 60*time.Second, func(ctx context.Context) (time.Time, string) {
			return stale, "stale equity feed"
		}),
		DBDataSource("db", "Database", func(ctx context.Context) error { return errors.New("connection refused") }),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/health/datasources", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (handler must always render header)", w.Code)
	}
	var got DataSourceHealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Sources[0].Healthy {
		t.Fatalf("stale alpaca should be unhealthy: %+v", got.Sources[0])
	}
	if got.Sources[0].Detail != "stale equity feed" {
		t.Fatalf("detail=%q", got.Sources[0].Detail)
	}
	if got.Sources[1].Healthy {
		t.Fatalf("db ping error should be unhealthy")
	}
	if got.Sources[1].Detail != "connection refused" {
		t.Fatalf("db detail=%q", got.Sources[1].Detail)
	}
}

func TestDataSourceHealthHandler_MethodNotAllowed(t *testing.T) {
	h := NewDataSourceHealthHandler(zerolog.Nop())
	req := httptest.NewRequest(http.MethodPost, "/api/health/datasources", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestDataSourceHealthHandler_CORSPreflight(t *testing.T) {
	h := NewDataSourceHealthHandler(zerolog.Nop())
	req := httptest.NewRequest(http.MethodOptions, "/api/health/datasources", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d", w.Code)
	}
}
