package execution

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type pendingOrder struct {
	intent      domain.OrderIntent
	tenantID    string
	envMode     domain.EnvMode
	submitStart time.Time
}

type Service struct {
	eventBus         ports.EventBusPort
	broker           ports.BrokerPort
	orderStream      ports.OrderStreamPort
	repo             ports.RepositoryPort
	riskEngine       *RiskEngine
	slippageGuard    *SlippageGuard
	killSwitch       *KillSwitch
	dailyLossBreaker *risk.DailyLossBreaker
	positionGate     *PositionGate
	buyingPowerGuard *BuyingPowerGuard
	accountEquity    float64
	log              zerolog.Logger
	metrics          *metrics.Metrics
	pendingOrders    sync.Map // brokerOrderID → *pendingOrder
}

// Option is a functional option for Service.
type Option func(*Service)

// WithPositionGate attaches a PositionGate to the execution pipeline.
func WithPositionGate(pg *PositionGate) Option {
	return func(s *Service) { s.positionGate = pg }
}

// WithBuyingPowerGuard attaches a BuyingPowerGuard to the execution pipeline.
// Only set when DTBP_FALLBACK=true.
func WithBuyingPowerGuard(bpg *BuyingPowerGuard) Option {
	return func(s *Service) { s.buyingPowerGuard = bpg }
}

func WithOrderStream(os ports.OrderStreamPort) Option {
	return func(s *Service) { s.orderStream = os }
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

func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.Subscribe(ctx, domain.EventOrderIntentCreated, s.handleIntent); err != nil {
		return fmt.Errorf("execution: failed to subscribe to OrderIntentCreated: %w", err)
	}
	s.log.Info().Msg("subscribed to OrderIntentCreated events")

	if s.orderStream != nil {
		ch, err := s.orderStream.SubscribeOrderUpdates(ctx)
		if err != nil {
			return fmt.Errorf("execution: failed to subscribe to order stream: %w", err)
		}
		go s.runFillListener(ctx, ch)
		go s.runReconciliationLoop(ctx)
		s.log.Info().Msg("WebSocket fill listener and reconciliation loop started")
	}

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

	// 1b. Reject SHORT direction — short selling not supported for this asset class.
	// Exit orders now use DirectionCloseLong, so this only catches new short entries.
	if intent.Direction == domain.DirectionShort {
		reason := "SHORT direction not supported"
		if intent.AssetClass == domain.AssetClassCrypto {
			reason = "SHORT direction not supported — crypto is long-only on Alpaca"
		} else {
			reason = "SHORT direction not supported — paper account cannot short sell"
		}
		l.Warn().Str("asset_class", intent.AssetClass.String()).Msg(reason)
		if s.metrics != nil {
			s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "short_disabled").Inc()
		}
		s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, reason))
		return nil
	}

	// 2. Validate risk (skip for exit orders — closing reduces exposure).
	if !intent.Direction.IsExit() {
		if err := s.riskEngine.Validate(intent, s.accountEquity); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by risk engine")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "risk").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
	}

	// 3. Validate slippage (skip for exit orders — we want to exit regardless).
	if !intent.Direction.IsExit() {
		if err := s.slippageGuard.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by slippage guard")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "validation").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
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

	// 5b. Buying power guard — pre-check DTBP for equity entries (only when DTBP_FALLBACK enabled).
	if s.buyingPowerGuard != nil {
		if err := s.buyingPowerGuard.Check(ctx, intent); err != nil {
			l.Warn().Err(err).Msg("order intent rejected by buying power guard")
			if s.metrics != nil {
				s.metrics.Orders.RejectsTotal.WithLabelValues("alpaca", intent.Strategy, "buying_power").Inc()
			}
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, err.Error()))
			return nil
		}
	}

	// 5a. For exit intents, resolve the full position quantity from the broker.
	if intent.Direction.IsExit() {
		positions, posErr := s.broker.GetPositions(ctx, event.TenantID, event.EnvMode)
		if posErr != nil {
			l.Error().Err(posErr).Msg("failed to query positions for exit — rejecting conservatively")
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, fmt.Sprintf("exit position query failed: %v", posErr)))
			return nil
		}
		var posQty float64
		for _, p := range positions {
			if p.Symbol == intent.Symbol {
				posQty += p.Quantity
			}
		}
		if posQty <= 0 {
			l.Warn().Msg("exit intent but no position found — rejecting")
			s.emit(ctx, domain.EventOrderIntentRejected, event.TenantID, event.EnvMode, intent.ID.String(), domain.NewOrderIntentRejectedPayload(intent, "position_gate: no_position_to_exit"))
			return nil
		}
		intent.Quantity = posQty
		l.Info().Float64("exit_qty", posQty).Msg("resolved exit quantity from broker position")
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

	// 8. Register intent for fill correlation and start fill detection.
	s.pendingOrders.Store(brokerOrderID, &pendingOrder{
		intent:      intent,
		tenantID:    event.TenantID,
		envMode:     event.EnvMode,
		submitStart: submitStart,
	})

	if s.orderStream == nil {
		// Fallback: poll for fill when no WebSocket stream is available (backtest, replay).
		go s.pollForFill(event.TenantID, event.EnvMode, intent, brokerOrderID, submitStart, l)
	}

	return nil
}

