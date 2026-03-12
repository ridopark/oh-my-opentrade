package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/adapters/llm"
	"github.com/oh-my-opentrade/backend/internal/adapters/noop"
	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/app/bootstrap"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

func main() {
	var (
		symbolsFlag   string
		fromFlag      string
		toFlag        string
		speedFlag     string
		configPath    string
		envPath       string
		backtestFlag  bool
		initialEquity float64
		slippageBPS   int64
		outputJSON    string
		noAIFlag      bool
	)

	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols to replay (default: use config file symbols)")
	flag.StringVar(&fromFlag, "from", "", "Start time (RFC3339 or YYYY-MM-DD)")
	flag.StringVar(&toFlag, "to", "", "End time (RFC3339 or YYYY-MM-DD) (default: now)")
	flag.StringVar(&speedFlag, "speed", "max", "Replay speed: max, 1x, 10x, or any float (e.g. 2.5)")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.BoolVar(&backtestFlag, "backtest", false, "Enable backtest mode: wire full execution pipeline with SimBroker")
	flag.Float64Var(&initialEquity, "initial-equity", 100000.0, "Initial account equity for backtest (default: 100000)")
	flag.Int64Var(&slippageBPS, "slippage-bps", 5, "Slippage in basis points for SimBroker fills (default: 5)")
	flag.StringVar(&outputJSON, "output-json", "", "Path to write backtest results as JSON (backtest mode only)")
	flag.BoolVar(&noAIFlag, "no-ai", true, "Disable AI signal debate enricher (default: true for backtest)")
	flag.Parse()

	logLevel := zerolog.InfoLevel
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if parsed, err := zerolog.ParseLevel(lvl); err == nil {
			logLevel = parsed
		}
	}
	log := logger.New(logger.Config{
		Level:  logLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-replay").Logger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Warn().Str("signal", sig.String()).Msg("received signal, canceling replay")
		cancel()
	}()

	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Error().Err(err).Msg("failed to load config")
		os.Exit(1)
	}

	symbols := resolveSymbols(symbolsFlag, cfg)
	if len(symbols) == 0 {
		log.Fatal().Msg("no symbols specified — use --symbols or configure in config.yaml")
	}
	timeframe := domain.Timeframe(cfg.Symbols.Timeframe)
	if _, err := domain.NewTimeframe(timeframe.String()); err != nil {
		log.Fatal().Err(err).Str("timeframe", timeframe.String()).Msg("invalid timeframe")
	}
	barDur, err := timeframeDuration(timeframe)
	if err != nil {
		log.Fatal().Err(err).Str("timeframe", timeframe.String()).Msg("unsupported timeframe")
	}

	fromTime, err := parseTimeFlag(fromFlag)
	if err != nil {
		log.Fatal().Err(err).Str("from", fromFlag).Msg("invalid --from")
	}
	if fromTime.IsZero() {
		log.Fatal().Msg("--from is required")
	}
	toTime, err := parseTimeFlag(toFlag)
	if err != nil {
		log.Fatal().Err(err).Str("to", toFlag).Msg("invalid --to")
	}
	if toTime.IsZero() {
		toTime = time.Now().UTC()
	}
	if !toTime.After(fromTime) {
		log.Fatal().Time("from", fromTime).Time("to", toTime).Msg("invalid time range: --to must be after --from")
	}

	speedFactor, maxSpeed, err := parseSpeed(speedFlag)
	if err != nil {
		log.Fatal().Err(err).Str("speed", speedFlag).Msg("invalid --speed")
	}
	perBarDelay := time.Duration(0)
	if !maxSpeed {
		perBarDelay = time.Duration(float64(barDur) / speedFactor)
		if perBarDelay < 0 {
			perBarDelay = 0
		}
	}

	eventBus := memory.NewBus()

	tracer := newEventTracer(log.With().Str("component", "event_tracer").Logger())
	for _, evtType := range allEventTypes() {
		if err := eventBus.Subscribe(ctx, evtType, tracer.Handle); err != nil {
			log.Fatal().Err(err).Str("event_type", evtType).Msg("failed to subscribe event tracer")
		}
	}

	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	pgxCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse DB config")
	}
	sqlDB := stdlib.OpenDB(*pgxCfg)
	if err := sqlDB.PingContext(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to TimescaleDB")
	}
	defer sqlDB.Close()
	log.Info().Msg("TimescaleDB connected")
	repo := timescaledb.NewRepositoryWithLogger(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "timescaledb").Logger())

	var currentBarTime atomic.Value
	currentBarTime.Store(time.Now())
	clockFn := func() time.Time {
		return currentBarTime.Load().(time.Time)
	}

	ingBundle, err := bootstrap.BuildIngestion(bootstrap.IngestionDeps{
		EventBus:   eventBus,
		Repo:       repo,
		IsBacktest: true,
		Logger:     log,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to build ingestion")
	}

	monitorSvc, err := bootstrap.BuildMonitor(bootstrap.MonitorDeps{
		EventBus: eventBus,
		Repo:     repo,
		Logger:   log,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to build monitor")
	}

	const specDir = "configs/strategies"
	specStore := store_fs.NewStore(specDir, strategy.LoadSpecFile)

	orbID, _ := start.NewStrategyID("orb_break_retest")
	if orbSpec, err := specStore.GetLatest(context.Background(), orbID); err == nil {
		monitorSvc.SetORBConfig(orbSpec.Params)
	}

	var (
		signalsMu         sync.Mutex
		signalsGenerated  int
		intentsGenerated  int
		lastSignalSummary string
		lastIntentSummary string
		simBrokerInst     *simbroker.Broker
		collectorInst     *backtest.Collector
		posMonSvc         *positionmonitor.Service // non-nil in backtest mode
		pipeline          *bootstrap.StrategyPipeline
	)
	if err := eventBus.Subscribe(ctx, domain.EventSignalCreated, func(_ context.Context, ev domain.Event) error {
		signalsMu.Lock()
		defer signalsMu.Unlock()
		signalsGenerated++
		lastSignalSummary = fmt.Sprintf("%T", ev.Payload)
		return nil
	}); err != nil {
		log.Fatal().Err(err).Msg("failed to subscribe SignalCreated counter")
	}

	if backtestFlag {
		log.Info().
			Float64("initial_equity", initialEquity).
			Int64("slippage_bps", slippageBPS).
			Bool("no_ai", noAIFlag).
			Msg("backtest mode enabled — wiring SimBroker + execution pipeline")

		simBrokerInst = simbroker.New(simbroker.Config{
			SlippageBPS:     slippageBPS,
			InitialEquity:   initialEquity,
			DisableFillChan: true,
		}, log.With().Str("component", "simbroker").Logger())

		execBundle, err := bootstrap.BuildExecutionService(bootstrap.ExecutionDeps{
			EventBus:      eventBus,
			Broker:        simBrokerInst,
			Repo:          &noop.NoopRepo{},
			QuoteProvider: simBrokerInst,
			AccountPort:   simBrokerInst,
			PnLRepo:       &noop.NoopPnLRepo{},
			TradeReader:   nil,
			Clock:         clockFn,
			Config:        cfg,
			InitialEquity: initialEquity,
			IsBacktest:    true,
			Logger:        log,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("failed to build execution service")
		}

		posMonBundle, err := bootstrap.BuildPositionMonitor(bootstrap.PosMonitorDeps{
			EventBus:     eventBus,
			PositionGate: execBundle.PositionGate,
			Broker:       simBrokerInst,
			SpecStore:    specStore,
			SnapshotFn:   monitorSvc.GetLastSnapshot,
			TenantID:     "default",
			EnvMode:      domain.EnvModePaper,
			Clock:        clockFn,
			IsBacktest:   true,
			Logger:       log,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("failed to build position monitor")
		}
		posMonSvc = posMonBundle.Service

		pipeline, err = bootstrap.BuildStrategyPipeline(bootstrap.StrategyDeps{
			EventBus:        eventBus,
			SpecStore:       specStore,
			AIAdvisor:       llm.NewNoOpAdvisor(),
			PositionLookup:  posMonBundle.Service.LookupPosition,
			MarketDataFn:    monitorSvc.GetLastSnapshot,
			Repo:            nil,
			TenantID:        "default",
			EnvMode:         domain.EnvModePaper,
			Equity:          initialEquity,
			Clock:           clockFn,
			DisableEnricher: noAIFlag,
			Logger:          log,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("failed to build strategy pipeline")
		}

		if pipeline.Enricher == nil {
			if err := eventBus.Subscribe(ctx, domain.EventSignalCreated, signalPassthrough(eventBus, log)); err != nil {
				log.Fatal().Err(err).Msg("failed to subscribe signal passthrough")
			}
		}

		signalTracker := perf.NewSignalTracker(eventBus, &noop.NoopPnLRepo{}, log.With().Str("component", "signal_tracker").Logger())
		monitorSvc.SetBaseSymbols(pipeline.BaseSymbols)

		// Backtest collector subscribes to FillReceived + MarketBarReceived.
		collectorInst, err = backtest.NewCollector(eventBus, backtest.Config{
			InitialEquity: initialEquity,
		}, log.With().Str("component", "backtest_collector").Logger())
		if err != nil {
			log.Fatal().Err(err).Msg("failed to create backtest collector")
		}

		if err := eventBus.Subscribe(ctx, domain.EventOrderIntentCreated, func(_ context.Context, ev domain.Event) error {
			signalsMu.Lock()
			defer signalsMu.Unlock()
			intentsGenerated++
			lastIntentSummary = fmt.Sprintf("%T", ev.Payload)
			return nil
		}); err != nil {
			log.Fatal().Err(err).Msg("failed to subscribe OrderIntentCreated counter")
		}

		if err := ingBundle.Service.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start ingestion")
		}
		if err := monitorSvc.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start monitor")
		}
		if err := execBundle.LedgerWriter.Start(ctx, "backtest", domain.EnvModePaper); err != nil {
			log.Fatal().Err(err).Msg("failed to start ledger writer")
		}
		if err := signalTracker.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start signal tracker")
		}
		if err := execBundle.Service.Start(ctx, "backtest", domain.EnvModePaper); err != nil {
			log.Fatal().Err(err).Msg("failed to start execution service")
		}
		if err := posMonBundle.PriceCache.Start(ctx, eventBus); err != nil {
			log.Fatal().Err(err).Msg("failed to start price cache")
		}
		if err := posMonBundle.Service.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start position monitor")
		}
		if err := pipeline.Runner.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start strategy runner")
		}
		if pipeline.Enricher != nil {
			if err := pipeline.Enricher.Start(ctx); err != nil {
				log.Fatal().Err(err).Msg("failed to start signal debate enricher")
			}
		}
		// Inject replay clock into risk sizer so exit cooldowns and circuit
		// breakers use simulated bar time instead of wall-clock time.
		pipeline.RiskSizer.SetNowFn(clockFn)
		if backtestFlag {
			pipeline.RiskSizer.SetExitCooldown(3 * time.Minute)
		}

		if err := pipeline.RiskSizer.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start risk sizer")
		}

	} else {
		var err error
		pipeline, err = bootstrap.BuildStrategyPipeline(bootstrap.StrategyDeps{
			EventBus:        eventBus,
			SpecStore:       specStore,
			AIAdvisor:       llm.NewNoOpAdvisor(),
			PositionLookup:  nil,
			MarketDataFn:    monitorSvc.GetLastSnapshot,
			Repo:            nil,
			TenantID:        "default",
			EnvMode:         domain.EnvModePaper,
			Equity:          initialEquity,
			Clock:           clockFn,
			DisableEnricher: noAIFlag,
			Logger:          log,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("failed to build strategy pipeline")
		}

		monitorSvc.SetBaseSymbols(pipeline.BaseSymbols)

		if pipeline.Enricher == nil {
			if err := eventBus.Subscribe(ctx, domain.EventSignalCreated, signalPassthrough(eventBus, log)); err != nil {
				log.Fatal().Err(err).Msg("failed to subscribe signal passthrough")
			}
		}

		if err := eventBus.Subscribe(ctx, domain.EventOrderIntentCreated, func(_ context.Context, ev domain.Event) error {
			intent, ok := ev.Payload.(domain.OrderIntent)
			if ok {
				log.Info().
					Str("intent_id", intent.ID.String()).
					Str("symbol", intent.Symbol.String()).
					Str("direction", intent.Direction.String()).
					Float64("qty", intent.Quantity).
					Float64("limit", intent.LimitPrice).
					Float64("stop", intent.StopLoss).
					Float64("confidence", intent.Confidence).
					Msg("MOCK EXECUTION: OrderIntentCreated")
			} else {
				log.Info().Str("payload_type", fmt.Sprintf("%T", ev.Payload)).Msg("MOCK EXECUTION: OrderIntentCreated")
			}
			signalsMu.Lock()
			defer signalsMu.Unlock()
			intentsGenerated++
			lastIntentSummary = fmt.Sprintf("%T", ev.Payload)
			return nil
		}); err != nil {
			log.Fatal().Err(err).Msg("failed to subscribe OrderIntentCreated mock execution")
		}

		if err := ingBundle.Service.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start ingestion")
		}
		if err := monitorSvc.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start monitor")
		}
		if err := pipeline.Runner.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start strategy runner")
		}
		if pipeline.Enricher != nil {
			if err := pipeline.Enricher.Start(ctx); err != nil {
				log.Fatal().Err(err).Msg("failed to start signal debate enricher")
			}
		}
		if err := pipeline.RiskSizer.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start risk sizer")
		}
	}

	warmupLog := log.With().Str("component", "warmup").Logger()
	prevStart, prevEnd := domain.PreviousRTHSession(fromTime)
	warmupFrom := prevEnd.Add(-120 * barDur)
	warmupTo := prevEnd
	warmupLog.Info().
		Time("prev_session_start", prevStart).
		Time("prev_session_end", prevEnd).
		Time("warmup_from", warmupFrom).
		Time("warmup_to", warmupTo).
		Msg("warming indicators from previous RTH session")

	warmupBarsCache := make(map[string][]domain.MarketBar, len(symbols))
	for _, sym := range symbols {
		bars, err := repo.GetMarketBars(ctx, sym, timeframe, warmupFrom, warmupTo)
		if err != nil {
			warmupLog.Warn().Err(err).Str("symbol", sym.String()).Msg("warmup fetch failed, starting cold")
			continue
		}
		warmupBarsCache[sym.String()] = bars
		n := monitorSvc.WarmUp(bars)
		monitorSvc.ResetSessionIndicators(sym.String())
		monitorSvc.MarkReady(sym.String())
		warmupLog.Info().Str("symbol", sym.String()).Int("bars", n).Msg("indicator warmup complete")
	}

	for _, sym := range symbols {
		if bars, ok := warmupBarsCache[sym.String()]; ok && len(bars) > 0 {
			n := ingBundle.Filter.Seed(sym, bars)
			warmupLog.Info().Str("symbol", sym.String()).Int("bars", n).Msg("adaptive spike filter seeded")
		}
	}

	if pipeline != nil && pipeline.Runner != nil {
		runnerCalc := monitor.NewIndicatorCalculator()
		snapshotFn := func(bar domain.MarketBar) start.IndicatorData {
			snap := runnerCalc.Update(bar)
			return start.IndicatorData{
				RSI:       snap.RSI,
				StochK:    snap.StochK,
				StochD:    snap.StochD,
				EMA9:      snap.EMA9,
				EMA21:     snap.EMA21,
				VWAP:      snap.VWAP,
				Volume:    snap.Volume,
				VolumeSMA: snap.VolumeSMA,
			}
		}
		for _, sym := range symbols {
			bars := warmupBarsCache[sym.String()]
			if len(bars) == 0 {
				continue
			}
			n := pipeline.Runner.WarmUp(sym.String(), bars, snapshotFn)
			warmupLog.Info().Str("symbol", sym.String()).Int("bars", n).Msg("strategy runner warmup complete")
		}
	}

	// Initialize MTFA aggregators so 1m bars produce 5m/15m candles + anchor regime.
	loc, _ := time.LoadLocation("America/New_York")
	fromET := fromTime.In(loc)
	replaySessionOpen := time.Date(fromET.Year(), fromET.Month(), fromET.Day(), 9, 30, 0, 0, loc)
	monitorSvc.InitAggregators(symbols, replaySessionOpen)
	log.Info().Time("session_open", replaySessionOpen).Msg("MTFA aggregators initialized for replay")

	log.Info().
		Strs("symbols", symbolStrings(symbols)).
		Str("timeframe", timeframe.String()).
		Time("from", fromTime).
		Time("to", toTime).
		Str("speed", speedFlag).
		Dur("per_bar_delay", perBarDelay).
		Msg("starting replay")

	streams := make([]*barStream, 0, len(symbols))
	for _, sym := range symbols {
		bars, err := repo.GetMarketBars(ctx, sym, timeframe, fromTime, toTime)
		if err != nil {
			log.Fatal().Err(err).Str("symbol", sym.String()).Msg("failed to load market bars")
		}
		streams = append(streams, &barStream{symbol: sym, bars: bars})
		log.Info().Str("symbol", sym.String()).Int("bars", len(bars)).Msg("loaded bars")
	}

	sort.Slice(streams, func(i, j int) bool { return streams[i].symbol.String() < streams[j].symbol.String() })

	const tenantID = "default"
	envMode := domain.EnvModePaper
	barsProcessed := 0
	groupsProcessed := 0

	// Track current session date for multi-day replays (reset aggregators on new day).
	currentSessionDate := replaySessionOpen

	for ctx.Err() == nil {

		minTime, ok := nextMinTime(streams)
		if !ok {
			break
		}

		groupsProcessed++

		currentBarTime.Store(minTime)

		// Reset MTFA aggregators on new trading day boundary.
		minET := minTime.In(loc)
		dayOpen := time.Date(minET.Year(), minET.Month(), minET.Day(), 9, 30, 0, 0, loc)
		if dayOpen.After(currentSessionDate) {
			monitorSvc.ResetAggregators(dayOpen)
			for _, sym := range symbols {
				monitorSvc.ResetSessionIndicators(sym.String())
			}
			currentSessionDate = dayOpen
			log.Debug().Time("new_session_open", dayOpen).Msg("MTFA aggregators reset for new trading day")
		}
		for _, s := range streams {
			if ctx.Err() != nil {
				break
			}
			bar, has := s.peek()
			if !has || !bar.Time.Equal(minTime) {
				continue
			}
			_ = s.pop()

			// In backtest mode, feed SimBroker the bar close price BEFORE publishing
			// so fills use the correct price.
			if simBrokerInst != nil {
				simBrokerInst.UpdatePrice(bar.Symbol, bar.Close, bar.Time)
			}

			evt, err := domain.NewEvent(domain.EventMarketBarReceived, tenantID, envMode, bar.Time.String()+string(bar.Symbol), bar)
			if err != nil {
				log.Error().Err(err).Str("symbol", bar.Symbol.String()).Msg("failed to create MarketBarReceived event")
				continue
			}
			if err := eventBus.Publish(ctx, *evt); err != nil {
				if ctx.Err() != nil {
					break
				}
				log.Error().Err(err).Str("symbol", bar.Symbol.String()).Msg("failed to publish MarketBarReceived")
				continue
			}
			barsProcessed++
		}

		if backtestFlag {
			eventBus.WaitPending()
			if posMonSvc != nil {
				posMonSvc.EvalExitRules(minTime)
				eventBus.WaitPending()
			}
		}

		if ctx.Err() != nil {
			break
		}
		if perBarDelay > 0 {
			t := time.NewTimer(perBarDelay)
			select {
			case <-ctx.Done():
				t.Stop()
				break
			case <-t.C:
			}
		}
	}

	cancel()

	eventCounts := tracer.Counts()
	signalsMu.Lock()
	sigN := signalsGenerated
	intN := intentsGenerated
	lastSig := lastSignalSummary
	lastIntent := lastIntentSummary
	signalsMu.Unlock()

	log.Info().
		Int("bars_processed", barsProcessed).
		Int("timestamp_groups", groupsProcessed).
		Int("signals", sigN).
		Int("order_intents", intN).
		Msg("replay complete")

	fmt.Println("\n=== REPLAY SUMMARY ===")
	fmt.Printf("Bars processed: %d\n", barsProcessed)
	fmt.Printf("Timestamp groups: %d\n", groupsProcessed)
	fmt.Printf("Signals created: %d\n", sigN)
	fmt.Printf("Order intents created: %d\n", intN)
	if lastSig != "" {
		fmt.Printf("Last signal payload type: %s\n", lastSig)
	}
	if lastIntent != "" {
		fmt.Printf("Last intent payload type: %s\n", lastIntent)
	}
	fmt.Println("\nEvents by type:")
	keys := make([]string, 0, len(eventCounts))
	for k := range eventCounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("- %s: %d\n", k, eventCounts[k])
	}

	// Backtest results.
	if backtestFlag && collectorInst != nil {
		result := collectorInst.Result()
		result.PrintReport()
		if outputJSON != "" {
			if err := result.WriteJSON(outputJSON); err != nil {
				log.Error().Err(err).Str("path", outputJSON).Msg("failed to write backtest JSON")
			} else {
				log.Info().Str("path", outputJSON).Msg("backtest results written to JSON")
			}
		}
	}
}

