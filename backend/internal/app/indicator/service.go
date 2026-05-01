package indicator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

type snapKey struct {
	symbol    domain.Symbol
	timeframe domain.Timeframe
}

type aggKey struct {
	symbol    domain.Symbol
	timeframe domain.Timeframe
}

// SubscribeFunc is the callback signature for HTF close events. The envelope
// carries the parent event's tenant/envMode/idem-key/occurredAt so callbacks
// can build derived events without reaching into the parent event payload.
//
// Type alias (not new type) so callers can pass plain function literals; this
// also keeps monitor.IndicatorShadow's structural conformance to *Service
// without forcing monitor to import the indicator package.
type SubscribeFunc = func(closed domain.MarketBar, snap domain.IndicatorSnapshot, env domain.MarketBarEnvelope)

type subscription struct {
	id uint64
	fn SubscribeFunc
}

// Service is the canonical indicator state owner. It is the SOLE driver of
// indicator.Update on its instance — both monitor and strategy.Runner consume
// state via Subscribe + LastSnapshot, neither calls Update directly. This
// invariant is enforced by Start subscribing the service to MarketBarSanitized
// events and by HandleSanitized{Direct,Typed} for the backtest direct-dispatch
// path. Violating it (a second Update caller for the same bar.Time) silently
// dedup's at the BarAggregator and calc layers, starving Subscribe callbacks.
type Service struct {
	mu          sync.RWMutex
	calc        *monitor.IndicatorCalculator
	last        map[snapKey]domain.IndicatorSnapshot
	aggregators map[aggKey]*domain.BarAggregator
	subs        map[aggKey][]subscription
	nextSubID   uint64
	sessionOpen time.Time

	// bus is set by Start; non-nil enables AppendPublish drain after Update
	// returns. Warmup paths (WarmUp/WarmUpCollect/PrimeAggregator) bypass
	// callbacks entirely and never touch pendingPublish, so a nil bus is
	// safe for those instances.
	bus ports.EventBusPort

	// pendingPublish accumulates events appended by Subscribe callbacks
	// during a single Update. Drained by handleSanitized /
	// HandleSanitized{Direct,Typed} AFTER the callback fan-out completes
	// and the lock is released. Single-goroutine invariant per instance:
	// callbacks fire on the entry handler's goroutine, no concurrent
	// AppendPublish from different goroutines on one instance.
	pendingPublish []domain.Event
}

// NewService labels the wrapped calc so parity-diag rows distinguish
// live vs backtest instances in shared log output.
func NewService(label string) *Service {
	calc := monitor.NewIndicatorCalculator()
	calc.Label = label
	return &Service{
		calc:        calc,
		last:        make(map[snapKey]domain.IndicatorSnapshot),
		aggregators: make(map[aggKey]*domain.BarAggregator),
		subs:        make(map[aggKey][]subscription),
	}
}

// Start wires the service as the sole subscriber that drives Update on
// EventMarketBarSanitized. Call BEFORE monitor.Start and runner.Start so the
// indicator handler runs first per bar — monitor and runner then read fresh
// state via LastSnapshot and Subscribe callbacks fire under the indicator's
// own goroutine. Stores the bus reference so AppendPublish drains can fan out.
func (s *Service) Start(ctx context.Context, bus ports.EventBusPort) error {
	if bus == nil {
		return fmt.Errorf("indicator: Start requires a non-nil bus")
	}
	s.mu.Lock()
	s.bus = bus
	s.mu.Unlock()
	if err := bus.Subscribe(ctx, domain.EventMarketBarSanitized, s.handleSanitized); err != nil {
		return fmt.Errorf("indicator: subscribe MarketBarSanitized: %w", err)
	}
	return nil
}

// HandleSanitizedDirect is the Event-wrapped direct-dispatch entry used by
// backtest pipelines that bypass the bus on the hot path. Mirrors what
// handleSanitized does when invoked via Subscribe.
func (s *Service) HandleSanitizedDirect(ctx context.Context, ev domain.Event) error {
	return s.handleSanitized(ctx, ev)
}

// HandleSanitizedTyped is the typed (allocation-free) direct-dispatch entry
// used by ProcessBarPhaseATyped. The typed path on monitor passes idemKey=""
// today (handleBarCore at monitor/service.go:855); we mirror that contract.
func (s *Service) HandleSanitizedTyped(ctx context.Context, bar domain.MarketBar, tenantID string, envMode domain.EnvMode, occurredAt time.Time) error {
	env := domain.MarketBarEnvelope{
		TenantID:   tenantID,
		EnvMode:    envMode,
		IdemKey:    "",
		OccurredAt: occurredAt,
	}
	s.UpdateWithEnv(bar, env)
	return s.drainPublish(ctx)
}

