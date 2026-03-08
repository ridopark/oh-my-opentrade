package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

var _ ports.ImageNotifierPort = (*DiscordNotifier)(nil)

type DiscordNotifier struct {
	webhookURL string
	client     *http.Client
}

func NewDiscordNotifier(webhookURL string, client *http.Client) *DiscordNotifier {
	if client == nil {
		client = http.DefaultClient
	}
	return &DiscordNotifier{
		webhookURL: webhookURL,
		client:     client,
	}
}

type discordPayload struct {
	Content string `json:"content"`
}

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

func (d *DiscordNotifier) NotifyWithImage(ctx context.Context, tenantID, message string, image ports.Attachment) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	embedPayload := map[string]any{
		"content": fmt.Sprintf("[%s] %s", tenantID, message),
		"embeds": []map[string]any{
			{
				"image": map[string]string{
					"url": fmt.Sprintf("attachment://%s", image.Filename),
				},
			},
		},
	}
	payloadJSON, err := json.Marshal(embedPayload)
	if err != nil {
		return fmt.Errorf("discord: marshal embed payload: %w", err)
	}

	if err := writer.WriteField("payload_json", string(payloadJSON)); err != nil {
		return fmt.Errorf("discord: write payload_json: %w", err)
	}

	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[0]"; filename="%s"`, image.Filename))
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return fmt.Errorf("discord: create file part: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(image.Data)); err != nil {
		return fmt.Errorf("discord: write file data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("discord: close multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, &body)
	if err != nil {
		return fmt.Errorf("discord: create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

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
