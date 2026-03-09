package positionmonitor

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// tick evaluates all exit rules against all monitored positions.
// Time-only rules (MAX_HOLDING_TIME, EOD_FLATTEN) are evaluated even when
// price data is stale so positions are never stuck past their time limits.
func (s *Service) tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()

	for _, pos := range s.positions {
		if pos.ExitPending {
			if now.Sub(pos.ExitPendingAt) > exitPendingTimeout {
				s.handleExitTimeout(pos)
			}
			continue
		}

		snap, ok := s.priceCache.LatestPrice(pos.Symbol)
		priceAvailable := ok && now.Sub(snap.ObservedAt) <= s.maxPriceStaleness

		// Phase 1: time-only rules — always evaluated, no price dependency.
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

		if pos.ExitPending {
			continue
		}

		// Phase 2: price-dependent rules — only with fresh price data.
		if !priceAvailable {
			continue
		}

		price := snap.Price
		pos.UpdateWaterMarks(price)

		evalCtx := EvalContext{}
		if s.snapshotFn != nil {
			if indSnap, ok := s.snapshotFn(string(pos.Symbol)); ok {
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
		UpdateStepStopState(pos, price, evalCtx)

		for _, rule := range pos.ExitRules {
			if !rule.Type.RequiresPrice() {
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
			s.log.Warn().Err(err).
				Str("symbol", string(pos.Symbol)).
				Str("broker_order_id", pos.ExitOrderID).
				Msg("failed to cancel stale exit order — may already be terminal")
		} else {
			s.log.Info().
				Str("symbol", string(pos.Symbol)).
				Str("broker_order_id", pos.ExitOrderID).
				Msg("cancelled stale exit order")
		}
	}

	pos.ExitPending = false
	pos.ExitOrderID = ""
	pos.ExitRetryCount++

	s.log.Warn().
		Str("symbol", string(pos.Symbol)).
		Int("retry_count", pos.ExitRetryCount).
		Msg("exit pending timeout — will retry with escalated price")
}

// triggerExit marks a position as exit-pending and emits an exit order intent.
func (s *Service) triggerExit(pos *domain.MonitoredPosition, rule domain.ExitRule, reason string, currentPrice float64, now time.Time) {
	pos.ExitPending = true
	pos.ExitPendingAt = now

	idempotencyKey := fmt.Sprintf("EXIT:%s:%s:%s:%d:%s:%d",
		pos.TenantID, pos.EnvMode, pos.Symbol, pos.EntryTime.Unix(), rule.Type, pos.ExitRetryCount)

	exitPrice, orderType, tif := exitOrderParams(rule.Type, currentPrice, pos.ExitRetryCount)

	intent, err := domain.NewOrderIntent(
		uuid.New(),
		pos.TenantID,
		pos.EnvMode,
		pos.Symbol,
		domain.DirectionCloseLong,
		exitPrice,
		0,
		0,
		pos.Quantity,
		pos.Strategy,
		fmt.Sprintf("exit_monitor:%s:%s", rule.Type, reason),
		1.0,
		idempotencyKey,
	)
	if err == nil {
		intent.OrderType = orderType
		intent.TimeInForce = tif
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

func isForcedExit(ruleType domain.ExitRuleType) bool {
	switch ruleType {
	case domain.ExitRuleMaxHoldingTime, domain.ExitRuleMaxLoss, domain.ExitRuleEODFlatten:
		return true
	default:
		return false
	}
}

// exitOrderParams determines order type, price, and TIF based on exit rule
// and retry count. Forced exits escalate: 2% → 3% → 5% → market.
func exitOrderParams(ruleType domain.ExitRuleType, currentPrice float64, retryCount int) (price float64, orderType, tif string) {
	if !isForcedExit(ruleType) {
		return currentPrice, "limit", ""
	}

	if retryCount >= maxExitRetries {
		return currentPrice, "market", "ioc"
	}

	buffers := []float64{0.02, 0.03, 0.05}
	buf := buffers[retryCount]
	return currentPrice * (1 - buf), "limit", "ioc"
}
