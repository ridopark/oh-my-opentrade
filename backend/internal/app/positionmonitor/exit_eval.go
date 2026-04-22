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
			// ExitManaging means a cancel-and-await goroutine already owns
			// the exit lifecycle. Re-entering handleExitTimeout here would
			// stack cancel/submit calls in flight and corrupt ExitRepegCount.
			if pos.ExitManaging {
				continue
			}
			timeout := exitTimeoutForPos(pos)
			if now.Sub(pos.ExitPendingAt) > timeout {
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

			// Track spot MFE/MAE (% of entry) for non-option positions.
			// Always-on instrumentation — mirrors premium_mfe_pct/premium_mae_pct
			// for options, but for equities and crypto spot.
			if pos.InstrumentType != domain.InstrumentTypeOption && pos.EntryPrice > 0 {
				if pos.CustomState == nil {
					pos.CustomState = make(map[string]float64)
				}
				pctChange := pos.UnrealizedPnLPct(price)
				if mfe, ok := pos.CustomState["spot_mfe_pct"]; !ok || pctChange > mfe {
					pos.CustomState["spot_mfe_pct"] = pctChange
				}
				if mae, ok := pos.CustomState["spot_mae_pct"]; !ok || pctChange < mae {
					pos.CustomState["spot_mae_pct"] = pctChange
				}
			}

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

			evalCtx := newEvalContext()
			evalCtx.BarDuration = s.barDurationFor(pos.Strategy)
			evalCtx.BarHigh = snap.High
			evalCtx.BarLow = snap.Low
			// ATR-bucketed PREMIUM_TRAIL multiplier stamped at fill time
			// into pos.CustomState["atr_trail_mult"]. Zero/missing →
			// newEvalContext()'s default (1.0) stands.
			if m, ok := pos.CustomState["atr_trail_mult"]; ok && m > 0 {
				evalCtx.TrailMult = m
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

			// When StrategyExitsPriority is set, strategy-emitted exits
			// (trailing_stop, hard_stop, signal_reversal) are authoritative;
			// skip price-based rule evaluation. Time-only rules (Phase 2
			// below) still fire as a safety net (MAX_HOLDING_TIME, EOD).
			if !pos.StrategyExitsPriority {
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

// handleExitTimeout is invoked from the tick loop when a pending exit has
// aged out of its asymmetric timeout. It picks re-peg vs market escalation,
// tags the outgoing order via RepegNotifier so execution.cleanupPendingOrder
// will not launch a dust sweep on the cancel, performs the broker cancel
// (releasing s.mu across the RPC), and re-emits a new exit intent in the
// SAME attempt — leaving ExitPending=true throughout.
//
// Invariant (post-SOFI phantom-short fix, 2026-04-16):
//
//	ExitPending stays true from the first triggerExit to either a fill or
//	full abandonment. Re-peg and market-escalate both re-use triggerExit,
//	which is idempotent-true→true on ExitPending. This is what stops the
//	tick loop from evaluating OTHER rules (STAGNATION_EXIT was the rule
//	that fired order 1605 in parallel with our re-peg cancel) while a
//	cancel+resubmit is in flight. The old flow cleared ExitPending=false
//	between the cancel terminal and the next triggerExit — a window where
//	the tick loop re-evaluated and emitted a parallel intent.
//
// The tick loop holds s.mu when it calls us. We release it across the
// broker call because holding a service-wide mutex across network I/O
// risks deadlock and the PositionGate leaf-critical-section invariant
// forbids calling ClearInflightExit under s.mu. The tick iterator is safe
// across the release because it re-looks-up each pos by key on every
// iteration.
func (s *Service) handleExitTimeout(pos *domain.MonitoredPosition) {
	now := s.nowFunc()

	if pos.ExitWallStartedAt.IsZero() {
		pos.ExitWallStartedAt = pos.ExitPendingAt
	}

	repegBudget := exitRepegBudget(pos)
	overWallTime := now.Sub(pos.ExitWallStartedAt) >= exitMaxWallTime
	nearClose := isNearSessionClose(now, pos.AssetClass)
	canRepeg := pos.ExitRepegCount < repegBudget && !overWallTime && !nearClose

	// TODO(atr-override): risk-manager asked for "if underlying moved
	// > 0.5*ATR(5m) against us during a pending exit, skip re-peg and go
	// to market". Wiring an ATR port is a separate plumbing effort.

	orderID := pos.ExitOrderID
	action := "escalate"
	if canRepeg {
		action = "repeg"
	}

	// ExitManaging is kept true across the full cancel-and-resubmit cycle.
	// With the single-ExitPending invariant it is belt-and-suspenders — the
	// tick loop's `if pos.ExitPending` check short-circuits rule evaluation
	// before we ever get here — but we keep the flag to also prevent a
	// concurrent timer-driven handleExitTimeout re-entry on the same pos
	// if the re-peg triggerExit somehow stamps an immediately-stale
	// ExitPendingAt (it won't today, but belt-and-suspenders).
	pos.ExitManaging = true

	tenantID := pos.TenantID
	envMode := pos.EnvMode
	symbol := pos.Symbol
	key := positionKey(tenantID, envMode, symbol)
	repegNotifier := s.repegNotifier

	s.log.Warn().
		Str("symbol", string(symbol)).
		Str("broker_order_id", orderID).
		Str("action", action).
		Int("repeg_count", pos.ExitRepegCount).
		Int("retry_count", pos.ExitRetryCount).
		Bool("over_wall_time", overWallTime).
		Bool("near_close", nearClose).
		Msg("exit pending timeout — managing")

	// Tag the pending order BEFORE we release s.mu and BEFORE the broker
	// cancel goes out. Both the re-peg and escalate paths need this — on
	// escalate, the to-be-canceled limit would otherwise trigger a
	// dust-sweep sibling order (the SOFI 1604→1606 bug). The call is
	// thread-safe against cleanupPendingOrder: if the order is already
	// gone we get ok=false and carry on. No s.mu held on the execution
	// side — MarkRepegCancel only touches the sync.Map.
	if repegNotifier != nil && orderID != "" {
		if !repegNotifier.MarkRepegCancel(orderID) {
			s.log.Debug().
				Str("broker_order_id", orderID).
				Msg("repeg notify: no pending order — likely already terminal")
		}
	}

	// Release s.mu across the broker RPC. pos pointer is not safe to read
	// after this point — re-acquire and re-lookup by key. handleExitTimeout
	// is required to return with s.mu held.
	s.mu.Unlock()
	reconciled := s.cancelAndAwaitTerminal(key, orderID)
	s.mu.Lock()

	livePos, ok := s.positions[key]
	if !ok {
		// pos deleted during the RPC window (fill reconcile, ghost cleanup).
		return
	}
	if reconciled {
		livePos.ExitManaging = false
		return
	}
	if livePos.ExitOrderID != orderID {
		// Another handler cycled the exit order. Do NOT clear ExitPending
		// — that handler owns it now.
		livePos.ExitManaging = false
		return
	}

	// Broker order is terminal. Under the single-ExitPending invariant we
	// do NOT clear ExitPending here — triggerExit below will overwrite
	// ExitPendingAt for the NEW attempt, and processExitSubmitted will
	// swap ExitOrderID in when the broker acks the resubmission. Leaving
	// ExitPending=true across this window is what prevents tick-loop rule
	// evaluation from firing a parallel CLOSE_LONG (the SOFI 1605 bug).
	//
	// We DO clear ExitOrderID so processExitTerminal's late-arriving event
	// for the old order finds "no tracked order" and returns without
	// bumping counters. triggerExit does not reference ExitOrderID.
	livePos.ExitOrderID = ""

	if action == "repeg" {
		livePos.ExitRepegCount++
		rule, ruleOK := currentExitRule(livePos)
		if !ruleOK {
			// No exit rule to re-fire — degrade to escalation and bail.
			livePos.ExitRetryCount++
			livePos.ExitRepegCount = 0
			livePos.ExitManaging = false
			// Keep ExitPending=true so the tick loop sees a pending exit
			// on the next tick; the asymmetric-timeout branch will re-fire
			// handleExitTimeout, and with wall-time/budget now exhausted
			// it will land on the escalate path and market-submit.
			livePos.ExitPendingAt = s.nowFunc()
			s.mu.Unlock()
			if s.positionGate != nil {
				s.positionGate.ClearInflightExit(tenantID, envMode, symbol)
			}
			s.mu.Lock()
			return
		}
		reason := fmt.Sprintf("repeg %d/%d", livePos.ExitRepegCount, exitRepegBudget(livePos))
		livePos.ExitManaging = false
		// Gate must be cleared BEFORE the re-emitted intent is processed
		// by execution (it will call TryMarkInflightExit). Release s.mu
		// (leaf invariant), clear gate, re-acquire, THEN triggerExit so
		// the sequence is gate-clear → trigger → publish. If we triggered
		// first the outbox publisher could race us to execution.handleIntent.
		s.mu.Unlock()
		if s.positionGate != nil {
			s.positionGate.ClearInflightExit(tenantID, envMode, symbol)
		}
		s.mu.Lock()
		livePos, ok = s.positions[key]
		if !ok {
			return
		}
		s.triggerExit(livePos, rule, reason, livePos.EntryPrice, s.nowFunc())
		return
	}

	// Escalate path: bump retry so exitOrderParams picks market, reset
	// per-attempt state, and re-emit via triggerExit so ExitPending stays
	// true continuously. Pick the current exit rule the same way re-peg
	// does; forced-exit rules will route through market naturally.
	livePos.ExitRetryCount++
	livePos.ExitRepegCount = 0
	livePos.ExitWallStartedAt = time.Time{}
	livePos.ExitLastSentPrice = 0
	livePos.ExitManaging = false

	rule, ruleOK := currentExitRule(livePos)
	s.mu.Unlock()
	if s.positionGate != nil {
		s.positionGate.ClearInflightExit(tenantID, envMode, symbol)
	}
	// Cancel any peer exit orders that may be working for this position
	// (e.g. an EOD_FLATTEN submitted in the gap between an unsolicited
	// broker-cancel of the primary limit and the escalate firing). The
	// single-slot ExitOrderID cannot police parallels; this enforces
	// "at most one working exit" before the market order goes out.
	peerReconciled := s.cancelAllPendingExits(key)
	s.mu.Lock()
	livePos, ok = s.positions[key]
	if !ok {
		return
	}
	if peerReconciled {
		// A peer cancel raced a fill and reconcile removed the position.
		return
	}
	if !ruleOK {
		// No rule — reset ExitPendingAt so the tick loop retries timeout
		// logic on the next pass. Leaves ExitPending=true.
		livePos.ExitPendingAt = s.nowFunc()
		return
	}
	s.triggerExit(livePos, rule, "escalate-to-market", livePos.EntryPrice, s.nowFunc())
}

// cancelAllPendingExits iterates pos.PendingExitOrderIDs and cancels each
// via cancelAndAwaitTerminal. Safe against races: the existing single-order
// helper returns (reconciled=true) if a cancel-fill race occurred, and the
// map entry will be removed by processExitTerminal before the race unwinds.
// The caller must NOT hold s.mu on entry — this helper acquires it to
// snapshot the set, then releases it before performing broker RPCs.
func (s *Service) cancelAllPendingExits(key string) (reconciledAny bool) {
	s.mu.Lock()
	pos, ok := s.positions[key]
	if !ok {
		s.mu.Unlock()
		return false
	}
	ids := make([]string, 0, len(pos.PendingExitOrderIDs))
	for id := range pos.PendingExitOrderIDs {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if r := s.cancelAndAwaitTerminal(key, id); r {
			reconciledAny = true
		}
	}
	return
}

// cancelAndAwaitTerminal issues the cancel and polls GetOrderDetails until
// the order reaches a terminal state or the confirm window elapses. Returns
// reconciled=true when the cancel raced a fill and the reconcile branch has
// already removed the position from the monitor.
func (s *Service) cancelAndAwaitTerminal(key, orderID string) (reconciled bool) {
	if orderID == "" || s.broker == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), exitCancelConfirm+2*time.Second)
	defer cancel()

	if err := s.broker.CancelOrder(ctx, orderID); err != nil {
		if strings.Contains(err.Error(), "filled") {
			// Cancel lost the race with a fill. Reconcile needs mutable
			// pos state, so re-acquire s.mu for that path — reconcileFilledOrder
			// removes the position from the map, which is what we report.
			s.mu.Lock()
			pos, ok := s.positions[key]
			if !ok {
				s.mu.Unlock()
				return true
			}
			if pos.ExitOrderID != orderID {
				s.mu.Unlock()
				return false
			}
			done := s.reconcileFilledOrder(pos)
			s.mu.Unlock()
			return done
		}
		s.log.Warn().Err(err).
			Str("broker_order_id", orderID).
			Msg("cancel failed — may already be terminal, continuing")
	}

	deadline := time.Now().Add(exitCancelConfirm)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if time.Now().After(deadline) {
				s.log.Warn().
					Str("broker_order_id", orderID).
					Msg("cancel-terminal wait expired — proceeding (force-clear)")
				return false
			}
			details, err := s.broker.GetOrderDetails(ctx, orderID)
			if err != nil {
				continue
			}
			switch details.Status {
			case "canceled", "expired", "rejected":
				return false
			case "filled":
				// Cancel lost the race with a fill during the wait. Run the
				// same reconcile branch as the cancel-error case.
				s.mu.Lock()
				pos, ok := s.positions[key]
				if !ok {
					s.mu.Unlock()
					return true
				}
				if pos.ExitOrderID != orderID {
					s.mu.Unlock()
					return false
				}
				done := s.reconcileFilledOrder(pos)
				s.mu.Unlock()
				return done
			}
		}
	}
}

