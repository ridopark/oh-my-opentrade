package indicator

import (
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

type snapKey struct {
	symbol    domain.Symbol
	timeframe domain.Timeframe
}

type aggKey struct {
	symbol    domain.Symbol
	timeframe domain.Timeframe
}

type callbackFn func(closed domain.MarketBar, snap domain.IndicatorSnapshot)

type subscription struct {
	id uint64
	fn callbackFn
}

type Service struct {
	mu          sync.RWMutex
	calc        *monitor.IndicatorCalculator
	last        map[snapKey]domain.IndicatorSnapshot
	aggregators map[aggKey]*domain.BarAggregator
	subs        map[aggKey][]subscription
	nextSubID   uint64
	sessionOpen time.Time
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

func (s *Service) Update(bar domain.MarketBar) domain.IndicatorSnapshot {
	type firing struct {
		closed domain.MarketBar
		snap   domain.IndicatorSnapshot
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
			pending = append(pending, firing{closed: closed, snap: htfSnap, subs: fired})
		}
	}
	s.mu.Unlock()

	for _, p := range pending {
		for _, sub := range p.subs {
			sub.fn(p.closed, p.snap)
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
//   - fn fires synchronously on the goroutine that called Update, after the
//     calc state and last-snapshot map are committed and the internal mutex
//     is released. LastSnapshot is safe to re-enter from fn.
//   - fn MUST NOT call Subscribe or the unsubscribe closure (deadlock: same
//     mutex). Defer state mutations or wrap with goroutines if needed.
//   - Multiple subscribers for the same (sym, tf) fire in registration order.
//   - The returned unsubscribe closure is idempotent: calling it twice is a
//     no-op.
func (s *Service) Subscribe(sym domain.Symbol, tf domain.Timeframe, fn func(closed domain.MarketBar, snap domain.IndicatorSnapshot)) func() {
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
