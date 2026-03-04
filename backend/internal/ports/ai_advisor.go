package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// DebateOption is a functional option for AIAdvisorPort.RequestDebate.
// It allows callers to attach optional context (e.g. option chain data) to a debate
// request without changing the base interface signature.
type DebateOption func(any)

// AIAdvisorPort defines the interface for interacting with the AI adversarial debate system.
type AIAdvisorPort interface {
	RequestDebate(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...DebateOption) (*domain.AdvisoryDecision, error)
}
