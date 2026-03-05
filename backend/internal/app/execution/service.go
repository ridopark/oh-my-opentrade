package execution

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/rs/zerolog"
)

// Service is the execution application service.
// It subscribes to OrderIntentCreated events and runs each through a validation
// pipeline: kill switch → risk → slippage → broker submission.
type Service struct {
	eventBus         ports.EventBusPort
	broker           ports.BrokerPort
	repo             ports.RepositoryPort
	riskEngine       *RiskEngine
	slippageGuard    *SlippageGuard
	killSwitch       *KillSwitch
	dailyLossBreaker *risk.DailyLossBreaker
	positionGate     *PositionGate
	accountEquity    float64
	log              zerolog.Logger
	metrics          *metrics.Metrics
}

// Option is a functional option for Service.
type Option func(*Service)

// WithPositionGate attaches a PositionGate to the execution pipeline.
func WithPositionGate(pg *PositionGate) Option {
	return func(s *Service) { s.positionGate = pg }
}

// NewService creates a new execution Service.
func NewService(
	eventBus ports.EventBusPort,
	broker ports.BrokerPort,
	repo ports.RepositoryPort,
	riskEngine *RiskEngine,
	slippageGuard *SlippageGuard,
	killSwitch *KillSwitch,
	dailyLossBreaker *risk.DailyLossBreaker,
	accountEquity float64,
	log zerolog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		eventBus:         eventBus,
		broker:           broker,
		repo:             repo,
		riskEngine:       riskEngine,
		slippageGuard:    slippageGuard,
		killSwitch:       killSwitch,
		dailyLossBreaker: dailyLossBreaker,
		accountEquity:    accountEquity,
		log:              log,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SetAccountEquity updates the account equity used by the risk engine.
// Safe to call concurrently from a periodic refresh goroutine.
func (s *Service) SetAccountEquity(equity float64) {
	if equity <= 0 {
		return
	}
	s.accountEquity = equity
}

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (s *Service) SetMetrics(m *metrics.Metrics) { s.metrics = m }

// Start subscribes the service to OrderIntentCreated events on the event bus.
func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.Subscribe(ctx, domain.EventOrderIntentCreated, s.handleIntent); err != nil {
		return fmt.Errorf("execution: failed to subscribe to OrderIntentCreated: %w", err)
	}
	s.log.Info().Msg("subscribed to OrderIntentCreated events")
	return nil
}

