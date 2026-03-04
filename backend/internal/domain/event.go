package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// EventType identifies the kind of domain event.
type EventType = string

// Domain event type constants covering the full trading pipeline.
const (
	EventMarketBarReceived     EventType = "MarketBarReceived"
	EventMarketBarSanitized    EventType = "MarketBarSanitized"
	EventMarketBarRejected     EventType = "MarketBarRejected"
	EventStateUpdated          EventType = "StateUpdated"
	EventRegimeShifted         EventType = "RegimeShifted"
	EventSetupDetected         EventType = "SetupDetected"
	EventDebateRequested       EventType = "DebateRequested"
	EventDebateCompleted       EventType = "DebateCompleted"
	EventOrderIntentCreated    EventType = "OrderIntentCreated"
	EventOrderIntentValidated  EventType = "OrderIntentValidated"
	EventOrderIntentRejected   EventType = "OrderIntentRejected"
	EventOrderSubmitted        EventType = "OrderSubmitted"
	EventOrderAccepted         EventType = "OrderAccepted"
	EventOrderRejected         EventType = "OrderRejected"
	EventFillReceived          EventType = "FillReceived"
	EventPositionUpdated       EventType = "PositionUpdated"
	EventKillSwitchEngaged     EventType = "KillSwitchEngaged"
	EventCircuitBreakerTripped  EventType = "CircuitBreakerTripped"
	EventOptionChainReceived    EventType = "OptionChainReceived"
	EventOptionContractSelected EventType = "OptionContractSelected"
)

// Event represents a domain event in the trading pipeline.
// Events are immutable once created and carry an idempotency key
// to support exactly-once processing semantics.
type Event struct {
	ID             string
	Type           EventType
	TenantID       string
	EnvMode        EnvMode
	OccurredAt     time.Time
	IdempotencyKey string
	Payload        any
}

// NewEvent creates an Event with auto-generated ID and timestamp.
func NewEvent(eventType EventType, tenantID string, envMode EnvMode, idempotencyKey string, payload any) (*Event, error) {
	if eventType == "" {
		return nil, errors.New("event type is required")
	}
	if idempotencyKey == "" {
		return nil, errors.New("idempotency key is required")
	}
	return &Event{
		ID:             uuid.NewString(),
		Type:           eventType,
		TenantID:       tenantID,
		EnvMode:        envMode,
		OccurredAt:     time.Now(),
		IdempotencyKey: idempotencyKey,
		Payload:        payload,
	}, nil
}
