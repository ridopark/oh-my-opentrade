package positionmonitor

import (
	"context"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// reconcileWithBroker compares monitored positions against actual broker positions.
//
//  1. Quantity sync — when a position exists in both the monitor and the broker but
//     quantities differ (e.g. a WebSocket fill was dropped), the monitor is updated
//     to match the broker's authoritative quantity.
//  2. Ghost removal — positions missing from the broker for ghostMissThreshold
//     consecutive checks are removed from the monitor.
//  3. DB orphan patching — when a ghost position is removed, a reconciliation SELL
//     trade is written to the trade DB so the DB net position returns to zero.
func (s *Service) reconcileWithBroker(ctx context.Context) {
	if s.broker == nil {
		return
	}

	brokerPositions, err := s.broker.GetPositions(ctx, s.tenantID, s.envMode)
	if err != nil {
		s.log.Warn().Err(err).Msg("reconcile: failed to query broker positions — skipping cycle")
		return
	}

	brokerBySymbol := make(map[domain.Symbol]domain.Trade, len(brokerPositions))
	for _, bp := range brokerPositions {
		brokerBySymbol[bp.Symbol] = bp
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for key, pos := range s.positions {
		bp, onBroker := brokerBySymbol[pos.Symbol]
		if onBroker {
			delete(s.ghostMissCounts, key)

			if math.Abs(bp.Quantity-pos.Quantity) > 1e-9 {
				s.log.Warn().
					Str("symbol", string(pos.Symbol)).
					Float64("monitor_qty", pos.Quantity).
					Float64("broker_qty", bp.Quantity).
					Msg("reconcile: quantity mismatch — syncing monitor to broker")
				pos.Quantity = bp.Quantity
			}
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

		if s.repo != nil && pos.Quantity > 0 {
			trade := domain.Trade{
				Time:      s.nowFunc(),
				TenantID:  s.tenantID,
				EnvMode:   s.envMode,
				TradeID:   uuid.New(),
				Symbol:    pos.Symbol,
				Side:      "SELL",
				Quantity:  pos.Quantity,
				Price:     pos.EntryPrice,
				Status:    "FILLED",
				Strategy:  "reconciliation",
				Rationale: fmt.Sprintf("auto-reconcile: position absent from broker for %d consecutive checks", missCount),
			}
			if err := s.repo.SaveTrade(ctx, trade); err != nil {
				s.log.Error().Err(err).
					Str("symbol", string(pos.Symbol)).
					Msg("reconcile: failed to save reconciliation SELL trade")
			} else {
				s.log.Info().
					Str("symbol", string(pos.Symbol)).
					Float64("quantity", pos.Quantity).
					Float64("price", pos.EntryPrice).
					Msg("reconcile: reconciliation SELL trade saved to DB")
			}
		}

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
