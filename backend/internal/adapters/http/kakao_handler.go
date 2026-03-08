package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type KakaoAuthExchanger interface {
	ExchangeCode(ctx context.Context, code, redirectURI string) error
}

type KakaoHandler struct {
	exchanger   KakaoAuthExchanger
	tokenStore  ports.TokenStorePort
	notifier    ports.NotifierPort
	restAPIKey  string
	redirectURI string
	log         zerolog.Logger
}

func NewKakaoHandler(
	exchanger KakaoAuthExchanger,
	tokenStore ports.TokenStorePort,
	notifier ports.NotifierPort,
	restAPIKey, redirectURI string,
	log zerolog.Logger,
) *KakaoHandler {
	return &KakaoHandler{
		exchanger:   exchanger,
		tokenStore:  tokenStore,
		notifier:    notifier,
		restAPIKey:  restAPIKey,
		redirectURI: redirectURI,
		log:         log,
	}
}

func (h *KakaoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "notifications" || parts[3] != "kakao" {
		h.jsonError(w, http.StatusNotFound, "not found")
		return
	}
	action := parts[4]

	switch {
	case r.Method == http.MethodGet && action == "auth-url":
		h.handleAuthURL(w)
	case r.Method == http.MethodGet && action == "callback":
		h.handleCallback(w, r)
	case r.Method == http.MethodGet && action == "status":
		h.handleStatus(w, r)
	case r.Method == http.MethodDelete && action == "disconnect":
		h.handleDisconnect(w, r)
	case r.Method == http.MethodPost && action == "test":
		h.handleTest(w, r)
	default:
		h.jsonError(w, http.StatusNotFound, "not found")
	}
}

func (h *KakaoHandler) handleAuthURL(w http.ResponseWriter) {
	params := url.Values{
		"client_id":     {h.restAPIKey},
		"redirect_uri":  {h.redirectURI},
		"response_type": {"code"},
		"scope":         {"talk_message"},
	}
	authURL := "https://kauth.kakao.com/oauth/authorize?" + params.Encode()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": authURL})
}

func (h *KakaoHandler) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/settings?kakao=error", http.StatusFound)
		return
	}

	if err := h.exchanger.ExchangeCode(r.Context(), code, h.redirectURI); err != nil {
		h.log.Error().Err(err).Msg("kakao code exchange failed")
		http.Redirect(w, r, "/settings?kakao=error", http.StatusFound)
		return
	}

	http.Redirect(w, r, "/settings?kakao=connected", http.StatusFound)
}

func (h *KakaoHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	token, err := h.tokenStore.LoadToken(r.Context(), "kakao", "default")
	if err != nil || token == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"connected": false})
		return
	}

	resp := map[string]any{"connected": true}
	if !token.ExpiresAt.IsZero() {
		resp["expires_at"] = token.ExpiresAt.UTC().Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *KakaoHandler) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := h.tokenStore.DeleteToken(r.Context(), "kakao", "default"); err != nil {
		h.log.Error().Err(err).Msg("kakao disconnect failed")
		h.jsonError(w, http.StatusInternalServerError, fmt.Sprintf("disconnect failed: %s", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *KakaoHandler) handleTest(w http.ResponseWriter, r *http.Request) {
	if err := h.notifier.Notify(r.Context(), "default", "\U0001f514 KakaoTalk test notification from oh-my-opentrade"); err != nil {
		h.log.Error().Err(err).Msg("kakao test notification failed")
		h.jsonError(w, http.StatusInternalServerError, fmt.Sprintf("test failed: %s", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (h *KakaoHandler) jsonError(w http.ResponseWriter, statusCode int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		h.log.Error().Err(err).Msg("failed to encode error response")
	}
}
