package positionmonitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Service is the active position monitor. It runs as a single-threaded actor:
//
//  1. FillReceived events are enqueued via channel (never processed inline).
//  2. A tick loop evaluates exit rules against all monitored positions.
//  3. Exit intents are emitted via an outbox channel (separate goroutine publishes).
//
// This design eliminates race conditions by construction and avoids blocking
// the synchronous in-memory event bus.
type Service struct {
	eventBus     ports.EventBusPort
	priceCache   ports.PriceCachePort
	positionGate *execution.PositionGate
	log          zerolog.Logger
	nowFunc      func() time.Time

	// Actor channels.
	fills  chan fillMsg
	outbox chan outboxMsg
	stopCh chan struct{}

	// State owned exclusively by the tick goroutine.
	positions map[string]*domain.MonitoredPosition // key: PositionKey()
	mu        sync.RWMutex // protects positions for concurrent reads (e.g. PositionCount)

	// Config.
	tickInterval      time.Duration
	maxPriceStaleness time.Duration
	tenantID          string
	envMode           domain.EnvMode
}

// fillMsg is the internal message type enqueued when a FillReceived event arrives.
type fillMsg struct {
	Symbol     domain.Symbol
	Side       string
	Price      float64
	Quantity   float64
	FilledAt   time.Time
	Strategy   string
	AssetClass domain.AssetClass
	ExitRules  []domain.ExitRule
}

// outboxMsg is an exit intent ready for publication on the event bus.
type outboxMsg struct {
	Intent         domain.OrderIntent
	ExitTriggered  domain.ExitTriggered
	TenantID       string
	EnvMode        domain.EnvMode
	IdempotencyKey string
}

// Option is a functional option for the Service.
type Option func(*Service)

// WithTickInterval overrides the default tick interval (1 second).
func WithTickInterval(d time.Duration) Option {
	return func(s *Service) { s.tickInterval = d }
}

// WithMaxPriceStaleness overrides the default max staleness for cached prices (30 seconds).
func WithMaxPriceStaleness(d time.Duration) Option {
	return func(s *Service) { s.maxPriceStaleness = d }
}

// WithNowFunc injects a deterministic clock for testing.
func WithNowFunc(fn func() time.Time) Option {
	return func(s *Service) { s.nowFunc = fn }
}

// NewService creates a new position monitor service.
func NewService(
	eventBus ports.EventBusPort,
	priceCache ports.PriceCachePort,
	positionGate *execution.PositionGate,
	tenantID string,
	envMode domain.EnvMode,
	log zerolog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		eventBus:          eventBus,
		priceCache:        priceCache,
		positionGate:      positionGate,
		log:               log.With().Str("service", "position_monitor").Logger(),
		nowFunc:           time.Now,
		fills:             make(chan fillMsg, 256),
		outbox:            make(chan outboxMsg, 64),
		stopCh:            make(chan struct{}),
		positions:         make(map[string]*domain.MonitoredPosition),
		tickInterval:      1 * time.Second,
		maxPriceStaleness: 30 * time.Second,
		tenantID:          tenantID,
		envMode:           envMode,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start subscribes to FillReceived events and launches the actor goroutines.
func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.Subscribe(ctx, domain.EventFillReceived, s.handleFillEvent); err != nil {
		return fmt.Errorf("position_monitor: failed to subscribe to FillReceived: %w", err)
	}

	// Outbox publisher goroutine — reads exit intents and publishes them.
	go s.runOutbox(ctx)

	// Actor tick loop — the single goroutine that owns all mutable state.
	go s.runTickLoop(ctx)

	s.log.Info().
		Dur("tick_interval", s.tickInterval).
		Dur("max_price_staleness", s.maxPriceStaleness).
		Msg("position monitor started")
	return nil
}

