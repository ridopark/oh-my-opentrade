package bootstrap

import (
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/gate"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/ports"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

type IngestionDeps struct {
	EventBus   ports.EventBusPort
	Repo       ports.RepositoryPort
	BarSaver   ingestion.BarBatchSaver // only needed when IsBacktest=false
	IsBacktest bool
	Logger     zerolog.Logger
}

// IngestionBundle groups wired ingestion components.
// BarWriter is nil in backtest mode. When non-nil the caller must call
// BarWriter.Start() to launch the background flush goroutine.
type IngestionBundle struct {
	Service   *ingestion.Service
	BarWriter *ingestion.AsyncBarWriter
	Filter    *ingestion.AdaptiveFilter
}

func BuildIngestion(deps IngestionDeps) (*IngestionBundle, error) {
	ingLog := deps.Logger.With().Str("component", "ingestion").Logger()

	filter := ingestion.NewAdaptiveFilter(20, 4.0)
	filter.SetPassthrough(true) // trust SIP bars — z-score gate rejects legitimate volatile moves
	svc := ingestion.NewService(deps.EventBus, deps.Repo, filter, ingLog)
	if deps.IsBacktest {
		svc.SetBacktest(true)
	}

	var barWriter *ingestion.AsyncBarWriter
	if !deps.IsBacktest {
		barWriter = ingestion.NewAsyncBarWriter(deps.BarSaver, ingLog)
		svc.SetBarWriter(barWriter)
	}

	return &IngestionBundle{
		Service:   svc,
		BarWriter: barWriter,
		Filter:    filter,
	}, nil
}

type MonitorDeps struct {
	EventBus   ports.EventBusPort
	Repo       ports.RepositoryPort
	Logger     zerolog.Logger
	BacktestID string // non-empty → tag ORB tracker logs with this ID
}

func BuildMonitor(deps MonitorDeps) (*monitor.Service, error) {
	monLog := deps.Logger.With().Str("component", "monitor").Logger()
	svc := monitor.NewService(deps.EventBus, deps.Repo, monLog)
	if deps.BacktestID != "" {
		svc.TagBacktest(deps.BacktestID)
	}
	return svc, nil
}

// WireGateChain builds and installs a MonitorGateChain + IndexTideTracker on
// the monitor service. If gateChainCfg is nil, DefaultMonitorGateConfigs() is
// used so the gate chain is always active once this function is called.
// The dnaChecker may be nil (the dna_approval gate will pass-through).
func WireGateChain(
	svc *monitor.Service,
	gateChainCfg *portstrategy.GateChainConfig,
	dnaChecker gate.DNAGateChecker,
	log zerolog.Logger,
) error {
	// Build gate configs from spec or defaults.
	var configs []gate.GateConfig
	if gateChainCfg != nil && len(gateChainCfg.Monitor) > 0 {
		configs = make([]gate.GateConfig, len(gateChainCfg.Monitor))
		for i, e := range gateChainCfg.Monitor {
			configs[i] = gate.GateConfig{
				Name:   e.Name,
				Params: e.Params,
			}
		}
	} else {
		configs = gate.DefaultMonitorGateConfigs()
	}

	tracker := gate.NewIndexTideTracker(30)
	deps := &gate.GateDeps{
		DNAGate:     dnaChecker,
		TideTracker: tracker,
	}

	registry := gate.NewDefaultRegistry()
	chain, err := registry.BuildChain(configs, deps, log)
	if err != nil {
		return err
	}

	svc.SetMonitorGateChain(chain)
	svc.SetTideTracker(tracker)
	return nil
}

// WireExecutionGateChain builds and installs an ExecutionGateChain on the
// execution service. If gateChainCfg is nil or has no Execution entries,
// DefaultExecutionGateConfigs() is used so the chain is always active.
func WireExecutionGateChain(
	svc *execution.Service,
	gateChainCfg *portstrategy.GateChainConfig,
	deps *gate.ExecutionGateDeps,
	log zerolog.Logger,
) error {
	var configs []gate.GateConfig
	if gateChainCfg != nil && len(gateChainCfg.Execution) > 0 {
		configs = make([]gate.GateConfig, len(gateChainCfg.Execution))
		for i, e := range gateChainCfg.Execution {
			configs[i] = gate.GateConfig{
				Name:   e.Name,
				Params: e.Params,
			}
		}
	} else {
		configs = gate.DefaultExecutionGateConfigs()
	}

	registry := gate.NewDefaultExecutionRegistry()
	chain, err := registry.BuildChain(configs, deps, log)
	if err != nil {
		return err
	}

	svc.SetExecutionGateChain(chain)
	return nil
}
