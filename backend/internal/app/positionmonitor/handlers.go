package positionmonitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// handleFillEvent is the EventBusPort handler.
// It enqueues fills to the actor channel for async processing.
// NEVER blocks the caller — fills are queued via a buffered channel.
// Fills may be dropped if the actor falls behind (channel buffer exhausted).
func (s *Service) handleFillEvent(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return nil
	}

	symbol, _ := payload["symbol"].(string)
	side, _ := payload["side"].(string)
	direction, _ := payload["direction"].(string)
	price, _ := payload["price"].(float64)
	quantity, _ := payload["quantity"].(float64)
	strategy, _ := payload["strategy"].(string)
	filledAt, _ := payload["filled_at"].(time.Time)
	assetClass, _ := payload["asset_class"].(string)
	riskModStr, _ := payload["risk_modifier"].(string)
	instrumentType, _ := payload["instrument_type"].(string)
	optionRight, _ := payload["option_right"].(string)
	optionExpiryStr, _ := payload["option_expiry"].(string)
	ivAtEntryStr, _ := payload["iv_at_entry"].(string)
	deltaAtEntryStr, _ := payload["delta_at_entry"].(string)
	signalTags, _ := payload["signal_tags"].(map[string]string)

	if symbol == "" || price <= 0 || quantity <= 0 {
		return nil
	}

	var optionExpiry time.Time
	if optionExpiryStr != "" {
		optionExpiry, _ = time.Parse("2006-01-02", optionExpiryStr)
	}
	var ivAtEntry float64
	if ivAtEntryStr != "" {
		_, _ = fmt.Sscanf(ivAtEntryStr, "%f", &ivAtEntry)
	}
	var deltaAtEntry float64
	if deltaAtEntryStr != "" {
		_, _ = fmt.Sscanf(deltaAtEntryStr, "%f", &deltaAtEntry)
	}

	select {
	case s.fills <- fillMsg{
		Symbol:         domain.Symbol(symbol),
		Side:           side,
		Direction:      direction,
		Price:          price,
		Quantity:       quantity,
		FilledAt:       filledAt,
		Strategy:       strategy,
		AssetClass:     domain.AssetClass(assetClass),
		RiskModifier:   domain.NewRiskModifier(riskModStr),
		InstrumentType: domain.InstrumentType(instrumentType),
		OptionExpiry:   optionExpiry,
		OptionRight:    optionRight,
		IVAtEntry:      ivAtEntry,
		DeltaAtEntry:   deltaAtEntry,
		SignalTags:     signalTags,
	}:
	default:
		s.log.Warn().Str("symbol", symbol).Msg("position monitor: fill channel full, dropping fill")
	}
	return nil
}

func (s *Service) handleExitOrderTerminal(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return nil
	}
	symbol, _ := payload["symbol"].(string)
	brokerOrderID, _ := payload["broker_order_id"].(string)
	if symbol == "" {
		return nil
	}

	select {
	case s.exitTerminal <- exitOrderTerminalMsg{
		Symbol:        domain.Symbol(symbol),
		BrokerOrderID: brokerOrderID,
	}:
	default:
		s.log.Warn().Str("symbol", symbol).Msg("position monitor: exitTerminal channel full")
	}
	return nil
}

func (s *Service) handleExitRejected(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(domain.OrderIntentEventPayload)
	if !ok {
		return nil
	}
	dir, _ := domain.NewDirection(payload.Direction)
	if !dir.IsExit() {
		return nil
	}
	if !strings.Contains(payload.Reason, "no_position_to_exit") {
		return nil
	}

	select {
	case s.exitRejected <- exitRejectedMsg{
		Symbol: domain.Symbol(payload.Symbol),
		Reason: payload.Reason,
	}:
	default:
		s.log.Warn().Str("symbol", payload.Symbol).Msg("position monitor: exitRejected channel full")
	}
	return nil
}

func (s *Service) handleOrderSubmitted(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(domain.OrderIntentEventPayload)
	if !ok {
		return nil
	}
	if payload.BrokerOrderID == "" {
		return nil
	}
	dir, _ := domain.NewDirection(payload.Direction)
	if !dir.IsExit() {
		return nil
	}

	select {
	case s.exitSubmitted <- exitOrderSubmittedMsg{
		Symbol:        domain.Symbol(payload.Symbol),
		BrokerOrderID: payload.BrokerOrderID,
		Direction:     payload.Direction,
	}:
	default:
		s.log.Warn().Str("symbol", payload.Symbol).Msg("position monitor: exitSubmitted channel full")
	}
	return nil
}

