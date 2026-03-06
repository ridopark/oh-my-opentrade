package execution

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// PositionGate prevents duplicate and conflicting entries by checking
// the broker's current positions and an in-memory inflight lock.
// It is the last line of defense before order submission.
type PositionGate struct {
	broker       ports.BrokerPort
	log          zerolog.Logger
	mu           sync.Mutex
	inflight     map[inflightKey]struct{}
	exitInflight map[inflightKey]struct{}
}

type inflightKey struct {
	TenantID string
	EnvMode  domain.EnvMode
	Symbol   domain.Symbol
}

// NewPositionGate creates a PositionGate backed by the given broker.
func NewPositionGate(broker ports.BrokerPort, log zerolog.Logger) *PositionGate {
	return &PositionGate{
		broker:       broker,
		log:          log,
		inflight:     make(map[inflightKey]struct{}),
		exitInflight: make(map[inflightKey]struct{}),
	}
}

// Check evaluates whether an order intent should be allowed through.
// Returns nil if allowed, or an error describing why the intent was rejected.
func (g *PositionGate) Check(ctx context.Context, intent domain.OrderIntent) error {
	// 1. Check inflight lock for entry intents.
	if isEntry(intent) {
		g.mu.Lock()
		_, locked := g.inflight[inflightKey{TenantID: intent.TenantID, EnvMode: intent.EnvMode, Symbol: intent.Symbol}]
		g.mu.Unlock()
		if locked {
			g.log.Warn().Str("symbol", string(intent.Symbol)).Msg("position gate: inflight entry exists")
			return ErrInflightEntry
		}
	}

	// 2. Query broker for current positions.
	positions, err := g.broker.GetPositions(ctx, intent.TenantID, intent.EnvMode)
	if err != nil {
		g.log.Error().Err(err).Str("symbol", string(intent.Symbol)).Msg("position gate: failed to query positions — rejecting conservatively")
		return fmt.Errorf("position_gate: broker error: %w", err)
	}

	// 3. Filter positions for this symbol.
	var symbolPositions []domain.Trade
	for _, p := range positions {
		if p.Symbol == intent.Symbol {
			symbolPositions = append(symbolPositions, p)
		}
	}
	side, _ := positionSide(symbolPositions)

	// 4. Apply gate rules.
	entry := isEntry(intent)

	if entry {
		switch side {
		case "BUY":
			// Already long, trying to buy more → duplicate.
			g.log.Warn().Str("symbol", string(intent.Symbol)).Msg("position gate: already in long position")
			return ErrAlreadyInPosition
		case "SELL":
			// Already short, trying to go long → conflict.
			g.log.Warn().Str("symbol", string(intent.Symbol)).Msg("position gate: conflicting position (short exists, long attempted)")
			return ErrConflictPosition
		}
		// side == "" → flat, entry allowed.
		return nil
	}

	// Exit intent.
	if side == "" {
		g.log.Warn().Str("symbol", string(intent.Symbol)).Msg("position gate: no position to exit")
		return ErrNoPositionToExit
	}

	// Check exit inflight lock.
	g.mu.Lock()
	_, exitLocked := g.exitInflight[inflightKey{TenantID: intent.TenantID, EnvMode: intent.EnvMode, Symbol: intent.Symbol}]
	g.mu.Unlock()
	if exitLocked {
		g.log.Warn().Str("symbol", string(intent.Symbol)).Msg("position gate: inflight exit exists")
		return ErrInflightExit
	}

	return nil
}

// MarkInflight records that an entry order has been submitted for a symbol
// and is awaiting fill. Subsequent entry intents for the same key will be rejected.
func (g *PositionGate) MarkInflight(tenantID string, envMode domain.EnvMode, symbol domain.Symbol) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight[inflightKey{TenantID: tenantID, EnvMode: envMode, Symbol: symbol}] = struct{}{}
}

// ClearInflight removes the inflight lock for a symbol, typically after
// a fill, cancel, or reject event.
func (g *PositionGate) ClearInflight(tenantID string, envMode domain.EnvMode, symbol domain.Symbol) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.inflight, inflightKey{TenantID: tenantID, EnvMode: envMode, Symbol: symbol})
}

// TryMarkInflightExit atomically attempts to set the exit inflight lock.
// Returns true if the lock was acquired (no prior exit inflight).
// Returns false if an exit is already inflight for this key.
func (g *PositionGate) TryMarkInflightExit(tenantID string, envMode domain.EnvMode, symbol domain.Symbol) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := inflightKey{TenantID: tenantID, EnvMode: envMode, Symbol: symbol}
	if _, exists := g.exitInflight[key]; exists {
		return false
	}
	g.exitInflight[key] = struct{}{}
	return true
}

// ClearInflightExit removes the exit inflight lock for a symbol, typically after
// a fill, cancel, reject, or timeout event clears the pending exit.
func (g *PositionGate) ClearInflightExit(tenantID string, envMode domain.EnvMode, symbol domain.Symbol) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.exitInflight, inflightKey{TenantID: tenantID, EnvMode: envMode, Symbol: symbol})
}

// isEntry returns true if the intent opens or increases a position.
func isEntry(intent domain.OrderIntent) bool {
	return !intent.Direction.IsExit()
}

// positionSide returns the net side ("BUY"/"SELL") and total quantity from positions.
// Handles both internal formats ("BUY"/"SELL") and Alpaca formats ("long"/"short").
func positionSide(positions []domain.Trade) (side string, qty float64) {
	for _, t := range positions {
		switch strings.ToLower(t.Side) {
		case "buy", "long":
			qty += t.Quantity
		case "sell", "short":
			qty -= t.Quantity
		}
	}
	switch {
	case qty > 0:
		return "BUY", qty
	case qty < 0:
		return "SELL", -qty
	default:
		return "", 0
	}
}

// Sentinel errors for position gate rejections.
var (
	ErrAlreadyInPosition = fmt.Errorf("position_gate: already_in_position")
	ErrConflictPosition  = fmt.Errorf("position_gate: conflict")
	ErrInflightEntry     = fmt.Errorf("position_gate: inflight")
	ErrInflightExit      = fmt.Errorf("position_gate: inflight_exit")
	ErrNoPositionToExit  = fmt.Errorf("position_gate: no_position_to_exit")
)
