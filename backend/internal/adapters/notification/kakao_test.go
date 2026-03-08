package notification_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/notification"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

type mockTokenStore struct {
	mu     sync.Mutex
	tokens map[string]*ports.OAuthToken
}

func newMockTokenStore() *mockTokenStore {
	return &mockTokenStore{tokens: make(map[string]*ports.OAuthToken)}
}

func (m *mockTokenStore) SaveToken(_ context.Context, token ports.OAuthToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := token.Provider + ":" + token.TenantID
	copied := token
	m.tokens[key] = &copied
	return nil
}

func (m *mockTokenStore) LoadToken(_ context.Context, provider, tenantID string) (*ports.OAuthToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := provider + ":" + tenantID
	t, ok := m.tokens[key]
	if !ok {
		return nil, nil
	}
	copied := *t
	return &copied, nil
}

func (m *mockTokenStore) DeleteToken(_ context.Context, provider, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, provider+":"+tenantID)
	return nil
}

func TestKakaoNotifier_Notify_Success(t *testing.T) {
	var capturedAuth string
	var capturedPath string
	var capturedTemplateObj string

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		_ = r.ParseForm()
		capturedTemplateObj = r.FormValue("template_object")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result_code":0}`))
	}))
	defer apiServer.Close()

	store := newMockTokenStore()
	_ = store.SaveToken(context.Background(), ports.OAuthToken{
		Provider:     "kakao",
		TenantID:     "tenant-1",
		AccessToken:  "valid-access-token",
		RefreshToken: "valid-refresh-token",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})

	n := notification.NewKakaoNotifier("test-api-key", store, apiServer.Client())
	n.SetAPIBaseURL(apiServer.URL)

	err := n.Notify(context.Background(), "tenant-1", "order filled")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if capturedPath != "/v2/api/talk/memo/default/send" {
		t.Errorf("expected path /v2/api/talk/memo/default/send, got: %s", capturedPath)
	}
	if capturedAuth != "Bearer valid-access-token" {
		t.Errorf("expected Bearer auth header, got: %s", capturedAuth)
	}
	if capturedTemplateObj == "" {
		t.Fatal("expected template_object form value to be set")
	}
	if !strings.Contains(capturedTemplateObj, "[tenant-1] order filled") {
		t.Errorf("expected template_object to contain formatted message, got: %s", capturedTemplateObj)
	}
}

func TestKakaoNotifier_Notify_AutoRefresh(t *testing.T) {
	var refreshCalled bool
	var sendCalled bool

	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalled = true
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "grant_type=refresh_token") {
			t.Errorf("expected refresh_token grant_type, got: %s", string(body))
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access-token",
			"expires_in":   3600,
		})
	}))
	defer authServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCalled = true
		auth := r.Header.Get("Authorization")
		if auth != "Bearer new-access-token" {
			t.Errorf("expected new access token after refresh, got: %s", auth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result_code":0}`))
	}))
	defer apiServer.Close()

	store := newMockTokenStore()
	_ = store.SaveToken(context.Background(), ports.OAuthToken{
		Provider:     "kakao",
		TenantID:     "tenant-1",
		AccessToken:  "expired-token",
		RefreshToken: "valid-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	})

	n := notification.NewKakaoNotifier("test-api-key", store, nil)
	n.SetAuthBaseURL(authServer.URL)
	n.SetAPIBaseURL(apiServer.URL)

	err := n.Notify(context.Background(), "tenant-1", "auto refresh test")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !refreshCalled {
		t.Error("expected refresh endpoint to be called")
	}
	if !sendCalled {
		t.Error("expected send endpoint to be called after refresh")
	}

	saved, _ := store.LoadToken(context.Background(), "kakao", "tenant-1")
	if saved.AccessToken != "new-access-token" {
		t.Errorf("expected saved token to have new access token, got: %s", saved.AccessToken)
	}
	if saved.RefreshToken != "valid-refresh-token" {
		t.Errorf("expected old refresh token to be kept, got: %s", saved.RefreshToken)
	}
}

func TestKakaoNotifier_Notify_NoToken(t *testing.T) {
	store := newMockTokenStore()
	n := notification.NewKakaoNotifier("test-api-key", store, nil)

	err := n.Notify(context.Background(), "tenant-missing", "hello")
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("expected 'not connected' in error, got: %v", err)
	}
}

