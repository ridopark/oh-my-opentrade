package notification_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/adapters/notification"
)

func TestTelegramNotifier_Notify_Success(t *testing.T) {
	var capturedBody map[string]string
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	n := notification.NewTelegramNotifier("test-token", "chat-123", server.Client())
	// Override base URL to point to test server
	n.SetBaseURL(server.URL)

	err := n.Notify(context.Background(), "tenant-A", "hello world")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !strings.HasSuffix(capturedPath, "/sendMessage") {
		t.Errorf("expected path to end with /sendMessage, got: %s", capturedPath)
	}
	if capturedBody["chat_id"] != "chat-123" {
		t.Errorf("expected chat_id=chat-123, got: %s", capturedBody["chat_id"])
	}
	if capturedBody["text"] != "[tenant-A] hello world" {
		t.Errorf("expected text=[tenant-A] hello world, got: %s", capturedBody["text"])
	}
	if capturedBody["parse_mode"] != "HTML" {
		t.Errorf("expected parse_mode=HTML, got: %s", capturedBody["parse_mode"])
	}
}

func TestTelegramNotifier_Notify_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer server.Close()

	n := notification.NewTelegramNotifier("bad-token", "chat-123", server.Client())
	n.SetBaseURL(server.URL)

	err := n.Notify(context.Background(), "tenant-A", "hello")
	if err == nil {
		t.Fatal("expected error for non-200 status, got nil")
	}
}

func TestTelegramNotifier_Notify_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	n := notification.NewTelegramNotifier("token", "chat-123", server.Client())
	n.SetBaseURL(server.URL)

	err := n.Notify(ctx, "tenant-A", "hello")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestTelegramNotifier_Notify_URLContainsToken(t *testing.T) {
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	n := notification.NewTelegramNotifier("my-secret-token", "chat-456", server.Client())
	n.SetBaseURL(server.URL)

	_ = n.Notify(context.Background(), "t1", "msg")

	if !strings.Contains(capturedPath, "my-secret-token") {
		t.Errorf("expected path to contain token, got: %s", capturedPath)
	}
}
