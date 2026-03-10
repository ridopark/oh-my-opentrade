package bootstrap

import (
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/ports"
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
	svc := ingestion.NewService(deps.EventBus, deps.Repo, filter, ingLog)

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
	EventBus ports.EventBusPort
	Repo     ports.RepositoryPort
	Logger   zerolog.Logger
}

func BuildMonitor(deps MonitorDeps) (*monitor.Service, error) {
	monLog := deps.Logger.With().Str("component", "monitor").Logger()
	return monitor.NewService(deps.EventBus, deps.Repo, monLog), nil
}