type barStream struct {
	symbol domain.Symbol
	bars   []domain.MarketBar
	idx    int
}

func (s *barStream) peek() (domain.MarketBar, bool) {
	if s == nil || s.idx >= len(s.bars) {
		return domain.MarketBar{}, false
	}
	return s.bars[s.idx], true
}

func (s *barStream) pop() bool {
	if s == nil || s.idx >= len(s.bars) {
		return false
	}
	s.idx++
	return true
}

func nextMinTime(streams []*barStream) (time.Time, bool) {
	var min time.Time
	found := false
	for _, s := range streams {
		b, ok := s.peek()
		if !ok {
			continue
		}
		if !found || b.Time.Before(min) {
			min = b.Time
			found = true
		}
	}
	return min, found
}

type eventTracer struct {
	log   zerolog.Logger
	mu    sync.Mutex
	seq   uint64
	count map[string]int
}

func newEventTracer(log zerolog.Logger) *eventTracer {
	return &eventTracer{log: log, count: make(map[string]int)}
}

func (t *eventTracer) Handle(_ context.Context, ev domain.Event) error {
	t.mu.Lock()
	t.seq++
	seq := t.seq
	t.count[ev.Type]++
	t.mu.Unlock()

	l := t.log.With().
		Uint64("seq", seq).
		Str("type", ev.Type).
		Time("occurred_at", ev.OccurredAt).
		Str("tenant", ev.TenantID).
		Str("env", ev.EnvMode.String()).
		Str("idempotency", ev.IdempotencyKey).
		Logger()

	switch p := ev.Payload.(type) {
	case domain.MarketBar:
		l.Info().
			Str("symbol", p.Symbol.String()).
			Str("timeframe", p.Timeframe.String()).
			Time("bar_time", p.Time).
			Float64("close", p.Close).
			Float64("volume", p.Volume).
			Msg("event")
	case domain.IndicatorSnapshot:
		l.Info().
			Str("symbol", p.Symbol.String()).
			Str("timeframe", p.Timeframe.String()).
			Time("snapshot_time", p.Time).
			Float64("rsi", p.RSI).
			Float64("vwap", p.VWAP).
			Msg("event")
	case monitor.SetupCondition:
		l.Info().
			Str("symbol", p.Symbol.String()).
			Str("timeframe", p.Timeframe.String()).
			Str("direction", p.Direction.String()).
			Str("trigger", p.Trigger).
			Float64("confidence", p.Confidence).
			Msg("event")
	case start.Signal:
		l.Info().
			Str("instance_id", p.StrategyInstanceID.String()).
			Str("symbol", p.Symbol).
			Str("type", p.Type.String()).
			Str("side", p.Side.String()).
			Float64("strength", p.Strength).
			Msg("event")
	case domain.OrderIntent:
		l.Info().
			Str("intent_id", p.ID.String()).
			Str("symbol", p.Symbol.String()).
			Str("direction", p.Direction.String()).
			Float64("qty", p.Quantity).
			Float64("limit", p.LimitPrice).
			Float64("stop", p.StopLoss).
			Float64("confidence", p.Confidence).
			Msg("event")
	default:
		l.Info().Str("payload_type", fmt.Sprintf("%T", ev.Payload)).Msg("event")
	}
	return nil
}

