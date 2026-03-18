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
	alpacaadapter "github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
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
	"github.com/oh-my-opentrade/backend/internal/ports"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

func main() {
	var (
		symbolsFlag    string
		fromFlag       string
		toFlag         string
		speedFlag      string
		timeframeFlag  string
		strategiesFlag string
		configPath     string
		envPath        string
		backtestFlag   bool
		initialEquity  float64
		slippageBPS    int64
		outputJSON     string
		noAIFlag       bool
	)

	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols to replay (default: use config file symbols)")
	flag.StringVar(&fromFlag, "from", "", "Start time (RFC3339 or YYYY-MM-DD)")
	flag.StringVar(&toFlag, "to", "", "End time (RFC3339 or YYYY-MM-DD) (default: now)")
	flag.StringVar(&speedFlag, "speed", "max", "Replay speed: max, 1x, 10x, or any float (e.g. 2.5)")
	flag.StringVar(&timeframeFlag, "timeframe", "", "Bar timeframe: 1m, 5m, 15m, 1h (default: use config file)")
	flag.StringVar(&strategiesFlag, "strategies", "", "Comma-separated strategy IDs to run (default: all strategies)")
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
	timeframe := resolveTimeframe(timeframeFlag, cfg)
	if _, err := domain.NewTimeframe(timeframe.String()); err != nil {
		log.Fatal().Err(err).Str("timeframe", timeframe.String()).Msg("invalid timeframe")
	}
	const replayTimeframe = domain.Timeframe("1m")

	strategyIDs := resolveStrategies(strategiesFlag)
	barDur, err := timeframeDuration(replayTimeframe)
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
		Repo:       &noop.NoopRepo{},
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

	const specDir = "/home/ridopark/src/oh-my-opentrade/configs/strategies"
	var specStore portstrategy.SpecStore = store_fs.NewStore(specDir, strategy.LoadSpecFile)
	if len(strategyIDs) > 0 {
		specStore = &filteredSpecStore{inner: specStore, allowed: strategyIDs}
		log.Info().Strs("strategies", strategyIDs).Msg("strategy filter applied")
	}

	orbID, _ := start.NewStrategyID("orb_break_retest")
	if orbSpec, err := specStore.GetLatest(context.Background(), orbID); err == nil {
		monitorSvc.SetORBConfig(orbSpec.Params)
	}

	var (
		signalsMu         sync.Mutex
		signalsGenerated  int
		signalsByStrategy = make(map[string]int)
		intentsGenerated  int
		lastIntentSummary string
		simBrokerInst     *simbroker.Broker
		collectorInst     *backtest.Collector
		posMonSvc         *positionmonitor.Service
		posMonPriceCache  *positionmonitor.PriceCache
		pipeline          *bootstrap.StrategyPipeline
		alpacaAdapt       *alpacaadapter.Adapter
		optionBarsCache   map[domain.Symbol][]domain.MarketBar
		optionBarsMu      sync.Mutex
	)
	if err := eventBus.Subscribe(ctx, domain.EventSignalCreated, func(_ context.Context, ev domain.Event) error {
		signalsMu.Lock()
		defer signalsMu.Unlock()
		signalsGenerated++
		if sig, ok := ev.Payload.(start.Signal); ok {
			parts := strings.SplitN(string(sig.StrategyInstanceID), ":", 2)
			signalsByStrategy[parts[0]]++
		}
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
		posMonPriceCache = posMonBundle.PriceCache
		optionBarsCache = make(map[domain.Symbol][]domain.MarketBar)

		if cfg.Alpaca.APIKeyID != "" {
			a, alpacaErr := alpacaadapter.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca_replay").Logger())
			if alpacaErr != nil {
				log.Warn().Err(alpacaErr).Msg("backtest: failed to create alpaca adapter — options chain and bar fetching disabled")
			} else {
				alpacaAdapt = a
				if err := eventBus.Subscribe(ctx, domain.EventFillReceived, func(_ context.Context, ev domain.Event) error {
					payload, ok := ev.Payload.(map[string]any)
					if !ok {
						return nil
					}
					instrType, _ := payload["instrument_type"].(string)
					if instrType != string(domain.InstrumentTypeOption) {
						return nil
					}
					symStr, _ := payload["symbol"].(string)
					if symStr == "" {
						return nil
					}
					sym := domain.Symbol(symStr)

					go func() {
						bars, fetchErr := alpacaAdapt.GetHistoricalOptionBars(ctx, []domain.Symbol{sym}, fromTime, toTime)
						if fetchErr != nil {
							log.Warn().Err(fetchErr).Str("symbol", symStr).Msg("backtest: failed to fetch historical option bars")
							return
						}
						optionBarsMu.Lock()
						for s, b := range bars {
							optionBarsCache[s] = b
						}
						optionBarsMu.Unlock()
						log.Info().Str("symbol", symStr).Int("bars", len(bars[sym])).Msg("backtest: options bars loaded for price injection")
					}()
					return nil
				}); err != nil {
					log.Fatal().Err(err).Msg("failed to subscribe FillReceived for options bars")
				}
			}
		}

		var optionsMarket ports.OptionsMarketDataPort
		if alpacaAdapt != nil {
			optionsMarket = newCachingOptionsMarket(alpacaAdapt)
			log.Info().Msg("backtest: options chain data enabled via Alpaca (cached per symbol+right)")
		} else {
			log.Warn().Msg("backtest: no Alpaca adapter — options_ai_scalping signals will be skipped")
		}

		pipeline, err = bootstrap.BuildStrategyPipeline(bootstrap.StrategyDeps{
			EventBus:        eventBus,
			SpecStore:       specStore,
			AIAdvisor:       llm.NewNoOpAdvisor(),
			PositionLookup:  posMonBundle.Service.LookupPosition,
			MarketDataFn:    monitorSvc.GetLastSnapshot,
			OptionsMarket:   optionsMarket,
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

	loc, _ := time.LoadLocation("America/New_York")
	warmupLog := log.With().Str("component", "warmup").Logger()

	const gapThreshold = 4 * time.Hour
	if backtestFlag && alpacaAdapt != nil {
		var gapWg sync.WaitGroup
		for _, sym := range symbols {
			gapWg.Add(1)
			go func(sym domain.Symbol) {
				defer gapWg.Done()
				gaps, gapErr := repo.FindDataGaps(ctx, sym, replayTimeframe, fromTime, toTime, gapThreshold)
				if gapErr != nil {
					warmupLog.Warn().Err(gapErr).Str("symbol", sym.String()).Msg("gap detection failed")
					return
				}
				for _, g := range gaps {
					gStart := g.Start.In(loc)
					if gStart.Weekday() == time.Saturday || gStart.Weekday() == time.Sunday {
						continue
					}
					rthOpen := time.Date(gStart.Year(), gStart.Month(), gStart.Day(), 9, 30, 0, 0, loc)
					rthClose := time.Date(gStart.Year(), gStart.Month(), gStart.Day(), 16, 0, 0, 0, loc)
					if !gStart.After(rthOpen) || !g.End.In(loc).Before(rthClose) {
						continue
					}
					warmupLog.Info().Str("symbol", sym.String()).Time("start", g.Start).Time("end", g.End).Dur("duration", g.Duration).Msg("detected RTH data gap — fetching from API")
					apiBars, apiErr := alpacaAdapt.GetHistoricalBars(ctx, sym, replayTimeframe, g.Start.Add(time.Minute), g.End)
					if apiErr != nil {
						warmupLog.Warn().Err(apiErr).Str("symbol", sym.String()).Msg("failed to fetch gap bars")
						continue
					}
					if len(apiBars) > 0 {
						saved, saveErr := repo.SaveMarketBars(ctx, apiBars)
						if saveErr != nil {
							warmupLog.Warn().Err(saveErr).Msg("failed to persist gap bars")
						} else {
							warmupLog.Info().Str("symbol", sym.String()).Int("fetched", len(apiBars)).Int("saved", saved).Msg("filled RTH data gap")
						}
					}
				}
			}(sym)
		}
		gapWg.Wait()
	}

	streams := make([]*barStream, 0, len(symbols))
	firstBarTime := make(map[string]time.Time)
	for _, sym := range symbols {
		bars, err := repo.GetMarketBars(ctx, sym, replayTimeframe, fromTime, toTime)
		if err != nil {
			log.Fatal().Err(err).Str("symbol", sym.String()).Msg("failed to load market bars")
		}
		streams = append(streams, &barStream{symbol: sym, bars: bars})
		if len(bars) > 0 {
			firstBarTime[sym.String()] = bars[0].Time
		}
		log.Info().Str("symbol", sym.String()).Int("bars", len(bars)).Msg("loaded bars")
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].symbol.String() < streams[j].symbol.String() })

	const minWarmupBars = 250
	warmupBarsCache := make(map[string][]domain.MarketBar, len(symbols))
	for _, sym := range symbols {
		warmupEnd := fromTime
		if t, ok := firstBarTime[sym.String()]; ok {
			warmupEnd = t
		}
		warmupStart := warmupEnd.Add(-7 * 24 * time.Hour)

		bars, fetchErr := repo.GetMarketBars(ctx, sym, replayTimeframe, warmupStart, warmupEnd)
		if fetchErr != nil {
			warmupLog.Warn().Err(fetchErr).Str("symbol", sym.String()).Msg("warmup fetch failed")
		}
		if len(bars) < minWarmupBars && backtestFlag && alpacaAdapt != nil {
			apiFrom := warmupEnd.Add(-30 * 24 * time.Hour)
			apiBars, apiErr := alpacaAdapt.GetHistoricalBars(ctx, sym, replayTimeframe, apiFrom, warmupEnd)
			if apiErr == nil && len(apiBars) > len(bars) {
				warmupLog.Info().Str("symbol", sym.String()).Int("db_bars", len(bars)).Int("api_bars", len(apiBars)).Msg("fetched warmup bars from market data API")
				for _, b := range apiBars {
					_ = repo.SaveMarketBar(ctx, b)
				}
				bars = apiBars
			} else if apiErr != nil {
				warmupLog.Warn().Err(apiErr).Str("symbol", sym.String()).Msg("API warmup fetch failed")
			}
		}
		if len(bars) > minWarmupBars {
			bars = bars[len(bars)-minWarmupBars:]
		}
		warmupBarsCache[sym.String()] = bars
		n := monitorSvc.WarmUp(bars)
		monitorSvc.ResetSessionIndicators(sym.String())
		monitorSvc.MarkReady(sym.String())
		warmupLog.Info().Str("symbol", sym.String()).Int("warmup_bars", n).Msg("indicator warmup done")
	}

	for _, s := range streams {
		replayBars := s.bars
		if len(replayBars) > 0 {
			bridgeCount := 50
			if bridgeCount > len(replayBars) {
				bridgeCount = len(replayBars)
			}
			monitorSvc.WarmUp(replayBars[:bridgeCount])
		}
	}

	for _, sym := range symbols {
		if bars, ok := warmupBarsCache[sym.String()]; ok && len(bars) > 0 {
			ingBundle.Filter.Seed(sym, bars)
		}
	}

	fromET := fromTime.In(loc)
	replaySessionOpen := time.Date(fromET.Year(), fromET.Month(), fromET.Day(), 9, 30, 0, 0, loc)
	monitorSvc.InitAggregators(symbols, replaySessionOpen)

	if pipeline != nil && pipeline.Runner != nil {
		snapshotFn := makeSnapshotFn()
		for _, sym := range symbols {
			bars := warmupBarsCache[sym.String()]
			if len(bars) == 0 {
				continue
			}
			pipeline.Runner.WarmUp(sym.String(), bars, snapshotFn)
		}
		pipeline.Runner.InitAggregators(replaySessionOpen)
		warmupLog.Info().Time("session_open", replaySessionOpen).Msg("strategy runner HTF aggregators initialized")
		pipeline.Runner.ClearAllPendingStates()
		warmupLog.Info().Msg("strategy runner pending states cleared after warmup")

		sessionResolver := backtest.NewSessionResolver(loc)
		for _, sym := range symbols {
			if loadErr := sessionResolver.Load(ctx, sqlDB, sym, fromTime, toTime); loadErr != nil {
				warmupLog.Warn().Err(loadErr).Str("symbol", sym.String()).Msg("failed to load session data")
			}
		}

		aiResolver := strategy.NewAIAnchorResolver(llm.NewNoOpAdvisor(), nil, nil)
		aiResolver.SetSessionResolver(sessionResolver.ResolveAnchors)
		for _, sym := range symbols {
			isCrypto := strings.Contains(sym.String(), "/") || strings.HasSuffix(sym.String(), "USD")
			aiResolver.RegisterSymbol(sym.String(), isCrypto)
		}
		pipeline.Runner.SetAIAnchorResolver(aiResolver)
		warmupLog.Info().Msg("AI anchor resolver configured for replay (with session baseline)")
	}
	log.Info().Time("session_open", replaySessionOpen).Msg("MTFA aggregators initialized for replay")

	log.Info().
		Strs("symbols", symbolStrings(symbols)).
		Str("timeframe", timeframe.String()).
		Time("from", fromTime).
		Time("to", toTime).
		Str("speed", speedFlag).
		Dur("per_bar_delay", perBarDelay).
		Msg("starting replay")

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
				if posMonPriceCache != nil {
					optionBarsMu.Lock()
					for sym, bars := range optionBarsCache {
						for i := len(bars) - 1; i >= 0; i-- {
							if !bars[i].Time.After(minTime) {
								posMonPriceCache.UpdatePrice(sym, bars[i].Close, bars[i].Time)
								break
							}
						}
					}
					optionBarsMu.Unlock()
				}
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
	sigByStrat := make(map[string]int, len(signalsByStrategy))
	for k, v := range signalsByStrategy {
		sigByStrat[k] = v
	}
	intN := intentsGenerated
	lastIntent := lastIntentSummary
	signalsMu.Unlock()

	var rthSuppressed int64
	if pipeline != nil && pipeline.Runner != nil {
		rthSuppressed = pipeline.Runner.SignalsRTHSuppressed()
	}

	log.Info().
		Int("bars_processed", barsProcessed).
		Int("timestamp_groups", groupsProcessed).
		Int("signals_rth", sigN).
		Int64("signals_suppressed_rth", rthSuppressed).
		Int("order_intents", intN).
		Msg("replay complete")

	fmt.Println("\n=== REPLAY SUMMARY ===")
	fmt.Printf("Bars processed:      %d\n", barsProcessed)
	fmt.Printf("Timestamp groups:    %d\n", groupsProcessed)
	fmt.Printf("Signals (RTH):       %d\n", sigN)
	fmt.Printf("Signals suppressed:  %d  (pre-market / outside RTH)\n", rthSuppressed)
	fmt.Printf("Order intents:       %d\n", intN)
	if len(sigByStrat) > 0 {
		fmt.Println("\nSignals by strategy:")
		stratKeys := make([]string, 0, len(sigByStrat))
		for k := range sigByStrat {
			stratKeys = append(stratKeys, k)
		}
		sort.Strings(stratKeys)
		for _, k := range stratKeys {
			fmt.Printf("  %-30s %d\n", k, sigByStrat[k])
		}
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
		domain.EventSignalGated,
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

func makeSnapshotFn() func(domain.MarketBar) start.IndicatorData {
	calc := monitor.NewIndicatorCalculator()
	return func(bar domain.MarketBar) start.IndicatorData {
		snap := calc.Update(bar)
		return start.IndicatorData{
			RSI:           snap.RSI,
			StochK:        snap.StochK,
			StochD:        snap.StochD,
			EMA9:          snap.EMA9,
			EMA21:         snap.EMA21,
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

func resolveTimeframe(flag string, cfg *config.Config) domain.Timeframe {
	if strings.TrimSpace(flag) != "" {
		return domain.Timeframe(strings.TrimSpace(flag))
	}
	return domain.Timeframe(cfg.Symbols.Timeframe)
}

func resolveStrategies(flag string) []string {
	if strings.TrimSpace(flag) == "" {
		return nil
	}
	parts := strings.Split(flag, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
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

// filteredSpecStore wraps a SpecStore and only returns strategies whose IDs
// are in the allowed list. Mirrors the same pattern used by backtest.Runner.
type filteredSpecStore struct {
	inner   portstrategy.SpecStore
	allowed []string
}

func (f *filteredSpecStore) List(ctx context.Context, filter *portstrategy.SpecFilter) ([]portstrategy.Spec, error) {
	all, err := f.inner.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	allow := make(map[string]bool, len(f.allowed))
	for _, id := range f.allowed {
		allow[id] = true
	}
	var out []portstrategy.Spec
	for _, s := range all {
		if allow[string(s.ID)] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *filteredSpecStore) Get(ctx context.Context, id start.StrategyID, version start.Version) (*portstrategy.Spec, error) {
	return f.inner.Get(ctx, id, version)
}

func (f *filteredSpecStore) GetLatest(ctx context.Context, id start.StrategyID) (*portstrategy.Spec, error) {
	return f.inner.GetLatest(ctx, id)
}

func (f *filteredSpecStore) Save(ctx context.Context, spec portstrategy.Spec) error {
	return f.inner.Save(ctx, spec)
}

func (f *filteredSpecStore) Watch(ctx context.Context) (<-chan start.StrategyID, error) {
	return f.inner.Watch(ctx)
}
