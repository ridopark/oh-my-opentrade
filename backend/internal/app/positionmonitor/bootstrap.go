package positionmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
)

// bootstrapPositions seeds the monitor with OMO-opened positions that still exist on the broker.
// It cross-references broker positions with our trade DB to identify positions that OMO opened.
// Positions opened manually by the user on the broker are NOT bootstrapped.
func (s *Service) bootstrapPositions(ctx context.Context) {
	if s.broker == nil || s.repo == nil {
		s.log.Debug().Msg("bootstrap skipped — broker or repo not configured")
		return
	}

	// 0. Cancel all open orders from any prior session.
	// At startup our in-memory state is lost, so every open order is stale.
	if canceled, err := s.broker.CancelAllOpenOrders(ctx); err != nil {
		s.log.Warn().Err(err).Msg("bootstrap: failed to cancel stale open orders — proceeding anyway")
	} else if canceled > 0 {
		s.log.Info().Int("canceled", canceled).Msg("bootstrap: canceled stale open orders from prior session")
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
		thesis   json.RawMessage
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
			totalCost := e.avgEntry*e.netQty + t.Price*t.Quantity
			e.netQty += t.Quantity
			if e.netQty > 0 {
				e.avgEntry = totalCost / e.netQty
			}
			e.entryAt = t.Time
			e.strategy = t.Strategy
			e.asset = t.AssetClass
			if len(t.Thesis) > 0 {
				e.thesis = t.Thesis
			}
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
			s.log.Warn().
				Str("symbol", string(sym)).
				Float64("orphan_qty", omo.netQty).
				Float64("avg_entry", omo.avgEntry).
				Msg("bootstrap: OMO trade found but no broker position — inserting reconciliation SELL")

			if s.repo != nil {
				trade, tErr := domain.NewTrade(
					now, s.tenantID, s.envMode, uuid.New(),
					sym, "SELL", omo.netQty, omo.avgEntry, 0,
					"FILLED", "reconciliation",
					fmt.Sprintf("bootstrap cleanup: orphaned %.8f %s (no broker position)", omo.netQty, sym),
				)
				if tErr != nil {
					s.log.Error().Err(tErr).Str("symbol", string(sym)).Msg("bootstrap: failed to construct reconciliation trade")
				} else if sErr := s.repo.SaveTrade(ctx, trade); sErr != nil {
					s.log.Error().Err(sErr).Str("symbol", string(sym)).Msg("bootstrap: failed to save reconciliation trade")
				} else {
					s.log.Info().
						Str("symbol", string(sym)).
						Float64("qty", omo.netQty).
						Str("trade_id", trade.TradeID.String()).
						Msg("bootstrap: reconciliation SELL inserted to zero out orphaned position")
				}
			}
			continue
		}

		// Skip dust positions (notional < $1) — remnants from IOC partial fills.
		if bp.Quantity*bp.Price < 1.0 {
			s.log.Info().
				Str("symbol", string(sym)).
				Float64("qty", bp.Quantity).
				Float64("price", bp.Price).
				Float64("notional", bp.Quantity*bp.Price).
				Msg("bootstrap: skipping dust position — notional < $1")
			continue
		}

		entryPrice := bp.Price
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

		if len(omo.thesis) > 0 {
			var thesis domain.EntryThesis
			if err := json.Unmarshal(omo.thesis, &thesis); err == nil {
				pos.EntryThesis = &thesis
				s.log.Info().Str("symbol", string(sym)).Msg("bootstrap: entry thesis restored from trade history")
			}
		}

		if pos.EntryThesis == nil {
			if thesisJSON, err := s.repo.GetLatestThesisForSymbol(ctx, s.tenantID, s.envMode, sym); err == nil && len(thesisJSON) > 0 {
				var thesis domain.EntryThesis
				if err := json.Unmarshal(thesisJSON, &thesis); err == nil {
					pos.EntryThesis = &thesis
					s.log.Info().Str("symbol", string(sym)).Msg("bootstrap: entry thesis restored via retroactive lookup")
				}
			}
		}

		if domain.IsOCCSymbol(sym) {
			pos.InstrumentType = domain.InstrumentTypeOption
		}

		if maxHigh, err := s.repo.GetMaxBarHighSince(ctx, sym, "1m", entryTime); err == nil && maxHigh > pos.HighWaterMark {
			pos.HighWaterMark = maxHigh
			s.log.Info().
				Str("symbol", string(sym)).
				Float64("hwm_restored", maxHigh).
				Float64("entry_price", entryPrice).
				Msg("bootstrap: high water mark restored from bar data")
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
			Bool("has_thesis", pos.EntryThesis != nil).
			Float64("high_water_mark", pos.HighWaterMark).
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
		s.log.Warn().Msg("bootstrap: specStore is nil — cannot resolve exit rules")
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