func (t *eventTracer) Counts() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int, len(t.count))
	for k, v := range t.count {
		out[k] = v
	}
	return out
}

func allEventTypes() []domain.EventType {
	return []domain.EventType{
		domain.EventMarketBarReceived,
		domain.EventMarketBarSanitized,
		domain.EventMarketBarRejected,
		domain.EventStateUpdated,
		domain.EventRegimeShifted,
		domain.EventSetupDetected,
		domain.EventDebateRequested,
		domain.EventDebateCompleted,
		domain.EventOrderIntentCreated,
		domain.EventOrderIntentValidated,
		domain.EventOrderIntentRejected,
		domain.EventOrderSubmitted,
		domain.EventOrderAccepted,
		domain.EventOrderRejected,
		domain.EventFillReceived,
		domain.EventPositionUpdated,
		domain.EventKillSwitchEngaged,
		domain.EventCircuitBreakerTripped,
		domain.EventOptionChainReceived,
		domain.EventOptionContractSelected,
		domain.EventSignalCreated,
		domain.EventSignalEnriched,
		"StrategyDomainEvent",
	}
}

func signalPassthrough(bus *memory.Bus, log zerolog.Logger) func(context.Context, domain.Event) error {
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
			Rationale:  fmt.Sprintf("passthrough (no-ai): %s %s strength=%.2f", sig.Type, sig.Side, sig.Strength),
		}
		enrichedEvt, err := domain.NewEvent(domain.EventSignalEnriched, ev.TenantID, ev.EnvMode, ev.IdempotencyKey+"-enriched", enrichment)
		if err != nil {
			log.Error().Err(err).Msg("failed to create enriched event in passthrough")
			return nil
		}
		return bus.Publish(ctx, *enrichedEvt)
	}
}

