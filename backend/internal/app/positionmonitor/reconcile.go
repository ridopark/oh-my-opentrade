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

// reconcileGlobal performs a full portfolio audit by comparing the entire trades DB
// against the broker's position list. Unlike reconcileWithBroker (which only checks
// in-memory monitored positions), this catches "invisible" orphans — DB residuals
// left behind after a position was fully closed and removed from the monitor.
func (s *Service) reconcileGlobal(ctx context.Context) {
	if s.broker == nil || s.repo == nil {
		return
	}

	brokerPositions, err := s.broker.GetPositions(ctx, s.tenantID, s.envMode)
	if err != nil {
		s.log.Warn().Err(err).Msg("global-reconcile: failed to query broker positions — skipping")
		return
	}
	brokerBySymbol := make(map[domain.Symbol]float64, len(brokerPositions))
	for _, bp := range brokerPositions {
		brokerBySymbol[bp.Symbol] = bp.Quantity
	}

	dbPositions, err := s.repo.GetNetPositions(ctx, s.tenantID, s.envMode)
	if err != nil {
		s.log.Warn().Err(err).Msg("global-reconcile: failed to query DB net positions — skipping")
		return
	}

	reconciled := 0
	for sym, dbQty := range dbPositions {
		if dbQty <= 1e-10 {
			continue
		}

		brokerQty, onBroker := brokerBySymbol[sym]

		if !onBroker {
			s.log.Warn().
				Str("symbol", string(sym)).
				Float64("db_net_qty", dbQty).
				Msg("global-reconcile: DB orphan detected — inserting reconciliation SELL")

			trade := domain.Trade{
				Time:      s.nowFunc(),
				TenantID:  s.tenantID,
				EnvMode:   s.envMode,
				TradeID:   uuid.New(),
				Symbol:    sym,
				Side:      "SELL",
				Quantity:  dbQty,
				Price:     0,
				Status:    "FILLED",
				Strategy:  "reconciliation",
				Rationale: fmt.Sprintf("global-reconcile: DB net %.10f but no broker position", dbQty),
			}
			if err := s.repo.SaveTrade(ctx, trade); err != nil {
				s.log.Error().Err(err).Str("symbol", string(sym)).Msg("global-reconcile: failed to save reconciliation SELL")
			} else {
				reconciled++
				s.log.Info().
					Str("symbol", string(sym)).
					Float64("quantity", dbQty).
					Msg("global-reconcile: reconciliation SELL inserted")
			}
			continue
		}

		drift := math.Abs(dbQty - brokerQty)
		if drift > 1e-6 {
			s.log.Warn().
				Str("symbol", string(sym)).
				Float64("db_qty", dbQty).
				Float64("broker_qty", brokerQty).
				Float64("drift", drift).
				Msg("global-reconcile: quantity drift detected (informational)")
		}
	}

	if reconciled > 0 {
		s.log.Info().Int("reconciled", reconciled).Msg("global-reconcile: cycle complete")
	}
}
