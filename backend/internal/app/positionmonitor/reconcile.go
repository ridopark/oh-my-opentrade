package positionmonitor

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// reconcileWithBroker compares monitored positions against actual broker positions.
// Positions missing from the broker for ghostMissThreshold consecutive checks are removed.
// This supplements (does NOT replace) the existing self-healing in processExitRejected.
func (s *Service) reconcileWithBroker(ctx context.Context) {
	if s.broker == nil {
		return
	}

	brokerPositions, err := s.broker.GetPositions(ctx, s.tenantID, s.envMode)
	if err != nil {
		s.log.Warn().Err(err).Msg("reconcile: failed to query broker positions — skipping cycle")
		return
	}

	brokerSymbols := make(map[domain.Symbol]struct{}, len(brokerPositions))
	for _, bp := range brokerPositions {
		brokerSymbols[bp.Symbol] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for key, pos := range s.positions {
		if _, onBroker := brokerSymbols[pos.Symbol]; onBroker {
			delete(s.ghostMissCounts, key)
			continue
		}

		s.ghostMissCounts[key]++
		missCount := s.ghostMissCounts[key]

		if missCount < ghostMissThreshold {
			s.log.Info().
				Str("symbol", string(pos.Symbol)).
				Int("miss_count", missCount).
				Int("threshold", ghostMissThreshold).
				Msg("reconcile: position not on broker — observing")
			continue
		}

		s.log.Warn().
			Str("symbol", string(pos.Symbol)).
			Int("miss_count", missCount).
			Msg("reconcile: ghost position confirmed — removing from monitor")

		if s.positionGate != nil {
			s.positionGate.ClearInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol)
		}
		delete(s.positions, key)
		delete(s.ghostMissCounts, key)
	}

	for key := range s.ghostMissCounts {
		if _, exists := s.positions[key]; !exists {
			delete(s.ghostMissCounts, key)
		}
	}
}