// handleFillEvent is the EventBusPort handler. It enqueues fills without processing
// them inline, ensuring we never block the synchronous event bus.
func (s *Service) handleFillEvent(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return nil
	}

	symbol, _ := payload["symbol"].(string)
	side, _ := payload["side"].(string)
	price, _ := payload["price"].(float64)
	quantity, _ := payload["quantity"].(float64)
	strategy, _ := payload["strategy"].(string)
	filledAt, _ := payload["filled_at"].(time.Time)

	if symbol == "" || price <= 0 || quantity <= 0 {
		return nil
	}

	select {
	case s.fills <- fillMsg{
		Symbol:   domain.Symbol(symbol),
		Side:     side,
		Price:    price,
		Quantity: quantity,
		FilledAt: filledAt,
		Strategy: strategy,
	}:
	default:
		s.log.Warn().Str("symbol", symbol).Msg("position monitor: fill channel full, dropping fill")
	}
	return nil
}

// runTickLoop is the main actor goroutine. It owns all position state.
func (s *Service) runTickLoop(ctx context.Context) {
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case fill := <-s.fills:
			s.processFill(fill)
		case <-ticker.C:
			s.tick()
		}
	}
}

// processFill handles a fill within the actor goroutine.
func (s *Service) processFill(fill fillMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s:%s:%s", s.tenantID, s.envMode, fill.Symbol)

	if fill.Side == "SELL" {
		// Exit fill — remove position from monitoring.
		if pos, exists := s.positions[key]; exists {
			s.log.Info().
				Str("symbol", string(fill.Symbol)).
				Float64("exit_price", fill.Price).
				Msg("position closed — removing from monitor")

			// Clear exit inflight lock.
			if s.positionGate != nil {
				s.positionGate.ClearInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol)
			}
			delete(s.positions, key)
		}
		return
	}

	// Entry fill — add or update monitored position.
	existing, exists := s.positions[key]
	if exists {
		// Scale-in: update average entry price, quantity, and water marks.
		totalQty := existing.Quantity + fill.Quantity
		existing.EntryPrice = (existing.EntryPrice*existing.Quantity + fill.Price*fill.Quantity) / totalQty
		existing.Quantity = totalQty
		existing.UpdateWaterMarks(fill.Price)
		s.log.Info().
			Str("symbol", string(fill.Symbol)).
			Float64("avg_entry", existing.EntryPrice).
			Float64("total_qty", totalQty).
			Msg("position scaled in — updated entry")
		return
	}

	// New position — look up exit rules from strategy config.
	exitRules := fill.ExitRules
	if exitRules == nil {
		exitRules = []domain.ExitRule{}
	}

	pos, err := domain.NewMonitoredPosition(
		fill.Symbol, fill.Price, fill.FilledAt,
		fill.Strategy, fill.AssetClass, exitRules,
		s.tenantID, s.envMode, fill.Quantity,
	)
	if err != nil {
		s.log.Error().Err(err).Str("symbol", string(fill.Symbol)).Msg("failed to create monitored position")
		return
	}

	s.positions[key] = &pos
	s.log.Info().
		Str("symbol", string(fill.Symbol)).
		Float64("entry_price", fill.Price).
		Float64("quantity", fill.Quantity).
		Int("exit_rules", len(exitRules)).
		Msg("new position added to monitor")
}

// tick evaluates all exit rules against all monitored positions.
func (s *Service) tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()

	for _, pos := range s.positions {
		if pos.ExitPending {
			// Check for exit pending timeout (30 seconds).
			if now.Sub(pos.ExitPendingAt) > 30*time.Second {
				s.log.Warn().
					Str("symbol", string(pos.Symbol)).
					Msg("exit pending timeout — clearing lock")
				pos.ExitPending = false
				if s.positionGate != nil {
					s.positionGate.ClearInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol)
				}
			}
			continue
		}

		// Get latest price.
		snap, ok := s.priceCache.LatestPrice(pos.Symbol)
		if !ok {
			continue
		}

		// Check price staleness.
		if now.Sub(snap.ObservedAt) > s.maxPriceStaleness {
			continue
		}

		price := snap.Price
		pos.UpdateWaterMarks(price)

		// Evaluate each exit rule.
		for _, rule := range pos.ExitRules {
			triggered, reason := Evaluate(rule, pos, price, now)
			if !triggered {
				continue
			}

			s.log.Info().
				Str("symbol", string(pos.Symbol)).
				Str("rule", string(rule.Type)).
				Str("reason", reason).
				Float64("price", price).
				Float64("entry_price", pos.EntryPrice).
				Msg("exit rule triggered")

			s.triggerExit(pos, rule, reason, price, now)
			break // Only one exit per tick per position.
		}
	}
}

