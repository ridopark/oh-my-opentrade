package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/gate"
	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	stratports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

// StrategyDeps holds all dependencies required to build the strategy pipeline.
type StrategyDeps struct {
	EventBus        ports.EventBusPort
	SpecStore       stratports.SpecStore
	AIAdvisor       ports.AIAdvisorPort
	PositionLookup  func(symbol string) (domain.MonitoredPosition, bool)
	MarketDataFn    func(symbol string) (domain.IndicatorSnapshot, bool)
	NewsProvider    strategy.NewsProvider
	Repo            ports.RepositoryPort
	StratPerf       ports.StrategyPerformancePort
	OptionsMarket   ports.OptionsMarketDataPort
	TenantID        string
	EnvMode         domain.EnvMode
	Equity          float64
	Clock           func() time.Time
	// DisableAI disables the LLM debate but keeps the enricher in the
	// pipeline. When true, the enricher emits pass-through SignalEnriched
	// events using the original signal's strength as confidence, so the
	// risk sizer still fires. This is the setting backtests want when
	// they pass no_ai=true: "run without LLM", not "kill the pipeline".
	DisableAI  bool
	Logger     zerolog.Logger
	BacktestID string // non-empty → tag slog output with backtest_id
	// TideTracker, when non-nil, is wired into the strategy runner so AVWAP
	// entry signals can be tagged with SPY/QQQ intraday-VWAP deviation for
	// retrospective telemetry analysis. Optional.
	TideTracker *gate.IndexTideTracker
	// Notifier, when non-nil, is wired into the strategy runner so recovered
	// OnBar/WarmupOnBar panics emit a Discord/Telegram alert in addition to
	// the log entry and prometheus counter. Optional: without this the runner
	// still recovers and logs panics — only the operator-facing alert is lost.
	Notifier ports.NotifierPort
	// PnLRepo, when non-nil, lets sentinel-routed strategies (e.g. copytrade)
	// replay their own persisted strategy_signal_events on startup to
	// rehydrate in-memory per-author state. Optional; absent repo skips the
	// bootstrap hook.
	PnLRepo ports.PnLPort
	// GetPositionsFn, when non-nil, is called during sentinel-strategy
	// bootstrap so replayed state can be cross-referenced against the
	// broker's current view and phantom positions dropped. Wraps
	// ports.BrokerPort.GetPositions at the caller site.
	GetPositionsFn func(ctx context.Context) ([]domain.Trade, error)
	// Indicator is the indicator.Service injected into the strategy
	// runner. The runner's HTF state lives here so live and backtest
	// share the same seeded calc per shard.
	Indicator *indicator.Service
}

// StrategyPipeline is the return value of BuildStrategyPipeline, exposing the
// wired components that callers need to start/manage independently.
type StrategyPipeline struct {
	Runner       *strategy.Runner
	Router       *strategy.Router
	Enricher     *strategy.SignalDebateEnricher // always constructed; runs in skip-AI mode when DisableAI=true
	RiskSizer    *strategy.RiskSizer
	LifecycleSvc *strategy.LifecycleService
	BaseSymbols  []string
	Activator    *PipelineActivator
}

// StrategyShared holds the parts of the strategy v2 pipeline that are
// constructed once per backtest run and reused across all worker shards:
// the builtin strategy registry, the loaded spec list, the clock closure,
// and the post-signal services that subscribe to the shared event bus
// (SignalDebateEnricher, RiskSizer). Phase 2 per-tick sharding builds one
// of these, then builds N per-shard Runner+Router pairs via
// BuildStrategyShard. Single-pipeline callers (legacy path, tests,
// dashboard runner) go through BuildStrategyPipeline which wraps both.
type StrategyShared struct {
	Registry  *strategy.MemRegistry
	Specs     []stratports.Spec
	Enricher  *strategy.SignalDebateEnricher // always constructed; runs in skip-AI mode when DisableAI=true
	RiskSizer *strategy.RiskSizer
	Clock     func() time.Time
	Logger    *slog.Logger
}

