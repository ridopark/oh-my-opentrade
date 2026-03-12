package positionmonitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
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
	fills         chan fillMsg
	exitSubmitted chan exitOrderSubmittedMsg
	exitTerminal  chan exitOrderTerminalMsg
	exitRejected  chan exitRejectedMsg
	outbox        chan outboxMsg
	stopCh        chan struct{}

	// State owned exclusively by the tick goroutine.
	positions            map[string]*domain.MonitoredPosition // key: PositionKey()
	ghostMissCounts      map[string]int                       // key: position key → consecutive broker-miss count
	pendingGlobalOrphans map[domain.Symbol]int                // key: symbol → consecutive global-reconcile misses
	mu                   sync.RWMutex                         // protects positions for concurrent reads (e.g. PositionCount)

	snapshotFn IndicatorSnapshotFunc

	// Config.
	tickInterval            time.Duration
	reconcileInterval       time.Duration
	globalReconcileInterval time.Duration
	maxPriceStaleness       time.Duration
	tenantID                string
	envMode                 domain.EnvMode

	// Backtest mode flags.
	disableTickLoop  bool // prevents runTickLoop goroutine from starting
	disableReconcile bool // prevents bootstrapPositions from running at Start
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

	// Option metadata (populated only for option fills).
	InstrumentType domain.InstrumentType
	OptionExpiry   time.Time
	OptionRight    string
}

// outboxMsg is an exit intent ready for publication on the event bus.
type outboxMsg struct {
	Intent         domain.OrderIntent
	ExitTriggered  domain.ExitTriggered
	TenantID       string
	EnvMode        domain.EnvMode
	IdempotencyKey string
}

type exitOrderSubmittedMsg struct {
	Symbol        domain.Symbol
	BrokerOrderID string
	Direction     string
}

type exitOrderTerminalMsg struct {
	Symbol        domain.Symbol
	BrokerOrderID string
}

type exitRejectedMsg struct {
	Symbol domain.Symbol
	Reason string
}

const (
	exitPendingTimeout             = 10 * time.Second
	maxExitRetries                 = 3
	defaultReconcileInterval       = 5 * time.Minute
	defaultGlobalReconcileInterval = 5 * time.Minute
	ghostMissThreshold             = 3
	// globalOrphanMissThreshold is the number of consecutive reconcileGlobal cycles
	// that must observe a symbol missing from the broker before a reconciliation SELL
	// is written to the DB. Two misses at the default 5-minute interval means the
	// absence must be confirmed for at least 10 minutes, guarding against the
	// false-positive where a transient Alpaca API hiccup returns an empty position
	// list and the reconciler prematurely zeros out a live DB position.
	globalOrphanMissThreshold = 2
)

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

