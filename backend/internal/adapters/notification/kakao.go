package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

// KakaoNotifier sends notifications via the KakaoTalk "Send to Me" (Memo) API.
type KakaoNotifier struct {
	restAPIKey  string
	tokenStore  ports.TokenStorePort
	client      *http.Client
	authBaseURL string
	apiBaseURL  string
}

// NewKakaoNotifier creates a new KakaoNotifier.
// client may be nil, in which case http.DefaultClient is used.
func NewKakaoNotifier(restAPIKey string, tokenStore ports.TokenStorePort, client *http.Client) *KakaoNotifier {
	if client == nil {
		client = http.DefaultClient
	}
	return &KakaoNotifier{
		restAPIKey:  restAPIKey,
		tokenStore:  tokenStore,
		client:      client,
		authBaseURL: "https://kauth.kakao.com",
		apiBaseURL:  "https://kapi.kakao.com",
	}
}

// SetAuthBaseURL overrides the Kakao auth base URL. Intended for testing.
func (k *KakaoNotifier) SetAuthBaseURL(url string) {
	k.authBaseURL = url
}

// SetAPIBaseURL overrides the Kakao API base URL. Intended for testing.
func (k *KakaoNotifier) SetAPIBaseURL(url string) {
	k.apiBaseURL = url
}

type kakaoTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// ExchangeCode exchanges an OAuth authorization code for tokens and saves them.
func (k *KakaoNotifier) ExchangeCode(ctx context.Context, code, redirectURI string) error {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", k.restAPIKey)
	form.Set("redirect_uri", redirectURI)
	form.Set("code", code)

	reqURL := fmt.Sprintf("%s/oauth/token", k.authBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("kakao: create exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("kakao: exchange request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kakao: exchange code: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("kakao: read exchange response: %w", err)
	}

	var tokenResp kakaoTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("kakao: unmarshal exchange response: %w", err)
	}

	token := ports.OAuthToken{
		Provider:     "kakao",
		TenantID:     "default",
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		UpdatedAt:    time.Now(),
	}

	if err := k.tokenStore.SaveToken(ctx, token); err != nil {
		return fmt.Errorf("kakao: save token: %w", err)
	}

	return nil
}

// Notify sends a message to the user's KakaoTalk via the Memo API.
func (k *KakaoNotifier) Notify(ctx context.Context, tenantID, message string) error {
	token, err := k.tokenStore.LoadToken(ctx, "kakao", tenantID)
	if err != nil {
		return fmt.Errorf("kakao: load token: %w", err)
	}
	if token == nil {
		return fmt.Errorf("kakao: not connected (no token for tenant %s)", tenantID)
	}

	if time.Now().After(token.ExpiresAt) {
		token, err = k.refreshToken(ctx, token)
		if err != nil {
			return err
		}
	}

	templateObject := map[string]any{
		"object_type": "text",
		"text":        fmt.Sprintf("[%s] %s", tenantID, message),
		"link": map[string]string{
			"web_url": "https://github.com/oh-my-opentrade",
		},
	}

	templateJSON, err := json.Marshal(templateObject)
	if err != nil {
		return fmt.Errorf("kakao: marshal template: %w", err)
	}

	form := url.Values{}
	form.Set("template_object", string(templateJSON))

	reqURL := fmt.Sprintf("%s/v2/api/talk/memo/default/send", k.apiBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("kakao: create send request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("kakao: send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kakao: send message: unexpected status %d", resp.StatusCode)
	}

	return nil
}

func (k *KakaoNotifier) refreshToken(ctx context.Context, token *ports.OAuthToken) (*ports.OAuthToken, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", k.restAPIKey)
	form.Set("refresh_token", token.RefreshToken)

	reqURL := fmt.Sprintf("%s/oauth/token", k.authBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("kakao: create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kakao: refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kakao: refresh token expired, re-authentication required")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kakao: read refresh response: %w", err)
	}

	var tokenResp kakaoTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("kakao: unmarshal refresh response: %w", err)
	}

	token.AccessToken = tokenResp.AccessToken
	token.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	token.UpdatedAt = time.Now()
	if tokenResp.RefreshToken != "" {
		token.RefreshToken = tokenResp.RefreshToken
	}

	if err := k.tokenStore.SaveToken(ctx, *token); err != nil {
		return nil, fmt.Errorf("kakao: save refreshed token: %w", err)
	}

	return token, nil
}
