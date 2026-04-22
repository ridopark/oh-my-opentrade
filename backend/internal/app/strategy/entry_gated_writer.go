package strategy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// EntryGatedWriter persists EntryGated events to the strategy_signal_events
// table with status=blocked. Wired as an async subscriber on the event bus so
// the strategy runner's publish path never blocks on DB latency — the writer
// runs in its own goroutine dispatched by the bus's SubscribeAsync path.
type EntryGatedWriter struct {
	repo ports.PnLPort
	log  zerolog.Logger
}

func NewEntryGatedWriter(repo ports.PnLPort, log zerolog.Logger) *EntryGatedWriter {
	return &EntryGatedWriter{
		repo: repo,
		log:  log.With().Str("component", "entry_gated_writer").Logger(),
	}
}

// Handle converts an EventEntryGated payload into a StrategySignalEvent row
// and appends it. Returns nil on malformed payload or DB failure so a failing
// write never back-pressures the strategy publisher.
func (w *EntryGatedWriter) Handle(ctx context.Context, ev domain.Event) error {
	if w == nil || w.repo == nil {
		return nil
	}
	payload, ok := ev.Payload.(domain.EntryGatedPayload)
	if !ok {
		w.log.Warn().Str("event_type", ev.Type).Msg("EntryGated subscriber received non-EntryGated payload")
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		w.log.Warn().Err(err).Msg("EntryGated payload marshal failed")
		return nil
	}
	reason := payload.BlockingGate
	if payload.BlockingDetail != "" {
		reason = payload.BlockingGate + ": " + payload.BlockingDetail
	}
	// SignalID is unique per emitted event; ev.OccurredAt is event-creation
	// time, not bar time, so duplicates are possible when FlushSignalProgress
	// re-emits a cached EntryGated on startup. Those duplicates render as
	// distinct rows at distinct timestamps, which is acceptable for an
	// append-only audit log — operators see "latest bar eval at 09:35" plus
	// "same bar re-seeded at 09:40 startup" as two separate decisions.
	signalID := fmt.Sprintf("gated:%s:%s:%d", payload.Strategy, payload.Symbol, ev.OccurredAt.UnixNano())
	side := sideFromAVWAPBias(payload.Indicators.AVWAPBias)
	confidence := 0.0
	if payload.Confluence.MaxScore > 0 {
		confidence = float64(payload.Confluence.Score) / float64(payload.Confluence.MaxScore)
	}
	evt, err := domain.NewStrategySignalEvent(
		ev.OccurredAt,
		ev.TenantID,
		ev.EnvMode,
		payload.Strategy,
		signalID,
		payload.Symbol,
		"entry",
		side,
		domain.SignalStatusBlocked,
		reason,
		confidence,
		raw,
	)
	if err != nil {
		w.log.Warn().Err(err).
			Str("symbol", payload.Symbol).
			Str("strategy", payload.Strategy).
			Msg("failed to build StrategySignalEvent from EntryGated payload")
		return nil
	}
	if saveErr := w.repo.SaveStrategySignalEvent(ctx, evt); saveErr != nil {
		w.log.Debug().Err(saveErr).
			Str("symbol", payload.Symbol).
			Str("strategy", payload.Strategy).
			Msg("failed to persist EntryGated — dropping")
	}
	return nil
}

// sideFromAVWAPBias maps the AVWAPBias string to the BUY/SELL vocabulary.
// Returns empty for setups without a populated bias (e.g. MACD, which is
// long-only today but may not be tomorrow) — the dashboard renders a dash
// and downstream filters can ignore the row rather than wrongly counting it
// as a long attempt.
func sideFromAVWAPBias(bias string) string {
	switch bias {
	case "LONG":
		return "BUY"
	case "SHORT":
		return "SELL"
	default:
		return ""
	}
}
