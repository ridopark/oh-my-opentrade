package debate

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Default order parameters used when generating an OrderIntent from an AI decision.
// These will be replaced by strategy DNA configuration in a future phase.
const (
	defaultLimitPrice     = 50000.0
	defaultStopLoss       = 49000.0
	defaultMaxSlippageBPS = 10
	defaultQuantity       = 1.0
)

// defaultAdvisorTimeout is the maximum time the service will wait for the LLM
// to return a debate result. Free-tier endpoints can be very slow under load;
// this hard cap prevents a slow LLM from causing trade slippage.
const defaultAdvisorTimeout = 5 * time.Second

// Service is the debate application service.
// It subscribes to SetupDetected events, runs each setup through the AI adversarial debate,
// and emits DebateRequested, DebateCompleted, and (conditionally) OrderIntentCreated events.
type Service struct {
	eventBus       ports.EventBusPort
	aiAdvisor      ports.AIAdvisorPort
	repo           ports.RepositoryPort
	minConfidence  float64
	advisorTimeout time.Duration
	log            zerolog.Logger
}

// Option is a functional option for Service.
type Option func(*Service)

// WithAdvisorTimeout sets the maximum duration to wait for the AI advisor to respond.
// If the advisor does not return within this duration, the debate is skipped (non-fatal).
func WithAdvisorTimeout(d time.Duration) Option {
	return func(s *Service) { s.advisorTimeout = d }
}

// NewService creates a new debate Service.
// minConfidence is the minimum AI confidence [0,1] required to emit an OrderIntentCreated event.
// opts are functional options (e.g. WithAdvisorTimeout).
func NewService(eventBus ports.EventBusPort, aiAdvisor ports.AIAdvisorPort, repo ports.RepositoryPort, minConfidence float64, log zerolog.Logger, opts ...Option) *Service {
	s := &Service{
		eventBus:       eventBus,
		aiAdvisor:      aiAdvisor,
		repo:           repo,
		minConfidence:  minConfidence,
		advisorTimeout: defaultAdvisorTimeout,
		log:            log,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start subscribes the service to SetupDetected events on the event bus.
func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.Subscribe(ctx, domain.EventSetupDetected, s.handleSetup); err != nil {
		return fmt.Errorf("debate: failed to subscribe to SetupDetected: %w", err)
	}
	s.log.Info().Msg("subscribed to SetupDetected events")
	return nil
}

// handleSetup processes a single SetupDetected event through the AI debate pipeline.
func (s *Service) handleSetup(ctx context.Context, event domain.Event) error {
	setup, ok := event.Payload.(monitor.SetupCondition)
	if !ok {
		return nil
	}

	l := s.log.With().
		Str("symbol", string(setup.Symbol)).
		Str("idempotency_key", event.IdempotencyKey).
		Logger()

	// 1. Emit DebateRequested to signal the debate is starting.
	s.emit(ctx, domain.EventDebateRequested, event.TenantID, event.EnvMode, event.IdempotencyKey+"-debate-requested", setup)
	l.Info().Msg("debate requested, querying AI advisor")

	// 2. Call AI advisor — capped by advisorTimeout so a slow free-tier LLM
	// cannot delay order execution or cause slippage.
	advCtx, advCancel := context.WithTimeout(ctx, s.advisorTimeout)
	defer advCancel()
	decision, err := s.aiAdvisor.RequestDebate(advCtx, setup.Symbol, setup.Regime, setup.Snapshot)
	if err != nil {
		l.Error().Err(err).Msg("AI advisor error — skipping debate")
		return nil
	}

	l.Info().
		Float64("confidence", decision.Confidence).
		Str("direction", string(decision.Direction)).
		Msg("AI debate completed")

	// 3. Emit DebateCompleted with the full AI decision payload.
	s.emit(ctx, domain.EventDebateCompleted, event.TenantID, event.EnvMode, event.IdempotencyKey+"-debate-completed", decision)

	// 4. Only proceed to execution if confidence meets the minimum threshold.
	if decision.Confidence < s.minConfidence {
		l.Info().
			Float64("confidence", decision.Confidence).
			Float64("min_confidence", s.minConfidence).
			Msg("confidence below threshold — not creating order intent")
		return nil
	}

	// 5. Build an enriched OrderIntent using the AI direction and rationale.
	intentID := uuid.New()
	intent, err := domain.NewOrderIntent(
		intentID,
		event.TenantID,
		event.EnvMode,
		setup.Symbol,
		decision.Direction,
		defaultLimitPrice,
		defaultStopLoss,
		defaultMaxSlippageBPS,
		defaultQuantity,
		"debate",
		decision.Rationale,
		decision.Confidence,
		intentID.String(),
	)
	if err != nil {
		l.Error().Err(err).Msg("failed to create order intent from AI decision")
		return nil
	}

	l.Info().Str("intent_id", intentID.String()).Msg("order intent created from AI debate")
	s.emit(ctx, domain.EventOrderIntentCreated, event.TenantID, event.EnvMode, intentID.String(), intent)

	// 6. Persist thought log for historical audit.
	if s.repo != nil {
		tl := domain.ThoughtLog{
			Time:           time.Now().UTC(),
			TenantID:       event.TenantID,
			EnvMode:        event.EnvMode,
			Symbol:         setup.Symbol,
			EventType:      "DebateCompleted",
			Direction:      string(decision.Direction),
			Confidence:     decision.Confidence,
			BullArgument:   decision.BullArgument,
			BearArgument:   decision.BearArgument,
			JudgeReasoning: decision.JudgeReasoning,
			Rationale:      decision.Rationale,
			IntentID:       intentID.String(),
		}
		if err := s.repo.SaveThoughtLog(ctx, tl); err != nil {
			l.Error().Err(err).Msg("failed to save thought log")
		}
	}

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
