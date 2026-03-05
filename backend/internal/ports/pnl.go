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
}
