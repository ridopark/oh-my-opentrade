package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// DiscordNotifier sends notifications via a Discord webhook.
type DiscordNotifier struct {
	webhookURL string
	client     *http.Client
}

// NewDiscordNotifier creates a new DiscordNotifier.
// client may be nil, in which case http.DefaultClient is used.
func NewDiscordNotifier(webhookURL string, client *http.Client) *DiscordNotifier {
	if client == nil {
		client = http.DefaultClient
	}
	return &DiscordNotifier{
		webhookURL: webhookURL,
		client:     client,
	}
}

// discordPayload represents the JSON body sent to a Discord webhook.
type discordPayload struct {
	Content string `json:"content"`
}

// Notify sends a message to the configured Discord webhook.
func (d *DiscordNotifier) Notify(ctx context.Context, tenantID, message string) error {
	payload := discordPayload{
		Content: fmt.Sprintf("[%s] %s", tenantID, message),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("discord: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("discord: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("discord: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discord: unexpected status %d", resp.StatusCode)
	}

	return nil
}
