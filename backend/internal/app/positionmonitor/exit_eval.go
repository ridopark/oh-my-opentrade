package positionmonitor

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

func parseBarDuration(tf string) time.Duration {
	switch tf {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "4h":
		return 4 * time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return time.Minute
	}
}

func (s *Service) barDurationFor(strategyName string) time.Duration {
	if s.specStore == nil || strategyName == "" {
		return time.Minute
	}
	if d, ok := s.barDurCache[strategyName]; ok {
		return d
	}
	sid, err := domstrategy.NewStrategyID(strategyName)
	if err != nil {
		return time.Minute
	}
	spec, err := s.specStore.GetLatest(context.Background(), sid)
	if err != nil || len(spec.Routing.Timeframes) == 0 {
		return time.Minute
	}
	d := parseBarDuration(string(spec.Routing.Timeframes[0]))
	s.barDurCache[strategyName] = d
	return d
}

// tick evaluates all exit rules against all monitored positions.
// Time-only rules (MAX_HOLDING_TIME, EOD_FLATTEN) are evaluated even when
// price data is stale so positions are never stuck past their time limits.
func (s *Service) tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()

	// Sort position keys for deterministic evaluation order across runs.
	// Go map iteration is random — without sorting, exit evaluations happen
	// in unpredictable order, causing non-deterministic backtest results.
	posKeys := make([]string, 0, len(s.positions))
	for k := range s.positions {
		posKeys = append(posKeys, k)
	}
	sort.Strings(posKeys)

	for _, key := range posKeys {
		pos := s.positions[key]
		if pos.ExitPending {
			if now.Sub(pos.ExitPendingAt) > exitPendingTimeout {
				s.handleExitTimeout(pos)
			}
			continue
		}

		// Skip exit evaluation outside RTH for non-crypto positions.
		// Exchanges reject orders when closed, causing infinite retry loops.
		// Positions will be re-evaluated when the next RTH session opens.
		if pos.AssetClass != domain.AssetClassCrypto {
			cal := domain.CalendarFor(pos.AssetClass)
			if !cal.IsOpen(now) {
				continue
			}
		}

		// For options, look up price by underlying symbol (bar data comes in under the underlying)
		priceSymbol := pos.Symbol
		if pos.InstrumentType == domain.InstrumentTypeOption {
			if underlying := domain.UnderlyingFromOCC(pos.Symbol); underlying != "" {
				priceSymbol = underlying
			}
		}
		snap, ok := s.priceCache.LatestPrice(priceSymbol)
		priceAvailable := ok && now.Sub(snap.ObservedAt) <= s.maxPriceStaleness

		// Phase 1: price-dependent rules — stops and targets have highest priority.
		// A stop hit at 10:30 AM should fire at 10:30, not be overridden by
		// EOD flatten at 3:55 PM because the time check ran first.
		if priceAvailable {
			price := snap.Price
			pos.UpdateWaterMarks(price)

			// Track premium high-water mark for option positions.
			// Premium-based exit rules (PREMIUM_TRAIL) use this to trail
			// from the best estimated premium since entry.
			if pos.InstrumentType == domain.InstrumentTypeOption {
				estPremium := pos.EstimatedPremium(price, now)
				if estPremium > 0 {
					if pos.CustomState == nil {
						pos.CustomState = make(map[string]float64)
					}
					// Existing: premium high-water mark
					if hwm, ok := pos.CustomState["premium_hwm"]; !ok || estPremium > hwm {
						pos.CustomState["premium_hwm"] = estPremium
					}
					// NEW: premium low-water mark
					if lwm, ok := pos.CustomState["premium_lwm"]; !ok || estPremium < lwm {
						pos.CustomState["premium_lwm"] = estPremium
					}
					// NEW: MFE/MAE as percentage of entry premium
					entryPremium := pos.CustomState["option_premium"]
					if entryPremium > 0 {
						pctChange := (estPremium - entryPremium) / entryPremium
						if mfe, ok := pos.CustomState["premium_mfe_pct"]; !ok || pctChange > mfe {
							pos.CustomState["premium_mfe_pct"] = pctChange
						}
						if mae, ok := pos.CustomState["premium_mae_pct"]; !ok || pctChange < mae {
							pos.CustomState["premium_mae_pct"] = pctChange
						}
						// Minutes since entry (time-based, not tick-based)
						minutesSinceEntry := now.Sub(pos.EntryTime).Minutes()
						pos.CustomState["minutes_since_entry"] = minutesSinceEntry
						// Minutes to first profit (set once when premium first exceeds entry)
						if _, set := pos.CustomState["minutes_to_first_profit"]; !set {
							if pctChange > 0 {
								pos.CustomState["minutes_to_first_profit"] = minutesSinceEntry
							}
						}
					}
				}
			}

			evalCtx := EvalContext{
				BarDuration: s.barDurationFor(pos.Strategy),
				BarHigh:     snap.High,
				BarLow:      snap.Low,
			}
			if s.snapshotFn != nil {
				if indSnap, ok := s.snapshotFn(string(priceSymbol)); ok {
					evalCtx.ATR = indSnap.ATR
					evalCtx.VWAPValue = indSnap.VWAP
					if indSnap.VWAPSD > 0 {
						evalCtx.SDBands = make(map[float64]float64)
						for _, level := range []float64{0.5, 1.0, 1.5, 2.0, 2.5, 3.0} {
							evalCtx.SDBands[level] = indSnap.VWAP + level*indSnap.VWAPSD
						}
					}
				}
			}
			var stepStopMinHoldBars float64
			for _, r := range pos.ExitRules {
				if r.Type == domain.ExitRuleStepStop {
					stepStopMinHoldBars = r.Param("min_hold_bars", 0)
					break
				}
			}
			UpdateStepStopState(pos, price, evalCtx, now, stepStopMinHoldBars)

			for _, rule := range pos.ExitRules {
				if rule.Type == domain.ExitRuleBreakevenStop {
					UpdateBreakevenStopState(pos, price,
						rule.Param("activation_pct", 0),
						rule.Param("buffer_pct", 0))
					break
				}
			}

			// Compute hold periods for take-profit suppression.
			// premium_hold_bars: suppresses premium/profit target exits (default 1)
			// exit_hold_bars: used by strategy-level AVWAP exit (unchanged)
			barDur := s.barDurationFor(pos.Strategy)
			timeSinceEntry := now.Sub(pos.EntryTime)

			premiumHoldBars := pos.CustomState["premium_hold_bars"]
			if premiumHoldBars <= 0 {
				premiumHoldBars = 1 // default: suppress same-bar exits
			}
			inPremiumHold := timeSinceEntry < time.Duration(premiumHoldBars)*barDur

			for _, rule := range pos.ExitRules {
				if !rule.Type.RequiresPrice() {
					continue
				}
				if inPremiumHold && isTakeProfitRule(rule.Type) {
					continue
				}
				adjusted := sessionAdjustRule(rule, pos.AssetClass, now)
				triggered, reason := Evaluate(adjusted, pos, price, now, evalCtx)
				if !triggered {
					continue
				}

				s.log.Info().
					Str("symbol", string(pos.Symbol)).
					Str("rule", string(rule.Type)).
					Str("reason", reason).
					Float64("price", price).
					Float64("entry_price", pos.EntryPrice).
					Msg("exit rule triggered")

				s.triggerExit(pos, rule, reason, price, now)
				break
			}
		}

		if pos.ExitPending {
			continue
		}

		// Phase 2: time-only rules (EOD_FLATTEN, MAX_HOLDING_TIME) — safety net.
		// These fire only if no price-based rule triggered first.
		for _, rule := range pos.ExitRules {
			if rule.Type.RequiresPrice() {
				continue
			}
			triggered, reason := Evaluate(rule, pos, 0, now, EvalContext{})
			if !triggered {
				continue
			}

			exitPrice := s.resolveExitPrice(pos, snap, priceAvailable)
			s.log.Info().
				Str("symbol", string(pos.Symbol)).
				Str("rule", string(rule.Type)).
				Str("reason", reason).
				Float64("price", exitPrice).
				Float64("entry_price", pos.EntryPrice).
				Bool("price_stale", !priceAvailable).
				Msg("exit rule triggered")

			s.triggerExit(pos, rule, reason, exitPrice, now)
			break
		}
	}
}