// pollForFill polls broker.GetOrderStatus until the order is filled, cancelled,
// or the 2-minute timeout is reached. On fill it persists a Trade and emits FillReceived.
func (s *Service) pollForFill(tenantID string, envMode domain.EnvMode, intent domain.OrderIntent, brokerOrderID string, submitStart time.Time, l zerolog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	defer s.pendingOrders.Delete(brokerOrderID)

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
	trade, err := domain.NewTrade(now, tenantID, envMode, uuid.New(), intent.Symbol, side, intent.Quantity, fillPrice, 0, "FILLED", intent.Strategy, intent.Rationale)
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
		"strategy":        intent.Strategy,
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

func (s *Service) runFillListener(ctx context.Context, ch <-chan ports.OrderUpdate) {
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-ch:
			if !ok {
				return
			}
			s.handleOrderUpdate(update)
		}
	}
}

func (s *Service) handleOrderUpdate(update ports.OrderUpdate) {
	l := s.log.With().
		Str("broker_order_id", update.BrokerOrderID).
		Str("event", update.Event).
		Logger()

	switch update.Event {
	case "fill", "partial_fill":
		s.handleStreamFill(update, l)
	case "canceled", "cancelled", "expired", "rejected":
		l.Info().Msg("order terminal via stream")
		s.cleanupPendingOrder(update.BrokerOrderID)
	}
}

const fastFillRetryDelay = 500 * time.Millisecond
const fastFillMaxRetries = 3

func (s *Service) handleStreamFill(update ports.OrderUpdate, l zerolog.Logger) {
	raw, ok := s.pendingOrders.Load(update.BrokerOrderID)
	if !ok {
		for i := 0; i < fastFillMaxRetries; i++ {
			time.Sleep(fastFillRetryDelay)
			raw, ok = s.pendingOrders.Load(update.BrokerOrderID)
			if ok {
				break
			}
		}
		if !ok {
			l.Warn().Msg("fill received for unknown order (not in pending map)")
			return
		}
	}

	po := raw.(*pendingOrder)

	if update.Event == "fill" {
		s.pendingOrders.Delete(update.BrokerOrderID)
	}

	fillPrice := update.Price
	if fillPrice <= 0 {
		fillPrice = update.FilledAvgPrice
	}
	if fillPrice <= 0 {
		fillPrice = po.intent.LimitPrice
	}

	fillQty := update.Qty
	if fillQty <= 0 {
		fillQty = update.FilledQty
	}
	if fillQty <= 0 {
		fillQty = po.intent.Quantity
	}

	s.handleFillWithPrice(po, update.BrokerOrderID, fillPrice, fillQty, update.FilledAt, l)
}

