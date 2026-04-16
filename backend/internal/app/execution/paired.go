package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// LegResult captures the outcome of a single leg within a paired group submission.
type LegResult struct {
	Intent     domain.OrderIntent
	OrderID    string
	Venue      domain.Venue
	Submitted  bool
	Error      error
	RolledBack bool
}

// PairedExecutor handles atomic submission of grouped order legs.
// It submits all legs in a group, and if any fail, rolls back
// (cancels) the successfully submitted ones.
type PairedExecutor struct {
	brokers map[domain.Venue]ports.BrokerPort
	sizer   *GroupSizer // nil disables notional validation
	log     zerolog.Logger
}

// NewPairedExecutor creates a PairedExecutor with broker adapters keyed by venue.
// An optional GroupSizer enforces aggregate notional limits before submission.
func NewPairedExecutor(brokers map[domain.Venue]ports.BrokerPort, log zerolog.Logger, sizer *GroupSizer) *PairedExecutor {
	return &PairedExecutor{
		brokers: brokers,
		sizer:   sizer,
		log:     log.With().Str("component", "paired-executor").Logger(),
	}
}

// SubmitGroup submits all intents in the group atomically. Returns results
// per-intent. If any submission fails, previously submitted legs are canceled
// (best-effort rollback).
func (p *PairedExecutor) SubmitGroup(ctx context.Context, intents []domain.OrderIntent) ([]LegResult, error) {
	if len(intents) == 0 {
		return nil, errors.New("paired: empty intent group")
	}

	groupID := intents[0].LegGroupID
	if groupID == "" {
		return nil, errors.New("paired: first intent has empty LegGroupID")
	}
	for i, intent := range intents {
		if intent.LegGroupID != groupID {
			return nil, fmt.Errorf("paired: intent[%d] LegGroupID %q does not match group %q", i, intent.LegGroupID, groupID)
		}
	}

	// Group-level notional risk check (when sizer is configured).
	if p.sizer != nil {
		prices := make(map[domain.Symbol]float64, len(intents))
		for _, intent := range intents {
			venue := intent.ResolvedVenue()
			if broker, ok := p.brokers[venue]; ok {
				if qty, err := broker.GetPosition(ctx, intent.Symbol); err == nil {
					_ = qty // price lookup: use limit price as proxy
				}
			}
			if intent.LimitPrice > 0 {
				prices[intent.Symbol] = intent.LimitPrice
			}
		}
		if err := p.sizer.Validate(intents, prices); err != nil {
			return nil, fmt.Errorf("paired: %w", err)
		}
	}

	results := make([]LegResult, len(intents))
	var failIdx = -1

	// Submit legs sequentially to detect failures early.
	for i, intent := range intents {
		venue := intent.ResolvedVenue()
		results[i] = LegResult{
			Intent: intent,
			Venue:  venue,
		}

		broker, ok := p.brokers[venue]
		if !ok {
			results[i].Error = fmt.Errorf("paired: no broker registered for venue %q", venue)
			failIdx = i
			break
		}

		orderID, err := broker.SubmitOrder(ctx, intent)
		if err != nil {
			results[i].Error = fmt.Errorf("paired: leg[%d] submit on %s: %w", i, venue, err)
			failIdx = i
			break
		}

		results[i].OrderID = orderID
		results[i].Submitted = true
		p.log.Info().
			Str("groupId", groupID).
			Int("leg", i).
			Str("venue", string(venue)).
			Str("symbol", string(intent.Symbol)).
			Str("orderId", orderID).
			Msg("leg submitted")
	}

	// If all legs succeeded, return.
	if failIdx == -1 {
		return results, nil
	}

	// Rollback: cancel previously submitted legs (best-effort).
	p.log.Warn().
		Str("groupId", groupID).
		Int("failedLeg", failIdx).
		Err(results[failIdx].Error).
		Msg("leg failed, rolling back submitted legs")

	for i := 0; i < failIdx; i++ {
		if !results[i].Submitted {
			continue
		}
		venue := results[i].Venue
		broker := p.brokers[venue]

		// Try cancel first (works for unfilled/pending orders on live brokers).
		if cancelErr := broker.CancelOrder(ctx, results[i].OrderID); cancelErr == nil {
			results[i].RolledBack = true
			p.log.Info().
				Str("groupId", groupID).Int("leg", i).
				Str("orderId", results[i].OrderID).
				Msg("leg rolled back via cancel")
			continue
		}

		// Cancel failed (order already filled) — submit a reversing close order.
		// This handles instant-fill brokers (simbroker) and live fills.
		reverseDir := reverseDirection(results[i].Intent.Direction)
		reverseIntent := results[i].Intent
		reverseIntent.Direction = reverseDir
		reverseIntent.LegGroupID = "" // don't group the reversal
		reverseIntent.Rationale = fmt.Sprintf("rollback leg %d of group %s", i, groupID)
		if _, closeErr := broker.SubmitOrder(ctx, reverseIntent); closeErr != nil {
			p.log.Error().Err(closeErr).
				Str("groupId", groupID).Int("leg", i).
				Msg("rollback close failed — position may be unhedged")
		} else {
			results[i].RolledBack = true
			p.log.Info().
				Str("groupId", groupID).Int("leg", i).
				Msg("leg rolled back via reversing close")
		}
	}

	return results, fmt.Errorf("paired: group %s failed at leg %d: %w", groupID, failIdx, results[failIdx].Error)
}

// reverseDirection returns the closing direction for a given entry direction.
func reverseDirection(d domain.Direction) domain.Direction {
	switch d {
	case domain.DirectionLong:
		return domain.DirectionCloseLong
	case domain.DirectionShort:
		return domain.DirectionCloseShort
	default:
		return d
	}
}