// WithReconcileInterval overrides the default broker reconciliation interval (60 seconds).
func WithReconcileInterval(d time.Duration) Option {
	return func(s *Service) { s.reconcileInterval = d }
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

// WithDisableTickLoop prevents the runTickLoop goroutine from starting in Start().
// Used in backtest mode where EvalExitRules is called explicitly per bar.
func WithDisableTickLoop() Option {
	return func(s *Service) { s.disableTickLoop = true }
}

// WithDisableReconcile prevents bootstrapPositions from running in Start().
// Used in backtest mode where there are no broker positions to reconcile.
func WithDisableReconcile() Option {
	return func(s *Service) { s.disableReconcile = true }
}

func WithSnapshotFunc(fn IndicatorSnapshotFunc) Option {
	return func(s *Service) { s.snapshotFn = fn }
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
		eventBus:                eventBus,
		priceCache:              priceCache,
		positionGate:            positionGate,
		log:                     log.With().Str("service", "position_monitor").Logger(),
		nowFunc:                 time.Now,
		fills:                   make(chan fillMsg, 256),
		exitSubmitted:           make(chan exitOrderSubmittedMsg, 64),
		exitTerminal:            make(chan exitOrderTerminalMsg, 64),
		exitRejected:            make(chan exitRejectedMsg, 64),
		outbox:                  make(chan outboxMsg, 64),
		stopCh:                  make(chan struct{}),
		positions:               make(map[string]*domain.MonitoredPosition),
		ghostMissCounts:         make(map[string]int),
		pendingGlobalOrphans:    make(map[domain.Symbol]int),
		tickInterval:            1 * time.Second,
		reconcileInterval:       defaultReconcileInterval,
		globalReconcileInterval: defaultGlobalReconcileInterval,
		maxPriceStaleness:       30 * time.Second,
		tenantID:                tenantID,
		envMode:                 envMode,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start subscribes to FillReceived events and launches the actor goroutines.
func (s *Service) Start(ctx context.Context) error {
	if err := s.eventBus.SubscribeAsync(ctx, domain.EventFillReceived, s.handleFillEvent); err != nil {
		return fmt.Errorf("position_monitor: failed to subscribe to FillReceived: %w", err)
	}
	if err := s.eventBus.Subscribe(ctx, domain.EventOrderSubmitted, s.handleOrderSubmitted); err != nil {
		return fmt.Errorf("position_monitor: failed to subscribe to OrderSubmitted: %w", err)
	}
	if err := s.eventBus.Subscribe(ctx, domain.EventExitOrderTerminal, s.handleExitOrderTerminal); err != nil {
		return fmt.Errorf("position_monitor: failed to subscribe to ExitOrderTerminal: %w", err)
	}
	if err := s.eventBus.Subscribe(ctx, domain.EventOrderIntentRejected, s.handleExitRejected); err != nil {
		return fmt.Errorf("position_monitor: failed to subscribe to OrderIntentRejected: %w", err)
	}

	// Bootstrap: seed monitor with OMO-opened positions that are still on the broker.
	if !s.disableReconcile {
		s.bootstrapPositions(ctx)
	}

	// Outbox publisher goroutine — reads exit intents and publishes them.
	go s.runOutbox(ctx)

	// Actor tick loop — the single goroutine that owns all mutable state.
	if !s.disableTickLoop {
		go s.runTickLoop(ctx)
	}

	s.log.Info().
		Dur("tick_interval", s.tickInterval).
		Dur("max_price_staleness", s.maxPriceStaleness).
		Int("bootstrapped_positions", len(s.positions)).
		Msg("position monitor started")
	return nil
}

// runTickLoop is the main actor goroutine. It owns all position state.
func (s *Service) runTickLoop(ctx context.Context) {
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	reconcileTicker := time.NewTicker(s.reconcileInterval)
	defer reconcileTicker.Stop()

	globalReconcileTicker := time.NewTicker(s.globalReconcileInterval)
	defer globalReconcileTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case fill := <-s.fills:
			s.processFill(fill)
		case msg := <-s.exitSubmitted:
			s.processExitSubmitted(msg)
		case msg := <-s.exitTerminal:
			s.processExitTerminal(msg)
		case msg := <-s.exitRejected:
			s.processExitRejected(msg)
		case <-reconcileTicker.C:
			s.reconcileWithBroker(ctx)
		case <-globalReconcileTicker.C:
			s.reconcileGlobal(ctx)
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Service) processExitSubmitted(msg exitOrderSubmittedMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s:%s:%s", s.tenantID, s.envMode, msg.Symbol)
	pos, ok := s.positions[key]
	if !ok {
		return
	}
	pos.ExitOrderID = msg.BrokerOrderID
	if !pos.ExitPending {
		pos.ExitPending = true
		pos.ExitPendingAt = s.nowFunc()
	}
	s.log.Info().
		Str("symbol", string(msg.Symbol)).
		Str("broker_order_id", msg.BrokerOrderID).
		Bool("exit_pending", pos.ExitPending).
		Msg("exit order tracked — position locked for exit")
}

// processExitTerminal clears ExitPending when an exit order is canceled, rejected, or expired.
// This allows the position monitor's tick loop to re-evaluate exit rules and retry.
func (s *Service) processExitTerminal(msg exitOrderTerminalMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s:%s:%s", s.tenantID, s.envMode, msg.Symbol)
	pos, ok := s.positions[key]
	if !ok {
		return
	}
	// Only clear if this terminal event matches the tracked exit order.
	if pos.ExitOrderID != "" && pos.ExitOrderID != msg.BrokerOrderID {
		return
	}
	pos.ExitPending = false
	pos.ExitOrderID = ""
	pos.ExitRetryCount++
	s.log.Info().
		Str("symbol", string(msg.Symbol)).
		Str("broker_order_id", msg.BrokerOrderID).
		Int("retry_count", pos.ExitRetryCount).
		Msg("exit order terminal — unlocking position for retry")
}

// processExitRejected removes a ghost position when the broker confirms no position exists.
func (s *Service) processExitRejected(msg exitRejectedMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s:%s:%s", s.tenantID, s.envMode, msg.Symbol)
	pos, ok := s.positions[key]
	if !ok {
		return
	}

	s.log.Warn().
		Str("symbol", string(msg.Symbol)).
		Str("reason", msg.Reason).
		Msg("exit rejected with no_position_to_exit — removing ghost position from monitor")

	if s.positionGate != nil {
		s.positionGate.ClearInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol)
	}
	delete(s.positions, key)
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
			// Keep ExitPending=true: the broker order is still active for the
			// remaining quantity. Clearing it would let the tick loop fire
			// another full-qty exit, causing double-sells.
			s.log.Info().
				Str("symbol", string(fill.Symbol)).
				Float64("exit_price", fill.Price).
				Float64("remaining_qty", pos.Quantity).
				Bool("exit_pending", pos.ExitPending).
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

	pos, err := domain.NewMonitoredPosition(
		fill.Symbol, fill.Price, fill.FilledAt,
		fill.Strategy, fill.AssetClass, exitRules,
		s.tenantID, s.envMode, fill.Quantity,
	)
	if err != nil {
		s.log.Error().Err(err).Str("symbol", string(fill.Symbol)).Msg("failed to create monitored position")
		return
	}
	pos.ExitRules = applyRiskModifierToExitRules(pos.InitialExitRules, fill.RiskModifier)

	if fill.InstrumentType == domain.InstrumentTypeOption {
		pos.InstrumentType = fill.InstrumentType
		pos.OptionExpiry = fill.OptionExpiry
		pos.OptionRight = fill.OptionRight
	}

	s.positions[key] = &pos
	s.log.Info().
		Str("symbol", string(fill.Symbol)).
		Float64("entry_price", fill.Price).
		Float64("quantity", fill.Quantity).
		Int("exit_rules", len(exitRules)).
		Msg("new position added to monitor")
}

// EvalExitRules synchronously evaluates exit rules for all active positions
// using the provided barTime as the current time. Used in backtest mode where
// the tick loop is disabled and exit evaluation is driven per-bar.
//
// After tick() triggers exits, drainOutbox() publishes the resulting
// OrderIntentCreated events synchronously so that WaitPending() correctly
// tracks the downstream execution handler. Without this, the runOutbox
// goroutine may publish after WaitPending returns, causing exit fills to
// use the next bar's price.
func (s *Service) EvalExitRules(barTime time.Time) {
	origNow := s.nowFunc
	s.nowFunc = func() time.Time { return barTime }
	defer func() { s.nowFunc = origNow }()

	s.drainFills()
	s.tick()
	s.drainOutbox()
}

// drainOutbox non-blockingly reads all pending exit intents from the outbox
// channel and publishes them synchronously on the event bus. This ensures
// exit OrderIntentCreated events are dispatched (and tracked by WaitPending)
// before EvalExitRules returns.
func (s *Service) drainOutbox() {
	ctx := context.Background()
	for {
		select {
		case msg := <-s.outbox:
			s.emit(ctx, domain.EventExitTriggered, msg.TenantID, msg.EnvMode, msg.IdempotencyKey, msg.ExitTriggered)
			s.emit(ctx, domain.EventOrderIntentCreated, msg.TenantID, msg.EnvMode, msg.Intent.IdempotencyKey, msg.Intent)
		default:
			return
		}
	}
}

// drainFills non-blockingly reads all pending messages from actor channels
// and processes them. Used before EvalExitRules to ensure fills from the
// current bar are incorporated before exit evaluation.
func (s *Service) drainFills() {
	for {
		select {
		case fill := <-s.fills:
			s.processFill(fill)
		case msg := <-s.exitSubmitted:
			s.processExitSubmitted(msg)
		case msg := <-s.exitTerminal:
			s.processExitTerminal(msg)
		case msg := <-s.exitRejected:
			s.processExitRejected(msg)
		default:
			return
		}
	}
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

// LookupPosition returns a copy of the MonitoredPosition for the given symbol, if one exists.
func (s *Service) LookupPosition(symbol string) (domain.MonitoredPosition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := fmt.Sprintf("%s:%s:%s", s.tenantID, s.envMode, symbol)
	pos, ok := s.positions[key]
	if !ok {
		return domain.MonitoredPosition{}, false
	}
	return *pos, true
}

func (s *Service) ListPositions() []domain.MonitoredPosition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	positions := make([]domain.MonitoredPosition, 0, len(s.positions))
	for _, pos := range s.positions {
		positions = append(positions, *pos)
	}
	return positions
}