// StrategyShard is a per-shard (Router, Runner) pair plus the subset of
// symbols that shard actually owns. Instances are only registered on the
// shard's Router when their declared symbol appears in the slab passed to
// BuildStrategyShard — empty slab means "no symbols restriction" and is
// how BuildStrategyPipeline gets the full legacy behavior.
type StrategyShard struct {
	Router      *strategy.Router
	Runner      *strategy.Runner
	BaseSymbols []string
}

// BuildStrategyShared constructs the shared portion of the strategy v2
// pipeline. Safe to call once per backtest run — the returned enricher and
// risk sizer are stateless-per-bar subscribers on the shared event bus and
// can service events from any shard.
func BuildStrategyShared(deps StrategyDeps) (*StrategyShared, error) {
	stratLog := slog.Default()
	if deps.BacktestID != "" {
		stratLog = stratLog.With("backtest_id", deps.BacktestID)
	}

	registry := strategy.NewMemRegistry()
	for _, s := range []start.Strategy{
		builtin.NewORBStrategy(),
		builtin.NewAVWAPStrategy(),
		builtin.NewAIScalperStrategy(),
		builtin.NewBreakRetestStrategy(),
		builtin.NewMACDStrategy(),
		builtin.NewOvernightZStrategy(),
		builtin.NewCryptoTSMStrategy(),
		builtin.NewCryptoRevertStrategy(),
		builtin.NewCopytradeStrategy(),
	} {
		if err := registry.Register(s); err != nil {
			return nil, fmt.Errorf("bootstrap: strategy: failed to register builtin %s: %w", s.Meta().ID, err)
		}
	}

	allSpecs, err := deps.SpecStore.List(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: strategy: failed to list specs: %w", err)
	}
	if len(allSpecs) == 0 {
		return nil, fmt.Errorf("bootstrap: strategy: no strategy specs found")
	}

	clockFn := deps.Clock
	if clockFn == nil {
		clockFn = time.Now
	}

	// Enricher is always constructed. When DisableAI=true it runs in
	// skip-AI mode — it still emits SignalEnriched events (using the
	// original signal's strength as confidence) but bypasses the LLM
	// round-trip. Prior behavior skipped the whole enricher, which broke
	// the risk_sizer subscription chain and produced empty backtest
	// results; see the 2026-04-17 no_ai=true bug.
	var opts []strategy.EnricherOption
	if deps.Repo != nil {
		opts = append(opts, strategy.WithRepository(deps.Repo))
	}
	if deps.MarketDataFn != nil {
		opts = append(opts, strategy.WithMarketDataProvider(deps.MarketDataFn))
	}
	if deps.PositionLookup != nil {
		opts = append(opts, strategy.WithPositionLookup(deps.PositionLookup))
	}
	if deps.StratPerf != nil {
		opts = append(opts, strategy.WithStrategyPerformance(deps.StratPerf))
	}
	if deps.NewsProvider != nil {
		opts = append(opts, strategy.WithNewsProvider(deps.NewsProvider))
	}
	opts = append(opts, strategy.WithDebateTimeout(30*time.Second), strategy.WithSkipAI(deps.DisableAI))
	enricher := strategy.NewSignalDebateEnricher(deps.EventBus, deps.AIAdvisor, stratLog, opts...)

	riskSizer := strategy.NewRiskSizer(deps.EventBus, deps.SpecStore, deps.Equity, stratLog)
	if deps.OptionsMarket != nil {
		riskSizer.SetOptionsMarket(deps.OptionsMarket)
	}

	return &StrategyShared{
		Registry:  registry,
		Specs:     allSpecs,
		Enricher:  enricher,
		RiskSizer: riskSizer,
		Clock:     clockFn,
		Logger:    stratLog,
	}, nil
}

// BuildStrategyShard constructs a per-shard Router+Runner using the shared
// services. Instances are registered only for symbols that appear in slab;
// passing an empty slab disables the filter and registers every
// spec×symbol instance (legacy single-pipeline behavior).
func BuildStrategyShard(shared *StrategyShared, slab []domain.Symbol, deps StrategyDeps) (*StrategyShard, error) {
	return buildStrategyShard(shared, slab, deps, false)
}

