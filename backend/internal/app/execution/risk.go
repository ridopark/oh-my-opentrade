package execution

import (
	"fmt"
	"math"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// RiskEngine validates that an order intent's potential loss
// does not exceed the configured maximum risk percentage of account equity.
type RiskEngine struct {
	maxRiskPct float64
}

// NewRiskEngine creates a RiskEngine with the given maximum risk percentage.
// For example, 0.02 means 2% of account equity.
func NewRiskEngine(maxRiskPct float64) *RiskEngine {
	return &RiskEngine{maxRiskPct: maxRiskPct}
}

// Validate checks that the order intent satisfies risk constraints.
// It verifies stop loss, limit price, and quantity are positive,
// then ensures the dollar risk does not exceed maxRiskPct * accountEquity.
func (r *RiskEngine) Validate(intent domain.OrderIntent, accountEquity float64) error {
	if intent.StopLoss <= 0 {
		return fmt.Errorf("stop loss must be > 0, got %g", intent.StopLoss)
	}
	if intent.LimitPrice <= 0 {
		return fmt.Errorf("limit price must be > 0, got %g", intent.LimitPrice)
	}
	if intent.Quantity <= 0 {
		return fmt.Errorf("quantity must be > 0, got %g", intent.Quantity)
	}

	risk := math.Abs(intent.LimitPrice-intent.StopLoss) * intent.Quantity
	maxAllowed := r.maxRiskPct * accountEquity

	if risk > maxAllowed {
		return fmt.Errorf("risk %.2f exceeds maximum risk %.2f (%.1f%% of %.2f equity)",
			risk, maxAllowed, r.maxRiskPct*100, accountEquity)
	}

	return nil
}
