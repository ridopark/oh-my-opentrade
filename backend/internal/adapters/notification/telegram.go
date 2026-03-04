package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// TelegramNotifier sends notifications via the Telegram Bot API.
type TelegramNotifier struct {
	botToken string
	chatID   string
	client   *http.Client
	baseURL  string
}

// NewTelegramNotifier creates a new TelegramNotifier.
// client may be nil, in which case http.DefaultClient is used.
func NewTelegramNotifier(botToken, chatID string, client *http.Client) *TelegramNotifier {
	if client == nil {
		client = http.DefaultClient
	}
	return &TelegramNotifier{
		botToken: botToken,
		chatID:   chatID,
		client:   client,
		baseURL:  "https://api.telegram.org",
	}
}

// SetBaseURL overrides the Telegram API base URL. Intended for testing.
func (t *TelegramNotifier) SetBaseURL(baseURL string) {
	t.baseURL = baseURL
}

// telegramPayload represents the JSON body sent to the Telegram Bot API.
type telegramPayload struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// Notify sends a message to the configured Telegram chat.
func (t *TelegramNotifier) Notify(ctx context.Context, tenantID, message string) error {
	payload := telegramPayload{
		ChatID:    t.chatID,
		Text:      fmt.Sprintf("[%s] %s", tenantID, message),
		ParseMode: "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: unexpected status %d", resp.StatusCode)
	}

	return nil
}
