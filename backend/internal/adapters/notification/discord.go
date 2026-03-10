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
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

var _ ports.ImageNotifierPort = (*DiscordNotifier)(nil)

const (
	maxRetries       = 2
	defaultRetryWait = 1 * time.Second
	maxRetryWait     = 10 * time.Second
	cooldownTTL      = 60 * time.Second
	maxCooldownKeys  = 500
)

type DiscordNotifier struct {
	webhookURL string
	client     *http.Client
	log        zerolog.Logger

	cooldownMu sync.RWMutex
	cooldown   map[string]time.Time
}

func NewDiscordNotifier(webhookURL string, client *http.Client, log ...zerolog.Logger) *DiscordNotifier {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	l := zerolog.Nop()
	if len(log) > 0 {
		l = log[0]
	}
	return &DiscordNotifier{
		webhookURL: webhookURL,
		client:     client,
		log:        l,
		cooldown:   make(map[string]time.Time),
	}
}

type discordPayload struct {
	Content string `json:"content"`
}

func (d *DiscordNotifier) Notify(ctx context.Context, tenantID, message string) error {
	if d.isCoolingDown(message) {
		return nil
	}

	payload := discordPayload{
		Content: fmt.Sprintf("[%s] %s", tenantID, message),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("discord: marshal payload: %w", err)
	}

	err = d.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		d.log.Error().Err(err).Str("tenant", tenantID).Msg("CRITICAL: discord webhook failed after retries — alert may be lost")
	}
	return err
}

func (d *DiscordNotifier) NotifyWithImage(ctx context.Context, tenantID, message string, image ports.Attachment) error {
	if d.isCoolingDown(message) {
		return nil
	}

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
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[0]"; filename=%q`, image.Filename))
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

	err = d.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentType)
		return req, nil
	})
	if err != nil {
		d.log.Error().Err(err).Str("tenant", tenantID).Msg("CRITICAL: discord image webhook failed after retries — alert may be lost")
	}
	return err
}

func (d *DiscordNotifier) isCoolingDown(message string) bool {
	if len(message) > 80 {
		message = message[:80]
	}

	now := time.Now()
	d.cooldownMu.RLock()
	if expiry, ok := d.cooldown[message]; ok && now.Before(expiry) {
		d.cooldownMu.RUnlock()
		return true
	}
	d.cooldownMu.RUnlock()

	d.cooldownMu.Lock()
	defer d.cooldownMu.Unlock()

	if len(d.cooldown) >= maxCooldownKeys {
		for k, exp := range d.cooldown {
			if now.After(exp) {
				delete(d.cooldown, k)
			}
		}
	}
	d.cooldown[message] = now.Add(cooldownTTL)
	return false
}

func (d *DiscordNotifier) doWithRetry(ctx context.Context, buildReq func() (*http.Request, error)) error {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := buildReq()
		if err != nil {
			return fmt.Errorf("discord: create request: %w", err)
		}

		resp, err := d.client.Do(req)
		if err != nil {
			if attempt < maxRetries {
				wait := defaultRetryWait * time.Duration(1<<uint(attempt))
				if wait > maxRetryWait {
					wait = maxRetryWait
				}
				d.log.Warn().Err(err).Int("attempt", attempt+1).Dur("backoff", wait).Msg("discord: transient error, retrying")
				select {
				case <-time.After(wait):
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return fmt.Errorf("discord: send request: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
			return nil
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if resp.StatusCode >= 500 && attempt < maxRetries {
			wait := defaultRetryWait * time.Duration(1<<uint(attempt))
			if wait > maxRetryWait {
				wait = maxRetryWait
			}
			d.log.Warn().Int("status", resp.StatusCode).Int("attempt", attempt+1).Dur("backoff", wait).Msg("discord: server error, retrying")
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		return fmt.Errorf("discord: unexpected status %d", resp.StatusCode)
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
