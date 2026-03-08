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
	"strconv"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
)

var _ ports.ImageNotifierPort = (*DiscordNotifier)(nil)

const (
	maxRetries       = 2
	defaultRetryWait = 1 * time.Second
	maxRetryWait     = 10 * time.Second
)

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

	return d.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

func (d *DiscordNotifier) NotifyWithImage(ctx context.Context, tenantID, message string, image ports.Attachment) error {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

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

	bodyBytes := buf.Bytes()
	contentType := writer.FormDataContentType()

	return d.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentType)
		return req, nil
	})
}

func (d *DiscordNotifier) doWithRetry(ctx context.Context, buildReq func() (*http.Request, error)) error {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := buildReq()
		if err != nil {
			return fmt.Errorf("discord: create request: %w", err)
		}

		resp, err := d.client.Do(req)
		if err != nil {
			return fmt.Errorf("discord: send request: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
			return nil
		}

		if resp.StatusCode != http.StatusTooManyRequests || attempt == maxRetries {
			return fmt.Errorf("discord: unexpected status %d", resp.StatusCode)
		}

		wait := parseRetryAfter(resp.Header.Get("Retry-After"))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return defaultRetryWait
	}
	secs, err := strconv.ParseFloat(header, 64)
	if err != nil || secs <= 0 {
		return defaultRetryWait
	}
	wait := time.Duration(secs*1000) * time.Millisecond
	if wait > maxRetryWait {
		return maxRetryWait
	}
	return wait
}
