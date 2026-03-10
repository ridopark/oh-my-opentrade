package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type stubQuoteProvider struct{}

func (stubQuoteProvider) GetQuote(_ context.Context, _ domain.Symbol) (float64, float64, error) {
	return 100.0, 100.05, nil
}

type stubPnLRepo struct{}

func (stubPnLRepo) UpsertDailyPnL(context.Context, domain.DailyPnL) error { return nil }
func (stubPnLRepo) GetDailyPnL(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.DailyPnL, error) {
	return nil, nil
}
func (stubPnLRepo) SaveEquityPoint(context.Context, domain.EquityPoint) error { return nil }
func (stubPnLRepo) GetEquityCurve(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.EquityPoint, error) {
	return nil, nil
}
func (stubPnLRepo) GetDailyRealizedPnL(context.Context, string, domain.EnvMode, time.Time) (float64, error) {
	return 0, nil
}
func (stubPnLRepo) GetBucketedEquityCurve(context.Context, string, domain.EnvMode, time.Time, time.Time, string) ([]domain.EquityPoint, error) {
	return nil, nil
}
func (stubPnLRepo) GetMaxDrawdown(context.Context, string, domain.EnvMode, time.Time, time.Time) (float64, error) {
	return 0, nil
}
func (stubPnLRepo) GetSharpe(context.Context, string, domain.EnvMode, time.Time, time.Time) (*float64, error) {
	return nil, nil
}
func (stubPnLRepo) GetSortino(context.Context, string, domain.EnvMode, time.Time, time.Time) (*float64, error) {
	return nil, nil
}
func (stubPnLRepo) ListStrategySummaries(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.StrategySummaryRow, error) {
	return nil, nil
}
func (stubPnLRepo) ListSymbolAttribution(context.Context, string, domain.EnvMode, string, time.Time, time.Time) ([]domain.SymbolAttribution, error) {
	return nil, nil
}
func (stubPnLRepo) UpsertStrategyDailyPnL(context.Context, domain.StrategyDailyPnL) error {
	return nil
}
func (stubPnLRepo) GetStrategyDailyPnL(context.Context, string, domain.EnvMode, string, time.Time, time.Time) ([]domain.StrategyDailyPnL, error) {
	return nil, nil
}
func (stubPnLRepo) SaveStrategyEquityPoint(context.Context, domain.StrategyEquityPoint) error {
	return nil
}
func (stubPnLRepo) GetStrategyEquityCurve(context.Context, string, domain.EnvMode, string, time.Time, time.Time) ([]domain.StrategyEquityPoint, error) {
	return nil, nil
}
func (stubPnLRepo) SaveStrategySignalEvent(context.Context, domain.StrategySignalEvent) error {
	return nil
}
func (stubPnLRepo) GetStrategySignalEvents(context.Context, ports.StrategySignalQuery) (ports.StrategySignalPage, error) {
	return ports.StrategySignalPage{}, nil
}
func (stubPnLRepo) GetStrategyDashboard(context.Context, string, domain.EnvMode, string, time.Time, time.Time) (domain.StrategyDashboard, error) {
	return domain.StrategyDashboard{}, nil
}

type stubAccountPort struct{}

func (stubAccountPort) GetAccountBuyingPower(context.Context) (ports.BuyingPower, error) {
	return ports.BuyingPower{EffectiveBuyingPower: 50000}, nil
}

func testConfig() *config.Config {
	return &config.Config{
		Trading: config.TradingConfig{
			MaxRiskPercent:         0.02,
			KillSwitchMaxStops:     3,
			KillSwitchWindow:       2 * time.Minute,
			KillSwitchHaltDuration: 15 * time.Minute,
			MaxDailyLossPct:        5.0,
			MaxDailyLossUSD:        5000,
		},
	}
}

func testDeps() ExecutionDeps {
	return ExecutionDeps{
		EventBus:      stubEventBus{},
		Broker:        stubBroker{},
		Repo:          stubRepo{},
		QuoteProvider: stubQuoteProvider{},
		AccountPort:   stubAccountPort{},
		PnLRepo:       stubPnLRepo{},
		TradeReader:   nil,
		Clock:         time.Now,
		Config:        testConfig(),
		InitialEquity: 100000.0,
		Logger:        zerolog.Nop(),
	}
}

func TestBuildExecutionService_FullChain(t *testing.T) {
	deps := testDeps()

	bundle, err := BuildExecutionService(deps)
	if err != nil {
		t.Fatalf("BuildExecutionService returned error: %v", err)
	}
	if bundle.Service == nil {
		t.Fatal("Service is nil")
	}
	if bundle.PositionGate == nil {
		t.Fatal("PositionGate is nil")
	}
	if bundle.LedgerWriter == nil {
		t.Fatal("LedgerWriter is nil")
	}
	if bundle.DailyLossBreaker == nil {
		t.Fatal("DailyLossBreaker is nil")
	}
}

func TestBuildExecutionService_NilAccount(t *testing.T) {
	deps := testDeps()
	deps.AccountPort = nil

	bundle, err := BuildExecutionService(deps)
	if err != nil {
		t.Fatalf("BuildExecutionService returned error: %v", err)
	}
	if bundle.Service == nil {
		t.Fatal("Service is nil")
	}
	if bundle.PositionGate == nil {
		t.Fatal("PositionGate is nil")
	}
	if bundle.LedgerWriter == nil {
		t.Fatal("LedgerWriter is nil")
	}
	if bundle.DailyLossBreaker == nil {
		t.Fatal("DailyLossBreaker is nil")
	}
}
