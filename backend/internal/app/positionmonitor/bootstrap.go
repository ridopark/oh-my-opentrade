package positionmonitor

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
)

// bootstrapDefaultIV is the fallback implied volatility used when
// IV cannot be calibrated from entry premium at restart (e.g. no
// underlying bar history at the entry timestamp). 30% is a reasonable
// stocks-equity-options anchor — high enough not to silently
// underestimate premium decay, low enough not to over-fire premium
// stops on real but slow-moving positions.
const bootstrapDefaultIV = 0.30

// bootstrapPositions seeds the monitor with OMO-opened positions that still exist on the broker.
// It cross-references broker positions with our trade DB to identify positions that OMO opened.
// Positions opened manually by the user on the broker are NOT bootstrapped.
func (s *Service) bootstrapPositions(ctx context.Context) {
	if s.broker == nil || s.repo == nil {
		s.log.Debug().Msg("bootstrap skipped — broker or repo not configured")
		return
	}

	// 0. Reconcile pre-existing broker open orders against the intent journal.
	// With OMO_ORDER_JOURNAL_ENABLED unset this falls back to CancelAllOpenOrders
	// (the legacy behavior). With the flag set, matched orders are resumed,
	// unmanaged orders trigger an operator alert, and journal rows the broker
	// no longer tracks are marked lost. See order_reconcile.go.
	s.reconcileOpenOrdersOnBoot(ctx)

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

	// 2. Query our trade DB for ALL fills to identify OMO-opened positions.
	//    Full history (not a rolling window): a window can desync once one
	//    leg of a reconciliation/rebalance pair (e.g. a ledger_rebalance
	//    offset for an earlier duplicate fill) ages out while the other
	//    remains, leaving a phantom non-zero net for a symbol that is flat
	//    in reality. Mirrors the d1c8acfb fix to queryGetNetPositions.
	now := s.nowFunc()
	trades, err := s.repo.GetTrades(ctx, s.tenantID, s.envMode, time.Time{}, now)
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
			if e.netQty == 0 {
				// Position fully closed — clear weighted avg.
				e.avgEntry = 0
			}
			// Negative netQty represents a short and is handled below:
			// line ~101 skips omo.netQty <= 0 so shorts don't trigger
			// the long-only "orphan" alert. Clamping to zero here would
			// lose the short context on subsequent BUY covers and let
			// phantom-reconcile pairs synthesize a false orphan.
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
			// Expired OCC contracts are not orphans: the broker auto-closes
			// the position at expiry without emitting a SELL fill, so our
			// ledger correctly retains the open BUY while the broker shows
			// nothing. Downgrade these to INFO so they don't drown out real
			// orphan alerts.
			if domain.IsOCCSymbol(sym) {
				if _, expiry, _, _, ok := domain.ParseOCC(sym); ok && !expiry.IsZero() && expiry.Before(now) {
					s.log.Info().
						Str("symbol", string(sym)).
						Float64("ledger_qty", omo.netQty).
						Float64("avg_entry", omo.avgEntry).
						Time("expiry", expiry).
						Msg("bootstrap: expired OCC absent on broker — assuming auto-close at expiry, no SELL fill expected")
					continue
				}
			}
			s.log.Error().
				Str("symbol", string(sym)).
				Float64("orphan_qty", omo.netQty).
				Float64("avg_entry", omo.avgEntry).
				Msg("bootstrap: OMO trade ledger shows open position but broker has none — skipping monitor seed; investigate manually (no synthetic trade written)")
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
		exitRules := s.resolveExitRules(ctx, strategy, sym, assetClass)

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
			if et, _, err := domain.ParseThesisJSON(omo.thesis); err == nil && et != nil {
				pos.EntryThesis = et
				s.log.Info().Str("symbol", string(sym)).Msg("bootstrap: entry thesis restored from trade history")
			}
		}

		if pos.EntryThesis == nil {
			if thesisJSON, err := s.repo.GetLatestThesisForSymbol(ctx, s.tenantID, s.envMode, sym); err == nil && len(thesisJSON) > 0 {
				if et, _, err := domain.ParseThesisJSON(thesisJSON); err == nil && et != nil {
					pos.EntryThesis = et
					s.log.Info().Str("symbol", string(sym)).Msg("bootstrap: entry thesis restored via retroactive lookup")
				}
			}
		}

		if domain.IsOCCSymbol(sym) {
			pos.InstrumentType = domain.InstrumentTypeOption
			// Override entry price to underlying for consistent exit rule evaluation.
			// Exit rules (MAX_LOSS, SWING_STOP, etc.) compare against the underlying
			// stock price from bar data, not the option premium.
			if underlying := domain.UnderlyingFromOCC(sym); underlying != "" {
				var underlyingPrice float64
				// Try price cache first (fast, in-memory)
				if snap, ok := s.priceCache.LatestPrice(underlying); ok {
					underlyingPrice = snap.Price
				} else if s.repo != nil {
					// Fallback: query last bar from DB (cache is empty at startup)
					now := s.nowFunc()
					bars, err := s.repo.GetMarketBars(ctx, underlying, "1m", now.Add(-30*time.Minute), now)
					if err == nil && len(bars) > 0 {
						underlyingPrice = bars[len(bars)-1].Close
					}
				}
				if underlyingPrice > 0 {
					pos.EntryPrice = underlyingPrice
					pos.HighWaterMark = underlyingPrice
					pos.LowWaterMark = underlyingPrice
					if pos.CustomState == nil {
						pos.CustomState = make(map[string]float64)
					}
					pos.CustomState["option_premium"] = entryPrice
					s.log.Info().
						Str("symbol", string(sym)).
						Str("underlying", string(underlying)).
						Float64("underlying_price", underlyingPrice).
						Float64("option_premium", entryPrice).
						Msg("bootstrap: overrode options entry price to underlying")
				}
				// Restore the BSM input set so EstimatedPremium has a usable
				// primary path post-restart. Without this rehydration,
				// EstimatedPremium returns 0 and PREMIUM_STOP false-fires
				// (the 2026-04-28 LLY 850P incident). strike/expiry/is_call
				// come straight from OCC; iv_at_entry is calibrated from the
				// recorded entry premium against the underlying price near
				// entry time, falling back to a 30% default if calibration
				// can't run (no bar history, malformed entry, etc).
				if underlyingPrice > 0 {
					s.restoreOptionBSMInputs(ctx, &pos, sym, underlying, entryPrice, underlyingPrice, entryTime)
				}
			}
		}

		// Restore ExitRetryCount if the last exit order was canceled/expired.
		// Without this, after-hours EOD flatten retries reset to 0 on every restart.
		if canceled, err := s.repo.HasCanceledExitOrder(ctx, s.tenantID, s.envMode, sym); err == nil && canceled {
			pos.ExitRetryCount = 1
			s.log.Info().Str("symbol", string(sym)).Msg("bootstrap: ExitRetryCount restored — last exit order was canceled")
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

	s.warmPriceCache(ctx)
	s.log.Info().Int("bootstrapped", bootstrapped).Int("broker_total", len(brokerPositions)).Msg("bootstrap complete")
}