func TestKakaoNotifier_Notify_RefreshTokenExpired(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer authServer.Close()

	store := newMockTokenStore()
	_ = store.SaveToken(context.Background(), ports.OAuthToken{
		Provider:     "kakao",
		TenantID:     "tenant-1",
		AccessToken:  "expired-token",
		RefreshToken: "expired-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	})

	n := notification.NewKakaoNotifier("test-api-key", store, nil)
	n.SetAuthBaseURL(authServer.URL)

	err := n.Notify(context.Background(), "tenant-1", "hello")
	if err == nil {
		t.Fatal("expected error for expired refresh token, got nil")
	}
	if !strings.Contains(err.Error(), "re-authentication required") {
		t.Errorf("expected 're-authentication required' in error, got: %v", err)
	}
}

func TestKakaoNotifier_Notify_SendError(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"msg":"insufficient scope"}`))
	}))
	defer apiServer.Close()

	store := newMockTokenStore()
	_ = store.SaveToken(context.Background(), ports.OAuthToken{
		Provider:     "kakao",
		TenantID:     "tenant-1",
		AccessToken:  "valid-token",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})

	n := notification.NewKakaoNotifier("test-api-key", store, apiServer.Client())
	n.SetAPIBaseURL(apiServer.URL)

	err := n.Notify(context.Background(), "tenant-1", "test")
	if err == nil {
		t.Fatal("expected error for non-200 API response, got nil")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("unexpected status %d", http.StatusForbidden)) {
		t.Errorf("expected 'unexpected status 403' in error, got: %v", err)
	}
}

func TestKakaoNotifier_Notify_ContextCancellation(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	store := newMockTokenStore()
	_ = store.SaveToken(context.Background(), ports.OAuthToken{
		Provider:     "kakao",
		TenantID:     "tenant-1",
		AccessToken:  "valid-token",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	n := notification.NewKakaoNotifier("test-api-key", store, apiServer.Client())
	n.SetAPIBaseURL(apiServer.URL)

	err := n.Notify(ctx, "tenant-1", "test")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestKakaoNotifier_ExchangeCode_Success(t *testing.T) {
	var capturedBody string

	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access-token",
			"refresh_token": "new-refresh-token",
			"expires_in":    21600,
		})
	}))
	defer authServer.Close()

	store := newMockTokenStore()
	n := notification.NewKakaoNotifier("my-rest-api-key", store, authServer.Client())
	n.SetAuthBaseURL(authServer.URL)

	err := n.ExchangeCode(context.Background(), "auth-code-123", "http://localhost/callback")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !strings.Contains(capturedBody, "grant_type=authorization_code") {
		t.Errorf("expected grant_type=authorization_code in body, got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "client_id=my-rest-api-key") {
		t.Errorf("expected client_id in body, got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "code=auth-code-123") {
		t.Errorf("expected code in body, got: %s", capturedBody)
	}

	saved, _ := store.LoadToken(context.Background(), "kakao", "default")
	if saved == nil {
		t.Fatal("expected token to be saved")
	}
	if saved.AccessToken != "new-access-token" {
		t.Errorf("expected access token 'new-access-token', got: %s", saved.AccessToken)
	}
	if saved.RefreshToken != "new-refresh-token" {
		t.Errorf("expected refresh token 'new-refresh-token', got: %s", saved.RefreshToken)
	}
}

func TestKakaoNotifier_ExchangeCode_Error(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer authServer.Close()

	store := newMockTokenStore()
	n := notification.NewKakaoNotifier("my-rest-api-key", store, authServer.Client())
	n.SetAuthBaseURL(authServer.URL)

	err := n.ExchangeCode(context.Background(), "bad-code", "http://localhost/callback")
	if err == nil {
		t.Fatal("expected error for failed exchange, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected status 400") {
		t.Errorf("expected 'unexpected status 400' in error, got: %v", err)
	}
}