// BuildStrategyShardWithSentinels is BuildStrategyShard but also registers
// sentinel-routed strategies (symbols shaped like __name__) that would
// otherwise be filtered out by slab. Sentinel strategies consume events, not
// bars, so they attach to a single shard regardless of slab membership.
// Callers must arrange to pass true to exactly one shard per pipeline,
// otherwise handlers on the shared bus will fire N times per event.
func BuildStrategyShardWithSentinels(shared *StrategyShared, slab []domain.Symbol, deps StrategyDeps) (*StrategyShard, error) {
	return buildStrategyShard(shared, slab, deps, true)
}

func buildStrategyShard(shared *StrategyShared, slab []domain.Symbol, deps StrategyDeps, includeSentinels bool) (*StrategyShard, error) {
	if shared == nil {
		return nil, fmt.Errorf("bootstrap: strategy: nil StrategyShared")
	}

	var slabFilter map[string]struct{}
	if len(slab) > 0 {
		slabFilter = make(map[string]struct{}, len(slab))
		for _, s := range slab {
			slabFilter[s.String()] = struct{}{}
		}
	}

	router := strategy.NewRouter()
	allSymbols := make(map[string]struct{})

	for _, spec := range shared.Specs {
		// Skip deactivated strategies entirely — they should not register
		// instances or consume symbols. The PipelineActivator also enforces
		// this for dynamic symbols.
		if !spec.Lifecycle.State.IsActive() {
			deps.Logger.Debug().Str("spec_id", spec.ID.String()).Str("state", spec.Lifecycle.State.String()).Msg("bootstrap: strategy: spec is not active, skipping")
			continue
		}
		deps.Logger.Info().Str("spec_id", spec.ID.String()).Str("state", spec.Lifecycle.State.String()).Msg("bootstrap: strategy: activating spec")

		hookRef, hasHook := spec.Hooks["signals"]
		if !hasHook {
			deps.Logger.Warn().Str("spec_id", spec.ID.String()).Msg("bootstrap: strategy: spec has no signals hook, skipping")
			continue
		}
		implID, err := start.NewStrategyID(hookRef.Name)
		if err != nil {
			deps.Logger.Warn().Str("spec_id", spec.ID.String()).Str("hook_name", hookRef.Name).Err(err).Msg("bootstrap: strategy: invalid hook signal name, skipping")
			continue
		}
		impl, err := shared.Registry.Get(implID)
		if err != nil {
			deps.Logger.Warn().Str("spec_id", spec.ID.String()).Str("impl_id", implID.String()).Msg("bootstrap: strategy: no builtin implementation for hook, skipping")
			continue
		}

		for _, sym := range spec.Routing.Symbols {
			if slabFilter != nil {
				if _, ok := slabFilter[sym]; !ok {
					if !includeSentinels || !isSentinelSymbol(sym) {
						continue
					}
				}
			}

			instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", spec.ID, spec.Version, sym))
			inst := strategy.NewInstance(instanceID, impl, spec.ParamsForSymbol(sym), strategy.InstanceAssignment{
				Symbols:           []string{sym},
				Timeframes:        spec.Routing.Timeframes,
				Priority:          spec.Routing.Priority,
				AllowedDirections: spec.Routing.AllowedDirections,
			}, spec.Lifecycle.State, shared.Logger)
			inst.SetNoviceDescription(spec.NoviceDescription)

			initCtx := strategy.NewContext(shared.Clock(), shared.Logger, nil)
			if err := inst.InitSymbol(initCtx, sym, nil); err != nil {
				return nil, fmt.Errorf("bootstrap: strategy: failed to init %s symbol %s: %w", spec.ID, sym, err)
			}
			if isSentinelSymbol(sym) {
				invokeSentinelBootstrap(inst, sym, deps, shared.Clock())
			}
			router.Register(inst)
			allSymbols[sym] = struct{}{}
		}
	}

	var runnerOpts []strategy.Option
	if deps.Indicator != nil {
		runnerOpts = append(runnerOpts, strategy.WithIndicator(deps.Indicator))
	}
	runner := strategy.NewRunner(deps.EventBus, router, deps.TenantID, deps.EnvMode, shared.Logger, runnerOpts...)
	if deps.BacktestID != "" {
		runner.TagBacktest(deps.BacktestID)
	}
	runner.SetDisableAI(deps.DisableAI)
	if deps.PositionLookup != nil {
		runner.SetPositionLookup(deps.PositionLookup)
	}
	if deps.TideTracker != nil {
		runner.SetTideTracker(deps.TideTracker)
	}
	if deps.Notifier != nil {
		runner.SetNotifier(deps.Notifier)
	}

	baseSymbols := make([]string, 0, len(allSymbols))
	for sym := range allSymbols {
		baseSymbols = append(baseSymbols, sym)
	}

	return &StrategyShard{
		Router:      router,
		Runner:      runner,
		BaseSymbols: baseSymbols,
	}, nil
}

