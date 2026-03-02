package execution

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// Default order parameters used when converting a SetupCondition into an OrderIntent.
// These will be replaced by strategy DNA configuration in a future phase.
const (
	defaultLimitPrice     = 50000.0
	defaultStopLoss       = 49000.0
	defaultMaxSlippageBPS = 10
	defaultQuantity       = 1.0
	defaultConfidence     = 1.0
)

// Service is the execution application service.
// It subscribes to SetupDetected events and runs each through a validation
// pipeline: kill switch → risk → slippage → broker submission.
type Service struct {
	eventBus      ports.EventBusPort
	broker        ports.BrokerPort
	repo          ports.RepositoryPort
	riskEngine    *RiskEngine
	slippageGuard *SlippageGuard
	killSwitch    *KillSwitch
	accountEquity float64
}

// NewService creates a new execution Service.
func NewService(
	eventBus ports.EventBusPort,
	broker ports.BrokerPort,
	repo ports.RepositoryPort,
	riskEngine *RiskEngine,
	slippageGuard *SlippageGuard,
	killSwitch *KillSwitch,
	accountEquity float64,
) *Service {
	return &Service{
		eventBus:      eventBus,
		broker:        broker,
		repo:          repo,
		riskEngine:    riskEngine,
		slippageGuard: slippageGuard,
		killSwitch:    killSwitch,
		accountEquity: accountEquity,
	}
}

// Start subscribes the service to SetupDetected events on the event bus.
func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.Subscribe(ctx, domain.EventSetupDetected, s.handleSetup); err != nil {
		return fmt.Errorf("execution: failed to subscribe to SetupDetected: %w", err)
	}
	return nil
}

// handleSetup processes a single SetupDetected event through the execution pipeline.
func (s *Service) handleSetup(ctx context.Context, event domain.Event) error {
	setup, ok := event.Payload.(monitor.SetupCondition)
	if !ok {
		return nil
	}

	// 1. Check kill switch before any work.
	if s.killSwitch.IsHalted(event.TenantID, setup.Symbol) {
		s.emit(ctx, domain.EventKillSwitchEngaged, event.TenantID, event.EnvMode, event.IdempotencyKey, nil)
		return nil
	}

	// 2. Create order intent from setup condition.
	intentID := uuid.New()
	intent, err := domain.NewOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		setup.Symbol,
		setup.Direction,
		defaultLimitPrice,
		defaultStopLoss,
		defaultMaxSlippageBPS,
		defaultQuantity,
		"strategy",
		"rationale",
		defaultConfidence,
		intentID.String(),
	)
	if err != nil {
		return nil
	}
	s.emit(ctx, domain.EventOrderIntentCreated, event.TenantID, event.EnvMode, intentID.String(), intent)

	// 3. Validate risk.
	if err := s.riskEngine.Validate(intent, s.accountEquity); err != nil {
		s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intentID.String(), err.Error())
		return nil
	}

	// 4. Validate slippage.
	if err := s.slippageGuard.Check(ctx, intent); err != nil {
		s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intentID.String(), err.Error())
		return nil
	}
	s.emit(ctx, domain.EventOrderIntentValidated, event.TenantID, event.EnvMode, intentID.String(), intent)

	// 5. Record stop — if this trips the circuit breaker, abort before broker submission.
	if err := s.killSwitch.RecordStop(event.TenantID, setup.Symbol); err != nil {
		s.emit(ctx, domain.EventCircuitBreakerTripped, event.TenantID, event.EnvMode, event.IdempotencyKey, err.Error())
		return nil
	}

	// 6. Submit to broker.
	if _, err := s.broker.SubmitOrder(ctx, intent); err != nil {
		s.emit(ctx, domain.EventOrderRejected, event.TenantID, event.EnvMode, intentID.String(), err.Error())
		return nil
	}
	s.emit(ctx, domain.EventOrderSubmitted, event.TenantID, event.EnvMode, intentID.String(), intent)

	return nil
}

// emit publishes a domain event on the event bus, discarding creation/publish errors
// (events are best-effort; the pipeline should not fail due to event emission).
func (s *Service) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = s.eventBus.Publish(ctx, *ev)
}
