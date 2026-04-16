package execution

import (
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// GroupSizer validates that a group of paired orders doesn't exceed
// aggregate notional limits.
type GroupSizer struct {
	maxGroupNotionalUSD float64
}

// NewGroupSizer creates a GroupSizer with the given notional cap.
// A zero or negative cap disables validation (always passes).
func NewGroupSizer(maxGroupNotionalUSD float64) *GroupSizer {
	return &GroupSizer{maxGroupNotionalUSD: maxGroupNotionalUSD}
}

// Validate checks that the total notional of all legs doesn't exceed the cap.
// prices maps each symbol to its current USD price; missing symbols cause an
// error. Returns nil when the cap is zero (disabled).
func (g *GroupSizer) Validate(intents []domain.OrderIntent, prices map[domain.Symbol]float64) error {
	if g.maxGroupNotionalUSD <= 0 {
		return nil // validation disabled
	}
	if len(intents) == 0 {
		return nil
	}

	var total float64
	for i, intent := range intents {
		price, ok := prices[intent.Symbol]
		if !ok {
			return fmt.Errorf("group_sizer: no price for symbol %s (leg %d)", intent.Symbol, i)
		}
		total += price * intent.Quantity
	}

	if total > g.maxGroupNotionalUSD {
		return fmt.Errorf("group_sizer: aggregate notional $%.2f exceeds cap $%.2f", total, g.maxGroupNotionalUSD)
	}
	return nil
}
