package indicator

import (
	"sync"

	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

type snapKey struct {
	symbol    domain.Symbol
	timeframe domain.Timeframe
}

type Service struct {
	mu   sync.RWMutex
	calc *monitor.IndicatorCalculator
	last map[snapKey]domain.IndicatorSnapshot
}

// NewService labels the wrapped calc so parity-diag rows distinguish
// live vs backtest instances in shared log output.
func NewService(label string) *Service {
	calc := monitor.NewIndicatorCalculator()
	calc.Label = label
	return &Service{
		calc: calc,
		last: make(map[snapKey]domain.IndicatorSnapshot),
	}
}

func (s *Service) Update(bar domain.MarketBar) domain.IndicatorSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.calc.Update(bar)
	s.last[snapKey{bar.Symbol, bar.Timeframe}] = snap
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
