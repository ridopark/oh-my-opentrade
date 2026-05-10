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

type tradingTheTrendTestBus struct {
	mu         sync.Mutex
	events     []domain.Event
	publishErr error
}

func (b *tradingTheTrendTestBus) Publish(_ context.Context, event domain.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.publishErr != nil {
		return b.publishErr
	}
	b.events = append(b.events, event)
	return nil
}

func (b *tradingTheTrendTestBus) Subscribe(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}
func (b *tradingTheTrendTestBus) SubscribeAsync(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}
func (b *tradingTheTrendTestBus) Unsubscribe(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}
func (b *tradingTheTrendTestBus) Close() {}

func (b *tradingTheTrendTestBus) snapshot() []domain.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]domain.Event, len(b.events))
	copy(out, b.events)
	return out
}

const tradingTheTrendTestSecret = "ttt-s3cret-xyz"

func validTradingTheTrendBody() map[string]any {
	return map[string]any{
		"signal_id":  "tradingthetrend:msg-123:0",
		"message_id": "msg-123",
		"author":     "TradingTheTrend",
		"posted_at":  "2026-04-23T13:20:00Z",
		"ticker":     "RKLB",
		"strike":     90.0,
		"right":      "C",
		"trigger":    88.00,
		"raw_line":   "RKLB 90c > 88.00",
	}
}

func newTradingTheTrendHandler(bus ports.EventBusPort, now time.Time) *TradingTheTrendHandler {
	h := NewTradingTheTrendHandler(bus, tradingTheTrendTestSecret, 60*time.Second, zerolog.Nop())
	h.now = func() time.Time { return now }
	return h
}

