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
	EventMarketBarReceived       EventType = "MarketBarReceived"
	EventMarketBarSanitized      EventType = "MarketBarSanitized"
	EventMarketBarRejected       EventType = "MarketBarRejected"
	EventStateUpdated            EventType = "StateUpdated"
	EventRegimeShifted           EventType = "RegimeShifted"
	EventSetupDetected           EventType = "SetupDetected"
	EventDNAVersionDetected      EventType = "DNAVersionDetected"
	EventDNAApprovalRequested    EventType = "DNAApprovalRequested"
	EventDNAApproved             EventType = "DNAApproved"
	EventDNARejected             EventType = "DNARejected"
	EventActiveDNAChanged        EventType = "ActiveDNAChanged"
	EventDebateRequested         EventType = "DebateRequested"
	EventDebateCompleted         EventType = "DebateCompleted"
	EventOrderIntentCreated      EventType = "OrderIntentCreated"
	EventOrderIntentValidated    EventType = "OrderIntentValidated"
	EventOrderIntentRejected     EventType = "OrderIntentRejected"
	EventOrderSubmitted          EventType = "OrderSubmitted"
	EventOrderAccepted           EventType = "OrderAccepted"
	EventOrderRejected           EventType = "OrderRejected"
	EventFillReceived            EventType = "FillReceived"
	EventPositionUpdated         EventType = "PositionUpdated"
	EventKillSwitchEngaged       EventType = "KillSwitchEngaged"
	EventCircuitBreakerTripped   EventType = "CircuitBreakerTripped"
	EventOptionChainReceived     EventType = "OptionChainReceived"
	EventOptionContractSelected  EventType = "OptionContractSelected"
	EventSignalCreated           EventType = "SignalCreated"
	EventSignalDebateRequested   EventType = "SignalDebateRequested"
	EventSignalEnriched          EventType = "SignalEnriched"
	EventScreenerTicked          EventType = "ScreenerTicked"
	EventScreenerCompleted       EventType = "ScreenerCompleted"
	EventAIScreenerCompleted     EventType = "AIScreenerCompleted"
	EventEffectiveSymbolsUpdated EventType = "EffectiveSymbolsUpdated"
	EventStrategySignalLifecycle EventType = "StrategySignalLifecycle"
	EventStrategyStateSnapshot   EventType = "StrategyStateSnapshot"
	EventExitTriggered           EventType = "ExitTriggered"
	EventExitOrderTerminal       EventType = "ExitOrderTerminal"
	EventRiskRevaluated          EventType = "RiskRevaluated"
	EventRiskDowngraded          EventType = "RiskDowngraded"
	EventSignalGated             EventType = "SignalGated"
	EventTradeReceived           EventType = "TradeReceived"
	EventTradeRealized           EventType = "TradeRealized"
	EventFormingBar              EventType = "FormingBar"
	EventFeedDegraded            EventType = "FeedDegraded"
	EventExitCircuitBroken       EventType = "ExitCircuitBroken"

	// Connectivity & system events.
	EventBrokerAPIError          EventType = "BrokerAPIError"
	EventWSCircuitBreakerTripped EventType = "WSCircuitBreakerTripped"
	EventFillPollTimeout         EventType = "FillPollTimeout"
	EventStaleOrderCancelled     EventType = "StaleOrderCancelled"
	EventSystemStarted           EventType = "SystemStarted"
	EventSymbolsActivated        EventType = "SymbolsActivated"
)

type SymbolsActivatedPayload struct {
	Symbols []string
	Source  string
}

type FeedDegradedPayload struct {
	Feed   string
	Reason string
}

type BrokerAPIErrorPayload struct {
	Endpoint   string
	StatusCode int
	Message    string
}

type WSCircuitBreakerTrippedPayload struct {
	Feed              string
	ConsecutiveFails  int
	BlockedForSeconds float64
}

type FillPollTimeoutPayload struct {
	Symbol        Symbol
	BrokerOrderID string
	Strategy      string
	Direction     string
	Quantity      float64
}

type StaleOrderCancelledPayload struct {
	Symbol        Symbol
	BrokerOrderID string
	Strategy      string
	Direction     string
	AgeSeconds    float64
}

type ExitCircuitBrokenPayload struct {
	Symbol       Symbol
	Failures     int
	CooldownSecs float64
}

type SystemStartedPayload struct {
	Version         string
	EnvMode         string
	Broker          string
	Symbols         []string
	EquityCount     int
	CryptoCount     int
	IBKRConnected   bool
	IBKRPaperMode   bool
	EMA200Succeeded int
	EMA200Failed    []string
	Strategies      []string
	StrategySymbols map[string][]string
}

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