// warmPriceCache seeds priceCache from the last persisted 1m bar for each
// bootstrapped position's price-symbol (underlying for options, symbol for
// equities). Without this, exit rules that require a current price are
// silent during the cold-start window between posMonitor.Start and first
// live bar arrival — a bounded window, but long enough to matter on fast
// opens. The warmed price uses the bar's real timestamp so the staleness
// gate still rejects it if it is too old (e.g. overnight startup).
func (s *Service) warmPriceCache(ctx context.Context) {
	if s.priceCache == nil || s.repo == nil {
		return
	}
	now := s.nowFunc()
	from := now.Add(-30 * time.Minute)
	seen := make(map[domain.Symbol]struct{})
	for _, pos := range s.positions {
		priceSym := pos.Symbol
		if domain.IsOCCSymbol(priceSym) {
			priceSym = domain.UnderlyingFromOCC(priceSym)
		}
		if priceSym == "" {
			continue
		}
		if _, dup := seen[priceSym]; dup {
			continue
		}
		seen[priceSym] = struct{}{}
		if _, cached := s.priceCache.LatestPrice(priceSym); cached {
			continue
		}
		qCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		bars, err := s.repo.GetMarketBars(qCtx, priceSym, "1m", from, now)
		cancel()
		if err != nil || len(bars) == 0 {
			continue
		}
		last := bars[len(bars)-1]
		s.priceCache.UpdatePrice(priceSym, last.Close, last.Time)
		s.log.Debug().Str("symbol", string(priceSym)).Float64("price", last.Close).Time("observed_at", last.Time).Msg("bootstrap: priceCache warmed from DB")
	}
}

