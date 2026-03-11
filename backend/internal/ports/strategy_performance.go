package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// StrategyPerformancePort provides aggregate strategy performance statistics
// for debate enrichment. Implementations query historical trade data.
type StrategyPerformancePort interface {
	// GetPerformanceSummary returns aggregate stats for a strategy+symbol
	// over the given lookback period. Returns nil (not error) when no data exists.
	// The returned summary includes overall stats and (when available) per-regime breakdown.
	GetPerformanceSummary(
		ctx context.Context,
		tenantID string,
		envMode domain.EnvMode,
		strategy string,
		symbol string,
		lookback time.Duration,
	) (*domain.StrategyPerformanceSummary, error)
}
