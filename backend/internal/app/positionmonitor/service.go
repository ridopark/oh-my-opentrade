package positionmonitor

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/options"
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
	eventBus      ports.EventBusPort
	priceCache    ports.PriceCachePort
	positionGate  *execution.PositionGate
	broker        ports.BrokerPort
	repo          ports.RepositoryPort
	intentJournal ports.OrderIntentJournal // Sprint 2 — nil means legacy cancel-all bootstrap
	// notifier, when non-nil, is used by the bootstrap reconciler to raise
	// Discord/Telegram alerts for unmanaged broker orders and lost journal
	// intents. Nil is safe — alerts fall back to log warnings only.
	notifier  ports.NotifierPort
	specStore portstrategy.SpecStore
	log       zerolog.Logger
	nowFunc   func() time.Time

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
	pendingGlobalDrifts  map[domain.Symbol]int                // key: symbol → consecutive broker>DB drift observations
	mu                   sync.RWMutex                         // protects positions for concurrent reads (e.g. PositionCount)

	barDurCache         map[string]time.Duration // cached barDurationFor results
	snapshotFn          IndicatorSnapshotFunc
	optionsPricePort    ports.OptionsPricePort
	earningsCalendar    ports.EarningsCalendarPort
	optionsPollInterval time.Duration

	// repegNotifier, when non-nil, is called by the re-peg/escalate path
	// just before a broker CancelOrder so the execution service can tag
	// the soon-to-be-terminal pending order as "do not record as failure,
	// do not dust-sweep". Nil-safe: without a notifier the cancel still
	// works, but cleanupPendingOrder will run its default terminal actions
	// — which caused today's SOFI phantom short.
	repegNotifier RepegNotifier

	// atrTrailCfg carries the ATR-bucketed premium-trail multiplier
	// configuration (see [exits.atr_trail] in YAML). When Enabled=false
	// the option-fill stamper short-circuits and positions carry no
	// atr_trail_mult; EvalContext defaults to 1.0 and evaluatePremiumTrail
	// produces byte-identical exit prices to the pre-fix code path. Held
	// as an embedded struct (not *config.ATRTrailConfig) so positionmonitor
	// does not import the config package on its hot path — SetATRTrailConfig
	// accepts a config value and copies the fields into this private mirror.
	atrTrailCfg atrTrailConfigValue

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

	// isShuttingDown is set via SignalShutdown() from the main shutdown
	// sequence. When true, all reconcile entry points early-return so a
	// reconciliation tick firing between orchestrator.Stop() and
	// broker.Close() cannot emit stale reconciliation trades or events.
	isShuttingDown atomic.Bool
}