// handleChandelierTrailArm externally arms a CHANDELIER_TRAIL rule on a
// matching option position. Target position is identified by the contract
// symbol (OCC); tenant/env are matched when supplied on the payload.
//
// Writes pos.CustomState["chandelier_ext_armed"] = 1 and seeds
// ["chandelier_ext_peak"] with the payload peak. The evaluator then tracks
// running peak and fires on giveback.
//
// Idempotent: re-arming updates peak only if the new peak is higher than the
// tracked one (callers typically arm exactly once per position).
func (s *Service) handleChandelierTrailArm(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(domain.ChandelierTrailArmPayload)
	if !ok {
		return nil
	}
	if payload.ContractSymbol == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var target *domain.MonitoredPosition
	for _, pos := range s.positions {
		if pos == nil {
			continue
		}
		if string(pos.Symbol) != payload.ContractSymbol {
			continue
		}
		if payload.TenantID != "" && pos.TenantID != payload.TenantID {
			continue
		}
		if payload.EnvMode != "" && string(pos.EnvMode) != payload.EnvMode {
			continue
		}
		if payload.Strategy != "" && pos.Strategy != payload.Strategy {
			continue
		}
		target = pos
		break
	}
	if target == nil {
		s.log.Warn().
			Str("contract_symbol", payload.ContractSymbol).
			Str("strategy", payload.Strategy).
			Msg("chandelier_trail_arm: no matching position")
		return nil
	}
	if target.CustomState == nil {
		target.CustomState = make(map[string]float64)
	}
	target.CustomState["chandelier_ext_armed"] = 1
	if payload.PeakPremium > target.CustomState["chandelier_ext_peak"] {
		target.CustomState["chandelier_ext_peak"] = payload.PeakPremium
	}
	s.log.Info().
		Str("contract_symbol", payload.ContractSymbol).
		Str("strategy", payload.Strategy).
		Float64("peak_premium", target.CustomState["chandelier_ext_peak"]).
		Msg("chandelier_trail armed externally")
	return nil
}

// handleCopytradeExitRequest routes a copytrade STC to the existing triggerExit
// path. The target position is identified by OCC contract symbol. Fraction is
// stashed in pos.CustomState["copytrade_exit_qty_frac"]; the fracKey loop in
// triggerExit consumes it to size the partial-close order. A synthetic
// COPYTRADE_STC rule is passed so logs and the idempotency key reflect the
// source.
func (s *Service) handleCopytradeExitRequest(_ context.Context, event domain.Event) error {
	payload, ok := event.Payload.(domain.CopytradeExitRequestPayload)
	if !ok {
		return nil
	}
	if payload.ContractSymbol == "" {
		return nil
	}
	if payload.Fraction <= 0 || payload.Fraction > 1.0 {
		s.log.Warn().
			Str("contract_symbol", payload.ContractSymbol).
			Float64("fraction", payload.Fraction).
			Msg("copytrade_exit_request: fraction out of range")
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var target *domain.MonitoredPosition
	for _, pos := range s.positions {
		if pos == nil {
			continue
		}
		if string(pos.Symbol) != payload.ContractSymbol {
			continue
		}
		if payload.TenantID != "" && pos.TenantID != payload.TenantID {
			continue
		}
		if payload.EnvMode != "" && string(pos.EnvMode) != payload.EnvMode {
			continue
		}
		if payload.Strategy != "" && pos.Strategy != payload.Strategy {
			continue
		}
		target = pos
		break
	}
	if target == nil {
		s.log.Warn().
			Str("contract_symbol", payload.ContractSymbol).
			Str("strategy", payload.Strategy).
			Msg("copytrade_exit_request: no matching position")
		return nil
	}
	if target.CustomState == nil {
		target.CustomState = make(map[string]float64)
	}
	// triggerExit's fracKey loop only honors frac < 1.0 for the partial path.
	// A 1.0 full close stashes the value but the loop will simply delete it
	// and proceed with exitQty = pos.Quantity, which is the correct behavior.
	target.CustomState["copytrade_exit_qty_frac"] = payload.Fraction

	reason := "copytrade:stc"
	if payload.Reason != "" {
		reason = fmt.Sprintf("copytrade:stc:%s", payload.Reason)
	}
	rule := domain.ExitRule{Type: domain.ExitRuleCopytradeSTC}
	// For option positions, triggerExit translates currentPrice via
	// EstimatedPremium if set. Pass EntryPrice as the underlying-price
	// equivalent (mirrors the re-peg/escalate callers at exit_eval.go:457,497).
	s.triggerExit(target, rule, reason, target.EntryPrice, s.nowFunc())
	s.log.Info().
		Str("contract_symbol", payload.ContractSymbol).
		Str("strategy", payload.Strategy).
		Float64("fraction", payload.Fraction).
		Str("keyword", payload.Reason).
		Msg("copytrade_exit_request dispatched")
	return nil
}
