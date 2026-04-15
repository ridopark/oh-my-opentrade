// Package bootstrap wires the canonical execution guard chain shared by
// omo-core (live/paper) and the backtest engine.
package bootstrap

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
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
	// PerStrategyMaxPositions, when non-empty, enforces a per-strategy
	// concurrent-position cap in the PortfolioGuard so multi-strategy
	// portfolios don't starve slower strategies when faster ones would
	// otherwise claim all the slots.
	PerStrategyMaxPositions map[string]int
	// IntentJournal, when non-nil, enables the Sprint 2 write-ahead journal
	// that persists OrderIntents before broker submission and stamps terminal
	// events back. Gated by OMO_ORDER_JOURNAL_ENABLED at the caller.
	IntentJournal ports.OrderIntentJournal
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
		if deps.Clock != nil {
			execOpts = append(execOpts, execution.WithNowFunc(deps.Clock))
		}
	}
	if deps.AccountPort != nil {
		bpGuard := execution.NewBuyingPowerGuard(deps.AccountPort, execLog)
		execOpts = append(execOpts, execution.WithBuyingPowerGuard(bpGuard))
	}
	if deps.BrokerName != "" {
		execOpts = append(execOpts, execution.WithBrokerName(deps.BrokerName))
	}
	if deps.IntentJournal != nil {
		execOpts = append(execOpts, execution.WithIntentJournal(deps.IntentJournal))
	}

	if cfg.Trading.MaxSimultaneousPos > 0 || cfg.Trading.MaxPositionsPerGroup > 0 || len(deps.PerStrategyMaxPositions) > 0 {
		pfGuard := execution.NewPortfolioGuard(
			func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
				return deps.Broker.GetPositions(ctx, tenantID, envMode)
			},
			cfg.Trading.MaxSimultaneousPos,
			cfg.Trading.MaxPositionsPerGroup,
			execLog,
		)
		if len(deps.PerStrategyMaxPositions) > 0 {
			pfGuard.SetPerStrategyMax(deps.PerStrategyMaxPositions)
		}
		execOpts = append(execOpts, execution.WithPortfolioGuard(pfGuard))
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

// BuildPortfolioHeat constructs the Sprint 4 portfolio-heat guard from a
// position source (typically positionmonitor.Service) and an equity
// provider. Intended to be called by callers that assemble
// gate.ExecutionGateDeps before invoking WireExecutionGateChain:
//
//	ph := bootstrap.BuildPortfolioHeat(cfg.Trading.MaxPortfolioHeat, posMon, equity, log)
//	execDeps.PortfolioHeatGuard = ph
//
// Returns nil when maxHeatPct <= 0 so callers can leave the gate field
// unset (the gate treats nil as disabled).
func BuildPortfolioHeat(maxHeatPct float64, posSource risk.PositionSource, equitySource risk.EquitySource, log zerolog.Logger) *risk.PortfolioHeat {
	if maxHeatPct <= 0 {
		return nil
	}
	return risk.NewPortfolioHeat(maxHeatPct, posSource, equitySource, log)
}

// BuildSectorExposure constructs the Sprint 4 sector/industry concentration
// guard. Returns nil when both caps are <= 0 so callers can leave the gate
// field unset (the gate treats nil as disabled). Usage mirrors
// BuildPortfolioHeat:
//
//	metadata, err := config.LoadSymbolMetadata(cfg.Trading.SymbolMetadataPath)
//	se := bootstrap.BuildSectorExposure(cfg.Trading.MaxSectorExposure,
//	    cfg.Trading.MaxIndustryExposure, metadata, posMon, equity, log)
//	execDeps.SectorExposureGuard = se
func BuildSectorExposure(
	maxSectorPct, maxIndustryPct float64,
	metadata config.SymbolMetadata,
	posSource risk.PositionSource,
	equitySource risk.EquitySource,
	log zerolog.Logger,
) *risk.SectorExposure {
	if maxSectorPct <= 0 && maxIndustryPct <= 0 {
		return nil
	}
	return risk.NewSectorExposure(maxSectorPct, maxIndustryPct, metadata, posSource, equitySource, log)
}

// BuildDirectionalBias constructs the Sprint 4 net-directional-exposure
// guard. Returns nil when maxBiasPct <= 0 so callers can leave the gate
// field unset (the gate treats nil as disabled). Usage mirrors
// BuildPortfolioHeat and BuildSectorExposure:
//
//	db := bootstrap.BuildDirectionalBias(cfg.Trading.MaxDirectionalBias,
//	    posMon, equity, log)
//	execDeps.DirectionalBiasGuard = db
func BuildDirectionalBias(maxBiasPct float64, posSource risk.PositionSource, equitySource risk.EquitySource, log zerolog.Logger) *risk.DirectionalBias {
	if maxBiasPct <= 0 {
		return nil
	}
	return risk.NewDirectionalBias(maxBiasPct, posSource, equitySource, log)
}

// BuildKillSwitch returns the DailyLossBreaker cast to the gate-facing
// KillSwitchChecker interface (via direct field assignment by the caller).
// This is a thin helper mirroring BuildPortfolioHeat et al. so bootstrap
// sites wire execDeps.KillSwitchGuard uniformly.
//
// Returns nil when breaker is nil so the kill_switch gate degrades to a
// no-op (same as the other Sprint 4 guards).
func BuildKillSwitch(breaker *risk.DailyLossBreaker) *risk.DailyLossBreaker {
	if breaker == nil {
		return nil
	}
	return breaker
}

// WarnMissingSymbolMetadata emits a WARN log for every active symbol absent
// from the loaded metadata table. These symbols will fail-open through the
// sector_exposure gate — operators need to know so they can backfill the
// TOML before relying on the cap.
func WarnMissingSymbolMetadata(metadata config.SymbolMetadata, activeSymbols []string, log zerolog.Logger) {
	if metadata == nil || len(activeSymbols) == 0 {
		return
	}
	missing := metadata.MissingSymbols(activeSymbols)
	if len(missing) == 0 {
		return
	}
	log.Warn().
		Strs("symbols", missing).
		Int("count", len(missing)).
		Msg("sector_exposure: active symbols missing from metadata table; gate will fail-open for these")
}
