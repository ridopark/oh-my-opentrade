package bootstrap

import (
	"database/sql"

	"github.com/oh-my-opentrade/backend/internal/adapters/dolthub"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/adapters/llm"
	"github.com/oh-my-opentrade/backend/internal/adapters/noop"
	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/optionsimport"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/ports"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

// BacktestDeps are the inputs for constructing backtest infrastructure.
type BacktestDeps struct {
	DB     *sql.DB
	AppCfg *config.Config
	Logger zerolog.Logger
}

// BacktestInfra holds the adapter instances for a single backtest run.
type BacktestInfra struct {
	DB          *sql.DB // raw DB handle for components that need it (session resolver)
	EventBus    ports.BacktestBus
	Repo        *timescaledb.Repository // concrete type — backtest needs FindDataGaps/SaveMarketBars
	NoopRepo    ports.RepositoryPort
	NoopPnLRepo ports.PnLPort
	SimBroker        *simbroker.Broker
	HistOptRepo      ports.HistoricalOptionsPort
	EarningsCalendar ports.EarningsCalendarPort
	Importer         *optionsimport.Service
	AIAdvisor        ports.AIAdvisorPort
}

// BacktestInfraOptions holds optional knobs for BuildBacktestInfra. Zero values
// preserve prior behavior so existing callers need not set them.
type BacktestInfraOptions struct {
	OptionExitSpreadMultiplier float64 // 0 => 1.0 (no scaling)
	OptionEntrySpreadEnabled   bool    // false => entries skip option half-spread (legacy)
}

// BuildBacktestInfra constructs all adapter-layer dependencies for a backtest.
// This keeps adapter imports out of the app/backtest package.
func BuildBacktestInfra(deps BacktestDeps, slippageBPS int64, initialEquity float64, noAI bool, opts ...BacktestInfraOptions) BacktestInfra {
	var opt BacktestInfraOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	log := deps.Logger

	bus := memory.NewSyncBus()

	dbAdapter := timescaledb.NewSqlDB(deps.DB)
	repo := timescaledb.NewRepositoryWithLogger(dbAdapter, log.With().Str("component", "timescaledb").Logger())
	histOptRepo := timescaledb.NewHistoricalOptionsRepository(dbAdapter, log.With().Str("component", "hist_options").Logger())

	dolthubClient := dolthub.NewClient(nil, log)
	importer := optionsimport.NewService(dolthubClient, histOptRepo, log)

	// Sprint 7 wiring: resolve fill model + fee schedule from the YAML-backed
	// BacktestConfig. applyBacktestDefaults guarantees non-empty names, but
	// if resolution fails (unknown name) we log and fall back to optimistic +
	// no fees rather than aborting startup — a bad YAML value must not
	// silently trade live behavior for zero fills.
	btCfg := deps.AppCfg.Backtest
	fillModel, err := simbroker.FillModelByName(btCfg.FillModel, btCfg.PessimisticSlippageMultiplier)
	if err != nil {
		log.Error().Err(err).Str("fill_model", btCfg.FillModel).Msg("unknown fill model; falling back to optimistic")
		fillModel = simbroker.OptimisticFillModel{}
	}
	feeSchedule, err := simbroker.FeeScheduleByName(btCfg.FeeSchedule)
	if err != nil {
		log.Error().Err(err).Str("fee_schedule", btCfg.FeeSchedule).Msg("unknown fee schedule; falling back to NoFees")
		feeSchedule = simbroker.NoFees{}
	}

	sim := simbroker.New(simbroker.Config{
		SlippageBPS:         slippageBPS,
		InitialEquity:       initialEquity,
		DisableFillChan:     true,
		VIXIVBeta:           0.7,  // large-cap equity default
		TODSeasonalEnabled:  true,
		EarningsRampEnabled: true,
		OptionExitSpreadMultiplier: opt.OptionExitSpreadMultiplier,
		OptionEntrySpreadEnabled:   opt.OptionEntrySpreadEnabled,
		FillModel:    fillModel,
		FeeSchedule:  feeSchedule,
		LatencyMsEq:  btCfg.LatencyMsEquity,
		LatencyMsOpt: btCfg.LatencyMsOption,
	}, log.With().Str("component", "simbroker").Logger())

	earningsRepo := timescaledb.NewEarningsRepo(deps.DB, log.With().Str("component", "earnings_repo").Logger())

	var aiAdvisor ports.AIAdvisorPort = llm.NewNoOpAdvisor()
	if !noAI && deps.AppCfg.AI.Enabled {
		aiAdvisor = llm.NewAdvisor(deps.AppCfg.AI.BaseURL, deps.AppCfg.AI.Model, deps.AppCfg.AI.APIKey, nil)
	}

	return BacktestInfra{
		DB:          deps.DB,
		EventBus:    bus,
		Repo:        repo,
		NoopRepo:    &noop.NoopRepo{},
		NoopPnLRepo: &noop.NoopPnLRepo{},
		SimBroker:        sim,
		HistOptRepo:      histOptRepo,
		EarningsCalendar: earningsRepo,
		Importer:         importer,
		AIAdvisor:   aiAdvisor,
	}
}

// NewBacktestSpecStore creates a filesystem-based spec store for backtests.
func NewBacktestSpecStore(dir string) portstrategy.SpecStore {
	return store_fs.NewStore(dir, strategy.LoadSpecFile)
}
