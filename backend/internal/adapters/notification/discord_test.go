package notification_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/adapters/notification"
)

func TestDiscordNotifier_Notify_Success(t *testing.T) {
	var capturedBody struct {
		Embeds []struct {
			Description string `json:"description"`
			Color       int    `json:"color"`
			Footer      struct {
				Text string `json:"text"`
			} `json:"footer"`
		} `json:"embeds"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	n := notification.NewDiscordNotifier(server.URL, server.Client())

	err := n.Notify(context.Background(), "tenant-B", "trade executed")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(capturedBody.Embeds) != 1 {
		t.Fatalf("expected 1 embed, got: %d", len(capturedBody.Embeds))
	}
	embed := capturedBody.Embeds[0]
	if embed.Description != "trade executed" {
		t.Errorf("expected description=trade executed, got: %s", embed.Description)
	}
	if embed.Footer.Text != "omo-core • tenant-B" {
		t.Errorf("expected footer=omo-core • tenant-B, got: %s", embed.Footer.Text)
	}
	if embed.Color == 0 {
		t.Error("expected non-zero embed color")
	}
}

func TestDiscordNotifier_Notify_Status200AlsoAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := notification.NewDiscordNotifier(server.URL, server.Client())

	err := n.Notify(context.Background(), "t", "m")
	if err != nil {
		t.Fatalf("expected no error for 200 status, got: %v", err)
	}
}

func TestDiscordNotifier_Notify_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	n := notification.NewDiscordNotifier(server.URL, server.Client())

	err := n.Notify(context.Background(), "t", "m")
	if err == nil {
		t.Fatal("expected error for 400 status, got nil")
	}
}

func TestDiscordNotifier_Notify_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	n := notification.NewDiscordNotifier(server.URL, server.Client())

	err := n.Notify(ctx, "t", "m")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestDiscordNotifier_Notify_PostsToWebhookURL(t *testing.T) {
	var capturedMethod string
	var capturedContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	n := notification.NewDiscordNotifier(server.URL, server.Client())
	_ = n.Notify(context.Background(), "t", "m")

	if capturedMethod != http.MethodPost {
		t.Errorf("expected POST, got: %s", capturedMethod)
	}
	if capturedContentType != "application/json" {
		t.Errorf("expected Content-Type=application/json, got: %s", capturedContentType)
	}
}