// resolveExitPrice returns the best available price for an exit order.
// When live price is available, uses it directly. When stale, falls back to
// the last known price. When no price exists at all, uses the entry price.
func (s *Service) resolveExitPrice(pos *domain.MonitoredPosition, snap ports.PriceSnapshot, priceAvailable bool) float64 {
	if priceAvailable {
		return snap.Price
	}
	if snap.Price > 0 {
		return snap.Price
	}
	return pos.EntryPrice
}

func (s *Service) handleExitTimeout(pos *domain.MonitoredPosition) {
	if pos.ExitOrderID != "" && s.broker != nil {
		if err := s.broker.CancelOrder(context.Background(), pos.ExitOrderID); err != nil {
			if strings.Contains(err.Error(), "filled") {
				if s.reconcileFilledOrder(pos) {
					return
				}
			}
			s.log.Warn().Err(err).
				Str("symbol", string(pos.Symbol)).
				Str("broker_order_id", pos.ExitOrderID).
				Msg("failed to cancel stale exit order — may already be terminal")
		} else {
			s.log.Info().
				Str("symbol", string(pos.Symbol)).
				Str("broker_order_id", pos.ExitOrderID).
				Msg("canceled stale exit order")
		}
	}

	pos.ExitPending = false
	pos.ExitOrderID = ""
	pos.ExitRetryCount++

	// Clear the execution service's inflight exit lock so the retry
	// won't be rejected with "exit already inflight".
	if s.positionGate != nil {
		s.positionGate.ClearInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol)
	}

	s.log.Warn().
		Str("symbol", string(pos.Symbol)).
		Int("retry_count", pos.ExitRetryCount).
		Msg("exit pending timeout — will retry with escalated price")
}