func (s *Service) handleSanitized(ctx context.Context, ev domain.Event) error {
	bar, ok := ev.Payload.(domain.MarketBar)
	if !ok {
		return fmt.Errorf("indicator: payload is not a MarketBar, got %T", ev.Payload)
	}
	s.UpdateWithEnv(bar, domain.EnvelopeFromEvent(ev))
	return s.drainPublish(ctx)
}

// AppendPublish queues an event to be published AFTER the current Update
// completes and the lock is released. Subscribe callbacks call this to emit
// derived events (e.g. monitor's HTF MarketBarSanitized + RegimeShifted)
// without re-entering the indicator while it holds state, and without each
// callback owning its own bus reference.
//
// Single-goroutine contract: callbacks on a given Service fire on the entry
// handler's goroutine; AppendPublish must NOT be called from a goroutine
// other than that one. No mutex protects pendingPublish.
func (s *Service) AppendPublish(ev domain.Event) {
	s.pendingPublish = append(s.pendingPublish, ev)
}

func (s *Service) drainPublish(ctx context.Context) error {
	if len(s.pendingPublish) == 0 {
		return nil
	}
	pending := s.pendingPublish
	s.pendingPublish = s.pendingPublish[:0]
	if s.bus == nil {
		return nil
	}
	for _, ev := range pending {
		if err := s.bus.Publish(ctx, ev); err != nil {
			return fmt.Errorf("indicator: drainPublish: %w", err)
		}
	}
	return nil
}

// SetLabel relabels the wrapped calc. Used by backtest tagging so parity-diag
// rows can distinguish parallel runs.
func (s *Service) SetLabel(label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calc.Label = label
}

// RegisterEMAConfig registers custom fast/slow EMA periods on the wrapped calc
// for the given (symbol, timeframe). No-op if periods are invalid.
func (s *Service) RegisterEMAConfig(symbol, timeframe string, fastPeriod, slowPeriod int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calc.RegisterEMAConfig(symbol, timeframe, fastPeriod, slowPeriod)
}

// SeedState pre-populates EMA values for (symbol, timeframe) on the wrapped
// calc so subsequent Update calls perform incremental EMA computation instead
// of waiting for SMA seeding.
func (s *Service) SeedState(symbol, timeframe string, ema9, ema21, ema50 float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calc.SeedState(symbol, timeframe, ema9, ema21, ema50)
}

// Update drives a 1m or native-HTF bar through the calc and HTF aggregators
// with an empty envelope. Warmup paths and tests that don't need to thread
// event metadata through Subscribe callbacks use this.
//
// Production callers (live + backtest) MUST go through Start /
// HandleSanitizedDirect / HandleSanitizedTyped — those paths build the
// envelope from the parent event so HTF MarketBarSanitized + RegimeShifted
// events monitor's callback emits carry the correct tenant/envMode/idem-key
// metadata.
func (s *Service) Update(bar domain.MarketBar) domain.IndicatorSnapshot {
	return s.UpdateWithEnv(bar, domain.MarketBarEnvelope{})
}

// UpdateWithEnv is the production entry called by handleSanitized and the
// direct-dispatch entries. The envelope is propagated to Subscribe callbacks
// so they can build derived events without parsing the parent event payload.
func (s *Service) UpdateWithEnv(bar domain.MarketBar, env domain.MarketBarEnvelope) domain.IndicatorSnapshot {
	type firing struct {
		closed domain.MarketBar
		snap   domain.IndicatorSnapshot
		env    domain.MarketBarEnvelope
		subs   []subscription
	}
	var pending []firing

	s.mu.Lock()
	snap := s.calc.Update(bar)
	s.last[snapKey{bar.Symbol, bar.Timeframe}] = snap

	if bar.Timeframe == "1m" && len(s.aggregators) > 0 {
		for k, agg := range s.aggregators {
			if k.symbol != bar.Symbol {
				continue
			}
			closed, ok := agg.Push(bar)
			if !ok {
				continue
			}
			htfSnap := s.calc.Update(closed)
			s.last[snapKey{closed.Symbol, closed.Timeframe}] = htfSnap
			subs := s.subs[k]
			if len(subs) == 0 {
				continue
			}
			fired := make([]subscription, len(subs))
			copy(fired, subs)
			pending = append(pending, firing{closed: closed, snap: htfSnap, env: env, subs: fired})
		}
	}
	s.mu.Unlock()

	for _, p := range pending {
		for _, sub := range p.subs {
			sub.fn(p.closed, p.snap, p.env)
		}
	}
	return snap
}

func (s *Service) LastSnapshot(sym domain.Symbol, tf domain.Timeframe) (domain.IndicatorSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.last[snapKey{sym, tf}]
	return snap, ok
}

func (s *Service) WarmUp(bars []domain.MarketBar) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range bars {
		snap := s.calc.Update(b)
		s.last[snapKey{b.Symbol, b.Timeframe}] = snap
	}
}

