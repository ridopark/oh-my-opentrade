package noop

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// NoopPnLRepo implements ports.PnLPort as a no-op for backtest/test mode.
// LedgerWriter calls UpsertDailyPnL/SaveEquityPoint which we discard.
type NoopPnLRepo struct{}

// Compile-time assertion that NoopPnLRepo implements ports.PnLPort.
var _ ports.PnLPort = (*NoopPnLRepo)(nil)

func (n *NoopPnLRepo) UpsertDailyPnL(_ context.Context, _ domain.DailyPnL) error { return nil }

func (n *NoopPnLRepo) GetDailyPnL(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.DailyPnL, error) {
	return nil, nil
}

func (n *NoopPnLRepo) SaveEquityPoint(_ context.Context, _ domain.EquityPoint) error { return nil }

func (n *NoopPnLRepo) GetEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.EquityPoint, error) {
	return nil, nil
}

func (n *NoopPnLRepo) GetDailyRealizedPnL(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) (float64, error) {
	return 0, nil
}

func (n *NoopPnLRepo) GetBucketedEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time, _ string) ([]domain.EquityPoint, error) {
	return nil, nil
}

func (n *NoopPnLRepo) GetMaxDrawdown(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (float64, error) {
	return 0, nil
}

func (n *NoopPnLRepo) GetSharpe(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (*float64, error) {
	return nil, nil
}

func (n *NoopPnLRepo) GetSortino(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (*float64, error) {
	return nil, nil
}

func (n *NoopPnLRepo) UpsertStrategyDailyPnL(_ context.Context, _ domain.StrategyDailyPnL) error {
	return nil
}

func (n *NoopPnLRepo) GetStrategyDailyPnL(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) ([]domain.StrategyDailyPnL, error) {
	return nil, nil
}

func (n *NoopPnLRepo) SaveStrategyEquityPoint(_ context.Context, _ domain.StrategyEquityPoint) error {
	return nil
}

func (n *NoopPnLRepo) GetStrategyEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) ([]domain.StrategyEquityPoint, error) {
	return nil, nil
}

func (n *NoopPnLRepo) SaveStrategySignalEvent(_ context.Context, _ domain.StrategySignalEvent) error {
	return nil
}

func (n *NoopPnLRepo) GetStrategySignalEvents(_ context.Context, _ ports.StrategySignalQuery) (ports.StrategySignalPage, error) {
	return ports.StrategySignalPage{}, nil
}

func (n *NoopPnLRepo) GetStrategyDashboard(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) (domain.StrategyDashboard, error) {
	return domain.StrategyDashboard{}, nil
}

func (n *NoopPnLRepo) ListStrategySummaries(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.StrategySummaryRow, error) {
	return nil, nil
}

func (n *NoopPnLRepo) ListSymbolAttribution(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) ([]domain.SymbolAttribution, error) {
	return nil, nil
}
