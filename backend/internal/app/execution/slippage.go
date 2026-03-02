package execution

import (
	"context"
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// QuoteProvider retrieves current bid/ask quotes for a given symbol.
type QuoteProvider interface {
	GetQuote(ctx context.Context, symbol domain.Symbol) (bid float64, ask float64, err error)
}

// SlippageGuard rejects order intents when the current market spread
// exceeds the intent's configured maximum slippage tolerance.
type SlippageGuard struct {
	provider QuoteProvider
}

// NewSlippageGuard creates a SlippageGuard backed by the given QuoteProvider.
func NewSlippageGuard(quoteProvider QuoteProvider) *SlippageGuard {
	return &SlippageGuard{provider: quoteProvider}
}

// Check verifies that current market prices are within the slippage tolerance
// of the order intent. For longs, the ask must not exceed limitPrice + tolerance.
// For shorts, the bid must not fall below limitPrice - tolerance.
func (s *SlippageGuard) Check(ctx context.Context, intent domain.OrderIntent) error {
	bid, ask, err := s.provider.GetQuote(ctx, intent.Symbol)
	if err != nil {
		return fmt.Errorf("slippage check: %w", err)
	}
	if bid == 0 && ask == 0 {
		return fmt.Errorf("zero bid/ask from quote provider for %s", intent.Symbol)
	}

	tolerance := intent.LimitPrice * float64(intent.MaxSlippageBPS) / 10000.0

	switch intent.Direction {
	case domain.DirectionLong:
		if ask > intent.LimitPrice+tolerance {
			return fmt.Errorf("slippage exceeded for %s long: ask %.2f > limit %.2f + tolerance %.2f",
				intent.Symbol, ask, intent.LimitPrice, tolerance)
		}
	case domain.DirectionShort:
		if bid < intent.LimitPrice-tolerance {
			return fmt.Errorf("slippage exceeded for %s short: bid %.2f < limit %.2f - tolerance %.2f",
				intent.Symbol, bid, intent.LimitPrice, tolerance)
		}
	}

	return nil
}
