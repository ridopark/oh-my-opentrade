package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type copytradeTestBus struct {
	mu       sync.Mutex
	events   []domain.Event
	publishErr error
}

func (b *copytradeTestBus) Publish(_ context.Context, event domain.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.publishErr != nil {
		return b.publishErr
	}
	b.events = append(b.events, event)
	return nil
}

func (b *copytradeTestBus) Subscribe(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}
func (b *copytradeTestBus) SubscribeAsync(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}
func (b *copytradeTestBus) Unsubscribe(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}
func (b *copytradeTestBus) Close() {}

func (b *copytradeTestBus) snapshot() []domain.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]domain.Event, len(b.events))
	copy(out, b.events)
	return out
}

const copytradeTestSecret = "s3cret-xyz"

func validCopytradeBody() map[string]any {
	return map[string]any{
		"signal_id":  "msg-123:0",
		"message_id": "msg-123",
		"author":     "alice",
		"posted_at":  "2026-04-23T14:05:00Z",
		"action":     "BTO",
		"ticker":     "AAPL",
		"expiry":     "2026-04-25",
		"strike":     190.0,
		"right":      "C",
		"price":      1.20,
		"tail":       "starter",
		"raw_line":   "BTO AAPL 4/25 190C @ 1.20 starter",
	}
}

func newCopytradeHandler(bus ports.EventBusPort, now time.Time) *CopytradeHandler {
	h := NewCopytradeHandler(bus, copytradeTestSecret, 120*time.Second, zerolog.Nop())
	h.now = func() time.Time { return now }
	return h
}

