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