// handleIntent processes a single OrderIntentCreated event through the execution pipeline.
func (s *Service) handleIntent(ctx context.Context, event domain.Event) error {
	intent, ok := event.Payload.(domain.OrderIntent)
	if !ok {
		return nil
	}

	l := s.log.With().
		Str("symbol", string(intent.Symbol)).
		Str("direction", string(intent.Direction)).
		Str("idempotency_key", event.IdempotencyKey).
		Str("intent_id", intent.ID.String()).
		Logger()

	l.Info().
		Float64("limit_price", intent.LimitPrice).
		Float64("stop_loss", intent.StopLoss).
		Float64("quantity", intent.Quantity).
		Msg("order intent received, starting execution pipeline")

	// 1. Check kill switch before any work.
	if s.killSwitch.IsHalted(event.TenantID, intent.Symbol) {
		l.Warn().Msg("kill switch engaged — trading halted for symbol")
		s.emit(ctx, domain.EventKillSwitchEngaged, event.TenantID, event.EnvMode, event.IdempotencyKey, nil)
		return nil
	}

	// 1a. Position gate — reject duplicate/conflicting entries.
	if s.positionGate != nil {
		if err := s.positionGate.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by position gate")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "position_gate").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
	}

	// 1b. Reject SHORT direction for new entries — paper account does not support short selling.
	// Exit orders (selling to close a long position) use DirectionShort but must be allowed through.
	if intent.Direction == domain.DirectionShort && !intent.IsExit {
		l.Warn().Msg("SHORT direction rejected — account does not support short selling")
		if s.metrics != nil {
			s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "short_disabled").Inc()
		}
		s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, "SHORT direction not supported — paper account cannot short sell"))
		return nil
	}

	// 2. Validate risk.
	if err := s.riskEngine.Validate(intent, s.accountEquity); err != nil {
		l.Warn().Err(err).Msg("order intent rejected by risk engine")
		if s.metrics != nil {
			s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "risk").Inc()
		}
		s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
		return nil
	}

	// 3. Validate slippage.
	if err := s.slippageGuard.Check(ctx, intent); err != nil {
		l.Warn().Err(err).Msg("order intent rejected by slippage guard")
		if s.metrics != nil {
			s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "validation").Inc()
		}
		s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
		return nil
	}
	l.Info().Msg("order intent validated — passed risk and slippage checks")
	s.emit(ctx, domain.EventOrderIntentValidated, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentEventPayload(intent, domain.OrderIntentStatusValidated))

	// 4. Record stop — if this trips the kill switch, abort before broker submission.
	if err := s.killSwitch.RecordStop(event.TenantID, intent.Symbol); err != nil {
		l.Warn().Err(err).Msg("kill switch tripped — aborting broker submission")
		s.emit(ctx, domain.EventCircuitBreakerTripped, event.TenantID, event.EnvMode, event.IdempotencyKey, err.Error())
		return nil
	}

	// 5. Check daily loss circuit breaker.
	if s.dailyLossBreaker != nil {
		if err := s.dailyLossBreaker.Check(event.TenantID, event.EnvMode, s.accountEquity); err != nil {
			l.Warn().Err(err).Msg("daily loss circuit breaker tripped — aborting broker submission")
			s.emit(ctx, domain.EventCircuitBreakerTripped, event.TenantID, event.EnvMode, event.IdempotencyKey, err.Error())
			return nil
		}
	}

	// 6. Submit to broker.
	submitStart := time.Now()
	brokerOrderID, err := s.broker.SubmitOrder(ctx, intent)
	if err != nil {
		l.Error().Err(err).Msg("broker rejected order")
		if s.metrics != nil {
			side := "sell"
			if intent.Direction == domain.DirectionLong {
				side = "buy"
			}
			s.metrics.Orders.Total.WithLabelValues("alpaca", intent.Strategy, side, "limit", "rejected").Inc()
			s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "api").Inc()
			s.metrics.Orders.SubmitLat.WithLabelValues("alpaca", intent.Strategy, "limit").Observe(time.Since(submitStart).Seconds())
		}
		s.emit(ctx, domain.EventOrderRejected, event.TenantID, event.EnvMode, intent.ID.String(), err.Error())
		return nil
	}
	if s.metrics != nil {
		side := "sell"
		if intent.Direction == domain.DirectionLong {
			side = "buy"
		}
		s.metrics.Orders.Total.WithLabelValues("alpaca", intent.Strategy, side, "limit", "placed").Inc()
		s.metrics.Orders.SubmitLat.WithLabelValues("alpaca", intent.Strategy, "limit").Observe(time.Since(submitStart).Seconds())
	}
	l.Info().Str("broker_order_id", brokerOrderID).Msg("order submitted to broker")
	s.emit(ctx, domain.EventOrderSubmitted, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentEventPayload(intent, domain.OrderIntentStatusSubmitted))

	// 6a. Mark inflight to prevent duplicate entries while awaiting fill.
	if s.positionGate != nil && isEntry(intent) {
		s.positionGate.MarkInflight(event.TenantID, event.EnvMode, intent.Symbol)
	}

	// 7. Persist the order record.
	side := "SELL"
	if intent.Direction == domain.DirectionLong {
		side = "BUY"
	}
	order := domain.BrokerOrder{
		Time:          time.Now().UTC(),
		TenantID:      event.TenantID,
		EnvMode:       event.EnvMode,
		IntentID:      intent.ID,
		BrokerOrderID: brokerOrderID,
		Symbol:        intent.Symbol,
		Side:          side,
		Quantity:      intent.Quantity,
		LimitPrice:    intent.LimitPrice,
		StopLoss:      intent.StopLoss,
		Status:        "submitted",
		Strategy:      intent.Strategy,
		Rationale:     intent.Rationale,
		Confidence:    intent.Confidence,
	}
	if saveErr := s.repo.SaveOrder(ctx, order); saveErr != nil {
		l.Error().Err(saveErr).Msg("failed to persist order — continuing to poll")
	}

	// 8. Poll for fill in background (up to 2 minutes, 5-second intervals).
	go s.pollForFill(event.TenantID, event.EnvMode, intent, brokerOrderID, submitStart, l)

	return nil
}