func doTradingTheTrendPost(t *testing.T, h http.Handler, body any, secret string) *httptest.ResponseRecorder {
	t.Helper()
	var buf []byte
	if s, ok := body.(string); ok {
		buf = []byte(s)
	} else {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		buf = b
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/tradingthetrend/signal", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-TradingTheTrend-Secret", secret)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestTradingTheTrendHandler_MethodNotAllowed(t *testing.T) {
	bus := &tradingTheTrendTestBus{}
	h := newTradingTheTrendHandler(bus, time.Now())
	req := httptest.NewRequest(http.MethodGet, "/internal/tradingthetrend/signal", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestTradingTheTrendHandler_AuthMissing(t *testing.T) {
	bus := &tradingTheTrendTestBus{}
	h := newTradingTheTrendHandler(bus, time.Date(2026, 4, 23, 13, 20, 30, 0, time.UTC))
	rr := doTradingTheTrendPost(t, h, validTradingTheTrendBody(), "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestTradingTheTrendHandler_AuthWrongSecret(t *testing.T) {
	bus := &tradingTheTrendTestBus{}
	h := newTradingTheTrendHandler(bus, time.Date(2026, 4, 23, 13, 20, 30, 0, time.UTC))
	rr := doTradingTheTrendPost(t, h, validTradingTheTrendBody(), "wrong-secret")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestTradingTheTrendHandler_AuthDisabledEmptySecretRejects(t *testing.T) {
	// Even with an empty configured secret, the endpoint must be unreachable.
	// Same defensive guarantee as copytrade.
	bus := &tradingTheTrendTestBus{}
	h := NewTradingTheTrendHandler(bus, "", 60*time.Second, zerolog.Nop())
	h.now = func() time.Time { return time.Date(2026, 4, 23, 13, 20, 30, 0, time.UTC) }
	rr := doTradingTheTrendPost(t, h, validTradingTheTrendBody(), "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestTradingTheTrendHandler_MalformedJSON(t *testing.T) {
	bus := &tradingTheTrendTestBus{}
	h := newTradingTheTrendHandler(bus, time.Date(2026, 4, 23, 13, 20, 30, 0, time.UTC))
	rr := doTradingTheTrendPost(t, h, "{not json", tradingTheTrendTestSecret)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestTradingTheTrendHandler_ValidationErrors(t *testing.T) {
	now := time.Date(2026, 4, 23, 13, 20, 30, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(m map[string]any)
	}{
		{"empty signal_id", func(m map[string]any) { m["signal_id"] = "" }},
		{"empty author", func(m map[string]any) { m["author"] = "" }},
		{"empty ticker", func(m map[string]any) { m["ticker"] = "" }},
		{"bad right", func(m map[string]any) { m["right"] = "Q" }},
		{"bad posted_at", func(m map[string]any) { m["posted_at"] = "not-a-date" }},
		{"zero strike", func(m map[string]any) { m["strike"] = 0.0 }},
		{"negative strike", func(m map[string]any) { m["strike"] = -1.0 }},
		{"zero trigger", func(m map[string]any) { m["trigger"] = 0.0 }},
		{"negative trigger", func(m map[string]any) { m["trigger"] = -10.0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := &tradingTheTrendTestBus{}
			h := newTradingTheTrendHandler(bus, now)
			body := validTradingTheTrendBody()
			tc.mutate(body)
			rr := doTradingTheTrendPost(t, h, body, tradingTheTrendTestSecret)
			require.Equal(t, http.StatusBadRequest, rr.Code, "body=%v", body)
			assert.Empty(t, bus.snapshot())
		})
	}
}

func TestTradingTheTrendHandler_StaleRejected(t *testing.T) {
	bus := &tradingTheTrendTestBus{}
	posted := time.Date(2026, 4, 23, 13, 20, 0, 0, time.UTC)
	now := posted.Add(5 * time.Minute) // well past 60s TTL
	h := newTradingTheTrendHandler(bus, now)
	body := validTradingTheTrendBody()
	body["posted_at"] = posted.Format(time.RFC3339)
	rr := doTradingTheTrendPost(t, h, body, tradingTheTrendTestSecret)
	require.Equal(t, http.StatusGone, rr.Code)
	assert.Empty(t, bus.snapshot())
}

func TestTradingTheTrendHandler_HappyPath_Call(t *testing.T) {
	bus := &tradingTheTrendTestBus{}
	now := time.Date(2026, 4, 23, 13, 20, 30, 0, time.UTC)
	h := newTradingTheTrendHandler(bus, now)

	rr := doTradingTheTrendPost(t, h, validTradingTheTrendBody(), tradingTheTrendTestSecret)
	require.Equal(t, http.StatusAccepted, rr.Code)

	events := bus.snapshot()
	require.Len(t, events, 1)
	evt := events[0]
	assert.Equal(t, domain.EventTradingTheTrendSignalReceived, evt.Type)
	assert.Equal(t, "tradingthetrend:msg-123:0", evt.IdempotencyKey)
	assert.Equal(t, domain.EnvModePaper, evt.EnvMode)

	payload, ok := evt.Payload.(domain.TradingTheTrendSignalPayload)
	require.True(t, ok, "payload type: %T", evt.Payload)
	assert.Equal(t, "tradingthetrend:msg-123:0", payload.SignalID)
	assert.Equal(t, "msg-123", payload.MessageID)
	assert.Equal(t, "TradingTheTrend", payload.Author)
	assert.Equal(t, domain.Symbol("RKLB"), payload.Ticker)
	assert.Equal(t, domain.OptionRightCall, payload.Right)
	assert.True(t, payload.PostedAt.Equal(time.Date(2026, 4, 23, 13, 20, 0, 0, time.UTC)))
	assert.Equal(t, 90.0, payload.Strike)
	assert.Equal(t, 88.00, payload.Trigger)
	assert.Equal(t, "RKLB 90c > 88.00", payload.RawLine)
}

func TestTradingTheTrendHandler_RightPut(t *testing.T) {
	bus := &tradingTheTrendTestBus{}
	now := time.Date(2026, 4, 23, 13, 20, 30, 0, time.UTC)
	h := newTradingTheTrendHandler(bus, now)

	body := validTradingTheTrendBody()
	body["signal_id"] = "tradingthetrend:msg-456:1"
	body["right"] = "P"
	body["ticker"] = "SPY"
	body["strike"] = 500.0
	body["trigger"] = 498.0

	rr := doTradingTheTrendPost(t, h, body, tradingTheTrendTestSecret)
	require.Equal(t, http.StatusAccepted, rr.Code)

	events := bus.snapshot()
	require.Len(t, events, 1)
	payload := events[0].Payload.(domain.TradingTheTrendSignalPayload)
	assert.Equal(t, domain.OptionRightPut, payload.Right)
	assert.Equal(t, domain.Symbol("SPY"), payload.Ticker)
}

func TestTradingTheTrendHandler_Dedupe(t *testing.T) {
	bus := &tradingTheTrendTestBus{}
	now := time.Date(2026, 4, 23, 13, 20, 30, 0, time.UTC)
	h := newTradingTheTrendHandler(bus, now)
	body := validTradingTheTrendBody()

	rr1 := doTradingTheTrendPost(t, h, body, tradingTheTrendTestSecret)
	require.Equal(t, http.StatusAccepted, rr1.Code)

	rr2 := doTradingTheTrendPost(t, h, body, tradingTheTrendTestSecret)
	require.Equal(t, http.StatusOK, rr2.Code)

	var resp tradingTheTrendSignalResponse
	require.NoError(t, json.Unmarshal(rr2.Body.Bytes(), &resp))
	assert.True(t, resp.Deduped)

	assert.Len(t, bus.snapshot(), 1, "duplicate must not re-publish")
}

func TestTradingTheTrendHandler_PublishErrorSurfaces500(t *testing.T) {
	bus := &tradingTheTrendTestBus{publishErr: tttAssertErr("bus down")}
	now := time.Date(2026, 4, 23, 13, 20, 30, 0, time.UTC)
	h := newTradingTheTrendHandler(bus, now)
	rr := doTradingTheTrendPost(t, h, validTradingTheTrendBody(), tradingTheTrendTestSecret)
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

type tttAssertErr string

func (e tttAssertErr) Error() string { return string(e) }