func doCopytradePost(t *testing.T, h http.Handler, body any, secret string) *httptest.ResponseRecorder {
	t.Helper()
	var buf []byte
	if s, ok := body.(string); ok {
		buf = []byte(s)
	} else {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		buf = b
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/copytrade/signal", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Copytrade-Secret", secret)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestCopytradeHandler_MethodNotAllowed(t *testing.T) {
	bus := &copytradeTestBus{}
	h := newCopytradeHandler(bus, time.Now())
	req := httptest.NewRequest(http.MethodGet, "/internal/copytrade/signal", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestCopytradeHandler_AuthMissing(t *testing.T) {
	bus := &copytradeTestBus{}
	h := newCopytradeHandler(bus, time.Date(2026, 4, 23, 14, 5, 30, 0, time.UTC))
	rr := doCopytradePost(t, h, validCopytradeBody(), "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestCopytradeHandler_AuthWrongSecret(t *testing.T) {
	bus := &copytradeTestBus{}
	h := newCopytradeHandler(bus, time.Date(2026, 4, 23, 14, 5, 30, 0, time.UTC))
	rr := doCopytradePost(t, h, validCopytradeBody(), "wrong-secret")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestCopytradeHandler_AuthDisabledEmptySecretRejects(t *testing.T) {
	bus := &copytradeTestBus{}
	h := NewCopytradeHandler(bus, "", 120*time.Second, zerolog.Nop())
	h.now = func() time.Time { return time.Date(2026, 4, 23, 14, 5, 30, 0, time.UTC) }
	// Even a matching empty header must be rejected: no secret configured means
	// the endpoint is never open. This guards against misconfig exposing it.
	rr := doCopytradePost(t, h, validCopytradeBody(), "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCopytradeHandler_MalformedJSON(t *testing.T) {
	bus := &copytradeTestBus{}
	h := newCopytradeHandler(bus, time.Date(2026, 4, 23, 14, 5, 30, 0, time.UTC))
	rr := doCopytradePost(t, h, "{not json", copytradeTestSecret)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestCopytradeHandler_ValidationErrors(t *testing.T) {
	now := time.Date(2026, 4, 23, 14, 5, 30, 0, time.UTC)
	cases := []struct {
		name  string
		mutate func(m map[string]any)
	}{
		{"empty signal_id", func(m map[string]any) { m["signal_id"] = "" }},
		{"empty author", func(m map[string]any) { m["author"] = "" }},
		{"empty ticker", func(m map[string]any) { m["ticker"] = "" }},
		{"bad action", func(m map[string]any) { m["action"] = "XYZ" }},
		{"bad right", func(m map[string]any) { m["right"] = "Q" }},
		{"bad posted_at", func(m map[string]any) { m["posted_at"] = "not-a-date" }},
		{"bad expiry", func(m map[string]any) { m["expiry"] = "2026/04/25" }},
		{"zero strike", func(m map[string]any) { m["strike"] = 0.0 }},
		{"negative strike", func(m map[string]any) { m["strike"] = -1.0 }},
		{"zero price", func(m map[string]any) { m["price"] = 0.0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := &copytradeTestBus{}
			h := newCopytradeHandler(bus, now)
			body := validCopytradeBody()
			tc.mutate(body)
			rr := doCopytradePost(t, h, body, copytradeTestSecret)
			require.Equal(t, http.StatusBadRequest, rr.Code, "body=%v", body)
			assert.Empty(t, bus.snapshot())
		})
	}
}

func TestCopytradeHandler_StaleRejected(t *testing.T) {
	bus := &copytradeTestBus{}
	posted := time.Date(2026, 4, 23, 14, 0, 0, 0, time.UTC)
	now := posted.Add(10 * time.Minute) // well past 120s TTL
	h := newCopytradeHandler(bus, now)
	body := validCopytradeBody()
	body["posted_at"] = posted.Format(time.RFC3339)
	rr := doCopytradePost(t, h, body, copytradeTestSecret)
	require.Equal(t, http.StatusGone, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestCopytradeHandler_HappyPath_BTO(t *testing.T) {
	bus := &copytradeTestBus{}
	now := time.Date(2026, 4, 23, 14, 5, 30, 0, time.UTC)
	h := newCopytradeHandler(bus, now)

	rr := doCopytradePost(t, h, validCopytradeBody(), copytradeTestSecret)
	require.Equal(t, http.StatusAccepted, rr.Code)

	events := bus.snapshot()
	require.Len(t, events, 1)
	evt := events[0]
	assert.Equal(t, domain.EventCopytradeSignalReceived, evt.Type)
	assert.Equal(t, "msg-123:0", evt.IdempotencyKey)
	assert.Equal(t, domain.EnvModePaper, evt.EnvMode)

	payload, ok := evt.Payload.(domain.CopytradeSignalPayload)
	require.True(t, ok, "payload type: %T", evt.Payload)
	assert.Equal(t, "msg-123:0", payload.SignalID)
	assert.Equal(t, "msg-123", payload.MessageID)
	assert.Equal(t, "alice", payload.Author)
	assert.Equal(t, domain.CopytradeActionBTO, payload.Action)
	assert.Equal(t, domain.Symbol("AAPL"), payload.Ticker)
	assert.Equal(t, domain.OptionRightCall, payload.Right)
	assert.True(t, payload.PostedAt.Equal(time.Date(2026, 4, 23, 14, 5, 0, 0, time.UTC)))
	assert.True(t, payload.Expiry.Equal(time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)))
	assert.Equal(t, 190.0, payload.Strike)
	assert.Equal(t, 1.20, payload.Price)
	assert.Equal(t, "starter", payload.Tail)
}

func TestCopytradeHandler_RightPutAndActionSTC(t *testing.T) {
	bus := &copytradeTestBus{}
	now := time.Date(2026, 4, 23, 14, 5, 30, 0, time.UTC)
	h := newCopytradeHandler(bus, now)

	body := validCopytradeBody()
	body["signal_id"] = "msg-456:1"
	body["action"] = "STC"
	body["right"] = "P"
	body["tail"] = "half out"

	rr := doCopytradePost(t, h, body, copytradeTestSecret)
	require.Equal(t, http.StatusAccepted, rr.Code)

	events := bus.snapshot()
	require.Len(t, events, 1)
	payload := events[0].Payload.(domain.CopytradeSignalPayload)
	assert.Equal(t, domain.CopytradeActionSTC, payload.Action)
	assert.Equal(t, domain.OptionRightPut, payload.Right)
	assert.Equal(t, "half out", payload.Tail)
}

func TestCopytradeHandler_Dedupe(t *testing.T) {
	bus := &copytradeTestBus{}
	now := time.Date(2026, 4, 23, 14, 5, 30, 0, time.UTC)
	h := newCopytradeHandler(bus, now)
	body := validCopytradeBody()

	rr1 := doCopytradePost(t, h, body, copytradeTestSecret)
	require.Equal(t, http.StatusAccepted, rr1.Code)

	rr2 := doCopytradePost(t, h, body, copytradeTestSecret)
	require.Equal(t, http.StatusOK, rr2.Code)

	var resp copytradeSignalResponse
	require.NoError(t, json.Unmarshal(rr2.Body.Bytes(), &resp))
	assert.True(t, resp.Deduped)

	assert.Len(t, bus.snapshot(), 1, "duplicate must not re-publish")
}

func TestCopytradeHandler_PublishErrorSurfaces500(t *testing.T) {
	bus := &copytradeTestBus{publishErr: assertErr("bus down")}
	now := time.Date(2026, 4, 23, 14, 5, 30, 0, time.UTC)
	h := newCopytradeHandler(bus, now)
	rr := doCopytradePost(t, h, validCopytradeBody(), copytradeTestSecret)
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
