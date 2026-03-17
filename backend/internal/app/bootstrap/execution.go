// Package bootstrap wires the canonical execution guard chain shared by
// omo-core (live/paper) and the backtest engine.
package bootstrap

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// ExecutionDeps holds all dependencies needed to build the execution guard chain.
type ExecutionDeps struct {
	EventBus      ports.EventBusPort
	Broker        ports.BrokerPort
	OrderStream   ports.OrderStreamPort // nil = poll fallback; set for SimBroker stream fills
	Repo          ports.RepositoryPort
	QuoteProvider execution.QuoteProvider // bid/ask for SlippageGuard
	AccountPort   ports.AccountPort       // nil = skip BuyingPowerGuard
	PnLRepo       ports.PnLPort
	TradeReader   perf.TradeReaderPort // nil OK for backtest
	Clock         func() time.Time
	Config        *config.Config
	InitialEquity float64
	IsBacktest    bool
	EnableOptions bool
	BrokerName    string
	Logger        zerolog.Logger
}

// ExecutionBundle is returned by BuildExecutionService with all wired components.
type ExecutionBundle struct {
	Service          *execution.Service
	PositionGate     *execution.PositionGate
	LedgerWriter     *perf.LedgerWriter
	DailyLossBreaker *risk.DailyLossBreaker
}

// BuildExecutionService produces the identical guard chain as omo-core's initCoreServices().
func BuildExecutionService(deps ExecutionDeps) (*ExecutionBundle, error) {
	execLog := deps.Logger.With().Str("component", "execution").Logger()
	ledgerLog := deps.Logger.With().Str("component", "ledger").Logger()
	breakerLog := deps.Logger.With().Str("component", "daily_loss_breaker").Logger()

	cfg := deps.Config

	riskEngine := execution.NewRiskEngine(cfg.Trading.MaxRiskPercent)
	slippageGuard := execution.NewSlippageGuard(deps.QuoteProvider)
	killSwitch := execution.NewKillSwitch(
		cfg.Trading.KillSwitchMaxStops,
		cfg.Trading.KillSwitchWindow,
		cfg.Trading.KillSwitchHaltDuration,
		deps.Clock,
	)

	ledgerWriter := perf.NewLedgerWriter(
		deps.EventBus,
		deps.PnLRepo,
		deps.Broker,
		deps.TradeReader,
		deps.InitialEquity,
		ledgerLog,
	)

	dailyLossBreaker := risk.NewDailyLossBreaker(
		cfg.Trading.MaxDailyLossPct/100.0,
		cfg.Trading.MaxDailyLossUSD,
		ledgerWriter,
		deps.Clock,
		breakerLog,
	)

	posGate := execution.NewPositionGate(deps.Broker, execLog)

	execOpts := []execution.Option{
		execution.WithPositionGate(posGate),
		execution.WithExposureGuard(execution.NewExposureGuard(deps.Broker, deps.InitialEquity, execLog)),
		execution.WithSpreadGuard(execution.NewSpreadGuard(deps.QuoteProvider, execLog)),
		execution.WithTradingWindowGuard(execution.NewTradingWindowGuardWithClock(deps.Clock, execLog)),
	}
	if deps.OrderStream != nil {
		execOpts = append(execOpts, execution.WithOrderStream(deps.OrderStream))
	}
	if deps.IsBacktest {
		execOpts = append(execOpts, execution.WithSyncFill())
	}
	if deps.AccountPort != nil {
		bpGuard := execution.NewBuyingPowerGuard(deps.AccountPort, execLog)
		execOpts = append(execOpts, execution.WithBuyingPowerGuard(bpGuard))
	}
	if deps.BrokerName != "" {
		execOpts = append(execOpts, execution.WithBrokerName(deps.BrokerName))
	}

	if deps.EnableOptions {
		optsCfg := cfg.Trading.OptionsRisk
		ore := execution.NewOptionsRiskEngine(
			cfg.Trading.MaxRiskPercent/100.0,
			optsCfg.MinOpenInterest,
			optsCfg.MaxSpreadPct,
			optsCfg.MaxIVCeiling,
			optsCfg.MinDTE,
			deps.Clock,
		)
		execOpts = append(execOpts, execution.WithOptionsRiskEngine(ore))
	}

	svc := execution.NewService(
		deps.EventBus,
		deps.Broker,
		deps.Repo,
		riskEngine,
		slippageGuard,
		killSwitch,
		dailyLossBreaker,
		deps.InitialEquity,
		execLog,
		execOpts...,
	)

	return &ExecutionBundle{
		Service:          svc,
		PositionGate:     posGate,
		LedgerWriter:     ledgerWriter,
		DailyLossBreaker: dailyLossBreaker,
	}, nil
}
