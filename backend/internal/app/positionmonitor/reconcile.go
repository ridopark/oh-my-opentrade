package positionmonitor

import (
	"context"
	"math"
	"sort"

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
	if s.isShuttingDown.Load() {
		return
	}
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

	// Sort for deterministic reconciliation order
	reconKeys := make([]string, 0, len(s.positions))
	for k := range s.positions { reconKeys = append(reconKeys, k) }
	sort.Strings(reconKeys)
	for _, key := range reconKeys {
		pos := s.positions[key]
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

		s.log.Error().
			Str("symbol", string(pos.Symbol)).
			Float64("monitor_qty", pos.Quantity).
			Float64("entry_price", pos.EntryPrice).
			Int("miss_count", missCount).
			Msg("reconcile: ghost position confirmed — removing from monitor (DB trade ledger unchanged; investigate manually)")

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

func (s *Service) reconcileGlobal(ctx context.Context) {
	if s.isShuttingDown.Load() {
		return
	}
	if s.broker == nil || s.repo == nil {
		return
	}

	brokerPositions, err := s.broker.GetPositions(ctx, s.tenantID, s.envMode)
	if err != nil {
		s.log.Warn().Err(err).Msg("global-reconcile: failed to query broker positions — skipping")
		return
	}
	// SignedQuantity resolves the broker sign convention: adapters return
	// non-negative Quantity + Side (canonical NewTrade contract), so we must
	// reconstruct the signed position here. Reading bp.Quantity directly
	// would miss shorts entirely — exactly the bug that let today's IBKR
	// -19 SOFI short go undetected by R1b.
	brokerBySymbol := make(map[domain.Symbol]float64, len(brokerPositions))
	for _, bp := range brokerPositions {
		brokerBySymbol[bp.Symbol] = bp.SignedQuantity()
	}

	dbPositions, err := s.repo.GetNetPositions(ctx, s.tenantID, s.envMode)
	if err != nil {
		s.log.Warn().Err(err).Msg("global-reconcile: failed to query DB net positions — skipping")
		return
	}

	// Snapshot symbols with an exit currently in flight. When the monitor has
	// emitted an exit order but the fill has not been recorded in the DB yet
	// (the gap from broker ack to SaveTrade), broker=0 / DB>0 is the expected
	// transient — not an orphan. Suppress the ERROR log and miss-count bump
	// for these symbols so Discord does not re-fire every 5 min until the
	// next session reconciles the DB.
	inFlightClosing := make(map[domain.Symbol]struct{})
	s.mu.RLock()
	for _, pos := range s.positions {
		if pos.ExitPending || len(pos.PendingExitOrderIDs) > 0 {
			inFlightClosing[pos.Symbol] = struct{}{}
		}
	}
	s.mu.RUnlock()

	// R1 — broker-only detection. Iterate the UNION of DB and broker
	// symbol sets, not just DB. Today's CRM phantom short (2026-04-16)
	// showed the broker holding -3 on a symbol whose DB net was 0, which
	// the DB-only loop below never surfaces. For broker-only symbols we
	// emit dedicated ERROR-level logs that route to Discord. This pass
	// is alert-only: no synthetic trades, no auto-cover. Auto-cover is on
	// a separate risk-manager track.
	for sym, brokerQty := range brokerBySymbol {
		if _, inDB := dbPositions[sym]; inDB {
			continue
		}
		if brokerQty < -1e-10 {
			// Long-only strategies (options are always long the contract)
			// should never produce a short at the broker. This log prefix
			// is monitored specifically — do not rephrase without updating
			// the alerting rules.
			s.log.Error().
				Str("symbol", string(sym)).
				Float64("broker_qty", brokerQty).
				Msg("UNINTENDED_SHORT: broker holds short qty on long-only instrument — manual intervention required")
			continue
		}
		if brokerQty > 1e-10 {
			s.log.Error().
				Str("symbol", string(sym)).
				Float64("broker_qty", brokerQty).
				Msg("broker-only position detected — DB has no record of this position")
		}
	}

	for sym, dbQty := range dbPositions {
		if dbQty < -1e-10 {
			s.log.Error().
				Str("symbol", string(sym)).
				Float64("db_net_qty", dbQty).
				Msg("global-reconcile: negative DB net detected — accounting error, investigate manually (no synthetic trade written)")
			delete(s.pendingGlobalOrphans, sym)
			delete(s.pendingGlobalDrifts, sym)
			continue
		}

		if dbQty <= 1e-10 {
			delete(s.pendingGlobalOrphans, sym)
			delete(s.pendingGlobalDrifts, sym)
			continue
		}

		brokerQty, onBroker := brokerBySymbol[sym]

		// R1b — UNINTENDED_SHORT check for symbols present at both DB and
		// broker. A broker holding a negative quantity on a long-only
		// instrument requires manual intervention regardless of the DB
		// state. Flag and continue; do not attempt drift math on a short.
		if onBroker && brokerQty < -1e-10 {
			s.log.Error().
				Str("symbol", string(sym)).
				Float64("broker_qty", brokerQty).
				Float64("db_net_qty", dbQty).
				Msg("UNINTENDED_SHORT: broker holds short qty on long-only instrument — manual intervention required")
			delete(s.pendingGlobalOrphans, sym)
			delete(s.pendingGlobalDrifts, sym)
			continue
		}

		if !onBroker {
			if _, closing := inFlightClosing[sym]; closing {
				// Monitor has an exit in flight — broker=0 / DB>0 is the
				// expected transient between broker-ack and DB SaveTrade.
				// Drop miss count and downgrade log so we do not alert.
				delete(s.pendingGlobalOrphans, sym)
				s.log.Debug().
					Str("symbol", string(sym)).
					Float64("db_net_qty", dbQty).
					Msg("global-reconcile: suppressing orphan check — exit in flight")
				continue
			}
			s.pendingGlobalOrphans[sym]++
			missCount := s.pendingGlobalOrphans[sym]

			if missCount < globalOrphanMissThreshold {
				s.log.Warn().
					Str("symbol", string(sym)).
					Float64("db_net_qty", dbQty).
					Int("miss_count", missCount).
					Int("threshold", globalOrphanMissThreshold).
					Msg("global-reconcile: DB orphan candidate — observing")
				continue
			}

			s.log.Error().
				Str("symbol", string(sym)).
				Float64("db_net_qty", dbQty).
				Int("miss_count", missCount).
				Msg("global-reconcile: DB orphan confirmed — broker has no position but DB shows open qty (investigate manually; no synthetic trade written)")
			continue
		}

		delete(s.pendingGlobalOrphans, sym)

		if brokerQty > dbQty+1e-6 {
			s.pendingGlobalDrifts[sym]++
			missCount := s.pendingGlobalDrifts[sym]
			delta := brokerQty - dbQty

			if missCount < globalOrphanMissThreshold {
				s.log.Warn().
					Str("symbol", string(sym)).
					Float64("db_qty", dbQty).
					Float64("broker_qty", brokerQty).
					Float64("delta", delta).
					Int("miss_count", missCount).
					Int("threshold", globalOrphanMissThreshold).
					Msg("global-reconcile: broker>DB drift candidate — observing before writing BUY")
				continue
			}

			s.log.Error().
				Str("symbol", string(sym)).
				Float64("db_qty", dbQty).
				Float64("broker_qty", brokerQty).
				Float64("delta", delta).
				Int("miss_count", missCount).
				Msg("global-reconcile: broker>DB drift confirmed — broker holds more than DB records (investigate manually; no synthetic trade written)")
			continue
		}

		delete(s.pendingGlobalDrifts, sym)

		if dbQty > brokerQty+1e-6 {
			s.log.Warn().
				Str("symbol", string(sym)).
				Float64("db_qty", dbQty).
				Float64("broker_qty", brokerQty).
				Float64("drift", dbQty-brokerQty).
				Msg("global-reconcile: DB>broker drift detected (informational)")
		}
	}
}
