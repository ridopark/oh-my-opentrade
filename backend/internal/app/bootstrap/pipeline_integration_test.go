package bootstrap

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/adapters/noop"
	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/rs/zerolog"
)

func TestPipelineIntegration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := zerolog.Nop()
	eventBus := memory.NewBus()
	defer eventBus.Close()

	var (
		cntBarReceived    atomic.Int64
		cntBarSanitized   atomic.Int64
		cntStateUpdated   atomic.Int64
		cntSignalCreated  atomic.Int64
		cntSignalEnriched atomic.Int64
		cntIntentCreated  atomic.Int64
		cntFillReceived   atomic.Int64
	)

	subCounter := func(evType domain.EventType, counter *atomic.Int64) {
		t.Helper()
		if err := eventBus.Subscribe(ctx, evType, func(_ context.Context, _ domain.Event) error {
			counter.Add(1)
			return nil
		}); err != nil {
			t.Fatalf("subscribe %s: %v", evType, err)
		}
	}

	subCounter(domain.EventMarketBarReceived, &cntBarReceived)
	subCounter(domain.EventMarketBarSanitized, &cntBarSanitized)
	subCounter(domain.EventStateUpdated, &cntStateUpdated)
	subCounter(domain.EventSignalCreated, &cntSignalCreated)
	subCounter(domain.EventSignalEnriched, &cntSignalEnriched)
	subCounter(domain.EventOrderIntentCreated, &cntIntentCreated)
	subCounter(domain.EventFillReceived, &cntFillReceived)

	var wallClock atomic.Value
	baseTime := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)
	wallClock.Store(baseTime)
	clockFn := func() time.Time { return wallClock.Load().(time.Time) }

	sim := simbroker.New(simbroker.Config{
		SlippageBPS:   5,
		InitialEquity: 100_000,
	}, log)

	specStore := specStoreFromConfigs(t)

	ingBundle, err := BuildIngestion(IngestionDeps{
		EventBus:   eventBus,
		Repo:       &noop.NoopRepo{},
		IsBacktest: true,
		Logger:     log,
	})
	if err != nil {
		t.Fatalf("BuildIngestion: %v", err)
	}

	monitorSvc, err := BuildMonitor(MonitorDeps{
		EventBus: eventBus,
		Repo:     &noop.NoopRepo{},
		Logger:   log,
	})
	if err != nil {
		t.Fatalf("BuildMonitor: %v", err)
	}

	execBundle, err := BuildExecutionService(ExecutionDeps{
		EventBus:      eventBus,
		Broker:        sim,
		Repo:          &noop.NoopRepo{},
		QuoteProvider: sim,
		AccountPort:   sim,
		PnLRepo:       &noop.NoopPnLRepo{},
		TradeReader:   nil,
		Clock:         clockFn,
		Config:        testConfig(),
		InitialEquity: 100_000,
		Logger:        log,
	})
	if err != nil {
		t.Fatalf("BuildExecutionService: %v", err)
	}

	posMonBundle, err := BuildPositionMonitor(PosMonitorDeps{
		EventBus:     eventBus,
		PositionGate: execBundle.PositionGate,
		Broker:       sim,
		SpecStore:    specStore,
		TenantID:     "test",
		EnvMode:      domain.EnvModePaper,
		Clock:        clockFn,
		IsBacktest:   true,
		Logger:       log,
	})
	if err != nil {
		t.Fatalf("BuildPositionMonitor: %v", err)
	}

	pipeline, err := BuildStrategyPipeline(StrategyDeps{
		EventBus:        eventBus,
		SpecStore:       specStore,
		AIAdvisor:       stubAIAdvisor{},
		PositionLookup:  posMonBundle.Service.LookupPosition,
		MarketDataFn:    monitorSvc.GetLastSnapshot,
		Repo:            nil,
		TenantID:        "test",
		EnvMode:         domain.EnvModePaper,
		Equity:          100_000,
		Clock:           clockFn,
		DisableEnricher: true,
		Logger:          log,
	})
	if err != nil {
		t.Fatalf("BuildStrategyPipeline: %v", err)
	}

	if len(pipeline.BaseSymbols) == 0 {
		t.Fatal("pipeline.BaseSymbols is empty; no strategies loaded")
	}
	t.Logf("pipeline loaded %d symbols: %v", len(pipeline.BaseSymbols), pipeline.BaseSymbols)

	if err := eventBus.Subscribe(ctx, domain.EventSignalCreated, pipelineTestPassthrough(eventBus)); err != nil {
		t.Fatalf("subscribe signal passthrough: %v", err)
	}

	monitorSvc.SetBaseSymbols(pipeline.BaseSymbols)

	loc, _ := time.LoadLocation("America/New_York")
	sessionOpen := time.Date(2025, 6, 15, 9, 30, 0, 0, loc)
	sym := domain.Symbol("SPY")
	monitorSvc.InitAggregators([]domain.Symbol{sym}, sessionOpen)

	orbID, _ := start.NewStrategyID("orb_break_retest")
	if orbSpec, sErr := specStore.GetLatest(ctx, orbID); sErr == nil {
		monitorSvc.SetORBConfig(orbSpec.Params)
	}

	tf := domain.Timeframe("1m")
	warmupBars := pipelineTestBars(sym, tf, baseTime.Add(-30*time.Minute), 25, 590.0)
	ingBundle.Filter.Seed(sym, warmupBars)

	signalTracker := perf.NewSignalTracker(eventBus, &noop.NoopPnLRepo{}, log)

	for _, step := range []struct {
		name string
		fn   func() error
	}{
		{"ingestion", func() error { return ingBundle.Service.Start(ctx) }},
		{"monitor", func() error { return monitorSvc.Start(ctx) }},
		{"ledgerWriter", func() error { return execBundle.LedgerWriter.Start(ctx, "test", domain.EnvModePaper) }},
		{"signalTracker", func() error { return signalTracker.Start(ctx) }},
		{"execution", func() error { return execBundle.Service.Start(ctx, "test", domain.EnvModePaper) }},
		{"priceCache", func() error { return posMonBundle.PriceCache.Start(ctx, eventBus) }},
		{"posMonitor", func() error { return posMonBundle.Service.Start(ctx) }},
		{"runner", func() error { return pipeline.Runner.Start(ctx) }},
		{"riskSizer", func() error { return pipeline.RiskSizer.Start(ctx) }},
	} {
		if err := step.fn(); err != nil {
			t.Fatalf("start %s: %v", step.name, err)
		}
	}

	bars := pipelineTestBars(sym, tf, baseTime, 10, 590.0)
	for _, bar := range bars {
		wallClock.Store(bar.Time)
		sim.UpdatePrice(bar.Symbol, bar.Close, bar.Time)

		evt, err := domain.NewEvent(
			domain.EventMarketBarReceived,
			"test",
			domain.EnvModePaper,
			bar.Time.String()+string(bar.Symbol),
			bar,
		)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if err := eventBus.Publish(ctx, *evt); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		eventBus.WaitPending()
	}

	t.Logf("EventMarketBarReceived:  %d", cntBarReceived.Load())
	t.Logf("EventMarketBarSanitized: %d", cntBarSanitized.Load())
	t.Logf("EventStateUpdated:       %d", cntStateUpdated.Load())
	t.Logf("EventSignalCreated:      %d", cntSignalCreated.Load())
	t.Logf("EventSignalEnriched:     %d", cntSignalEnriched.Load())
	t.Logf("EventOrderIntentCreated: %d", cntIntentCreated.Load())
	t.Logf("EventFillReceived:       %d", cntFillReceived.Load())

	if cntBarReceived.Load() == 0 {
		t.Error("expected EventMarketBarReceived > 0")
	}
	if cntBarSanitized.Load() == 0 {
		t.Error("expected EventMarketBarSanitized > 0")
	}
}