func (s *Service) reconcileFilledOrder(pos *domain.MonitoredPosition) bool {
	ctx := context.Background()

	details, err := s.broker.GetOrderDetails(ctx, pos.ExitOrderID)
	if err != nil {
		s.log.Warn().Err(err).
			Str("symbol", string(pos.Symbol)).
			Str("broker_order_id", pos.ExitOrderID).
			Msg("exit-timeout: could not fetch order details for filled order — will retry exit")
		return false
	}

	missingQty := pos.Quantity
	if details.FilledQty < missingQty-1e-9 {
		s.log.Warn().
			Str("symbol", string(pos.Symbol)).
			Float64("broker_filled_qty", details.FilledQty).
			Float64("monitor_remaining_qty", missingQty).
			Msg("exit-timeout: broker filled qty less than remaining — will retry exit")
		return false
	}

	if s.repo != nil {
		trade := domain.Trade{
			Time:      s.nowFunc(),
			TenantID:  s.tenantID,
			EnvMode:   s.envMode,
			TradeID:   uuid.New(),
			Symbol:    pos.Symbol,
			Side:      "SELL",
			Quantity:  missingQty,
			Price:     details.FilledAvgPrice,
			Status:    "FILLED",
			Strategy:  pos.Strategy,
			Rationale: fmt.Sprintf("exit-timeout: fill reconciliation for order %s (missed WS fill events)", pos.ExitOrderID),
		}
		if err := s.repo.SaveTrade(ctx, trade); err != nil {
			s.log.Error().Err(err).
				Str("symbol", string(pos.Symbol)).
				Str("broker_order_id", pos.ExitOrderID).
				Msg("exit-timeout: failed to save reconciliation fill — will retry exit")
			return false
		}
	}

	s.log.Info().
		Str("symbol", string(pos.Symbol)).
		Str("broker_order_id", pos.ExitOrderID).
		Float64("missing_qty", missingQty).
		Float64("fill_price", details.FilledAvgPrice).
		Msg("exit-timeout: filled order reconciled — closing position")

	if s.positionGate != nil {
		s.positionGate.ClearInflightExit(pos.TenantID, pos.EnvMode, pos.Symbol)
	}
	key := fmt.Sprintf("%s:%s:%s", s.tenantID, s.envMode, pos.Symbol)
	delete(s.positions, key)
	return true
}

