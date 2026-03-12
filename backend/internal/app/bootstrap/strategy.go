package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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
	TenantID        string
	EnvMode         domain.EnvMode
	Equity          float64
	Clock           func() time.Time
	DisableEnricher bool
	Logger          zerolog.Logger
}

// StrategyPipeline is the return value of BuildStrategyPipeline, exposing the
// wired components that callers need to start/manage independently.
type StrategyPipeline struct {
	Runner       *strategy.Runner
	Router       *strategy.Router
	Enricher     *strategy.SignalDebateEnricher // nil when DisableEnricher
	RiskSizer    *strategy.RiskSizer
	LifecycleSvc *strategy.LifecycleService
	BaseSymbols  []string
	Activator    *PipelineActivator
}

// BuildStrategyPipeline constructs the canonical strategy v2 pipeline:
//
//	Runner → SignalDebateEnricher → RiskSizer
//
// This produces the IDENTICAL pipeline as omo-core's initStrategyPipeline().
func BuildStrategyPipeline(deps StrategyDeps) (*StrategyPipeline, error) {
	stratLog := slog.Default()

	registry := strategy.NewMemRegistry()
	for _, s := range []start.Strategy{
		builtin.NewORBStrategy(),
		builtin.NewAVWAPStrategy(),
		builtin.NewAIScalperStrategy(),
		builtin.NewBreakRetestStrategy(),
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

	router := strategy.NewRouter()
	allSymbols := make(map[string]struct{})

	clockFn := deps.Clock
	if clockFn == nil {
		clockFn = time.Now
	}

	for _, spec := range allSpecs {
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
		impl, err := registry.Get(implID)
		if err != nil {
			deps.Logger.Warn().Str("spec_id", spec.ID.String()).Str("impl_id", implID.String()).Msg("bootstrap: strategy: no builtin implementation for hook, skipping")
			continue
		}

		for _, sym := range spec.Routing.Symbols {
			instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", spec.ID, spec.Version, sym))
			inst := strategy.NewInstance(instanceID, impl, spec.Params, strategy.InstanceAssignment{
				Symbols:           []string{sym},
				Timeframes:        spec.Routing.Timeframes,
				Priority:          spec.Routing.Priority,
				AllowedDirections: spec.Routing.AllowedDirections,
			}, start.LifecycleLiveActive, stratLog)

			initCtx := strategy.NewContext(clockFn(), stratLog, nil)
			if err := inst.InitSymbol(initCtx, sym, nil); err != nil {
				return nil, fmt.Errorf("bootstrap: strategy: failed to init %s symbol %s: %w", spec.ID, sym, err)
			}
			router.Register(inst)
			allSymbols[sym] = struct{}{}
		}
	}

	runner := strategy.NewRunner(deps.EventBus, router, deps.TenantID, deps.EnvMode, stratLog)
	if deps.PositionLookup != nil {
		runner.SetPositionLookup(deps.PositionLookup)
	}

	var enricher *strategy.SignalDebateEnricher
	if !deps.DisableEnricher {
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
		opts = append(opts, strategy.WithDebateTimeout(30*time.Second))
		enricher = strategy.NewSignalDebateEnricher(deps.EventBus, deps.AIAdvisor, stratLog, opts...)
	}

	riskSizer := strategy.NewRiskSizer(deps.EventBus, deps.SpecStore, deps.Equity, stratLog)
	lifecycleSvc := strategy.NewLifecycleService(router, stratLog)

	baseSymbols := make([]string, 0, len(allSymbols))
	for sym := range allSymbols {
		baseSymbols = append(baseSymbols, sym)
	}

	activator := &PipelineActivator{
		runner:   runner,
		router:   router,
		registry: registry,
		specs:    allSpecs,
		logger:   slog.Default(),
		clock:    clockFn,
	}

	return &StrategyPipeline{
		Runner:       runner,
		Router:       router,
		Enricher:     enricher,
		RiskSizer:    riskSizer,
		LifecycleSvc: lifecycleSvc,
		BaseSymbols:  baseSymbols,
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
		inst := strategy.NewInstance(instanceID, impl, spec.Params, strategy.InstanceAssignment{
			Symbols:           []string{symbol},
			Timeframes:        spec.Routing.Timeframes,
			Priority:          spec.Routing.Priority,
			AllowedDirections: spec.Routing.AllowedDirections,
		}, start.LifecycleLiveActive, pa.logger)

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
	for _, tf := range collectHTFTimeframes(pa.router, symbol) {
		if len(barsHTF) > 0 {
			pa.runner.WarmUpTF(symbol, tf, barsHTF, snapshotFn)
		}
	}
	pa.runner.InitAggregators(sessionOpen)
}

func makeSnapshotFn() strategy.IndicatorSnapshotFunc {
	calc := monitor.NewIndicatorCalculator()
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
		}
	}
}

func collectHTFTimeframes(router *strategy.Router, symbol string) []string {
	seen := make(map[string]struct{})
	for _, inst := range router.InstancesForSymbol(symbol) {
		for _, tf := range inst.Assignment().Timeframes {
			if tf != "1m" {
				seen[tf] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for tf := range seen {
		result = append(result, tf)
	}
	return result
}
