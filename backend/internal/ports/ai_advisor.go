package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

type DebateOption func(any)

type AIAdvisorPort interface {
	RequestDebate(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...DebateOption) (*domain.AdvisoryDecision, error)
}

// StrategyPerfSetter is implemented by adapter-internal debate request carriers
// that accept strategy performance data. This allows the app layer to inject
// performance stats without importing adapter packages.
type StrategyPerfSetter interface {
	SetStrategyPerformance(summary *domain.StrategyPerformanceSummary)
}

func WithStrategyPerformance(summary *domain.StrategyPerformanceSummary) DebateOption {
	return func(raw any) {
		if setter, ok := raw.(StrategyPerfSetter); ok {
			setter.SetStrategyPerformance(summary)
		}
	}
}
