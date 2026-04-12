package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

// DecayTelemetryPort defines persistence operations for strategy decay
// telemetry: per-trade stats insertion and rolling/attribution queries.
type DecayTelemetryPort interface {
	// InsertTradeStats records a single trade's stats for decay analysis.
	InsertTradeStats(ctx context.Context, tradeID uuid.UUID, strategy, symbol string, pnl float64, regime, vixBucket string) error

	// GetRollingDecayStats computes rolling PF/WR over trailing windows for
	// all trades belonging to the given strategy, ordered by insertion time.
	GetRollingDecayStats(ctx context.Context, strategy string) ([]domain.RollingDecayPoint, error)

	// GetComponentAttribution computes PF-with vs PF-without for each
	// confluence component by joining strategy_trade_stats with the
	// trades.thesis JSONB column.
	GetComponentAttribution(ctx context.Context, strategy string) ([]domain.ComponentAttribution, error)
}