// triggerExit marks a position as exit-pending and emits an exit order intent.
func (s *Service) triggerExit(pos *domain.MonitoredPosition, rule domain.ExitRule, reason string, currentPrice float64, now time.Time) {
	const maxExitRetries = 50
	if pos.ExitRetryCount >= maxExitRetries {
		s.log.Warn().
			Str("symbol", string(pos.Symbol)).
			Str("rule", string(rule.Type)).
			Int("retry_count", pos.ExitRetryCount).
			Msg("exit retry limit reached — requires manual intervention")
		return
	}

	pos.ExitPending = true
	pos.ExitPendingAt = now

	idempotencyKey := fmt.Sprintf("EXIT:%s:%s:%s:%d:%s:%d",
		pos.TenantID, pos.EnvMode, pos.Symbol, pos.EntryTime.Unix(), rule.Type, pos.ExitRetryCount)

	exitPrice, orderType, tif := exitOrderParams(rule.Type, currentPrice, pos.ExitRetryCount, pos.IsShort(), pos.InstrumentType == domain.InstrumentTypeOption)

	exitDirection := domain.DirectionCloseLong
	if pos.IsShort() {
		exitDirection = domain.DirectionCloseShort
	}

	// Partial close: check if the evaluator set a qty fraction in CustomState.
	exitQty := pos.Quantity
	for _, fracKey := range []string{"tiered_tp_exit_qty_frac", "time_partial_exit_qty_frac"} {
		if frac := pos.CustomState[fracKey]; frac > 0 && frac < 1.0 {
			partial := math.Ceil(pos.Quantity * frac)
			if partial > 0 && partial < pos.Quantity {
				exitQty = partial
			}
			delete(pos.CustomState, fracKey)
			break
		} else if frac >= 1.0 {
			delete(pos.CustomState, fracKey)
		}
	}

	intent, err := domain.NewOrderIntent(
		uuid.New(),
		pos.TenantID,
		pos.EnvMode,
		pos.Symbol,
		exitDirection,
		exitPrice,
		0,
		0,
		exitQty,
		pos.Strategy,
		fmt.Sprintf("exit_monitor:%s:%s", rule.Type, reason),
		1.0,
		idempotencyKey,
	)
	if err == nil {
		intent.OrderType = orderType
		intent.TimeInForce = tif

		// Attach MFE/MAE trade analytics to intent metadata for backtest collector.
		if pos.CustomState != nil {
			if intent.Meta == nil {
				intent.Meta = make(map[string]string)
			}
			if v, ok := pos.CustomState["premium_mfe_pct"]; ok {
				intent.Meta["premium_mfe_pct"] = fmt.Sprintf("%.6f", v)
			}
			if v, ok := pos.CustomState["premium_mae_pct"]; ok {
				intent.Meta["premium_mae_pct"] = fmt.Sprintf("%.6f", v)
			}
			if v, ok := pos.CustomState["minutes_to_first_profit"]; ok {
				intent.Meta["minutes_to_first_profit"] = fmt.Sprintf("%.1f", v)
			} else {
				intent.Meta["minutes_to_first_profit"] = "-1"
			}
			if v, ok := pos.CustomState["minutes_since_entry"]; ok {
				intent.Meta["minutes_held"] = fmt.Sprintf("%.1f", v)
			}
		}

		// Attach Instrument metadata for option positions so the broker adapter
		// dispatches to the options order path (e.g. Alpaca SubmitOptionOrder).
		if pos.InstrumentType == domain.InstrumentTypeOption {
			underlying := domain.UnderlyingFromOCC(pos.Symbol)
			inst, instErr := domain.NewInstrument(domain.InstrumentTypeOption, string(pos.Symbol), string(underlying))
			if instErr == nil {
				intent.Instrument = &inst
			}
			// Copy option meta for simbroker BSM exit pricing
			if intent.Meta == nil {
				intent.Meta = make(map[string]string)
			}
			intent.Meta["option_right"] = string(pos.OptionRight)
			intent.Meta["expiry"] = pos.OptionExpiry.Format("2006-01-02")
			intent.Meta["underlying"] = string(underlying)
			// Extract strike from OCC symbol (last 8 digits / 1000)
			occStr := string(pos.Symbol)
			if len(occStr) >= 8 {
				strikeStr := occStr[len(occStr)-8:]
				var strikeInt int
				_, _ = fmt.Sscanf(strikeStr, "%d", &strikeInt)
				intent.Meta["strike"] = fmt.Sprintf("%.2f", float64(strikeInt)/1000.0)
			}
			// Use entry IV stored in custom state (set during fill)
			if pos.CustomState != nil {
				if ivVal := pos.CustomState["iv_at_entry"]; ivVal > 0 {
					intent.Meta["iv_at_entry"] = fmt.Sprintf("%.4f", ivVal)
				}
			}
			// Propagate entry context for SimBroker delta-approximation exit pricing
			intent.Meta["entry_date"] = pos.EntryTime.Format("2006-01-02")
			intent.Meta["entry_underlying"] = fmt.Sprintf("%.4f", pos.EntryPrice)
			if pos.CustomState != nil {
				if prem := pos.CustomState["option_premium"]; prem > 0 {
					intent.Meta["premium"] = fmt.Sprintf("%.2f", prem)
				}
				if delta := pos.CustomState["delta_at_entry"]; delta != 0 {
					intent.Meta["delta_at_entry"] = fmt.Sprintf("%.4f", delta)
				}
				if vix := pos.CustomState["vix_at_entry"]; vix > 0 {
					intent.Meta["vix_at_entry"] = fmt.Sprintf("%.2f", vix)
				}
				if dte := pos.CustomState["days_to_earnings"]; dte > 0 {
					intent.Meta["days_to_earnings"] = fmt.Sprintf("%.0f", dte)
				}
			}
		}
	}
	if err != nil {
		s.log.Error().Err(err).Str("symbol", string(pos.Symbol)).Msg("failed to create exit order intent")
		pos.ExitPending = false
		return
	}

	exitTriggered := domain.ExitTriggered{
		Symbol:       pos.Symbol,
		Rule:         rule.Type,
		Reason:       reason,
		CurrentPrice: currentPrice,
		EntryPrice:   pos.EntryPrice,
		Strategy:     pos.Strategy,
		TenantID:     pos.TenantID,
		EnvMode:      pos.EnvMode,
	}

	select {
	case s.outbox <- outboxMsg{
		Intent:         intent,
		ExitTriggered:  exitTriggered,
		TenantID:       pos.TenantID,
		EnvMode:        pos.EnvMode,
		IdempotencyKey: idempotencyKey,
	}:
	default:
		s.log.Error().Str("symbol", string(pos.Symbol)).Msg("outbox full — dropping exit intent")
		pos.ExitPending = false
	}
}

