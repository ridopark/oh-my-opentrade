package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
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
	"github.com/oh-my-opentrade/backend/internal/domain"
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
	Importer         *optionsimport.DoltHubService
	AIAdvisor        ports.AIAdvisorPort
}

// BacktestInfraOptions holds optional knobs for BuildBacktestInfra. Zero values
// give realistic fills (entry half-spread on); pass an explicit &false to
// OptionEntrySpreadEnabled to reproduce legacy byte-identical backtests.
type BacktestInfraOptions struct {
	OptionExitSpreadMultiplier float64 // 0 => 1.0 (no scaling)
	OptionEntrySpreadEnabled   *bool   // nil => default true (spread applied); &false => legacy mid-fill

	// Tier 1 market-impact knobs. Both zero (default) is byte-identical to
	// today: helper short-circuits, port is never constructed. BacktestFrom/To
	// frame the [from, to] window the bar-volume adapter pre-fetches per OCC
	// on first touch.
	OptionImpactScaleBps      float64
	OptionMaxParticipationPct float64
	BacktestFrom              time.Time
	BacktestTo                time.Time
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
	importer := optionsimport.NewDoltHubService(dolthubClient, histOptRepo, log)

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
		SlippageBPS:                slippageBPS,
		InitialEquity:              initialEquity,
		DisableFillChan:            true,
		VIXIVBeta:                  0.7, // large-cap equity default
		TODSeasonalEnabled:         true,
		EarningsRampEnabled:        true,
		MoveCrushEnabled:           true,
		MoveCrushCallK:             0.6, // calls crush harder on underlying moves
		MoveCrushPutK:              0.4, // puts crush less due to supportive skew
		MoveCrushFloor:             0.5, // cap crush at 50% of entry IV
		OptionExitSpreadMultiplier: opt.OptionExitSpreadMultiplier,
		OptionEntrySpreadEnabled:   opt.OptionEntrySpreadEnabled == nil || *opt.OptionEntrySpreadEnabled,
		OptionImpactScaleBps:       opt.OptionImpactScaleBps,
		OptionMaxParticipationPct:  opt.OptionMaxParticipationPct,
		FillModel:                  fillModel,
		FeeSchedule:                feeSchedule,
		LatencyMsEq:                btCfg.LatencyMsEquity,
		LatencyMsOpt:               btCfg.LatencyMsOption,
	}, log.With().Str("component", "simbroker").Logger())

	// Wire the OCC option-expiry sweep so SimBroker can publish synthetic
	// SELL FillReceived events when contracts cross their expiry session
	// close. SimBroker stays decoupled from the bus; the callback bridges
	// to PublishDirect. FreezeHandlers is invoked by the runner before the
	// replay loop reaches the first ExpireOptions call, so PublishDirect
	// is safe at invocation time.
	expiryLog := log.With().Str("component", "simbroker_expiry").Logger()
	sim.SetOnExpiryFill(func(payload map[string]any) {
		symStr, _ := payload["symbol"].(string)
		filledAt, _ := payload["filled_at"].(time.Time)
		idem := fmt.Sprintf("option-expiry:%s:%d", symStr, filledAt.UnixNano())
		evt := domain.NewBacktestEvent(
			domain.EventFillReceived,
			"default",
			domain.EnvModePaper,
			idem,
			payload,
			filledAt,
		)
		if err := bus.PublishDirect(context.Background(), evt); err != nil {
			expiryLog.Warn().Err(err).Str("symbol", symStr).Msg("option-expiry FillReceived publish failed")
		}
	})

	// Tier 1 market-impact: lazy-construct the bar-volume adapter ONLY when
	// either knob is non-zero. Zero+zero leaves sim.optionBarVolume == nil so
	// the helper short-circuits. The adapter is bound to [BacktestFrom,
	// BacktestTo] and pre-fetches one bar series per OCC on first touch via
	// the existing Alpaca options-bars REST endpoint.
	if (opt.OptionImpactScaleBps > 0 || opt.OptionMaxParticipationPct > 0) && !opt.BacktestFrom.IsZero() && !opt.BacktestTo.IsZero() {
		if deps.AppCfg.Alpaca.APIKeyID == "" {
			log.Warn().Msg("option market-impact knobs ON but Alpaca credentials missing — bar-volume lookup disabled, helper no-ops")
		} else {
			alpacaAdapt, alpacaErr := alpaca.NewAdapter(deps.AppCfg.Alpaca, log.With().Str("component", "alpaca_bv").Logger())
			if alpacaErr != nil {
				log.Warn().Err(alpacaErr).Msg("option market-impact knobs ON but alpaca adapter failed — bar-volume helper no-ops")
			} else {
				barVol := simbroker.NewOptionBarVolumeAdapter(alpacaAdapt, opt.BacktestFrom, opt.BacktestTo, log)
				sim.SetOptionBarVolume(barVol)
				log.Info().
					Float64("scale_bps", opt.OptionImpactScaleBps).
					Float64("max_part_pct", opt.OptionMaxParticipationPct).
					Time("from", opt.BacktestFrom).
					Time("to", opt.BacktestTo).
					Msg("option market-impact knobs ON — bar-volume adapter wired")
			}
		}
	}

	earningsRepo := timescaledb.NewEarningsRepo(deps.DB, log.With().Str("component", "earnings_repo").Logger())

	var aiAdvisor ports.AIAdvisorPort = llm.NewNoOpAdvisor()
	if !noAI && deps.AppCfg.AI.Enabled {
		model := deps.AppCfg.AI.BacktestModel
		if model == "" {
			model = deps.AppCfg.AI.Model
		}
		aiAdvisor = llm.NewAdvisor(deps.AppCfg.AI.BaseURL, model, deps.AppCfg.AI.APIKey, nil)
		log.Info().Str("model", model).Msg("backtest AI advisor using model")
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