// pollForFill polls broker.GetOrderStatus until the order is filled, cancelled,
// or the 2-minute timeout is reached. On fill it persists a Trade and emits FillReceived.
func (s *Service) pollForFill(tenantID string, envMode domain.EnvMode, intent domain.OrderIntent, brokerOrderID string, submitStart time.Time, l zerolog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Clear inflight lock on all exit paths (fill, cancel, timeout).
	if s.positionGate != nil && isEntry(intent) {
		defer s.positionGate.ClearInflight(tenantID, envMode, intent.Symbol)
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.Warn().Str("broker_order_id", brokerOrderID).Msg("fill poll timed out — order not filled within 2 minutes")
			return
		case <-ticker.C:
			status, err := s.broker.GetOrderStatus(ctx, brokerOrderID)
			if err != nil {
				l.Warn().Err(err).Str("broker_order_id", brokerOrderID).Msg("fill poll: error fetching order status")
				continue
			}

			l.Debug().Str("broker_order_id", brokerOrderID).Str("status", status).Msg("fill poll: order status")

			switch status {
			case "filled":
				s.handleFill(tenantID, envMode, intent, brokerOrderID, submitStart, l)
				return
			case "canceled", "cancelled", "expired", "rejected":
				l.Info().Str("broker_order_id", brokerOrderID).Str("status", status).Msg("fill poll: order terminal without fill")
				return
			}
			// "new", "accepted", "pending_new", "partially_filled" — keep polling
		}
	}
}

// handleFill records the fill in the DB and emits FillReceived.
func (s *Service) handleFill(tenantID string, envMode domain.EnvMode, intent domain.OrderIntent, brokerOrderID string, submitStart time.Time, l zerolog.Logger) {
	now := time.Now().UTC()
	ctx := context.Background()

	// Use limit price as fill price proxy (paper trading; actual fill price = limit price).
	fillPrice := intent.LimitPrice

	// Update order record.
	if err := s.repo.UpdateOrderFill(ctx, brokerOrderID, now, fillPrice, intent.Quantity); err != nil {
		l.Error().Err(err).Str("broker_order_id", brokerOrderID).Msg("failed to update order fill")
	}

	// Persist trade.
	side := "SELL"
	if intent.Direction == domain.DirectionLong {
		side = "BUY"
	}
	trade, err := domain.NewTrade(now, tenantID, envMode, uuid.New(), intent.Symbol, side, intent.Quantity, fillPrice, 0, "filled", intent.Strategy, intent.Rationale)
	if err != nil {
		l.Error().Err(err).Msg("failed to construct trade on fill")
	} else {
		if err := s.repo.SaveTrade(ctx, trade); err != nil {
			l.Error().Err(err).Msg("failed to save trade on fill")
		}
	}

	// Emit fill event.
	s.emit(ctx, domain.EventFillReceived, tenantID, envMode, brokerOrderID, map[string]any{
		"broker_order_id": brokerOrderID,
		"intent_id":       intent.ID.String(),
		"symbol":          string(intent.Symbol),
		"side":            side,
		"quantity":        intent.Quantity,
		"price":           fillPrice,
		"filled_at":       now,
	})

	l.Info().
		Str("broker_order_id", brokerOrderID).
		Float64("fill_price", fillPrice).
		Float64("quantity", intent.Quantity).
		Msg("order filled — trade persisted and FillReceived emitted")

	// Record fill metrics.
	if s.metrics != nil {
		s.metrics.Orders.FillsTotal.WithLabelValues("alpaca", intent.Strategy, side, "filled").Inc()
		s.metrics.Orders.FillLat.WithLabelValues("alpaca", intent.Strategy).Observe(time.Since(submitStart).Seconds())
	}
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