// isSentinelSymbol recognizes symbols shaped like "__name__" used as routing
// keys for event-driven strategies that don't subscribe to per-symbol bars
// (e.g. the copytrade strategy's "__copytrade__"). These strategies need a
// runner instance somewhere to consume events, but do not participate in the
// per-symbol slab distribution used by sharded bar dispatch.
func isSentinelSymbol(sym string) bool {
	return strings.HasPrefix(sym, "__") && strings.HasSuffix(sym, "__") && len(sym) >= 4
}

// invokeSentinelBootstrap runs the optional per-instance rehydration hook
// for sentinel-routed strategies whose in-memory state does not survive
// restart. Currently only copytrade implements this; unknown strategies
// and specs with no bootstrap dependencies wired are silent no-ops.
func invokeSentinelBootstrap(inst *strategy.Instance, sym string, deps StrategyDeps, now time.Time) {
	cs, ok := inst.Strategy().(*builtin.CopytradeStrategy)
	if !ok {
		return
	}
	state, ok := inst.GetState(sym)
	if !ok {
		return
	}
	if deps.PnLRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bootDeps := builtin.CopytradeBootstrapDeps{
		TenantID: deps.TenantID,
		EnvMode:  deps.EnvMode,
		Now:      now,
		SignalEvents: func(qctx context.Context, stratName string, from, to time.Time) ([]domain.StrategySignalEvent, error) {
			page, err := deps.PnLRepo.GetStrategySignalEvents(qctx, ports.StrategySignalQuery{
				TenantID: deps.TenantID,
				EnvMode:  deps.EnvMode,
				Strategy: stratName,
				From:     from,
				To:       to,
				Limit:    200,
			})
			if err != nil {
				return nil, err
			}
			return page.Items, nil
		},
		Logger: slog.Default().With("strategy", "copytrade_v1"),
	}
	if deps.GetPositionsFn != nil {
		bootDeps.Positions = deps.GetPositionsFn
	}
	if _, err := cs.Bootstrap(ctx, state, bootDeps); err != nil {
		deps.Logger.Warn().Err(err).Str("strategy", "copytrade_v1").Msg("bootstrap: sentinel strategy Bootstrap returned error")
	}
}

// BuildStrategyPipeline constructs the canonical single-shard strategy v2
// pipeline:
//
//	Runner → SignalDebateEnricher → RiskSizer
//
// This produces the IDENTICAL pipeline as omo-core's initStrategyPipeline().
// Internally it delegates to BuildStrategyShared + BuildStrategyShard(nil)
// so every spec×symbol instance is registered on one Router — the same
// behavior as before Phase 2's bootstrap split.
func BuildStrategyPipeline(deps StrategyDeps) (*StrategyPipeline, error) {
	shared, err := BuildStrategyShared(deps)
	if err != nil {
		return nil, err
	}
	shard, err := BuildStrategyShard(shared, nil, deps)
	if err != nil {
		return nil, err
	}

	lifecycleSvc := strategy.NewLifecycleService(shard.Router, shared.Logger)
	activator := &PipelineActivator{
		runner:   shard.Runner,
		router:   shard.Router,
		registry: shared.Registry,
		specs:    shared.Specs,
		logger:   slog.Default(),
		clock:    shared.Clock,
	}

	return &StrategyPipeline{
		Runner:       shard.Runner,
		Router:       shard.Router,
		Enricher:     shared.Enricher,
		RiskSizer:    shared.RiskSizer,
		LifecycleSvc: lifecycleSvc,
		BaseSymbols:  shard.BaseSymbols,
		Activator:    activator,
	}, nil
}

