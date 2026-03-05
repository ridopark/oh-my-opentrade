// Package sse provides a Server-Sent Events HTTP adapter that bridges the
// internal domain event bus to browser clients.
//
// The handler subscribes to every domain event type on startup and fans them
// out to all connected SSE clients.  Each client connection is managed in its
// own goroutine; slow or disconnected clients are cleaned up automatically.
package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// eventTypes is the full set of domain events the SSE handler forwards.
var eventTypes = []domain.EventType{
	domain.EventMarketBarReceived,
	domain.EventMarketBarSanitized,
	domain.EventMarketBarRejected,
	domain.EventStateUpdated,
	domain.EventRegimeShifted,
	domain.EventSetupDetected,
	domain.EventDebateRequested,
	domain.EventDebateCompleted,
	domain.EventOrderIntentCreated,
	domain.EventOrderIntentValidated,
	domain.EventOrderIntentRejected,
	domain.EventOrderSubmitted,
	domain.EventOrderAccepted,
	domain.EventOrderRejected,
	domain.EventFillReceived,
	domain.EventPositionUpdated,
	domain.EventKillSwitchEngaged,
	domain.EventCircuitBreakerTripped,
	domain.EventStrategySignalLifecycle,
	domain.EventStrategyStateSnapshot,
}
// wireEvent is the JSON shape sent over SSE — mirrors the dashboard's DomainEvent type.
type wireEvent struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	TenantID       string `json:"tenantId"`
	EnvMode        string `json:"envMode"`
	OccurredAt     string `json:"occurredAt"`
	IdempotencyKey string `json:"idempotencyKey"`
	Payload        any    `json:"payload"`
}

// client represents a single connected SSE consumer.
type client struct {
	ch chan wireEvent
}

// Handler is an http.Handler that streams domain events as SSE.
type Handler struct {
	bus     ports.EventBusPort
	mu      sync.RWMutex
	clients map[*client]struct{}
	log     zerolog.Logger
}

// NewHandler creates an SSE Handler and subscribes to every domain event type
// on the provided bus.  Call Register() on your mux to expose it, then Start()
// inside a goroutine to process events.
func NewHandler(bus ports.EventBusPort, log zerolog.Logger) *Handler {
	return &Handler{
		bus:     bus,
		clients: make(map[*client]struct{}),
		log:     log,
	}
}

// Start subscribes to all domain event types and begins forwarding events to
// connected clients.  It blocks until ctx is cancelled.
func (h *Handler) Start(ctx context.Context) error {
	for _, et := range eventTypes {
		et := et // capture loop variable
		if err := h.bus.Subscribe(ctx, et, func(_ context.Context, evt domain.Event) error {
			w := wireEvent{
				ID:             evt.ID,
				Type:           evt.Type,
				TenantID:       evt.TenantID,
				EnvMode:        string(evt.EnvMode),
				OccurredAt:     evt.OccurredAt.UTC().Format(time.RFC3339Nano),
				IdempotencyKey: evt.IdempotencyKey,
				Payload:        evt.Payload,
			}
			h.broadcast(w)
			return nil
		}); err != nil {
			return fmt.Errorf("sse: subscribe to %s: %w", et, err)
		}
	}
	h.log.Info().Int("event_types", len(eventTypes)).Msg("handler started, subscribed to all event types")
	<-ctx.Done()
	return nil
}

// broadcast sends an event to every connected client, dropping slow clients.
func (h *Handler) broadcast(evt wireEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.ch <- evt:
		default:
			// Client channel full — skip to avoid blocking the event bus.
			h.log.Warn().Str("event_type", evt.Type).Msg("SSE client channel full, dropping event for slow consumer")
		}
	}
}

// ServeHTTP handles an SSE connection.  It keeps the connection alive with
// periodic keep-alive pings and flushes each event as it arrives.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS headers so the dashboard can connect from any origin in development.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	c := &client{ch: make(chan wireEvent, 64)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	clientCount := len(h.clients)
	h.mu.Unlock()

	h.log.Info().Str("remote", r.RemoteAddr).Int("total_clients", clientCount).Msg("SSE client connected")

	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		remaining := len(h.clients)
		h.mu.Unlock()
		h.log.Info().Str("remote", r.RemoteAddr).Int("total_clients", remaining).Msg("SSE client disconnected")
	}()

	ctx := r.Context()
	keepAlive := time.NewTicker(30 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-keepAlive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case evt := <-c.ch:
			data, err := json.Marshal(evt)
			if err != nil {
				h.log.Error().Err(err).Str("event_type", evt.Type).Msg("failed to marshal SSE event")
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
			flusher.Flush()
		}
	}
}