// runOutbox is the outbox publisher goroutine. It reads exit intents from the
// outbox channel and publishes them on the event bus.
func (s *Service) runOutbox(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case msg := <-s.outbox:
			// Emit ExitTriggered event.
			s.emit(ctx, domain.EventExitTriggered, msg.TenantID, msg.EnvMode, msg.IdempotencyKey, msg.ExitTriggered)

			// Emit OrderIntentCreated event to feed the execution pipeline.
			s.emit(ctx, domain.EventOrderIntentCreated, msg.TenantID, msg.EnvMode, msg.Intent.IdempotencyKey, msg.Intent)
		}
	}
}

// emit publishes a domain event on the event bus (best-effort).
func (s *Service) emit(ctx context.Context, eventType string, tenantID string, envMode domain.EnvMode, idempotencyKey string, payload any) {
	ev, err := domain.NewEvent(eventType, tenantID, envMode, idempotencyKey, payload)
	if err != nil {
		return
	}
	_ = s.eventBus.Publish(ctx, *ev)
}

// isTakeProfitRule returns true for exit rules that should be suppressed
// during the initial hold period (exit_hold_bars) to prevent same-bar
// look-ahead exits where BSM repricing on the entry bar inflates gains.
func isTakeProfitRule(ruleType domain.ExitRuleType) bool {
	switch ruleType {
	case domain.ExitRuleProfitTarget, domain.ExitRulePremiumTarget,
		domain.ExitRulePremiumTrail, domain.ExitRuleTieredTP,
		domain.ExitRuleSDTarget:
		return true
	default:
		return false
	}
}

func isForcedExit(ruleType domain.ExitRuleType) bool {
	switch ruleType {
	case domain.ExitRuleMaxHoldingTime, domain.ExitRuleMaxLoss, domain.ExitRuleEODFlatten:
		return true
	default:
		return false
	}
}

// exitOrderParams determines order type, price, and TIF based on exit rule
// and retry count. All exits escalate: first attempt uses an aggressive limit
// with 5% buffer, subsequent retries and forced exits use market.
// Options use DAY TIF because IBKR paper expires MKT IOC options orders
// immediately (no simulated liquidity). Equities use IOC.
func exitOrderParams(ruleType domain.ExitRuleType, currentPrice float64, retryCount int, short, isOption bool) (price float64, orderType, tif string) {
	optionTIF := func(baseTIF string) string {
		if isOption {
			return "day"
		}
		return baseTIF
	}

	// Forced exits (EOD flatten, max loss, max holding time) go straight to market.
	// Any retry (retryCount >= 1) also escalates to market.
	if isForcedExit(ruleType) || retryCount >= 1 {
		return currentPrice, "market", optionTIF("ioc")
	}
	// First attempt: aggressive limit with 5% buffer to catch wide spreads.
	buf := 0.05
	if short {
		return currentPrice * (1 + buf), "limit", optionTIF("ioc")
	}
	return currentPrice * (1 - buf), "limit", optionTIF("ioc")
}
