package sse_test

import (
"bufio"
"context"
"encoding/json"
"io"
"net/http"
"net/http/httptest"
"strings"
"testing"
"time"

"github.com/rs/zerolog"
"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
"github.com/oh-my-opentrade/backend/internal/adapters/sse"
"github.com/oh-my-opentrade/backend/internal/domain"
"github.com/stretchr/testify/assert"
"github.com/stretchr/testify/require"
)

func TestHandler_ServeHTTP_StreamsEvent(t *testing.T) {
	bus := memory.NewBus()
	handler := sse.NewHandler(bus, zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start the handler (subscribes to bus) in background.
	go func() { _ = handler.Start(ctx) }() //nolint:errcheck

	// Give subscriptions time to register.
	time.Sleep(20 * time.Millisecond)

	// Pipe SSE response through a recorder with a real pipe so we can read streaming data.
	pr, pw := newStreamPipe()
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	go handler.ServeHTTP(pw, req)

	// Publish a real event via the bus.
	evt, err := domain.NewEvent(
		domain.EventOrderIntentCreated,
		"tenant-1",
		domain.EnvModePaper,
		"idem-key-1",
		map[string]any{"symbol": "AAPL"},
	)
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)
	require.NoError(t, bus.Publish(ctx, *evt))

	// Read lines from the SSE stream until we get an "event:" line.
	scanner := bufio.NewScanner(pr)
	deadline := time.After(3 * time.Second)
	var gotEvent, gotData string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			gotEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			gotData = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for SSE event")
		default:
		}
	}

	assert.Equal(t, "OrderIntentCreated", gotEvent)
	require.NotEmpty(t, gotData)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(gotData), &parsed))
	assert.Equal(t, "OrderIntentCreated", parsed["type"])
	assert.Equal(t, "tenant-1", parsed["tenantId"])
}

func TestHandler_ServeHTTP_CORSHeaders(t *testing.T) {
	bus := memory.NewBus()
	handler := sse.NewHandler(bus, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	rw := httptest.NewRecorder()

	go handler.ServeHTTP(rw, req)
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	assert.Equal(t, "*", rw.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "text/event-stream", rw.Header().Get("Content-Type"))
}

func TestHandler_ServeHTTP_OptionsPreFlight(t *testing.T) {
	bus := memory.NewBus()
	handler := sse.NewHandler(bus, zerolog.Nop())

	req := httptest.NewRequest(http.MethodOptions, "/events", nil)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	assert.Equal(t, http.StatusNoContent, rw.Code)
}

// --- helpers ---

// streamRecorder wraps httptest.ResponseRecorder with a write-side pipe so
// that ServeHTTP can write and flush incrementally while the test reads.
type streamRecorder struct {
	*httptest.ResponseRecorder
	pw *io.PipeWriter
}

func (s *streamRecorder) Write(b []byte) (int, error) { return s.pw.Write(b) }
func (s *streamRecorder) Flush()                      {}

// newStreamPipe creates a pair (reader, flushing ResponseRecorder).
func newStreamPipe() (*io.PipeReader, *streamResponseWriter) {
	pr, pw := io.Pipe()
	return pr, &streamResponseWriter{
		header: http.Header{},
		pw:     pw,
	}
}

// streamResponseWriter satisfies http.ResponseWriter + http.Flusher.
type streamResponseWriter struct {
	header     http.Header
	pw         *io.PipeWriter
	statusCode int
}

func (s *streamResponseWriter) Header() http.Header { return s.header }
func (s *streamResponseWriter) WriteHeader(code int) { s.statusCode = code }
func (s *streamResponseWriter) Write(b []byte) (int, error) { return s.pw.Write(b) }
func (s *streamResponseWriter) Flush()                      {}