// currentExitRule returns the most-relevant exit rule for a re-peg. Since
// we don't persist which rule triggered the original exit, we pick the
// first stop-category rule (stops dominate target re-evaluation during a
// pending exit — if the underlying moved against us, the re-peg is a
// worst-case protective exit). Falls back to the first rule when none is
// a stop. Returns ok=false only when the position has no exit rules at all.
func currentExitRule(pos *domain.MonitoredPosition) (domain.ExitRule, bool) {
	if len(pos.ExitRules) == 0 {
		return domain.ExitRule{}, false
	}
	for _, r := range pos.ExitRules {
		if ruleCategoryIsStop(r.Type) || isForcedExit(r.Type) {
			return r, true
		}
	}
	return pos.ExitRules[0], true
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

// isExitRetryReason reports whether a triggerExit call is part of the
// cancel-and-resubmit lifecycle owned by handleExitTimeout (re-peg or
// market-escalate). New rule-driven reasons (PREMIUM_TRAIL, EOD_FLATTEN,
// stops, targets) are NOT retries. Used by triggerExit to hard-skip a
// second rule-driven trigger while a prior exit is still in flight.
func isExitRetryReason(reason string) bool {
	return reason == "escalate-to-market" || strings.HasPrefix(reason, "repeg ")
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

	// Cross-reason exit arbitration: if an exit is already in flight AND this
	// is not a retry (re-peg/escalate), skip. The tick-loop Phase 1/Phase 2
	// guard handles the intra-tick case; this protects against any
	// out-of-band caller (future evaluator, revaluator, bootstrap) emitting
	// a parallel intent while a prior exit order is still working at the
	// broker. EOD_FLATTEN firing while a PREMIUM_TRAIL limit is working is
	// the motivating case — both reaching SubmitOrder caused duplicate fills.
	if pos.ExitPending && !isExitRetryReason(reason) && len(pos.PendingExitOrderIDs) > 0 {
		s.log.Warn().
			Str("symbol", string(pos.Symbol)).
			Str("rule", string(rule.Type)).
			Str("reason", reason).
			Int("pending_orders", len(pos.PendingExitOrderIDs)).
			Msg("exit trigger suppressed — prior exit still in flight")
		return
	}

	pos.ExitPending = true
	pos.ExitPendingAt = now

	idempotencyKey := fmt.Sprintf("EXIT:%s:%s:%s:%d:%s:%d",
		pos.TenantID, pos.EnvMode, pos.Symbol, pos.EntryTime.Unix(), rule.Type, pos.ExitRetryCount)

	// For option positions, currentPrice here is the UNDERLYING spot (used
	// to evaluate exit rules that are defined in underlying terms). The
	// exit order needs to be priced in option-premium terms, so translate
	// via EstimatedPremium before handing to exitOrderParams. Market/IOC
	// exits ignore the limit, but this keeps the telemetry (DB limit_price,
	// reconcile checks, UI display) honest.
	priceForOrder := currentPrice
	if pos.InstrumentType == domain.InstrumentTypeOption {
		if est := pos.EstimatedPremium(currentPrice, now); est > 0 {
			priceForOrder = est
		}
	}

	// For options on the first attempt, try to price the exit limit against
	// the live bid/ask. This catches cases where the 5%-below-mid formula
	// would sit stranded above the bid — the symptom that motivated this
	// change. If the quote is stale, blown out, or missing, fall through to
	// the mid-based formula unchanged.
	isOption := pos.InstrumentType == domain.InstrumentTypeOption
	var quote *domain.OptionQuote
	if isOption && !isForcedExit(rule.Type) && pos.ExitRetryCount == 0 && s.optionsPricePort != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		if quotes, err := s.optionsPricePort.GetOptionPrices(ctx, []domain.Symbol{pos.Symbol}); err == nil {
			if q, ok := quotes[pos.Symbol]; ok {
				qc := q
				quote = &qc
			}
		}
		cancel()
	}

	dte := 0
	if isOption {
		dte = dteFromExpiry(pos.OptionExpiry, now)
	}
	exitPrice, orderType, tif := s.exitOrderParams(rule.Type, priceForOrder, pos.ExitRetryCount, pos.IsShort(), isOption, quote, dte, now)
	// Record the last-sent limit price so the re-peg path can tighten
	// against it on a subsequent cycle.
	pos.ExitLastSentPrice = exitPrice

	exitDirection := domain.DirectionCloseLong
	if pos.IsShort() {
		exitDirection = domain.DirectionCloseShort
	}
	// For options, the broker position is always LONG the contract regardless
	// of the thesis direction (short thesis = long puts, long thesis = long
	// calls). Closing is always a SELL. CLOSE_SHORT would trigger a BUY via
	// brokerSideFor, doubling the position instead of closing it.
	if pos.InstrumentType == domain.InstrumentTypeOption {
		exitDirection = domain.DirectionCloseLong
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
			if pos.InstrumentType != domain.InstrumentTypeOption {
				if v, ok := pos.CustomState["spot_mfe_pct"]; ok {
					intent.Meta["spot_mfe_pct"] = fmt.Sprintf("%.6f", v)
				}
				if v, ok := pos.CustomState["spot_mae_pct"]; ok {
					intent.Meta["spot_mae_pct"] = fmt.Sprintf("%.6f", v)
				}
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
		domain.ExitRuleSDTarget, domain.ExitRuleChandelierTrail:
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
// and retry count. All exits escalate: first attempt uses an aggressive limit,
// subsequent retries and forced exits use market. Options use DAY TIF because
// IBKR paper expires MKT IOC options orders immediately (no simulated
// liquidity). Equities use IOC.
//
// For option first-attempts, when quote is non-nil and buildExitLimitPrice
// returns usable=true, we price against the live bid/ask using a DTE-scaled
// k. Otherwise we fall back to the historical mid ± 5% formula so behavior
// is a strict superset of the prior code path — critical so a missing quote
// or new failure mode in the pricer never silently suppresses an exit.
func (s *Service) exitOrderParams(
	ruleType domain.ExitRuleType,
	currentPrice float64,
	retryCount int,
	short, isOption bool,
	quote *domain.OptionQuote,
	dte int,
	now time.Time,
) (price float64, orderType, tif string) {
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

	if isOption && quote != nil {
		if p, ok := buildExitLimitPrice(*quote, now, dte, short); ok {
			s.log.Debug().
				Float64("bid", quote.Bid).
				Float64("ask", quote.Ask).
				Int("bid_size", quote.BidSize).
				Int("dte", dte).
				Float64("limit", p).
				Msg("exit: quote-based limit pricing")
			return p, "limit", optionTIF("ioc")
		}
	}

	// First attempt fallback: aggressive limit with 5% buffer off mid.
	buf := 0.05
	if short {
		return currentPrice * (1 + buf), "limit", optionTIF("ioc")
	}
	return currentPrice * (1 - buf), "limit", optionTIF("ioc")
}

// positionKey mirrors the position map key scheme used throughout the
// service. Exposed as a helper so the async exit-management path can
// re-lookup a position without rebuilding the fmt.Sprintf by hand.
func positionKey(tenantID string, envMode domain.EnvMode, symbol domain.Symbol) string {
	return fmt.Sprintf("%s:%s:%s", tenantID, envMode, symbol)
}

// ruleCategoryIsStop reports whether an exit rule type is a capital-protection
// stop. Stops escalate fast (short timeout, one re-peg) because an adverse
// move compounds. Targets (ProfitTarget, PremiumTarget, TieredTP) defer
// profit and can afford more re-pegs.
func ruleCategoryIsStop(ruleType domain.ExitRuleType) bool {
	switch ruleType {
	case domain.ExitRuleTrailingStop,
		domain.ExitRulePremiumTrail,
		domain.ExitRulePremiumStop,
		domain.ExitRuleVolatilityStop,
		domain.ExitRuleStepStop,
		domain.ExitRuleSwingStop,
		domain.ExitRuleBreakevenStop,
		domain.ExitRuleChandelierTrail,
		domain.ExitRuleStagnationExit,
		domain.ExitRuleFastFail:
		return true
	}
	return isForcedExit(ruleType)
}

// exitTimeoutForPos returns the asymmetric pending-exit timeout for a
// monitored position. Equity always uses the legacy 10s. Options split by
// rule category: stops 10s, targets 30s. When multiple rules are attached,
// the most-conservative (shortest) timeout wins because the first rule to
// timeout is the one holding capital exposed.
func exitTimeoutForPos(pos *domain.MonitoredPosition) time.Duration {
	if pos.InstrumentType != domain.InstrumentTypeOption {
		return exitPendingTimeoutEquity
	}
	timeout := exitPendingTimeoutOptionTarget
	for _, r := range pos.ExitRules {
		if ruleCategoryIsStop(r.Type) {
			return exitPendingTimeoutOptionStop
		}
	}
	return timeout
}

// exitRepegBudget returns the max number of re-pegs for the current exit
// attempt. Stops get a single re-peg before market escalation; targets get
// three. Equity gets zero (the equity path never re-pegs — US equity
// spreads are usually tight enough that chasing is counterproductive).
func exitRepegBudget(pos *domain.MonitoredPosition) int {
	if pos.InstrumentType != domain.InstrumentTypeOption {
		return 0
	}
	for _, r := range pos.ExitRules {
		if ruleCategoryIsStop(r.Type) {
			return exitMaxRepegsStop
		}
	}
	return exitMaxRepegsTarget
}

// isNearSessionClose reports whether now is within exitNoRepegBeforeCloseMin
// minutes of the US equity-options session close. Outside RTH (crypto,
// off-hours) always returns false — the near-close guard does not apply.
func isNearSessionClose(now time.Time, assetClass domain.AssetClass) bool {
	if assetClass == domain.AssetClassCrypto {
		return false
	}
	loc := domain.NYLocation()
	if loc == nil {
		return false
	}
	et := now.In(loc)
	if et.Weekday() == time.Saturday || et.Weekday() == time.Sunday {
		return false
	}
	close := time.Date(et.Year(), et.Month(), et.Day(), 16, 0, 0, 0, loc)
	delta := close.Sub(et)
	return delta > 0 && delta <= time.Duration(exitNoRepegBeforeCloseMin)*time.Minute
}
