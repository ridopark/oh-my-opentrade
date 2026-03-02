package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// AIAdvisorPort defines the interface for interacting with the AI adversarial debate system.
type AIAdvisorPort interface {
	RequestDebate(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot) (*domain.AdvisoryDecision, error)
}
