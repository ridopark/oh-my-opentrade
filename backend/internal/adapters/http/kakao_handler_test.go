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

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type fakeKakaoExchanger struct {
	err      error
	gotCode  string
	gotRedir string
}

func (f *fakeKakaoExchanger) ExchangeCode(_ context.Context, code, redirectURI string) error {
	f.gotCode = code
	f.gotRedir = redirectURI
	return f.err
}

type fakeTokenStore struct {
	token   *ports.OAuthToken
	loadErr error
	delErr  error
	deleted bool
}

func (f *fakeTokenStore) SaveToken(_ context.Context, _ ports.OAuthToken) error { return nil }
func (f *fakeTokenStore) LoadToken(_ context.Context, _, _ string) (*ports.OAuthToken, error) {
	return f.token, f.loadErr
}
func (f *fakeTokenStore) DeleteToken(_ context.Context, _, _ string) error {
	f.deleted = true
	return f.delErr
}

type fakeNotifier struct {
	err     error
	called  bool
	lastMsg string
}

func (f *fakeNotifier) Notify(_ context.Context, _ string, message string) error {
	f.called = true
	f.lastMsg = message
	return f.err
}

func newTestKakaoHandler(ex *fakeKakaoExchanger, ts *fakeTokenStore, n *fakeNotifier) *KakaoHandler {
	return NewKakaoHandler(ex, ts, n, "test-api-key", "http://localhost/callback", zerolog.Nop())
}

func TestKakaoHandler_AuthURL(t *testing.T) {
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, &fakeTokenStore{}, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/kakao/auth-url", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	u := resp["url"]
	if !strings.HasPrefix(u, "https://kauth.kakao.com/oauth/authorize") {
		t.Fatalf("unexpected url prefix: %s", u)
	}
	if !strings.Contains(u, "client_id=test-api-key") {
		t.Fatalf("missing client_id: %s", u)
	}
	if !strings.Contains(u, "response_type=code") {
		t.Fatalf("missing response_type: %s", u)
	}
	if !strings.Contains(u, "scope=talk_message") {
		t.Fatalf("missing scope: %s", u)
	}
}

func TestKakaoHandler_AuthURL_CORS(t *testing.T) {
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, &fakeTokenStore{}, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/kakao/auth-url", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("cors header=%q", got)
	}
}

func TestKakaoHandler_Callback_Success(t *testing.T) {
	ex := &fakeKakaoExchanger{}
	h := newTestKakaoHandler(ex, &fakeTokenStore{}, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/kakao/callback?code=abc123", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc != "/settings?kakao=connected" {
		t.Fatalf("redirect location=%q", loc)
	}
	if ex.gotCode != "abc123" {
		t.Fatalf("exchanger got code=%q", ex.gotCode)
	}
}

func TestKakaoHandler_Callback_ExchangeError(t *testing.T) {
	ex := &fakeKakaoExchanger{err: errors.New("exchange failed")}
	h := newTestKakaoHandler(ex, &fakeTokenStore{}, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/kakao/callback?code=bad", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc != "/settings?kakao=error" {
		t.Fatalf("redirect location=%q", loc)
	}
}

func TestKakaoHandler_Callback_MissingCode(t *testing.T) {
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, &fakeTokenStore{}, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/kakao/callback", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc != "/settings?kakao=error" {
		t.Fatalf("redirect location=%q", loc)
	}
}

func TestKakaoHandler_Status_Connected(t *testing.T) {
	exp := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	ts := &fakeTokenStore{token: &ports.OAuthToken{
		Provider:     "kakao",
		TenantID:     "default",
		AccessToken:  "tok",
		RefreshToken: "ref",
		ExpiresAt:    exp,
	}}
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, ts, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/kakao/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["connected"] != true {
		t.Fatalf("expected connected=true, got %v", resp["connected"])
	}
	if resp["expires_at"] != exp.Format(time.RFC3339) {
		t.Fatalf("expires_at=%v", resp["expires_at"])
	}
}

func TestKakaoHandler_Status_Disconnected(t *testing.T) {
	ts := &fakeTokenStore{token: nil}
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, ts, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/kakao/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["connected"] != false {
		t.Fatalf("expected connected=false, got %v", resp["connected"])
	}
}

func TestKakaoHandler_Disconnect_Success(t *testing.T) {
	ts := &fakeTokenStore{}
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, ts, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/notifications/kakao/disconnect", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !ts.deleted {
		t.Fatal("expected DeleteToken to be called")
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["ok"] != true {
		t.Fatalf("expected ok=true, got %v", resp["ok"])
	}
}

func TestKakaoHandler_Disconnect_Error(t *testing.T) {
	ts := &fakeTokenStore{delErr: errors.New("db error")}
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, ts, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/notifications/kakao/disconnect", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestKakaoHandler_Test_Success(t *testing.T) {
	n := &fakeNotifier{}
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, &fakeTokenStore{}, n)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/kakao/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !n.called {
		t.Fatal("expected Notify to be called")
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["ok"] != true {
		t.Fatalf("expected ok=true, got %v", resp["ok"])
	}
}

func TestKakaoHandler_Test_Error(t *testing.T) {
	n := &fakeNotifier{err: errors.New("send failed")}
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, &fakeTokenStore{}, n)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/kakao/test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestKakaoHandler_NotFound(t *testing.T) {
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, &fakeTokenStore{}, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/kakao/unknown", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestKakaoHandler_Options_Preflight(t *testing.T) {
	h := newTestKakaoHandler(&fakeKakaoExchanger{}, &fakeTokenStore{}, &fakeNotifier{})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/notifications/kakao/auth-url", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d", w.Code)
	}
}
