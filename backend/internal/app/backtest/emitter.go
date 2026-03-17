// Package backtest — emitter.go provides an SSE emitter that bridges a
// backtest's isolated event bus to connected HTTP clients.  Each backtest
// Runner creates its own Emitter so events never leak to the live bus.
package backtest

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

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// SSEEvent is the wire format sent to SSE clients.
type SSEEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// emitterClient represents a single connected SSE consumer.
type emitterClient struct {
	ch chan SSEEvent
}

// Emitter fans out backtest domain events to connected HTTP SSE clients.
type SnapshotFn func(symbol string) (domain.IndicatorSnapshot, bool)

type Emitter struct {
	mu            sync.RWMutex
	clients       map[*emitterClient]struct{}
	log           zerolog.Logger
	baseTimeframe domain.Timeframe
	snapshotFn    SnapshotFn

	historyMu sync.Mutex
	history   []SSEEvent
}

// NewEmitter creates a new Emitter.
func NewEmitter(log zerolog.Logger, baseTimeframe domain.Timeframe) *Emitter {
	return &Emitter{
		clients:       make(map[*emitterClient]struct{}),
		log:           log.With().Str("component", "backtest_emitter").Logger(),
		baseTimeframe: baseTimeframe,
	}
}

func (e *Emitter) SetSnapshotFn(fn SnapshotFn) {
	e.snapshotFn = fn
}

// Subscribe wires up domain event listeners on the given (isolated) event bus
// and converts them to typed SSE events for connected clients.
func (e *Emitter) Subscribe(ctx context.Context, bus ports.EventBusPort) error {
	type sub struct {
		eventType domain.EventType
		handler   func(context.Context, domain.Event) error
	}

	subs := []sub{
		{domain.EventMarketBarSanitized, e.onCandle},
		{domain.EventSignalCreated, e.onSignal},
		{domain.EventSignalEnriched, e.onSignalEnriched},
		{domain.EventFillReceived, e.onTrade},
		{domain.EventOrderIntentCreated, e.onIntent},
		{domain.EventOrderIntentRejected, e.onIntentRejected},
	}

	for _, s := range subs {
		if err := bus.Subscribe(ctx, s.eventType, s.handler); err != nil {
			return fmt.Errorf("backtest emitter: subscribe %s: %w", s.eventType, err)
		}
	}
	return nil
}

func (e *Emitter) onCandle(_ context.Context, ev domain.Event) error {
	bar, ok := ev.Payload.(domain.MarketBar)
	if !ok {
		return nil
	}
	if e.baseTimeframe != "" && bar.Timeframe != e.baseTimeframe {
		return nil
	}
	data := map[string]any{
		"time":      bar.Time.Unix(),
		"symbol":    string(bar.Symbol),
		"timeframe": string(bar.Timeframe),
		"open":      bar.Open,
		"high":      bar.High,
		"low":       bar.Low,
		"close":     bar.Close,
		"volume":    bar.Volume,
	}
	fn := e.snapshotFn
	if fn != nil {
		if snap, ok := fn(string(bar.Symbol)); ok {
			data["ema9"] = snap.EMA9
			data["ema21"] = snap.EMA21
			if snap.EMA50 > 0 {
				data["ema50"] = snap.EMA50
			}
			if snap.EMA200 > 0 {
				data["ema200"] = snap.EMA200
			}
		}
	}
	e.Emit(SSEEvent{Type: "backtest:candle", Data: data})
	return nil
}

func (e *Emitter) onSignal(_ context.Context, ev domain.Event) error {
	sig, ok := ev.Payload.(start.Signal)
	if !ok {
		return nil
	}
	side := "buy"
	if sig.Side == start.SideSell {
		side = "sell"
	}
	kind := "entry"
	if sig.Type == start.SignalExit {
		kind = "exit"
	}
	e.Emit(SSEEvent{Type: "backtest:signal", Data: map[string]any{
		"time":     ev.OccurredAt.Unix(),
		"symbol":   sig.Symbol,
		"side":     side,
		"kind":     kind,
		"strategy": string(sig.StrategyInstanceID),
		"strength": sig.Strength,
	}})
	return nil
}