// PipelineActivator creates and registers strategy instances for dynamically
// screened symbols. Satisfies activation.StrategyActivator.
type PipelineActivator struct {
	runner   *strategy.Runner
	router   *strategy.Router
	registry *strategy.MemRegistry
	specs    []stratports.Spec
	logger   *slog.Logger
	clock    func() time.Time
}

func (pa *PipelineActivator) ActivateSymbol(symbol string, bars1m, barsHTF []domain.MarketBar, sessionOpen time.Time) {
	for _, spec := range pa.specs {
		if !spec.Lifecycle.State.IsActive() {
			continue
		}

		hookRef, hasHook := spec.Hooks["signals"]
		if !hasHook {
			continue
		}
		implID, err := start.NewStrategyID(hookRef.Name)
		if err != nil {
			continue
		}
		impl, err := pa.registry.Get(implID)
		if err != nil {
			continue
		}

		existing := pa.router.InstancesForSymbol(symbol)
		alreadyRegistered := false
		for _, inst := range existing {
			if inst.Strategy().Meta().ID == implID {
				alreadyRegistered = true
				break
			}
		}
		if alreadyRegistered {
			continue
		}

		instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s:dynamic", spec.ID, spec.Version, symbol))
		inst := strategy.NewInstance(instanceID, impl, spec.ParamsForSymbol(symbol), strategy.InstanceAssignment{
			Symbols:           []string{symbol},
			Timeframes:        spec.Routing.Timeframes,
			Priority:          spec.Routing.Priority,
			AllowedDirections: spec.Routing.AllowedDirections,
		}, spec.Lifecycle.State, pa.logger)
		inst.SetNoviceDescription(spec.NoviceDescription)

		initCtx := strategy.NewContext(pa.clock(), pa.logger, nil)
		if err := inst.InitSymbol(initCtx, symbol, nil); err != nil {
			pa.logger.Warn("activation: failed to init symbol", "spec", spec.ID.String(), "symbol", symbol, "error", err)
			continue
		}

		pa.router.Register(inst)
	}

	snapshotFn := makeSnapshotFn()
	if len(bars1m) > 0 {
		pa.runner.WarmUp(symbol, bars1m, snapshotFn)
	}
	for _, tf := range pa.runner.HTFTimeframesForSymbol(symbol) {
		if len(barsHTF) > 0 {
			pa.runner.WarmUpTF(symbol, tf, barsHTF, snapshotFn)
		}
	}
	pa.runner.InitAggregators(sessionOpen)
}

func makeSnapshotFn() strategy.IndicatorSnapshotFunc {
	calc := monitor.NewIndicatorCalculator()
	calc.Label = "bootstrap_snapshot_fn"
	return func(bar domain.MarketBar) start.IndicatorData {
		snap := calc.Update(bar)
		return start.IndicatorData{
			RSI:           snap.RSI,
			StochK:        snap.StochK,
			StochD:        snap.StochD,
			EMA9:          snap.EMA9,
			EMA21:         snap.EMA21,
			EMA50:         snap.EMA50,
			EMAFast:       snap.EMAFast,
			EMASlow:       snap.EMASlow,
			EMAFastPeriod: snap.EMAFastPeriod,
			EMASlowPeriod: snap.EMASlowPeriod,
			VWAP:          snap.VWAP,
			Volume:        snap.Volume,
			VolumeSMA:     snap.VolumeSMA,
			ATR:           snap.ATR,
			EMA200:        snap.EMA200,
			BBUpper:       snap.BBUpper,
			BBMiddle:      snap.BBMiddle,
			BBLower:       snap.BBLower,
			BBPercentB:    snap.BBPercentB,
			BBBandwidth:   snap.BBBandwidth,
			MACDLine:      snap.MACDLine,
			MACDSignal:    snap.MACDSignal,
			MACDHistogram: snap.MACDHistogram,
			ADX:           snap.ADX,
			RegimeScore:   snap.RegimeScore,
		}
	}
}