func pipelineTestPassthrough(bus *memory.Bus) func(context.Context, domain.Event) error {
	return func(ctx context.Context, ev domain.Event) error {
		sig, ok := ev.Payload.(start.Signal)
		if !ok {
			return nil
		}
		direction := domain.DirectionLong
		if sig.Side == start.SideSell {
			direction = domain.DirectionShort
		}
		if sig.Type == start.SignalExit {
			direction = domain.DirectionCloseLong
		}
		enrichment := domain.SignalEnrichment{
			Signal: domain.SignalRef{
				StrategyInstanceID: string(sig.StrategyInstanceID),
				Symbol:             sig.Symbol,
				SignalType:         sig.Type.String(),
				Side:               sig.Side.String(),
				Strength:           sig.Strength,
				Tags:               sig.Tags,
			},
			Status:     domain.EnrichmentSkipped,
			Confidence: sig.Strength,
			Direction:  direction,
			Rationale:  fmt.Sprintf("passthrough: %s %s strength=%.2f", sig.Type, sig.Side, sig.Strength),
		}
		enrichedEvt, err := domain.NewEvent(
			domain.EventSignalEnriched,
			ev.TenantID,
			ev.EnvMode,
			ev.IdempotencyKey+"-enriched",
			enrichment,
		)
		if err != nil {
			return nil
		}
		return bus.Publish(ctx, *enrichedEvt)
	}
}

func pipelineTestBars(sym domain.Symbol, tf domain.Timeframe, startTime time.Time, n int, basePrice float64) []domain.MarketBar {
	bars := make([]domain.MarketBar, n)
	for i := range bars {
		barTime := startTime.Add(time.Duration(i) * time.Minute)
		c := basePrice + float64(i%5)*0.10
		bars[i] = domain.MarketBar{
			Time:      barTime,
			Symbol:    sym,
			Timeframe: tf,
			Open:      c - 0.05,
			High:      c + 0.15,
			Low:       c - 0.10,
			Close:     c,
			Volume:    1_000_000,
		}
	}
	return bars
}