func (s *Service) handleFillWithPrice(po *pendingOrder, brokerOrderID string, fillPrice, fillQty float64, filledAt time.Time, l zerolog.Logger) {
	ctx := context.Background()

	if err := s.repo.UpdateOrderFill(ctx, brokerOrderID, filledAt, fillPrice, fillQty); err != nil {
		l.Error().Err(err).Msg("failed to update order fill")
	}

	side := "SELL"
	if po.intent.Direction == domain.DirectionLong {
		side = "BUY"
	}
	trade, err := domain.NewTrade(filledAt, po.tenantID, po.envMode, uuid.New(), po.intent.Symbol, side, fillQty, fillPrice, 0, "FILLED", po.intent.Strategy, po.intent.Rationale)
	if err != nil {
		l.Error().Err(err).Msg("failed to construct trade on fill")
	} else {
		if err := s.repo.SaveTrade(ctx, trade); err != nil {
			l.Error().Err(err).Msg("failed to save trade on fill")
		}
	}

	s.emit(ctx, domain.EventFillReceived, po.tenantID, po.envMode, brokerOrderID, map[string]any{
		"broker_order_id": brokerOrderID,
		"intent_id":       po.intent.ID.String(),
		"symbol":          string(po.intent.Symbol),
		"side":            side,
		"quantity":        fillQty,
		"price":           fillPrice,
		"filled_at":       filledAt,
		"strategy":        po.intent.Strategy,
		"asset_class":     string(po.intent.AssetClass),
	})

	l.Info().
		Str("broker_order_id", brokerOrderID).
		Float64("fill_price", fillPrice).
		Float64("quantity", fillQty).
		Msg("order filled — trade persisted and FillReceived emitted")

	if s.metrics != nil {
		s.metrics.Orders.FillsTotal.WithLabelValues("alpaca", po.intent.Strategy, side, "filled").Inc()
		s.metrics.Orders.FillLat.WithLabelValues("alpaca", po.intent.Strategy).Observe(time.Since(po.submitStart).Seconds())
	}

	if s.positionGate != nil && isEntry(po.intent) {
		s.positionGate.ClearInflight(po.tenantID, po.envMode, po.intent.Symbol)
	}
}

func (s *Service) cleanupPendingOrder(brokerOrderID string) {
	if raw, ok := s.pendingOrders.LoadAndDelete(brokerOrderID); ok {
		po := raw.(*pendingOrder)
		if s.positionGate != nil && isEntry(po.intent) {
			s.positionGate.ClearInflight(po.tenantID, po.envMode, po.intent.Symbol)
		}
	}
}

const reconcileInterval = 60 * time.Second

func (s *Service) runReconciliationLoop(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcilePendingOrders(ctx)
		}
	}
}

func (s *Service) reconcilePendingOrders(ctx context.Context) {
	s.pendingOrders.Range(func(key, value any) bool {
		brokerOrderID := key.(string)
		po := value.(*pendingOrder)

		// Skip orders that are too new (avoid racing with WS).
		if time.Since(po.submitStart) < 10*time.Second {
			return true
		}

		status, err := s.broker.GetOrderStatus(ctx, brokerOrderID)
		if err != nil {
			s.log.Warn().Err(err).Str("broker_order_id", brokerOrderID).Msg("reconcile: status check failed")
			return true
		}

		l := s.log.With().Str("broker_order_id", brokerOrderID).Str("status", status).Logger()

		switch status {
		case "filled":
			l.Info().Msg("reconcile: detected fill via REST fallback")
			s.handleStreamFill(ports.OrderUpdate{
				BrokerOrderID:  brokerOrderID,
				Event:          "fill",
				Qty:            po.intent.Quantity,
				Price:          po.intent.LimitPrice,
				FilledAvgPrice: po.intent.LimitPrice,
				FilledQty:      po.intent.Quantity,
				FilledAt:       time.Now().UTC(),
			}, l)
		case "canceled", "cancelled", "expired", "rejected":
			l.Info().Msg("reconcile: order terminal via REST fallback")
			s.cleanupPendingOrder(brokerOrderID)
		}

		// Expire stale pending orders (> 2 minutes old).
		if time.Since(po.submitStart) > 2*time.Minute {
			l.Warn().Msg("reconcile: pending order expired")
			s.cleanupPendingOrder(brokerOrderID)
		}

		return true
	})
}

func (s *Service) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = s.eventBus.Publish(ctx, *ev)
}
