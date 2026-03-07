package positionmonitor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
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
	broker       ports.BrokerPort
	repo         ports.RepositoryPort
	specStore    portstrategy.SpecStore
	log          zerolog.Logger
	nowFunc      func() time.Time

	// Actor channels.
	fills  chan fillMsg
	outbox chan outboxMsg
	stopCh chan struct{}

	// State owned exclusively by the tick goroutine.
	positions map[string]*domain.MonitoredPosition // key: PositionKey()
	mu        sync.RWMutex                         // protects positions for concurrent reads (e.g. PositionCount)

	// Config.
	tickInterval      time.Duration
	maxPriceStaleness time.Duration
	tenantID          string
	envMode           domain.EnvMode
}

// fillMsg is the internal message type enqueued when a FillReceived event arrives.
type fillMsg struct {
	Symbol       domain.Symbol
	Side         string
	Price        float64
	Quantity     float64
	FilledAt     time.Time
	Strategy     string
	AssetClass   domain.AssetClass
	ExitRules    []domain.ExitRule // set by bootstrap; nil from live fill events
	RiskModifier domain.RiskModifier
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

// WithBroker injects a BrokerPort for startup position bootstrap.
func WithBroker(b ports.BrokerPort) Option {
	return func(s *Service) { s.broker = b }
}

// WithRepo injects a RepositoryPort for startup position bootstrap.
func WithRepo(r ports.RepositoryPort) Option {
	return func(s *Service) { s.repo = r }
}

// WithSpecStore injects a SpecStore for resolving exit rules during bootstrap.
func WithSpecStore(ss portstrategy.SpecStore) Option {
	return func(s *Service) { s.specStore = ss }
}

// SetSpecStore sets the spec store after construction (for deferred wiring).
func (s *Service) SetSpecStore(ss portstrategy.SpecStore) {
	s.specStore = ss
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

	// Bootstrap: seed monitor with OMO-opened positions that are still on the broker.
	s.bootstrapPositions(ctx)

	// Outbox publisher goroutine — reads exit intents and publishes them.
	go s.runOutbox(ctx)

	// Actor tick loop — the single goroutine that owns all mutable state.
	go s.runTickLoop(ctx)

	s.log.Info().
		Dur("tick_interval", s.tickInterval).
		Dur("max_price_staleness", s.maxPriceStaleness).
		Int("bootstrapped_positions", len(s.positions)).
		Msg("position monitor started")
	return nil
}

// bootstrapPositions seeds the monitor with OMO-opened positions that still exist on the broker.
// It cross-references broker positions with our trade DB to identify positions that OMO opened.
// Positions opened manually by the user on the broker are NOT bootstrapped.
func (s *Service) bootstrapPositions(ctx context.Context) {
	if s.broker == nil || s.repo == nil {
		s.log.Debug().Msg("bootstrap skipped — broker or repo not configured")
		return
	}

	// 1. Query broker for all current positions.
	brokerPositions, err := s.broker.GetPositions(ctx, s.tenantID, s.envMode)
	if err != nil {
		s.log.Warn().Err(err).Msg("bootstrap: failed to query broker positions — skipping")
		return
	}
	if len(brokerPositions) == 0 {
		s.log.Debug().Msg("bootstrap: no open broker positions")
		return
	}

	// Build a lookup of broker positions by symbol.
	brokerBySymbol := make(map[domain.Symbol]domain.Trade, len(brokerPositions))
	for _, bp := range brokerPositions {
		brokerBySymbol[bp.Symbol] = bp
	}

	// 2. Query our trade DB for recent BUY fills to identify OMO-opened positions.
	//    We look back 30 days to cover long-held positions (especially crypto).
	now := s.nowFunc()
	from := now.Add(-30 * 24 * time.Hour)
	trades, err := s.repo.GetTrades(ctx, s.tenantID, s.envMode, from, now)
	if err != nil {
		s.log.Warn().Err(err).Msg("bootstrap: failed to query trade history — skipping")
		return
	}

	// 3. Compute net OMO position per symbol from trade history.
	//    Positive net qty = we have a long; negative = short (not currently supported).
	type omoEntry struct {
		netQty   float64
		avgEntry float64 // weighted average entry price
		entryAt  time.Time
		strategy string
		asset    domain.AssetClass
	}
	omoPositions := make(map[domain.Symbol]*omoEntry)
	for _, t := range trades {
		e, exists := omoPositions[t.Symbol]
		if !exists {
			e = &omoEntry{}
			omoPositions[t.Symbol] = e
		}

		switch strings.ToUpper(t.Side) {
		case "BUY":
			// Weighted average entry price on buys.
			totalCost := e.avgEntry*e.netQty + t.Price*t.Quantity
			e.netQty += t.Quantity
			if e.netQty > 0 {
				e.avgEntry = totalCost / e.netQty
			}
			e.entryAt = t.Time
			e.strategy = t.Strategy
			e.asset = t.AssetClass
		case "SELL":
			e.netQty -= t.Quantity
			if e.netQty <= 0 {
				// Position fully closed — clear.
				e.netQty = 0
				e.avgEntry = 0
			}
		}
	}

	// 4. For each OMO position with net qty > 0 that also exists on the broker, seed the monitor.
	bootstrapped := 0
	for sym, omo := range omoPositions {
		if omo.netQty <= 0 {
			continue
		}
		bp, onBroker := brokerBySymbol[sym]
		if !onBroker {
			// OMO thinks we have a position but broker disagrees — stale trade data.
			s.log.Warn().Str("symbol", string(sym)).Msg("bootstrap: OMO trade found but no broker position — skipping")
			continue
		}

		// Use broker's actual qty/price as the source of truth for current state.
		entryPrice := bp.Price // avg_entry_price from broker
		quantity := bp.Quantity
		assetClass := bp.AssetClass
		strategy := omo.strategy
		entryTime := omo.entryAt

		// Look up exit rules from strategy spec.
		exitRules := s.resolveExitRules(ctx, strategy, assetClass)

		pos, err := domain.NewMonitoredPosition(
			sym, entryPrice, entryTime,
			strategy, assetClass, exitRules,
			s.tenantID, s.envMode, quantity,
		)
		if err != nil {
			s.log.Warn().Err(err).Str("symbol", string(sym)).Msg("bootstrap: failed to create monitored position")
			continue
		}

		key := pos.PositionKey()
		s.positions[key] = &pos
		bootstrapped++
		s.log.Info().
			Str("symbol", string(sym)).
			Float64("entry_price", entryPrice).
			Float64("quantity", quantity).
			Str("strategy", strategy).
			Int("exit_rules", len(exitRules)).
			Msg("bootstrap: position restored from trade history")
	}

	s.log.Info().Int("bootstrapped", bootstrapped).Int("broker_total", len(brokerPositions)).Msg("bootstrap complete")
}

// resolveExitRules looks up exit rules from the strategy spec store.
// When multiple specs share the same strategy ID (e.g. equity vs crypto variants),
// it prefers the spec whose asset_classes include the given assetClass.
// Falls back to conservative defaults if the spec store is unavailable or the strategy has no rules.
func (s *Service) resolveExitRules(ctx context.Context, strategy string, assetClass domain.AssetClass) []domain.ExitRule {
	if s.specStore != nil && strategy != "" {
		// List all specs and find the best match: same ID + matching asset class.
		all, err := s.specStore.List(ctx, nil)
		if err != nil {
			s.log.Warn().Err(err).Str("strategy", strategy).Msg("bootstrap: failed to list specs for exit rule resolution")
		} else {
			var bestMatch *portstrategy.Spec
			var fallbackMatch *portstrategy.Spec
			for i := range all {
				sp := &all[i]
				if sp.ID != domstrategy.StrategyID(strategy) {
					continue
				}
				// Check if this spec's asset_classes includes our asset class.
				if matchesAssetClass(sp.Routing.AssetClasses, assetClass) {
					if bestMatch == nil || compareSpecPriority(sp, bestMatch) > 0 {
						bestMatch = sp
					}
				} else if fallbackMatch == nil {
					fallbackMatch = sp
				}
			}
			chosen := bestMatch
			if chosen == nil {
				chosen = fallbackMatch
			}
			if chosen != nil && len(chosen.ExitRules) > 0 {
				s.log.Info().
					Str("strategy", strategy).
					Str("asset_class", string(assetClass)).
					Int("rules", len(chosen.ExitRules)).
					Msg("bootstrap: exit rules from spec")
				return chosen.ExitRules
			}
			if chosen != nil {
				s.log.Warn().Str("strategy", strategy).Str("asset_class", string(assetClass)).Msg("bootstrap: spec found but has no exit rules")
			}
		}
	} else if s.specStore == nil {
		s.log.Warn().Msg("bootstrap: specStore is nil \u2014 cannot resolve exit rules")
	}

	// Conservative defaults: max loss at 5% and EOD flatten 5 min before close.
	var defaults []domain.ExitRule
	if r, err := domain.NewExitRule(domain.ExitRuleMaxLoss, map[string]float64{"pct": 0.05}); err == nil {
		defaults = append(defaults, r)
	}
	if r, err := domain.NewExitRule(domain.ExitRuleEODFlatten, map[string]float64{"minutes_before_close": 5}); err == nil {
		defaults = append(defaults, r)
	}
	s.log.Debug().Str("strategy", strategy).Msg("bootstrap: using default exit rules")
	return defaults
}

// matchesAssetClass returns true if the spec's asset_classes list contains the given asset class,
// or if the list is empty (meaning it applies to all).
func matchesAssetClass(specClasses []string, ac domain.AssetClass) bool {
	if len(specClasses) == 0 {
		return true // no restriction
	}
	for _, c := range specClasses {
		if strings.EqualFold(c, string(ac)) {
			return true
		}
	}
	return false
}

// compareSpecPriority compares two specs; higher priority wins, then higher version.
func compareSpecPriority(a, b *portstrategy.Spec) int {
	if a.Routing.Priority != b.Routing.Priority {
		if a.Routing.Priority > b.Routing.Priority {
			return 1
		}
		return -1
	}
	return 0
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
	assetClass, _ := payload["asset_class"].(string)
	riskModStr, _ := payload["risk_modifier"].(string)

	if symbol == "" || price <= 0 || quantity <= 0 {
		return nil
	}

	select {
	case s.fills <- fillMsg{
		Symbol:       domain.Symbol(symbol),
		Side:         side,
		Price:        price,
		Quantity:     quantity,
		FilledAt:     filledAt,
		Strategy:     strategy,
		AssetClass:   domain.AssetClass(assetClass),
		RiskModifier: domain.NewRiskModifier(riskModStr),
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
		pos, exists := s.positions[key]
		if !exists {
			return
		}

		pos.Quantity -= fill.Quantity
		if pos.Quantity <= 1e-9 {
			s.log.Info().
				Str("symbol", string(fill.Symbol)).
				Float64("exit_price", fill.Price).
				Msg("position fully closed — removing from monitor")
			if s.positionGate != nil {
				s.positionGate.ClearInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol)
			}
			delete(s.positions, key)
		} else {
			pos.ExitPending = false
			s.log.Info().
				Str("symbol", string(fill.Symbol)).
				Float64("exit_price", fill.Price).
				Float64("remaining_qty", pos.Quantity).
				Msg("position partially closed — still monitoring")
		}
		return
	}

	existing, exists := s.positions[key]
	if exists {
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

	exitRules := fill.ExitRules
	if exitRules == nil {
		exitRules = s.resolveExitRules(context.Background(), fill.Strategy, fill.AssetClass)
	}
	exitRules = applyRiskModifierToExitRules(exitRules, fill.RiskModifier)

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
		currentPrice, // limit price = current price for market-like exit
		0,            // no stop loss for exits — plain limit order
		0,            // no slippage check for exits
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

// applyRiskModifierToExitRules scales TRAILING_STOP and MAX_LOSS pct params
// based on the AI judge's risk modifier. TIGHT tightens stops (0.7x),
// WIDE gives more room (1.5x). NORMAL/empty returns rules unchanged.
func applyRiskModifierToExitRules(rules []domain.ExitRule, modifier domain.RiskModifier) []domain.ExitRule {
	if modifier == domain.RiskModifierNormal || modifier == "" {
		return rules
	}

	var mult float64
	switch modifier {
	case domain.RiskModifierTight:
		mult = 0.70
	case domain.RiskModifierWide:
		mult = 1.50
	default:
		return rules
	}

	scaled := make([]domain.ExitRule, len(rules))
	for i, r := range rules {
		if (r.Type == domain.ExitRuleTrailingStop || r.Type == domain.ExitRuleMaxLoss) && r.Params["pct"] > 0 {
			newParams := make(map[string]float64, len(r.Params))
			for k, v := range r.Params {
				newParams[k] = v
			}
			newParams["pct"] = r.Params["pct"] * mult
			scaled[i] = domain.ExitRule{Type: r.Type, Params: newParams}
		} else {
			scaled[i] = r
		}
	}
	return scaled
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
