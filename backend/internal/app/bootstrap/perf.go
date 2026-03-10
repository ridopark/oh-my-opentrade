package bootstrap

import (
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type PerfDeps struct {
	EventBus      ports.EventBusPort
	PnLRepo       ports.PnLPort
	Broker        ports.BrokerPort
	TradeReader   perf.TradeReaderPort
	InitialEquity float64
	Logger        zerolog.Logger
}

type PerfBundle struct {
	LedgerWriter  *perf.LedgerWriter
	SignalTracker *perf.SignalTracker
}

func BuildPerfServices(deps PerfDeps) (*PerfBundle, error) {
	ledgerLog := deps.Logger.With().Str("component", "ledger").Logger()
	trackerLog := deps.Logger.With().Str("component", "signal_tracker").Logger()

	lw := perf.NewLedgerWriter(
		deps.EventBus,
		deps.PnLRepo,
		deps.Broker,
		deps.TradeReader,
		deps.InitialEquity,
		ledgerLog,
	)

	st := perf.NewSignalTracker(deps.EventBus, deps.PnLRepo, trackerLog)

	return &PerfBundle{
		LedgerWriter:  lw,
		SignalTracker: st,
	}, nil
}