func (e *Emitter) onSignalEnriched(_ context.Context, ev domain.Event) error {
	enrichment, ok := ev.Payload.(domain.SignalEnrichment)
	if !ok {
		return nil
	}
	e.Emit(SSEEvent{Type: "backtest:signal_enriched", Data: map[string]any{
		"symbol":     enrichment.Signal.Symbol,
		"strategy":   enrichment.Signal.StrategyInstanceID,
		"status":     string(enrichment.Status),
		"confidence": enrichment.Confidence,
		"direction":  enrichment.Direction.String(),
		"rationale":  enrichment.Rationale,
	}})
	return nil
}

func (e *Emitter) onTrade(_ context.Context, ev domain.Event) error {
	payload, ok := ev.Payload.(map[string]any)
	if !ok {
		return nil
	}
	e.Emit(SSEEvent{Type: "backtest:trade", Data: payload})
	return nil
}

func (e *Emitter) onIntent(_ context.Context, ev domain.Event) error {
	intent, ok := ev.Payload.(domain.OrderIntent)
	if !ok {
		return nil
	}
	e.Emit(SSEEvent{Type: "backtest:intent", Data: map[string]any{
		"intent_id":  intent.ID.String(),
		"symbol":     string(intent.Symbol),
		"direction":  intent.Direction.String(),
		"quantity":   intent.Quantity,
		"limit":      intent.LimitPrice,
		"stop":       intent.StopLoss,
		"confidence": intent.Confidence,
	}})
	return nil
}

func (e *Emitter) onIntentRejected(_ context.Context, ev domain.Event) error {
	e.Emit(SSEEvent{Type: "backtest:intent_rejected", Data: ev.Payload})
	return nil
}

// Emit broadcasts an SSE event to all connected clients (non-blocking).
func (e *Emitter) Emit(evt SSEEvent) {
	e.historyMu.Lock()
	e.history = append(e.history, evt)
	e.historyMu.Unlock()

	e.mu.RLock()
	defer e.mu.RUnlock()
	for c := range e.clients {
		select {
		case c.ch <- evt:
		default:
		}
	}
}

// EmitProgress sends a progress update to all connected clients.
func (e *Emitter) EmitProgress(p *ProgressInfo) {
	e.Emit(SSEEvent{Type: "backtest:progress", Data: p})
}

// EmitMetrics sends current backtest metrics to all connected clients.
func (e *Emitter) EmitMetrics(m map[string]any) {
	e.Emit(SSEEvent{Type: "backtest:metrics", Data: m})
}

// EmitComplete sends the final backtest result to all connected clients.
func (e *Emitter) EmitComplete(result *Result) {
	e.Emit(SSEEvent{Type: "backtest:complete", Data: map[string]any{
		"total_trades":     result.TradeCount,
		"final_equity":     result.FinalEquity,
		"total_return_pct": result.TotalReturn,
		"total_pnl":        result.TotalPnL,
		"sharpe_ratio":     result.SharpeRatio,
		"max_drawdown_pct": result.MaxDrawdown,
		"win_rate_pct":     result.WinRate,
		"profit_factor":    result.ProfitFactor,
	}})
}

// ClientCount returns the number of connected SSE clients.
func (e *Emitter) ClientCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.clients)
}

// ServeHTTP handles an SSE connection for this backtest's event stream.
func (e *Emitter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	c := &emitterClient{ch: make(chan SSEEvent, 4096)}
	e.mu.Lock()
	e.clients[c] = struct{}{}
	clientCount := len(e.clients)
	e.mu.Unlock()

	e.log.Info().Str("remote", r.RemoteAddr).Int("total_clients", clientCount).Msg("backtest SSE client connected")

	fmt.Fprintf(w, ": keepalive\n\n")
	flusher.Flush()

	e.historyMu.Lock()
	replay := make([]SSEEvent, len(e.history))
	copy(replay, e.history)
	e.historyMu.Unlock()

	for _, evt := range replay {
		data, err := json.Marshal(evt)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
	}
	if len(replay) > 0 {
		flusher.Flush()
	}

	defer func() {
		e.mu.Lock()
		delete(e.clients, c)
		remaining := len(e.clients)
		e.mu.Unlock()
		e.log.Info().Str("remote", r.RemoteAddr).Int("total_clients", remaining).Msg("backtest SSE client disconnected")
	}()

	ctx := r.Context()
	keepAlive := time.NewTicker(15 * time.Second)
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
				e.log.Error().Err(err).Str("event_type", evt.Type).Msg("failed to marshal backtest SSE event")
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
			flusher.Flush()
		}
	}
}
