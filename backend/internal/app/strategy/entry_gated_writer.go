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
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	reason := payload.BlockingGate
	if payload.BlockingDetail != "" {
		reason = payload.BlockingGate + ": " + payload.BlockingDetail
	}
	// SignalID must be unique enough to avoid primary-key collisions across
	// bars. Bar-time + strategy + symbol is sufficient: emitEarlyGated
	// dedups on LastGatedBarTime, so at most one event per bar per symbol
	// per strategy instance reaches here.
	signalID := fmt.Sprintf("gated:%s:%s:%d", payload.Strategy, payload.Symbol, ev.OccurredAt.UnixNano())
	side := inferSideFromBias(payload.Indicators.AVWAPBias)
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

// inferSideFromBias maps the AVWAPBias string onto the same BUY/SELL
// vocabulary strategy-emitted signals use, so dashboard filters that key on
// Side still render blocked rows. Empty bias (e.g. MACD setups that don't
// populate it) falls back to BUY because MACD today is a long-only setup.
func inferSideFromBias(bias string) string {
	switch bias {
	case "SHORT":
		return "SELL"
	default:
		return "BUY"
	}
}