// restoreOptionBSMInputs populates pos.CustomState with the inputs
// EstimatedPremium needs to run BSM after a restart. strike/expiry/is_call
// are derived directly from the OCC symbol (zero-cost, exact). iv_at_entry
// is calibrated by inverting BSM against the recorded entry premium with
// the underlying price near entry time as the reference spot. When bar
// history is unavailable for the entry timestamp, falls back to the
// current underlying price; when calibration itself can't converge,
// stamps bootstrapDefaultIV. The CustomState marker
// "bsm_inputs_restored_at_boot" is set so downstream code can distinguish
// fill-time-stamped IV from boot-restored IV.
func (s *Service) restoreOptionBSMInputs(
	ctx context.Context,
	pos *domain.MonitoredPosition,
	occSym domain.Symbol,
	underlying domain.Symbol,
	entryPremium, currentUnderlying float64,
	entryTime time.Time,
) {
	if pos == nil || pos.CustomState == nil {
		return
	}
	_, expiry, right, strike, ok := domain.ParseOCC(occSym)
	if !ok || strike <= 0 {
		s.log.Warn().Str("symbol", string(occSym)).Msg("bootstrap: OCC parse failed — BSM inputs not restored")
		return
	}
	pos.CustomState["strike"] = strike
	pos.CustomState["expiry_unix"] = float64(expiry.Unix())
	if right == string(domain.OptionRightCall) {
		pos.CustomState["is_call"] = 1.0
	} else {
		pos.CustomState["is_call"] = 0.0
	}
	pos.OptionExpiry = expiry
	pos.OptionRight = right

	// Resolve underlying-at-entry. Best estimate of the IV that originally
	// produced entryPremium is from the spot at entry time, not now.
	underlyingAtEntry := s.fetchUnderlyingAtEntry(ctx, underlying, entryTime)
	if underlyingAtEntry == 0 {
		underlyingAtEntry = currentUnderlying
	}

	// Calibrate IV. ImpliedVol returns the chain-IV seed (defaultIV here)
	// unchanged when calibration can't run (deep ITM/OTM, expired, vega
	// near zero). That fallback is acceptable — HasBSMInputs() returns
	// true either way, which is the property evaluatePremiumStop relies on.
	ivAtEntry := bootstrapDefaultIV
	if entryPremium > 0 && underlyingAtEntry > 0 && !expiry.IsZero() && !entryTime.IsZero() {
		dteYears := expiry.Sub(entryTime).Hours() / (365.25 * 24)
		if dteYears > 0 {
			isCall := right == string(domain.OptionRightCall)
			calibrated := options.ImpliedVol(
				entryPremium, underlyingAtEntry, strike, dteYears, 0.045, isCall, bootstrapDefaultIV,
			)
			if calibrated > 0 && !math.IsNaN(calibrated) && !math.IsInf(calibrated, 0) {
				ivAtEntry = calibrated
			}
		}
	}
	pos.CustomState["iv_at_entry"] = ivAtEntry
	pos.CustomState["bsm_inputs_restored_at_boot"] = 1.0

	s.log.Info().
		Str("symbol", string(occSym)).
		Float64("strike", strike).
		Time("expiry", expiry).
		Str("right", right).
		Float64("entry_premium", entryPremium).
		Float64("underlying_at_entry", underlyingAtEntry).
		Float64("iv_at_entry", ivAtEntry).
		Msg("bootstrap: BSM inputs restored")
}

// fetchUnderlyingAtEntry queries the bar repo for the bar nearest entryTime
// in a +/- 30 minute window. Returns 0 when the window has no bars.
func (s *Service) fetchUnderlyingAtEntry(ctx context.Context, underlying domain.Symbol, entryTime time.Time) float64 {
	if s.repo == nil || entryTime.IsZero() {
		return 0
	}
	const window = 30 * time.Minute
	qCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	bars, err := s.repo.GetMarketBars(qCtx, underlying, "1m", entryTime.Add(-window), entryTime.Add(window))
	cancel()
	if err != nil || len(bars) == 0 {
		return 0
	}
	best := bars[0]
	bestDiff := absDuration(best.Time.Sub(entryTime))
	for _, b := range bars[1:] {
		d := absDuration(b.Time.Sub(entryTime))
		if d < bestDiff {
			best = b
			bestDiff = d
		}
	}
	return best.Close
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// resolveExitRules looks up exit rules from the strategy spec store.
// When multiple specs share the same strategy ID (e.g. equity vs crypto variants),
// it prefers the spec whose asset_classes include the given assetClass.
// Falls back to conservative defaults if the spec store is unavailable or the strategy has no rules.
func (s *Service) resolveExitRules(ctx context.Context, strategy string, symbol domain.Symbol, assetClass domain.AssetClass) []domain.ExitRule {
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
					Str("symbol", string(symbol)).
					Str("asset_class", string(assetClass)).
					Int("rules", len(chosen.ExitRules)).
					Msg("bootstrap: exit rules from spec")
				return chosen.ExitRulesForSymbol(symbol.String())
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
