package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	omhttp "github.com/oh-my-opentrade/backend/internal/adapters/http"
	"github.com/oh-my-opentrade/backend/internal/adapters/llm"
	"github.com/oh-my-opentrade/backend/internal/adapters/middleware"
	"github.com/oh-my-opentrade/backend/internal/adapters/notification"
	"github.com/oh-my-opentrade/backend/internal/adapters/sse"
	"github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/debate"
	"github.com/oh-my-opentrade/backend/internal/app/dnaapproval"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/notify"
	"github.com/oh-my-opentrade/backend/internal/app/orchestrator"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	screenerapp "github.com/oh-my-opentrade/backend/internal/app/screener"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	"github.com/oh-my-opentrade/backend/internal/app/symbolrouter"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

func main() {
	// 0. Initialize structured logger
	logLevel := zerolog.InfoLevel
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if parsed, err := zerolog.ParseLevel(lvl); err == nil {
			logLevel = parsed
		}
	}
	log := logger.New(logger.Config{
		Level:  logLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-core").Logger()

	log.Info().Msg("starting")

	// 1. Load configuration
	cfg, err := config.Load(".env", "configs/config.yaml")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	log.Info().Msg("config loaded")

	// 2. Initialize event bus
	eventBus := memory.NewBus()
	log.Info().Msg("event bus initialized")

	// 3. Initialize Alpaca adapter (MarketDataPort + BrokerPort + QuoteProvider)
	alpacaAdapter, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger())
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter")
	}
	log.Info().Msg("Alpaca adapter initialized")

	// 4. Initialize TimescaleDB repository
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
	log.Info().Msg("TimescaleDB connected")
	repo := timescaledb.NewRepositoryWithLogger(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "timescaledb").Logger())
	pnlRepo := timescaledb.NewPnLRepository(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "pnl").Logger())
	dnaApprovalRepo := timescaledb.NewDNAApprovalRepo(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "dna_approval_repo").Logger())

	// 5. Initialize application services
	ingestionLog := log.With().Str("component", "ingestion").Logger()
	monitorLog := log.With().Str("component", "monitor").Logger()
	executionLog := log.With().Str("component", "execution").Logger()

	zscoreFilter := ingestion.NewZScoreFilter(20, 4.0) // 20-bar rolling window, 4σ threshold
	ingestionSvc := ingestion.NewService(eventBus, repo, zscoreFilter, ingestionLog)

	monitorSvc := monitor.NewService(eventBus, repo, monitorLog)

	riskEngine := execution.NewRiskEngine(cfg.Trading.MaxRiskPercent)
	slippageGuard := execution.NewSlippageGuard(alpacaAdapter) // Adapter implements QuoteProvider
	killSwitch := execution.NewKillSwitch(
		cfg.Trading.KillSwitchMaxStops,
		cfg.Trading.KillSwitchWindow,
		cfg.Trading.KillSwitchHaltDuration,
		time.Now,
	)
	accountEquity := 100000.0 // fallback
	if equity, err := alpacaAdapter.GetAccountEquity(context.Background()); err == nil {
		accountEquity = equity
		log.Info().Float64("equity", equity).Msg("account equity fetched from broker")
	} else {
		log.Warn().Err(err).Float64("fallback_equity", accountEquity).Msg("failed to fetch account equity, using fallback")
	}
	ledgerWriter := perf.NewLedgerWriter(eventBus, pnlRepo, alpacaAdapter, accountEquity, log.With().Str("component", "ledger").Logger())
	signalTracker := perf.NewSignalTracker(eventBus, pnlRepo, log.With().Str("component", "signal_tracker").Logger())
	dailyLossBreaker := risk.NewDailyLossBreaker(cfg.Trading.MaxDailyLossPct/100.0, cfg.Trading.MaxDailyLossUSD, ledgerWriter, time.Now, log.With().Str("component", "daily_loss_breaker").Logger())
	positionGate := execution.NewPositionGate(alpacaAdapter, executionLog)
	executionSvc := execution.NewService(
		eventBus,
		alpacaAdapter, // BrokerPort
		repo,
		riskEngine,
		slippageGuard,
		killSwitch,
		dailyLossBreaker,
		accountEquity,
		executionLog,
		execution.WithPositionGate(positionGate),
	)

	// 5a. Initialize notification adapters (gracefully no-op if tokens not set)
	var notifiers []ports.NotifierPort
	if cfg.Notification.TelegramBotToken != "" && cfg.Notification.TelegramChatID != "" {
		notifiers = append(notifiers, notification.NewTelegramNotifier(cfg.Notification.TelegramBotToken, cfg.Notification.TelegramChatID, nil))
		log.Info().Msg("Telegram notifier enabled")
	}
	if cfg.Notification.DiscordWebhookURL != "" {
		notifiers = append(notifiers, notification.NewDiscordNotifier(cfg.Notification.DiscordWebhookURL, nil))
		log.Info().Msg("Discord notifier enabled")
	}
	multiNotifier := notification.NewMultiNotifier(notifiers...)
	notifyLog := log.With().Str("component", "notify").Logger()
	notifySvc := notify.NewService(eventBus, multiNotifier, notifyLog)
	log.Info().Int("active", len(notifiers)).Msg("notification adapters initialized")

	dnaApprovalSvc := dnaapproval.NewService(dnaApprovalRepo, eventBus, log.With().Str("component", "dnaapproval").Logger())
	monitorSvc.SetDNAGate(dnaApprovalSvc, "orb_break_retest")

	// 5b. Initialize strategy pipeline
	useStrategyV2 := os.Getenv("STRATEGY_V2") == "true"

	// v1 (legacy) — always create so the /strategies/current endpoint works
	dnaManager := strategy.NewDNAManager()
	strategySvc := strategy.NewService(eventBus)
	strategySvc.SetAccountEquity(accountEquity)
	const dnaPath = "configs/strategies/orb_break_retest.toml"
	if dna, err := dnaManager.Load(dnaPath); err == nil {
		strategySvc.RegisterDNA(dna)
		monitorSvc.SetORBConfig(dna.Parameters)
		log.Info().Str("strategy_id", dna.ID).Str("version", dna.Version).Msg("strategy DNA loaded")
	} else {
		log.Info().Err(err).Msg("no strategy DNA file found, using deterministic defaults")
	}

	// v2 — new StrategyRunner + RiskSizer pipeline (feature-flagged)
	var strategyRunner *strategy.Runner
	var riskSizer *strategy.RiskSizer
	var lifecycleSvc *strategy.LifecycleService
	var symRouter *symbolrouter.Service
	var specStore *store_fs.Store
	var router *strategy.Router
	var symRouterSpecs []symbolrouter.StrategySpec
	if useStrategyV2 {
		const specDir = "configs/strategies"
		specStore = store_fs.NewStore(specDir, strategy.LoadSpecFile)

		// Register all builtin strategies in the in-memory registry.
		registry := strategy.NewMemRegistry()
		for _, s := range []strat.Strategy{
			builtin.NewORBStrategy(),
			builtin.NewAVWAPStrategy(),
			builtin.NewAIScalperStrategy(),
		} {
			if err := registry.Register(s); err != nil {
				log.Fatal().Err(err).Str("strategy", s.Meta().ID.String()).Msg("strategy v2: failed to register builtin strategy")
			}
		}

		// Load all specs from the store.
		allSpecs, err := specStore.List(context.Background(), nil)
		if err != nil {
			log.Fatal().Err(err).Msg("strategy v2: failed to list specs")
		}
		if len(allSpecs) == 0 {
			log.Fatal().Msg("strategy v2: no strategy specs found")
		}

		// Create the router and wire instances for each spec × symbol.
		router = strategy.NewRouter()
		stratLog := slog.Default()
		// symRouterSpecs is hoisted above for multi-account access.
		allSymbols := make(map[string]struct{})
		totalInstances := 0

		for _, spec := range allSpecs {
			impl, err := registry.Get(spec.ID)
			if err != nil {
				log.Warn().Str("spec_id", spec.ID.String()).Msg("strategy v2: no builtin implementation for spec, skipping")
				continue
			}

			for _, sym := range spec.Routing.Symbols {
				instanceID, _ := strat.NewInstanceID(fmt.Sprintf("%s:%s:%s", spec.ID, spec.Version, sym))
				inst := strategy.NewInstance(instanceID, impl, spec.Params, strategy.InstanceAssignment{
					Symbols:  []string{sym},
					Priority: spec.Routing.Priority,
				}, strat.LifecycleLiveActive, stratLog)
				initCtx := strategy.NewContext(time.Now(), stratLog, nil)
				if err := inst.InitSymbol(initCtx, sym, nil); err != nil {
					log.Fatal().Err(err).
						Str("strategy", spec.ID.String()).
						Str("symbol", sym).
						Msg("strategy v2: failed to init symbol")
				}
				router.Register(inst)
				allSymbols[sym] = struct{}{}
				totalInstances++
			}

			symRouterSpecs = append(symRouterSpecs, symbolrouter.StrategySpec{
				Key:           spec.ID.String(),
				BaseSymbols:   spec.Routing.Symbols,
				WatchlistMode: spec.Routing.WatchlistMode,
			})

			log.Info().
				Str("strategy", spec.ID.String()).
				Str("version", spec.Version.String()).
				Int("symbols", len(spec.Routing.Symbols)).
				Int("priority", spec.Routing.Priority).
				Msg("strategy v2: spec loaded")
		}

		strategyRunner = strategy.NewRunner(eventBus, router, "default", domain.EnvModePaper, stratLog)
		riskSizer = strategy.NewRiskSizer(eventBus, specStore, accountEquity, stratLog)
		lifecycleSvc = strategy.NewLifecycleService(router, stratLog)

		// Also set ORB params on monitor for backward compatibility.
		orbID, _ := strat.NewStrategyID("orb_break_retest")
		if orbSpec, err := specStore.GetLatest(context.Background(), orbID); err == nil {
			monitorSvc.SetORBConfig(orbSpec.Params)
		}

		log.Info().
			Int("specs", len(allSpecs)).
			Int("instances", totalInstances).
			Msg("strategy v2 pipeline initialized")

		symRouter = symbolrouter.NewService(
			eventBus,
			symRouterSpecs,
			"default",
			domain.EnvModePaper,
			log.With().Str("component", "symbolrouter").Logger(),
		)

		// Deduplicate symbols for monitor base symbols.
		baseSymbols := make([]string, 0, len(allSymbols))
		for sym := range allSymbols {
			baseSymbols = append(baseSymbols, sym)
		}
		monitorSvc.SetBaseSymbols(baseSymbols)
	}

	// 5b-multi: Multi-account orchestrator (feature-flagged).
	var orch *orchestrator.AccountOrchestrator
	if cfg.MultiAccount && useStrategyV2 {
		accounts, err := config.LoadAccounts("configs/accounts.toml")
		if err != nil {
			log.Fatal().Err(err).Msg("multi-account: failed to load accounts.toml")
		}
		shared := orchestrator.SharedDeps{
			EventBus:   eventBus,
			Repo:       repo,
			PnLRepo:    pnlRepo,
			MarketData: alpacaAdapter,
			SpecStore:  nil, // not used directly by orchestrator
			Metrics:    nil, // wired later after metrics.New()
			Log:        log.With().Str("component", "orchestrator").Logger(),
		}
		orch = orchestrator.New(shared)

		for _, acct := range accounts {
			acctLog := log.With().Str("tenant", acct.TenantID).Logger()
			acctAlpacaCfg := acct.ToAlpacaConfig()
			acctAdapter, err := alpaca.NewAdapter(acctAlpacaCfg, acctLog.With().Str("component", "alpaca").Logger())
			if err != nil {
				log.Fatal().Err(err).Str("tenant", acct.TenantID).Msg("multi-account: failed to create Alpaca adapter")
			}

			acctEquity := 100000.0
			if eq, eqErr := acctAdapter.GetAccountEquity(context.Background()); eqErr == nil {
				acctEquity = eq
				acctLog.Info().Float64("equity", eq).Msg("account equity fetched")
			} else {
				acctLog.Warn().Err(eqErr).Float64("fallback", acctEquity).Msg("using fallback equity")
			}

			acctLedger := perf.NewLedgerWriter(eventBus, pnlRepo, acctAdapter, acctEquity, acctLog.With().Str("component", "ledger").Logger())
			acctBreaker := risk.NewDailyLossBreaker(
				cfg.Trading.MaxDailyLossPct/100.0,
				cfg.Trading.MaxDailyLossUSD,
				acctLedger,
				time.Now,
				acctLog.With().Str("component", "daily_loss_breaker").Logger(),
			)
			acctExecLog := acctLog.With().Str("component", "execution").Logger()
			acctPosGate := execution.NewPositionGate(acctAdapter, acctExecLog)
			acctExec := execution.NewService(
				eventBus, acctAdapter, repo,
				execution.NewRiskEngine(cfg.Trading.MaxRiskPercent),
				execution.NewSlippageGuard(acctAdapter),
				execution.NewKillSwitch(
					cfg.Trading.KillSwitchMaxStops,
					cfg.Trading.KillSwitchWindow,
					cfg.Trading.KillSwitchHaltDuration,
					time.Now,
				),
				acctBreaker,
				acctEquity,
				acctExecLog,
				execution.WithPositionGate(acctPosGate),
			)

			// Per-account strategy pipeline reuses shared router + specStore
			acctStratLog := slog.Default()
			acctRunner := strategy.NewRunner(eventBus, router, acct.TenantID, domain.EnvModePaper, acctStratLog)
			acctRiskSizer := strategy.NewRiskSizer(eventBus, specStore, acctEquity, acctStratLog)
			acctLifecycle := strategy.NewLifecycleService(router, acctStratLog)
			acctSymRouter := symbolrouter.NewService(
				eventBus, symRouterSpecs, acct.TenantID, domain.EnvModePaper,
				acctLog.With().Str("component", "symbolrouter").Logger(),
			)

			handle := &orchestrator.AccountHandle{
				TenantID:         acct.TenantID,
				Label:            acct.Label,
				EnvMode:          domain.EnvModePaper,
				Equity:           acctAdapter,
				Close:            acctAdapter,
				Execution:        acctExec,
				LedgerWriter:     acctLedger,
				DailyLossBreaker: acctBreaker,
				StrategyRunner:   acctRunner,
				RiskSizer:        acctRiskSizer,
				Lifecycle:        acctLifecycle,
				SymbolRouter:     acctSymRouter,
			}
			if err := orch.Add(handle); err != nil {
				log.Fatal().Err(err).Str("tenant", acct.TenantID).Msg("multi-account: failed to add account")
			}
			acctLog.Info().Str("label", acct.Label).Msg("multi-account: account wired")
		}

		log.Info().Int("accounts", len(accounts)).Msg("multi-account orchestrator initialized")
	}

	// 5c. Initialize AI debate service (only if enabled in config)
	var debateSvc *debate.Service
	if cfg.AI.Enabled {
		debateLog := log.With().Str("component", "debate").Logger()
		aiAdvisor := llm.NewAdvisor(cfg.AI.BaseURL, cfg.AI.Model, cfg.AI.APIKey, nil)
		debateSvc = debate.NewService(eventBus, aiAdvisor, repo, cfg.AI.MinConfidence, debateLog)
		log.Info().
			Float64("min_confidence", cfg.AI.MinConfidence).
			Str("base_url", cfg.AI.BaseURL).
			Msg("AI debate service enabled")
	} else {
		log.Info().Msg("AI debate service disabled (set LLM_ENABLED=true to enable)")
	}

	// 6. Start services (subscribe to event bus)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ingestionSvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start ingestion")
	}
	if err := monitorSvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start monitor")
	}
	if err := ledgerWriter.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start ledger writer")
	}
	if err := signalTracker.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start signal tracker")
	}
	if err := executionSvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start execution")
	}
	if !useStrategyV2 {
		if err := strategySvc.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start strategy")
		}
	}
	if debateSvc != nil {
		if err := debateSvc.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start debate")
		}
	}
	if useStrategyV2 {
		if err := strategyRunner.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start strategy runner v2")
		}
		if err := riskSizer.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start risk sizer v2")
		}
	}
	if symRouter != nil {
		if err := symRouter.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start symbol router v2")
		}
	}
	if orch != nil {
		if err := orch.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start multi-account orchestrator")
		}
	}
	if err := notifySvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start notification service")
	}
	if err := dnaApprovalSvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start dna approval service")
	}

	screenerEnabled := os.Getenv("SCREENER_ENABLED") == "true"
	if screenerEnabled {
		screenerRepo := timescaledb.NewScreenerRepo(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "screener_repo").Logger())
		screenerSvc, err := screenerapp.NewService(
			log.With().Str("component", "screener").Logger(),
			screenerapp.Config{
				Enabled:          true,
				RVOLLookbackDays: 20,
				TopN:             50,
				GapWeight:        1.0,
				RVOLWeight:       1.0,
				NewsWeight:       0.5,
			},
			"default",
			string(domain.EnvModePaper),
			cfg.Symbols.Symbols,
			domain.AssetClassEquity,
			eventBus,
			alpacaAdapter,
			alpacaAdapter,
			screenerRepo,
			nil,
		)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to create screener service")
		}
		if err := screenerSvc.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start screener service")
		}
	}
	// 5b (continued): hot-reload DNA after all services are started
	if !useStrategyV2 {
		go dnaManager.Watch(ctx, dnaPath, func(updated *strategy.StrategyDNA) {
			strategySvc.RegisterDNA(updated)
			publishDNAVersionDetected(ctx, eventBus, log, dnaPath, updated.ID, orch != nil)
			log.Info().Str("strategy_id", updated.ID).Str("version", updated.Version).Msg("strategy DNA hot-reloaded")
		})
	}
	log.Info().Msg("all services started")
	// 5c (continued): periodic account equity refresh every 5 minutes.
	// Skipped when multi-account is active — orchestrator handles per-account refresh.
	if orch == nil {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if eq, err := alpacaAdapter.GetAccountEquity(ctx); err == nil {
						executionSvc.SetAccountEquity(eq)
						ledgerWriter.SetAccountEquity(eq)
						strategySvc.SetAccountEquity(eq)
						if riskSizer != nil {
							riskSizer.SetAccountEquity(eq)
						}
						log.Info().Float64("equity", eq).Msg("account equity refreshed")
					} else {
						log.Warn().Err(err).Msg("failed to refresh account equity")
					}
				}
			}
		}()
	}

	// 6a. Start SSE handler — subscribes to the event bus and fans out to HTTP clients.
	sseLog := log.With().Str("component", "sse").Logger()
	sseHandler := sse.NewHandler(eventBus, sseLog)
	go func() {
		if err := sseHandler.Start(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("SSE handler error")
		}
	}()

	// 6b. Start HTTP server for the SSE endpoint.
	httpLog := log.With().Str("component", "http").Logger()
	// Initialize Prometheus metrics and instrumented mux.
	met := metrics.New("dev", "unknown", "main", useStrategyV2)

	// Wire Prometheus metrics into subsystems.
	executionSvc.SetMetrics(met)
	ingestionSvc.SetMetrics(met)
	dailyLossBreaker.SetMetrics(met)
	ledgerWriter.SetMetrics(met)
	alpacaAdapter.WSClient().SetMetrics(met)
	if useStrategyV2 {
		strategyRunner.SetMetrics(met)
	}
	if orch != nil {
		orch.SetMetrics(met)
	}
	imux := &metrics.InstrumentedMux{Mux: http.NewServeMux(), Metrics: met}
	imux.Handle("/bars", omhttp.NewBarsHandler(repo, alpacaAdapter, httpLog))
	imux.Handle("/events", sseHandler)
	imux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Health and strategy endpoints.
	healthHandler := omhttp.NewHealthHandler(httpLog,
		omhttp.DBChecker(sqlDB),
		omhttp.StaticChecker("ingestion"),
		omhttp.StaticChecker("monitor"),
		omhttp.StaticChecker("execution"),
		omhttp.StaticChecker("strategy"),
		omhttp.FeedChecker("ws_feed", func() (bool, string) {
			fh := alpacaAdapter.WSClient().FeedHealth()
			if fh.IsHealthy() {
				return true, ""
			}
			detail := fmt.Sprintf("state=%s connected=%v last_bar_age=%s", fh.State, fh.Connected, fh.LastBarAge.Round(time.Second))
			return false, detail
		}),
	)
	imux.Handle("/healthz/services", healthHandler)

	const strategyBasePath = "configs/strategies"
	strategyHandler := omhttp.NewStrategyHandler(dnaManager, strategyBasePath, httpLog)
	imux.Handle("/strategies/", strategyHandler)
	dnaApprovalHandler := omhttp.NewDNAApprovalHandler(dnaApprovalSvc, httpLog)
	imux.Handle("/api/dna/", dnaApprovalHandler)
	if useStrategyV2 {
		lifecycleHandler := omhttp.NewLifecycleHandler(lifecycleSvc, httpLog)
		imux.Handle("/strategies/v2/", lifecycleHandler)
		stratPerfHandler := omhttp.NewStrategyPerfHandler(strategyRunner, pnlRepo, httpLog)
		imux.Handle("/api/strategies/", stratPerfHandler)
	}
	imux.HandleFunc("/strategies/current", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		type dnaJSON struct {
			ID          string         `json:"id"`
			Version     string         `json:"version"`
			Description string         `json:"description,omitempty"`
			Parameters  map[string]any `json:"parameters"`
		}
		all := dnaManager.GetAll()
		if len(all) == 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no strategy loaded"}`))
			return
		}
		dna := all[0]
		json.NewEncoder(w).Encode(dnaJSON{
			ID:          dna.ID,
			Version:     dna.Version,
			Description: dna.Description,
			Parameters:  dna.Parameters,
		})
	})
	imux.HandleFunc("/orb", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		type orbJSON struct {
			Symbol    string  `json:"symbol"`
			State     string  `json:"state"`
			High      float64 `json:"orb_high"`
			Low       float64 `json:"orb_low"`
			BarCount  int     `json:"range_bar_count"`
			BreakDir  string  `json:"breakout_direction,omitempty"`
			BreakRVOL float64 `json:"breakout_rvol,omitempty"`
			Signals   int     `json:"signals_fired"`
		}
		var results []orbJSON
		for _, sym := range cfg.Symbols.Symbols {
			sess := monitorSvc.GetORBSession(sym)
			if sess == nil {
				results = append(results, orbJSON{Symbol: sym, State: "NO_SESSION"})
				continue
			}
			o := orbJSON{
				Symbol:   sess.Symbol,
				State:    string(sess.State),
				High:     sess.OrbHigh,
				Low:      sess.OrbLow,
				BarCount: sess.RangeBarCount,
				Signals:  sess.SignalsFired,
			}
			if sess.Breakout.Confirmed {
				o.BreakDir = string(sess.Breakout.Direction)
				o.BreakRVOL = sess.Breakout.RVOL
			}
			results = append(results, o)
		}
		json.NewEncoder(w).Encode(results)
	})

	// Performance dashboard API
	perfHandler := omhttp.NewPerformanceHandler(pnlRepo, repo, httpLog)
	imux.Handle("/performance/", perfHandler)
	// Historical orders API
	orderHandler := omhttp.NewOrderHandler(repo, httpLog)
	imux.Handle("/orders", orderHandler)

	imux.HandleFunc("/pnl", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		envMode := domain.EnvModePaper
		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		// Default: last 30 days
		from := today.AddDate(0, 0, -30)
		to := today
		if qFrom := r.URL.Query().Get("from"); qFrom != "" {
			if t, err := time.Parse("2006-01-02", qFrom); err == nil {
				from = t
			}
		}
		if qTo := r.URL.Query().Get("to"); qTo != "" {
			if t, err := time.Parse("2006-01-02", qTo); err == nil {
				to = t
			}
		}
		tenantID := r.URL.Query().Get("tenant")
		if tenantID == "" {
			tenantID = "default"
		}
		pnlData, err := pnlRepo.GetDailyPnL(r.Context(), tenantID, envMode, from, to)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		type pnlJSON struct {
			Date        string  `json:"date"`
			Realized    float64 `json:"realized_pnl"`
			Unrealized  float64 `json:"unrealized_pnl"`
			TradeCount  int     `json:"trade_count"`
			MaxDrawdown float64 `json:"max_drawdown"`
		}
		var results []pnlJSON
		for _, p := range pnlData {
			results = append(results, pnlJSON{
				Date:        p.Date.Format("2006-01-02"),
				Realized:    p.RealizedPnL,
				Unrealized:  p.UnrealizedPnL,
				TradeCount:  p.TradeCount,
				MaxDrawdown: p.MaxDrawdown,
			})
		}
		if results == nil {
			results = []pnlJSON{}
		}
		json.NewEncoder(w).Encode(results)
	})

	// Prometheus metrics endpoint (not instrumented by InstrumentedMux to avoid recursion).
	imux.Mux.Handle("/metrics", promhttp.HandlerFor(met.Reg, promhttp.HandlerOpts{}))
	httpServer := &http.Server{
		Addr:         ":8080",
		Handler:      middleware.AccessLog(httpLog)(imux.Mux),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 0, // SSE streams are long-lived — no write timeout
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		log.Info().Str("addr", httpServer.Addr).Msg("HTTP server listening")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("HTTP server error")
		}
	}()

	symbols := make([]domain.Symbol, len(cfg.Symbols.Symbols))
	for i, s := range cfg.Symbols.Symbols {
		symbols[i] = domain.Symbol(s)
	}
	timeframe := domain.Timeframe(cfg.Symbols.Timeframe)

	warmupLog := log.With().Str("component", "warmup").Logger()
	prevStart, prevEnd := domain.PreviousRTHSession(time.Now())
	warmupFrom := prevEnd.Add(-120 * time.Minute)
	warmupTo := prevEnd
	warmupLog.Info().
		Time("prev_session_start", prevStart).
		Time("prev_session_end", prevEnd).
		Time("warmup_from", warmupFrom).
		Time("warmup_to", warmupTo).
		Msg("warming indicators from previous RTH session")
	warmupBarsCache := make(map[string][]domain.MarketBar)
	for _, sym := range symbols {
		bars, err := alpacaAdapter.GetHistoricalBars(ctx, sym, timeframe, warmupFrom, warmupTo)
		if err != nil {
			warmupLog.Warn().Err(err).Str("symbol", string(sym)).Msg("warmup fetch failed, starting cold")
			continue
		}
		n := monitorSvc.WarmUp(bars)
		monitorSvc.ResetSessionIndicators(sym.String())
		warmupBarsCache[string(sym)] = bars
		warmupLog.Info().
			Str("symbol", string(sym)).
			Int("bars", n).
			Msg("indicator warmup complete")
	}

	var runnerWarmupCalc *monitor.IndicatorCalculator
	var runnerWarmupSnapshotFn strategy.IndicatorSnapshotFunc
	if useStrategyV2 && strategyRunner != nil {
		runnerWarmupCalc = monitor.NewIndicatorCalculator()
		runnerWarmupSnapshotFn = func(bar domain.MarketBar) strat.IndicatorData {
			snap := runnerWarmupCalc.Update(bar)
			return strat.IndicatorData{
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
			bars := warmupBarsCache[string(sym)]
			if len(bars) == 0 {
				continue
			}
			n := strategyRunner.WarmUp(string(sym), bars, runnerWarmupSnapshotFn)
			warmupLog.Info().
				Str("symbol", string(sym)).
				Int("bars", n).
				Msg("strategy runner warmup complete")
		}
	}

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("EST", -5*3600)
	}
	nowET := time.Now().In(loc)
	isWeekday := nowET.Weekday() != time.Saturday && nowET.Weekday() != time.Sunday
	isOpen := !domain.IsNYSEHoliday(nowET) && isWeekday
	todayOpen := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 9, 30, 0, 0, loc)
	if isOpen && nowET.After(todayOpen) {
		warmupLog.Info().Msg("replaying current-session bars for ORB state recovery")
		for _, sym := range symbols {
			orbBars, err := alpacaAdapter.GetHistoricalBars(ctx, sym, timeframe, todayOpen.UTC(), time.Now())
			if err != nil {
				warmupLog.Warn().Err(err).Str("symbol", string(sym)).Msg("ORB warmup fetch failed")
				continue
			}
			monitorSvc.WarmUpORB(orbBars)
			if useStrategyV2 && strategyRunner != nil {
				if runnerWarmupCalc == nil {
					runnerWarmupCalc = monitor.NewIndicatorCalculator()
				}
				if runnerWarmupSnapshotFn == nil {
					runnerWarmupSnapshotFn = func(bar domain.MarketBar) strat.IndicatorData {
						snap := runnerWarmupCalc.Update(bar)
						return strat.IndicatorData{
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
				}
				_ = strategyRunner.WarmUp(string(sym), orbBars, runnerWarmupSnapshotFn)
			}
			warmupLog.Info().
				Str("symbol", string(sym)).
				Int("bars", len(orbBars)).
				Msg("ORB warmup complete")
		}
	}

	monitorSvc.InitAggregators(symbols, todayOpen)

	log.Info().
		Strs("symbols", symbolStrings(symbols)).
		Str("timeframe", string(timeframe)).
		Msg("starting WebSocket stream")
	go func() {
		barHandler := func(bCtx context.Context, bar domain.MarketBar) error {
			barTenant := "default"
			if orch != nil {
				barTenant = "system"
			}
			evt, err := domain.NewEvent(domain.EventMarketBarReceived, barTenant, domain.EnvModePaper, bar.Time.String()+string(bar.Symbol), bar)
			if err != nil {
				log.Error().Err(err).Str("symbol", string(bar.Symbol)).Msg("failed to create bar event")
				return nil
			}
			if err := eventBus.Publish(bCtx, *evt); err != nil {
				log.Error().Err(err).Str("symbol", string(bar.Symbol)).Msg("failed to publish bar event")
			}
			return nil
		}
		if err := alpacaAdapter.StreamBars(ctx, symbols, timeframe, barHandler); err != nil {
			log.Error().Err(err).Msg("WebSocket stream error")
		}
	}()
	log.Info().Msg("ready — WebSocket streaming active")

	// 8. Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info().Str("signal", sig.String()).Msg("received signal, shutting down")

	// 9. Graceful shutdown — close WebSocket FIRST so Alpaca receives an RFC6455
	// close frame and immediately releases the session slot. Only then cancel the
	// context so the stream goroutine and services can exit cleanly.
	if orch != nil {
		orch.Stop()
	}
	if err := alpacaAdapter.Close(); err != nil {
		log.Error().Err(err).Msg("error closing Alpaca adapter")
	}
	cancel() // Cancel context to stop all services
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("HTTP server shutdown error")
	}
	if err := sqlDB.Close(); err != nil {
		log.Error().Err(err).Msg("error closing DB connection")
	}
	log.Info().Msg("shutdown complete")
}

func publishDNAVersionDetected(ctx context.Context, bus ports.EventBusPort, log zerolog.Logger, filePath, strategyKey string, multiAccount bool) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Error().Err(err).Str("path", filePath).Msg("failed to read dna toml")
		return
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	payload := dnaapproval.VersionDetectedPayload{
		StrategyKey: strategyKey,
		ContentTOML: string(data),
		ContentHash: hash,
		DetectedAt:  time.Now().UTC(),
	}
	dnaTenant := "default"
	if multiAccount {
		dnaTenant = "system"
	}
	ev, err := domain.NewEvent(domain.EventDNAVersionDetected, dnaTenant, domain.EnvModePaper, hash+"-"+strategyKey, payload)
	if err != nil {
		log.Error().Err(err).Msg("failed to create DNAVersionDetected event")
		return
	}
	if err := bus.Publish(ctx, *ev); err != nil {
		log.Error().Err(err).Msg("failed to publish DNAVersionDetected event")
	}
}

// symbolStrings converts []domain.Symbol to []string for log fields.
func symbolStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = string(s)
	}
	return out
}