func resolveSymbols(symbolsFlag string, cfg *config.Config) []domain.Symbol {
	if strings.TrimSpace(symbolsFlag) != "" {
		parts := strings.Split(symbolsFlag, ",")
		out := make([]domain.Symbol, 0, len(parts))
		for _, p := range parts {
			s := strings.TrimSpace(p)
			if s == "" {
				continue
			}
			out = append(out, domain.Symbol(s))
		}
		return out
	}
	out := make([]domain.Symbol, 0, len(cfg.Symbols.Symbols))
	for _, s := range cfg.Symbols.Symbols {
		out = append(out, domain.Symbol(s))
	}
	return out
}

func symbolStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.String()
	}
	return out
}

func parseTimeFlag(v string) (time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format: %q", v)
}

func parseSpeed(v string) (factor float64, max bool, err error) {
	s := strings.TrimSpace(strings.ToLower(v))
	if s == "" {
		return 0, false, fmt.Errorf("speed is empty")
	}
	if s == "max" {
		return 0, true, nil
	}
	s = strings.TrimSuffix(s, "x")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false, err
	}
	if f <= 0 {
		return 0, false, fmt.Errorf("speed must be > 0")
	}
	return f, false, nil
}

func timeframeDuration(tf domain.Timeframe) (time.Duration, error) {
	switch tf {
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown timeframe: %s", tf)
	}
}