// triggerExit marks a position as exit-pending and emits an exit order intent.
func (s *Service) triggerExit(pos *domain.MonitoredPosition, rule domain.ExitRule, reason string, currentPrice float64, now time.Time) {
	// Try to acquire exit inflight lock.
	if s.positionGate != nil {
		if !s.positionGate.TryMarkInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol) {
			s.log.Warn().
				Str("symbol", string(pos.Symbol)).
				Msg("exit inflight lock already held — skipping")
			return
		}
	}

	pos.ExitPending = true
	pos.ExitPendingAt = now

	// Build deterministic idempotency key.
	idempotencyKey := fmt.Sprintf("EXIT:%s:%s:%s:%d:%s",
		pos.TenantID, pos.EnvMode, pos.Symbol, pos.EntryTime.Unix(), rule.Type)

	// Create exit order intent.
	intent, err := domain.NewOrderIntent(
		uuid.New(),
		pos.TenantID,
		pos.EnvMode,
		pos.Symbol,
		domain.DirectionCloseLong,
		currentPrice,   // limit price = current price for market-like exit
		currentPrice/2, // stop loss = half current price (safety fallback)
		0,              // no slippage check for exits
		pos.Quantity,
		pos.Strategy,
		fmt.Sprintf("exit_monitor:%s:%s", rule.Type, reason),
		1.0, // max confidence for rule-based exits
		idempotencyKey,
	)
	if err != nil {
		s.log.Error().Err(err).Str("symbol", string(pos.Symbol)).Msg("failed to create exit order intent")
		// Rollback locks.
		pos.ExitPending = false
		if s.positionGate != nil {
			s.positionGate.ClearInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol)
		}
		return
	}

	exitTriggered := domain.ExitTriggered{
		Symbol:       pos.Symbol,
		Rule:         rule.Type,
		Reason:       reason,
		CurrentPrice: currentPrice,
		EntryPrice:   pos.EntryPrice,
		Strategy:     pos.Strategy,
		TenantID:     pos.TenantID,
		EnvMode:      pos.EnvMode,
	}

	// Enqueue to outbox — never publish directly from tick goroutine.
	select {
	case s.outbox <- outboxMsg{
		Intent:         intent,
		ExitTriggered:  exitTriggered,
		TenantID:       pos.TenantID,
		EnvMode:        pos.EnvMode,
		IdempotencyKey: idempotencyKey,
	}:
	default:
		s.log.Error().Str("symbol", string(pos.Symbol)).Msg("outbox full — dropping exit intent")
		pos.ExitPending = false
		if s.positionGate != nil {
			s.positionGate.ClearInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol)
		}
	}
}

// runOutbox is the outbox publisher goroutine. It reads exit intents from the
// outbox channel and publishes them on the event bus.
func (s *Service) runOutbox(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case msg := <-s.outbox:
			// Emit ExitTriggered event.
			s.emit(ctx, domain.EventExitTriggered, msg.TenantID, msg.EnvMode, msg.IdempotencyKey, msg.ExitTriggered)

			// Emit OrderIntentCreated event to feed the execution pipeline.
			s.emit(ctx, domain.EventOrderIntentCreated, msg.TenantID, msg.EnvMode, msg.Intent.IdempotencyKey, msg.Intent)
		}
	}
}

// emit publishes a domain event on the event bus (best-effort).
func (s *Service) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = s.eventBus.Publish(ctx, *ev)
}

// Stop signals the actor goroutines to shut down.
func (s *Service) Stop() {
	close(s.stopCh)
}

// PositionCount returns the number of actively monitored positions (for diagnostics).
func (s *Service) PositionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.positions)
}
