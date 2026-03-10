package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// PnLPort defines the interface for P&L persistence operations.
type PnLPort interface {
	// UpsertDailyPnL inserts or updates the daily P&L record for a given date.
	UpsertDailyPnL(ctx context.Context, pnl domain.DailyPnL) error

	// GetDailyPnL retrieves daily P&L records for a tenant within a date range.
	GetDailyPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.DailyPnL, error)

	// SaveEquityPoint appends a point to the equity curve.
	SaveEquityPoint(ctx context.Context, pt domain.EquityPoint) error

	// GetEquityCurve retrieves equity curve points for a tenant within a time range.
	GetEquityCurve(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.EquityPoint, error)

	// GetDailyRealizedPnL returns the cumulative realized P&L for the current day.
	// This is the hot path used by the circuit breaker on every order.
	GetDailyRealizedPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, date time.Time) (float64, error)

	// GetBucketedEquityCurve returns equity points bucketed by the given interval.
	// Uses TimescaleDB time_bucket for efficient downsampling.
	GetBucketedEquityCurve(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time, bucket string) ([]domain.EquityPoint, error)

	// GetMaxDrawdown returns the worst drawdown value in the given time range.
	GetMaxDrawdown(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) (float64, error)

	// GetSharpe computes the annualized Sharpe ratio from daily equity returns.
	// Returns nil if insufficient data (< 2 days or zero stdev).
	GetSharpe(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) (*float64, error)

	// GetSortino computes the annualized Sortino ratio from daily equity returns.
	// Returns nil if insufficient data or zero downside deviation.
	GetSortino(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) (*float64, error)

	// --- Per-strategy performance methods ---

	// UpsertStrategyDailyPnL inserts or updates the per-strategy daily P&L record.
	UpsertStrategyDailyPnL(ctx context.Context, pnl domain.StrategyDailyPnL) error

	// GetStrategyDailyPnL retrieves daily P&L records for a specific strategy within a date range.
	GetStrategyDailyPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, strategy string, from, to time.Time) ([]domain.StrategyDailyPnL, error)

	// SaveStrategyEquityPoint appends a point to the per-strategy equity curve.
	SaveStrategyEquityPoint(ctx context.Context, pt domain.StrategyEquityPoint) error

	// GetStrategyEquityCurve retrieves per-strategy equity curve points within a time range.
	GetStrategyEquityCurve(ctx context.Context, tenantID string, envMode domain.EnvMode, strategy string, from, to time.Time) ([]domain.StrategyEquityPoint, error)

	// SaveStrategySignalEvent appends a signal lifecycle event.
	SaveStrategySignalEvent(ctx context.Context, evt domain.StrategySignalEvent) error

	// GetStrategySignalEvents retrieves signal events with optional filters.
	GetStrategySignalEvents(ctx context.Context, q StrategySignalQuery) (StrategySignalPage, error)

	// GetStrategyDashboard computes an aggregated dashboard for a strategy within a date range.
	GetStrategyDashboard(ctx context.Context, tenantID string, envMode domain.EnvMode, strategy string, from, to time.Time) (domain.StrategyDashboard, error)
}

// StrategySignalQuery defines filter and pagination for strategy signal events.
type StrategySignalQuery struct {
	TenantID   string
	EnvMode    domain.EnvMode
	Strategy   string
	Symbol     string // optional filter
	From       time.Time
	To         time.Time
	Limit      int
	CursorTime *time.Time // keyset cursor: events before this time
	CursorID   string     // keyset cursor: signal_id at cursor time
}

// StrategySignalPage is a paginated result set of strategy signal events.
type StrategySignalPage struct {
	Items      []domain.StrategySignalEvent
	NextCursor string // opaque cursor for next page, empty if no more
}