// WarmUpCollect drives bars through the calc and returns the per-bar snapshot
// produced by each Update, without pushing into HTF aggregators or firing
// Subscribe callbacks. Used by warmup paths that need to capture seeded
// snapshots for downstream replay (e.g. ORB seeding via cached BarSnapshots)
// while keeping HTF state untouched.
func (s *Service) WarmUpCollect(bars []domain.MarketBar) []domain.IndicatorSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.IndicatorSnapshot, 0, len(bars))
	for _, b := range bars {
		snap := s.calc.Update(b)
		s.last[snapKey{b.Symbol, b.Timeframe}] = snap
		out = append(out, snap)
	}
	return out
}

// sessionResetTimeframes are the per-symbol timeframes whose session-aligned
// state (VWAP accumulators) must be cleared between trading sessions. Mirrors
// monitor's anchorTimeframes plus 1m.
var sessionResetTimeframes = []domain.Timeframe{"1m", "5m", "15m", "1h"}

// ResetSessionIndicators clears session-VWAP and other session-reset state for
// sym across the calc's tracked timeframes, so that the next bar accumulates
// session VWAP from a clean baseline. Used by boot and replay paths between
// trading days.
func (s *Service) ResetSessionIndicators(sym domain.Symbol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	symStr := sym.String()
	for _, tf := range sessionResetTimeframes {
		s.calc.ResetSession(symStr, tf.String())
	}
}

// SetSessionOpen records the session-aligned anchor used by lazily-constructed
// equity aggregators and resets all existing aggregators. Crypto symbols use
// clock-aligned buckets and ignore the anchor.
func (s *Service) SetSessionOpen(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionOpen = t
	for k, agg := range s.aggregators {
		if k.symbol.IsCryptoSymbol() {
			continue
		}
		agg.Reset(t)
	}
}

// Subscribe registers fn to receive closed (sym, tf) HTF bars derived from 1m
// Updates. The aggregator is constructed lazily on first Subscribe per
// (sym, tf). Callers receive an unsubscribe closure.
//
// Contract:
//   - fn fires synchronously on the goroutine that drove Update (the
//     indicator's bus handler or HandleSanitized{Direct,Typed} caller),
//     after the calc state and last-snapshot map are committed and the
//     internal mutex is released. LastSnapshot is safe to re-enter from fn.
//   - fn receives the parent event envelope so it can build derived events
//     (HTF MarketBarSanitized, RegimeShifted) without re-parsing the parent
//     event. AppendPublish queues those derived events for fan-out after
//     the current Update completes.
//   - fn MUST NOT call Subscribe or the unsubscribe closure (deadlock: same
//     mutex). Defer state mutations or wrap with goroutines if needed.
//   - Multiple subscribers for the same (sym, tf) fire in registration order.
//   - The returned unsubscribe closure is idempotent: calling it twice is a
//     no-op.
func (s *Service) Subscribe(sym domain.Symbol, tf domain.Timeframe, fn SubscribeFunc) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := aggKey{sym, tf}
	if _, ok := s.aggregators[k]; !ok {
		if agg := s.newAggregatorLocked(sym, tf); agg != nil {
			s.aggregators[k] = agg
		}
	}
	s.nextSubID++
	id := s.nextSubID
	s.subs[k] = append(s.subs[k], subscription{id: id, fn: fn})

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		list := s.subs[k]
		for i, sub := range list {
			if sub.id == id {
				s.subs[k] = append(list[:i], list[i+1:]...)
				return
			}
		}
	}
}

// PrimeAggregator feeds 1m bars through the (sym, tf) aggregator without
// driving calc.Update and without firing subscribers. Used by warmup paths to
// seed today's pre-boot 1m bars into HTF buckets after calc state has already
// been built from a different source.
func (s *Service) PrimeAggregator(sym domain.Symbol, tf domain.Timeframe, bars1m []domain.MarketBar) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := aggKey{sym, tf}
	agg, ok := s.aggregators[k]
	if !ok {
		agg = s.newAggregatorLocked(sym, tf)
		if agg == nil {
			return
		}
		s.aggregators[k] = agg
	}
	for _, b := range bars1m {
		agg.Push(b)
	}
}

func (s *Service) newAggregatorLocked(sym domain.Symbol, tf domain.Timeframe) *domain.BarAggregator {
	if sym.IsCryptoSymbol() {
		agg, err := domain.NewClockAlignedAggregator(sym, tf)
		if err != nil {
			return nil
		}
		return agg
	}
	if s.sessionOpen.IsZero() {
		return nil
	}
	agg, err := domain.NewBarAggregator(sym, tf, s.sessionOpen)
	if err != nil {
		return nil
	}
	return agg
}
