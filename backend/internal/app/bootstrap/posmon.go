package bootstrap

import (
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

// PosMonitorDeps holds all dependencies required to build the position monitor.
type PosMonitorDeps struct {
	EventBus     ports.EventBusPort
	PositionGate *execution.PositionGate
	Broker       ports.BrokerPort       // optional — used in live mode for bootstrap reconciliation
	Repo         ports.RepositoryPort   // optional — used in live mode for bootstrap reconciliation
	SpecStore    portstrategy.SpecStore // optional — set on service if non-nil
	TenantID     string
	EnvMode      domain.EnvMode
	Clock        func() time.Time
	IsBacktest   bool
	Logger       zerolog.Logger
}

// PosMonitorBundle is the return value of BuildPositionMonitor, exposing the
// wired components that callers need to start/manage independently.
type PosMonitorBundle struct {
	PriceCache *positionmonitor.PriceCache
	Service    *positionmonitor.Service
}

// BuildPositionMonitor constructs the position monitor subsystem (PriceCache + Service).
//
// In backtest mode the tick loop and broker reconciliation are disabled so that
// exit-rule evaluation is driven explicitly per bar via Service.EvalExitRules.
//
// Revaluator is intentionally NOT created here — it requires AI risk-assessor
// and indicator-snapshot functions that are omo-core specific.
func BuildPositionMonitor(deps PosMonitorDeps) (*PosMonitorBundle, error) {
	priceCacheLog := deps.Logger.With().Str("component", "price_cache").Logger()
	priceCache := positionmonitor.NewPriceCache(priceCacheLog, positionmonitor.WithClock(deps.Clock))

	var opts []positionmonitor.Option

	if deps.IsBacktest {
		opts = append(opts,
			positionmonitor.WithDisableTickLoop(),
			positionmonitor.WithDisableReconcile(),
			positionmonitor.WithNowFunc(deps.Clock),
		)
	} else {
		if deps.Broker != nil {
			opts = append(opts, positionmonitor.WithBroker(deps.Broker))
		}
		if deps.Repo != nil {
			opts = append(opts, positionmonitor.WithRepo(deps.Repo))
		}
	}

	posMonLog := deps.Logger.With().Str("component", "position_monitor").Logger()
	svc := positionmonitor.NewService(
		deps.EventBus,
		priceCache,
		deps.PositionGate,
		deps.TenantID,
		deps.EnvMode,
		posMonLog,
		opts...,
	)

	if deps.SpecStore != nil {
		svc.SetSpecStore(deps.SpecStore)
	}

	return &PosMonitorBundle{
		PriceCache: priceCache,
		Service:    svc,
	}, nil
}