// fillMsg is the internal message type enqueued when a FillReceived event arrives.
type fillMsg struct {
	Symbol       domain.Symbol
	Side         string
	Direction    string // original intent direction (e.g. "LONG", "SHORT", "CLOSE_LONG")
	Price        float64
	Quantity     float64
	FilledAt     time.Time
	Strategy     string
	AssetClass   domain.AssetClass
	ExitRules    []domain.ExitRule // set by bootstrap; nil from live fill events
	RiskModifier domain.RiskModifier

	// Signal tags propagated from the entry signal through OrderIntent.Meta.
	// Used to carry per-trade exit modifiers (e.g. Z-conditioned multipliers).
	SignalTags map[string]string

	// Option metadata (populated only for option fills).
	InstrumentType domain.InstrumentType
	OptionExpiry   time.Time
	OptionRight    string
	IVAtEntry      float64
	DeltaAtEntry   float64
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
	// exitPendingTimeout is the legacy equity timeout and the default when a
	// rule has no asymmetric override. Kept for the equity path which has no
	// re-peg budget (tight US-equity spreads make chasing a worse idea than
	// market-escalating after 10s).
	exitPendingTimeout = 10 * time.Second

	// Asymmetric exit pending timeouts. Stops protect capital — cancel fast
	// and market-escalate. Targets defer profit — give the book time to come
	// up before giving up the spread. Equity stays at 10s regardless.
	exitPendingTimeoutEquity       = 10 * time.Second
	exitPendingTimeoutOptionStop   = 10 * time.Second
	exitPendingTimeoutOptionTarget = 30 * time.Second

	// Re-peg budget: how many tightening cancel/resubmit cycles per exit
	// attempt before we escalate to market. Stops get one re-peg (fast
	// escalation); targets get three (more patience for profit-taking).
	exitMaxRepegsStop   = 1
	exitMaxRepegsTarget = 3

	// exitMaxWallTime bounds a single exit attempt end-to-end. Guards
	// against a pathological broker-feed combo where cancel+resubmit cycles
	// could loop indefinitely under the per-retry cap.
	exitMaxWallTime = 120 * time.Second

	// exitCancelConfirm caps the wait for terminal broker status after a
	// cancel before proceeding. Past this we log and force-clear because
	// live-trading cannot wedge waiting for a single order ack.
	exitCancelConfirm = 1 * time.Second

	// exitNoRepegBeforeCloseMin is the t-minus window before session close
	// during which we stop re-pegging and go straight to market. Avoids
	// ending the day flat-on-limit-no-fill on 0DTE / deep-ITM exits.
	exitNoRepegBeforeCloseMin = 15

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

// RepegNotifier is the narrow hook the position monitor uses to tell the
// execution service a pending broker order is about to be canceled as
// part of a re-peg/escalate sequence. Implemented by *execution.Service's
// MarkRepegCancel method. Defined here (not in ports) because it is an
// app-layer coordination primitive between two app services — there's no
// domain concept behind it, just a suppression flag.
type RepegNotifier interface {
	MarkRepegCancel(brokerOrderID string) bool
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

// WithIntentJournal injects the Sprint 2 order-intent journal. When set,
// bootstrap uses the journal-aware reconciliation flow; when nil, it
// falls back to the legacy cancel-all behavior. Gated at the caller by
// cfg.OrderJournalEnabled.
func WithIntentJournal(j ports.OrderIntentJournal) Option {
	return func(s *Service) { s.intentJournal = j }
}

// WithNotifier injects a NotifierPort so the startup reconciler can push
// operator-facing alerts (unmanaged broker orders, lost journal intents,
// fallback trips) through the same Discord/Telegram fan-out the rest of
// the system uses. Nil is safe — alerts fall back to log warnings only.
func WithNotifier(n ports.NotifierPort) Option {
	return func(s *Service) { s.notifier = n }
}

// SetNotifier is a post-construction setter for the reconciliation
// notifier, mirroring the existing Runner.SetNotifier pattern. Needed
// because cmd/omo-core constructs the position monitor before the
// notification adapters are wired (revaluator depends on posMonitor,
// notifiers depend on neither, but the current init order builds pos-
// Monitor first). Reconciliation alerts only fire during bootstrap
// which runs when Start(ctx) is invoked — well after init — so wiring
// the notifier post-construction is safe.
func (s *Service) SetNotifier(n ports.NotifierPort) { s.notifier = n }

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

func WithOptionsPricePort(p ports.OptionsPricePort) Option {
	return func(s *Service) { s.optionsPricePort = p }
}

// WithEarningsCalendar injects the earnings calendar port for days-to-earnings lookups.
func WithEarningsCalendar(ec ports.EarningsCalendarPort) Option {
	return func(s *Service) { s.earningsCalendar = ec }
}

// WithRepegNotifier wires the execution-service hook that suppresses
// default terminal actions (dust-sweep, failure-count) for broker orders
// canceled by the re-peg/escalate path.
func WithRepegNotifier(n RepegNotifier) Option {
	return func(s *Service) { s.repegNotifier = n }
}

// SetRepegNotifier is a post-construction setter for WithRepegNotifier.
// Needed because in cmd/omo-core the execution service is built before the
// position monitor (shared PositionGate via execBundle), and the monitor's
// constructor runs before we have a stable handle to execution.Service.
// Calling this after Start() is safe — it only affects subsequent cancels.
func (s *Service) SetRepegNotifier(n RepegNotifier) { s.repegNotifier = n }

// atrTrailConfigValue is the private mirror of config.ATRTrailConfig
// that lives on the Service. Keeping the fields here (rather than an
// imported config struct) avoids pulling the config package into the
// positionmonitor hot path, which would complicate backtest wiring.
type atrTrailConfigValue struct {
	Enabled                       bool
	ATRPeriod                     int
	ATRLookbackDays               int
	ATRLookbackDaysCrypto         int
	TercileLowPctile              float64
	TercileHighPctile             float64
	TercileMultipliers            []float64
	InsufficientHistoryMultiplier float64
	MinHistoryDays                int
}

// SetATRTrailConfig wires the ATR-bucketed premium-trail config. Called
// from cmd/omo-core after config load. Calling with Enabled=false (or
// not calling at all) leaves atr_trail_mult unset on new positions and
// preserves byte-identical exit behavior via the EvalContext default of 1.0.
func (s *Service) SetATRTrailConfig(
	enabled bool,
	atrPeriod, lookbackDays, lookbackDaysCrypto, minHistoryDays int,
	tercileLow, tercileHigh, insufficientHistMult float64,
	tercileMultipliers []float64,
) {
	s.atrTrailCfg = atrTrailConfigValue{
		Enabled:                       enabled,
		ATRPeriod:                     atrPeriod,
		ATRLookbackDays:               lookbackDays,
		ATRLookbackDaysCrypto:         lookbackDaysCrypto,
		TercileLowPctile:              tercileLow,
		TercileHighPctile:             tercileHigh,
		TercileMultipliers:            append([]float64(nil), tercileMultipliers...),
		InsufficientHistoryMultiplier: insufficientHistMult,
		MinHistoryDays:                minHistoryDays,
	}
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
		barDurCache:             make(map[string]time.Duration),
		positions:               make(map[string]*domain.MonitoredPosition),
		ghostMissCounts:         make(map[string]int),
		pendingGlobalOrphans:    make(map[domain.Symbol]int),
		pendingGlobalDrifts:     make(map[domain.Symbol]int),
		tickInterval:            1 * time.Second,
		optionsPollInterval:     30 * time.Second,
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
	if err := s.eventBus.Subscribe(ctx, domain.EventChandelierTrailArm, s.handleChandelierTrailArm); err != nil {
		return fmt.Errorf("position_monitor: failed to subscribe to ChandelierTrailArm: %w", err)
	}
	if err := s.eventBus.Subscribe(ctx, domain.EventCopytradeExitRequest, s.handleCopytradeExitRequest); err != nil {
		return fmt.Errorf("position_monitor: failed to subscribe to CopytradeExitRequest: %w", err)
	}

	// Bootstrap: seed monitor with OMO-opened positions that are still on the broker.
	if !s.disableReconcile {
		s.bootstrapPositions(ctx)
	}

	// Outbox publisher goroutine — reads exit intents and publishes them.
	// In backtest mode (disableTickLoop), drainOutbox handles this synchronously
	// to avoid race conditions with the sync event bus.
	if !s.disableTickLoop {
		go s.runOutbox(ctx)
		// Actor tick loop — the single goroutine that owns all mutable state.
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

	var optionsPollTicker *time.Ticker
	var optionsPollCh <-chan time.Time
	if s.optionsPricePort != nil {
		optionsPollTicker = time.NewTicker(s.optionsPollInterval)
		defer optionsPollTicker.Stop()
		optionsPollCh = optionsPollTicker.C
	}

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
		case <-optionsPollCh:
			go s.pollOptionPrices(ctx)
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Service) pollOptionPrices(ctx context.Context) {
	s.mu.RLock()
	var symbols []domain.Symbol
	for _, pos := range s.positions {
		if pos.InstrumentType == domain.InstrumentTypeOption {
			symbols = append(symbols, pos.Symbol)
		}
	}
	s.mu.RUnlock()

	if len(symbols) == 0 {
		return
	}

	quotes, err := s.optionsPricePort.GetOptionPrices(ctx, symbols)
	if err != nil {
		s.log.Warn().Err(err).Msg("options price poll failed")
		return
	}

	now := s.nowFunc()
	updated := 0
	for sym, q := range quotes {
		mid := (q.Bid + q.Ask) / 2
		if mid <= 0 {
			mid = q.Last
		}
		if mid > 0 {
			s.priceCache.UpdatePrice(sym, mid, now)
			updated++
		}
	}

	if updated > 0 {
		s.log.Info().Int("updated", updated).Msg("options price poll: cache refreshed")
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
	if pos.PendingExitOrderIDs == nil {
		pos.PendingExitOrderIDs = make(map[string]struct{})
	}
	pos.PendingExitOrderIDs[msg.BrokerOrderID] = struct{}{}
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

// processExitTerminal is the event-bus handler for EventExitOrderTerminal.
// Under the single-ExitPending invariant (post SOFI phantom-short fix,
// 2026-04-16), handleExitTimeout owns the cancel-and-resubmit lifecycle
// and stamps a fresh ExitPendingAt via triggerExit — ExitPending is
// never toggled false between attempts. So when this handler sees a
// terminal event whose broker-order-id no longer matches the tracked
// ExitOrderID (because handleExitTimeout already cleared or replaced it
// with the next attempt), it must be a no-op. We do not bump counters
// and we do not clear ExitPending. The counter bookkeeping lives
// exclusively in handleExitTimeout from here on.
func (s *Service) processExitTerminal(msg exitOrderTerminalMsg) {
	s.mu.Lock()
	key := fmt.Sprintf("%s:%s:%s", s.tenantID, s.envMode, msg.Symbol)
	pos, ok := s.positions[key]
	if !ok {
		s.mu.Unlock()
		return
	}
	if pos.PendingExitOrderIDs != nil {
		delete(pos.PendingExitOrderIDs, msg.BrokerOrderID)
	}
	if pos.ExitOrderID == "" {
		// handleExitTimeout already consumed the terminal and is mid-resubmit.
		s.mu.Unlock()
		return
	}
	if pos.ExitOrderID != msg.BrokerOrderID {
		// Event refers to a stale broker order id (old attempt). Ignore.
		s.mu.Unlock()
		return
	}
	// Terminal for the currently-tracked order WITHOUT handleExitTimeout
	// having driven the lifecycle (e.g. the broker canceled it unilaterally,
	// or a rejection arrived on a fresh exit). If other peer exit orders
	// are still working on this position, keep ExitPending set and cancel
	// them — the single-slot ExitOrderID cannot police a parallel working
	// exit, and the tick loop's ExitPending guard is the only thing that
	// stops a new rule from firing a second order in the gap.
	if len(pos.PendingExitOrderIDs) > 0 {
		// Clear the just-terminal id from the tracked slot so a follow-up
		// handleExitTimeout tick does not cancel-probe an already-terminal
		// order. ExitPending stays true; the peer sweep drives the lifecycle.
		pos.ExitOrderID = ""
		s.log.Warn().
			Str("symbol", string(msg.Symbol)).
			Str("terminal_order_id", msg.BrokerOrderID).
			Int("peer_count", len(pos.PendingExitOrderIDs)).
			Msg("exit order terminal with peer working exits - canceling peers, holding ExitPending")
		s.mu.Unlock()
		s.cancelAllPendingExits(key)
		return
	}
	pos.ExitPending = false
	pos.ExitOrderID = ""
	pos.ExitRetryCount++
	s.log.Info().
		Str("symbol", string(msg.Symbol)).
		Str("broker_order_id", msg.BrokerOrderID).
		Int("retry_count", pos.ExitRetryCount).
		Msg("exit order terminal (unsolicited) — unlocking position for retry")
	s.mu.Unlock()
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

	isExit := fill.Side == "SELL"
	// SHORT entries also have side="SELL" but should be treated as new positions.
	if fill.Direction != "" {
		isExit = domain.Direction(fill.Direction).IsExit()
	}
	if isExit {
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
		exitRules = s.resolveExitRules(context.Background(), fill.Strategy, fill.Symbol, fill.AssetClass)
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
	pos.Side = fill.Side // "BUY" for long, "SELL" for short

	if fill.InstrumentType == domain.InstrumentTypeOption {
		pos.InstrumentType = fill.InstrumentType
		pos.OptionExpiry = fill.OptionExpiry
		pos.OptionRight = fill.OptionRight
		// Store premium, delta, IV, strike, expiry, and option right for BSM exit pricing
		if pos.CustomState != nil {
			pos.CustomState["option_premium"] = fill.Price
			pos.CustomState["delta_at_entry"] = fill.DeltaAtEntry
			if fill.IVAtEntry > 0 {
				pos.CustomState["iv_at_entry"] = fill.IVAtEntry
			}
			// Store VIX level at entry for dynamic IV adjustment
			if vixSnap, ok := s.priceCache.LatestPrice("VIX"); ok && vixSnap.Price > 0 {
				pos.CustomState["vix_at_entry"] = vixSnap.Price
			}
			// Compute days-to-earnings for IV ramp model
			if s.earningsCalendar != nil {
				underlying := string(domain.UnderlyingFromOCC(fill.Symbol))
				if underlying != "" {
					if entry, err := s.earningsCalendar.GetNextEarnings(context.Background(), underlying); err == nil && entry != nil {
						days := int(entry.EarningsDate.Sub(fill.FilledAt.Truncate(24*time.Hour)).Hours() / 24)
						if days >= 0 {
							pos.CustomState["days_to_earnings"] = float64(days)
						}
					}
				}
			}
			// BSM recalculation fields: extract strike from OCC symbol
			if _, _, _, strike, ok := domain.ParseOCC(fill.Symbol); ok && strike > 0 {
				pos.CustomState["strike"] = strike
			}
			if !fill.OptionExpiry.IsZero() {
				pos.CustomState["expiry_unix"] = float64(fill.OptionExpiry.Unix())
			}
			if fill.OptionRight == "CALL" {
				pos.CustomState["is_call"] = 1.0
			} else {
				pos.CustomState["is_call"] = 0.0
			}
		}
		// Calibrate IV: solve for the IV that makes BSM reproduce the entry
		// premium at the current underlying price. This ensures that subsequent
		// BSM repricing only reflects actual underlying movement, not a
		// model-vs-market mismatch from using daily chain IV with an intraday
		// underlying price.
		if underlying := domain.UnderlyingFromOCC(fill.Symbol); underlying != "" {
			if snap, ok := s.priceCache.LatestPrice(underlying); ok {
				strike := pos.CustomState["strike"]
				expiryUnix := pos.CustomState["expiry_unix"]
				chainIV := pos.CustomState["iv_at_entry"]
				isCall := pos.CustomState["is_call"] == 1.0

				if strike > 0 && expiryUnix > 0 && fill.Price > 0 {
					expiryTime := time.Unix(int64(expiryUnix), 0)
					dteYears := expiryTime.Sub(fill.FilledAt).Hours() / (365.25 * 24)
					if dteYears > 0 {
						calibratedIV := options.ImpliedVol(
							fill.Price, snap.Price, strike, dteYears, 0.045, isCall, chainIV,
						)
						if calibratedIV != chainIV {
							s.log.Debug().
								Str("symbol", string(fill.Symbol)).
								Float64("chain_iv", chainIV).
								Float64("calibrated_iv", calibratedIV).
								Float64("entry_premium", fill.Price).
								Float64("underlying", snap.Price).
								Msg("IV calibrated to match entry premium")
						}
						pos.CustomState["iv_at_entry"] = calibratedIV
					}
				}

				// Set entry price to the UNDERLYING price so exit rules
				// (SD target, step stop, etc.) evaluate on the underlying's price action.
				pos.EntryPrice = snap.Price
				pos.HighWaterMark = snap.Price
				pos.LowWaterMark = snap.Price
			}
		}

		// Stamp the ATR%-bucket trail multiplier (2026-04-16 MRVL/SOXL
		// premature-exit fix). Once-per-position: the tick loop reads
		// pos.CustomState["atr_trail_mult"] on every bar without re-
		// computing. Enabled=false short-circuits to no-op; missing
		// history leaves the field unset and EvalContext defaults to 1.0.
		s.stampATRTrailOnPos(&pos)
	}

	// Store strategy params for the position monitor's hold-period guards.
	// premium_hold_bars: suppresses take-profit exits (default 1 bar if absent)
	// exit_hold_bars: used by AVWAP strategy-level exit (separate concern)
	if s.specStore != nil && fill.Strategy != "" {
		if sid, err := domstrategy.NewStrategyID(fill.Strategy); err == nil {
			if spec, err := s.specStore.GetLatest(context.Background(), sid); err == nil {
				for _, key := range []string{"exit_hold_bars", "premium_hold_bars"} {
					if v, ok := spec.Params[key]; ok {
						switch n := v.(type) {
						case int:
							pos.CustomState[key] = float64(n)
						case int64:
							pos.CustomState[key] = float64(n)
						case float64:
							pos.CustomState[key] = n
						}
					}
				}
			}
		}
	}

	// Store Z-conditioned exit multipliers from signal tags into CustomState.
	// These modulate PREMIUM_TARGET and STAGNATION_EXIT evaluators per-trade.
	if pos.CustomState != nil && fill.SignalTags != nil {
		for _, tagKey := range []string{"dp_z_premium_target_mult", "dp_z_stagnation_mult"} {
			if v, ok := fill.SignalTags[tagKey]; ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
					pos.CustomState[tagKey] = f
				}
			}
		}
	}

	// strategy_exits_priority flag: strategy's OnBar exits are authoritative,
	// skip price-based exit rules (see MonitoredPosition.StrategyExitsPriority).
	if fill.SignalTags != nil {
		if v, ok := fill.SignalTags["strategy_exits_priority"]; ok && v == "true" {
			pos.StrategyExitsPriority = true
		}
	}

	if len(fill.SignalTags) > 0 {
		pos.EntrySignalTags = make(map[string]string, len(fill.SignalTags))
		for k, v := range fill.SignalTags {
			pos.EntrySignalTags[k] = v
		}
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

// SignalShutdown marks the service as shutting down so that any subsequent
// reconciliation tick becomes a no-op. Call this from the main shutdown
// sequence BEFORE closing the broker connection — it prevents a reconcile
// tick from racing the shutdown and emitting a bogus reconciliation trade
// or position-close event against a half-torn-down broker.
func (s *Service) SignalShutdown() {
	s.isShuttingDown.Store(true)
}

// IsShuttingDown reports whether SignalShutdown has been called. Exposed for
// tests that need to assert the guard is wired correctly.
func (s *Service) IsShuttingDown() bool {
	return s.isShuttingDown.Load()
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
