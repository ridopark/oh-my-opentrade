package notify

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Service subscribes to trading-relevant events on the event bus and fans out
// notifications to all configured NotifierPort implementations (Telegram, Discord, etc.).
type Service struct {
	eventBus ports.EventBusPort
	notifier ports.NotifierPort
	log      zerolog.Logger
}

// NewService creates a new notification Service.
// notifier is expected to be a MultiNotifier (or any NotifierPort).
func NewService(eventBus ports.EventBusPort, notifier ports.NotifierPort, log zerolog.Logger) *Service {
	return &Service{
		eventBus: eventBus,
		notifier: notifier,
		log:      log,
	}
}

// Start subscribes the service to order-lifecycle and safety events on the event bus.
func (s *Service) Start(ctx context.Context) error {
	events := []struct {
		eventType string
		formatter func(domain.Event) string
	}{
		{domain.EventOrderSubmitted, s.fmtOrderSubmitted},
		{domain.EventOrderAccepted, s.fmtOrderAccepted},
		{domain.EventOrderRejected, s.fmtOrderRejected},
		{domain.EventOrderIntentRejected, s.fmtOrderIntentRejected},
		{domain.EventFillReceived, s.fmtFillReceived},
		{domain.EventKillSwitchEngaged, s.fmtKillSwitch},
		{domain.EventCircuitBreakerTripped, s.fmtCircuitBreaker},
		{domain.EventDebateCompleted, s.fmtDebateCompleted},
	}

	for _, e := range events {
		formatter := e.formatter // capture for closure
		eventType := e.eventType
		handler := func(ctx context.Context, ev domain.Event) error {
			msg := formatter(ev)
			if err := s.notifier.Notify(ctx, ev.TenantID, msg); err != nil {
				s.log.Warn().Err(err).Str("event", eventType).Msg("notification failed")
			}
			return nil // notification failures are non-fatal
		}
		if err := s.eventBus.Subscribe(ctx, eventType, handler); err != nil {
			return fmt.Errorf("notify: failed to subscribe to %s: %w", eventType, err)
		}
	}
	s.log.Info().Int("event_types", len(events)).Msg("notification service subscribed to events")
	return nil
}

func (s *Service) fmtOrderSubmitted(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.OrderIntentEventPayload); ok {
		return fmt.Sprintf("📤 Order Submitted: %s %s @ $%.2f (qty: %.2f)",
			p.Direction, p.Symbol, p.LimitPrice, p.Quantity)
	}
	return "📤 Order Submitted"
}

func (s *Service) fmtOrderAccepted(ev domain.Event) string {
	return "✅ Order Accepted by broker"
}

func (s *Service) fmtOrderRejected(ev domain.Event) string {
	if reason, ok := ev.Payload.(string); ok {
		return fmt.Sprintf("❌ Order Rejected: %s", reason)
	}
	return "❌ Order Rejected"
}

func (s *Service) fmtOrderIntentRejected(ev domain.Event) string {
	if p, ok := ev.Payload.(domain.OrderIntentEventPayload); ok {
		if p.Reason != "" {
			return fmt.Sprintf("⚠️ Intent Rejected: %s %s — %s", p.Direction, p.Symbol, p.Reason)
		}
		return fmt.Sprintf("⚠️ Intent Rejected: %s %s — failed risk/slippage check",
			p.Direction, p.Symbol)
	}
	return "⚠️ Order Intent Rejected (risk/slippage check failed)"
}

func (s *Service) fmtFillReceived(ev domain.Event) string {
	if m, ok := ev.Payload.(map[string]any); ok {
		sym, _ := m["symbol"].(string)
		side, _ := m["side"].(string)
		qty, _ := m["quantity"].(float64)
		price, _ := m["price"].(float64)
		return fmt.Sprintf("💰 Fill: %s %s — %.2f shares @ $%.2f", side, sym, qty, price)
	}
	return "💰 Order Filled"
}

func (s *Service) fmtKillSwitch(ev domain.Event) string {
	return "🚨 KILL SWITCH ENGAGED — Trading halted for symbol"
}

func (s *Service) fmtCircuitBreaker(ev domain.Event) string {
	if reason, ok := ev.Payload.(string); ok {
		return fmt.Sprintf("🔴 CIRCUIT BREAKER TRIPPED: %s", reason)
	}
	return "🔴 CIRCUIT BREAKER TRIPPED — System-wide trading halt"
}

func (s *Service) fmtDebateCompleted(ev domain.Event) string {
	return "🤖 AI Debate completed"
}
