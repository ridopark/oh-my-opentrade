package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/charting"
	"github.com/oh-my-opentrade/backend/internal/adapters/llm"
	"github.com/oh-my-opentrade/backend/internal/adapters/notification"
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
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	screenerapp "github.com/oh-my-opentrade/backend/internal/app/screener"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	"github.com/oh-my-opentrade/backend/internal/app/symbolrouter"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type appServices struct {
	ingestion        *ingestion.Service
	barWriter        *ingestion.AsyncBarWriter
	monitor          *monitor.Service
	execution        *execution.Service
	priceCache       *positionmonitor.PriceCache
	posMonitor       *positionmonitor.Service
	posRevaluator    *positionmonitor.Revaluator
	notifySvc        *notify.Service
	dnaApproval      *dnaapproval.Service
	ledgerWriter     *perf.LedgerWriter
	signalTracker    *perf.SignalTracker
	dailyLossBreaker *risk.DailyLossBreaker
	spikeFilter      *ingestion.AdaptiveFilter

	// Strategy v1
	dnaManager  *strategy.DNAManager
	strategySvc *strategy.Service
	dnaPaths    []string

	// Strategy v2 (nil when not enabled)
	strategyRunner *strategy.Runner
	riskSizer      *strategy.RiskSizer
	signalEnricher *strategy.SignalDebateEnricher
	lifecycleSvc   *strategy.LifecycleService
	symRouter      *symbolrouter.Service
	specStore      *store_fs.Store
	router         *strategy.Router
	symRouterSpecs []symbolrouter.StrategySpec

	orchestrator *orchestrator.AccountOrchestrator
	debateSvc    *debate.Service
	aiAdvisor    ports.AIAdvisorPort
	// kakaoNotifier *notification.KakaoNotifier — disabled

	accountEquity float64
	useStrategyV2 bool
}

func initCoreServices(cfg *config.Config, infra *infraDeps, log zerolog.Logger) *appServices {
	svc := &appServices{}

	ingestionLog := log.With().Str("component", "ingestion").Logger()
	monitorLog := log.With().Str("component", "monitor").Logger()
	executionLog := log.With().Str("component", "execution").Logger()

	svc.spikeFilter = ingestion.NewAdaptiveFilter(20, 4.0)
	svc.ingestion = ingestion.NewService(infra.eventBus, infra.repo, svc.spikeFilter, ingestionLog)

	svc.barWriter = ingestion.NewAsyncBarWriter(infra.repo, ingestionLog)
	svc.barWriter.Start()
	svc.ingestion.SetBarWriter(svc.barWriter)

	svc.monitor = monitor.NewService(infra.eventBus, infra.repo, monitorLog)

	riskEngine := execution.NewRiskEngine(cfg.Trading.MaxRiskPercent)
	slippageGuard := execution.NewSlippageGuard(infra.alpacaAdapter)
	killSwitch := execution.NewKillSwitch(
		cfg.Trading.KillSwitchMaxStops,
		cfg.Trading.KillSwitchWindow,
		cfg.Trading.KillSwitchHaltDuration,
		time.Now,
	)
	svc.accountEquity = 100000.0 // fallback
	if equity, err := infra.alpacaAdapter.GetAccountEquity(context.Background()); err == nil {
		svc.accountEquity = equity
		log.Info().Float64("equity", equity).Msg("account equity fetched from broker")
	} else {
		log.Warn().Err(err).Float64("fallback_equity", svc.accountEquity).Msg("failed to fetch account equity, using fallback")
	}
	svc.ledgerWriter = perf.NewLedgerWriter(infra.eventBus, infra.pnlRepo, infra.alpacaAdapter, svc.accountEquity, log.With().Str("component", "ledger").Logger())
	svc.signalTracker = perf.NewSignalTracker(infra.eventBus, infra.pnlRepo, log.With().Str("component", "signal_tracker").Logger())
	svc.dailyLossBreaker = risk.NewDailyLossBreaker(cfg.Trading.MaxDailyLossPct/100.0, cfg.Trading.MaxDailyLossUSD, svc.ledgerWriter, time.Now, log.With().Str("component", "daily_loss_breaker").Logger())
	positionGate := execution.NewPositionGate(infra.alpacaAdapter, executionLog)
	execOpts := []execution.Option{
		execution.WithPositionGate(positionGate),
		execution.WithOrderStream(infra.alpacaAdapter),
	}
	if os.Getenv("DTBP_FALLBACK") == "true" {
		bpGuard := execution.NewBuyingPowerGuard(infra.alpacaAdapter, executionLog)
		execOpts = append(execOpts, execution.WithBuyingPowerGuard(bpGuard))
		log.Info().Msg("DTBP fallback enabled — buying power guard active")
	}
	svc.execution = execution.NewService(
		infra.eventBus,
		infra.alpacaAdapter, // BrokerPort
		infra.repo,
		riskEngine,
		slippageGuard,
		killSwitch,
		svc.dailyLossBreaker,
		svc.accountEquity,
		executionLog,
		execOpts...,
	)

	// 5a-risk: Active position monitor (price cache + exit rule evaluation).
	priceCacheLog := log.With().Str("component", "price_cache").Logger()
	svc.priceCache = positionmonitor.NewPriceCache(priceCacheLog)
	posMonitorLog := log.With().Str("component", "position_monitor").Logger()
	svc.posMonitor = positionmonitor.NewService(infra.eventBus, svc.priceCache, positionGate, "default", domain.EnvModePaper, posMonitorLog,
		positionmonitor.WithBroker(infra.alpacaAdapter),
		positionmonitor.WithRepo(infra.repo),
	)

	// 5a-risk-reval: Position revaluator (AI-driven periodic risk re-evaluation).
	var riskAssessor ports.RiskAssessorPort
	if cfg.AI.Enabled {
		var raOpts []llm.RiskAssessorOption
		if cfg.AI.ProviderSort != "" {
			raOpts = append(raOpts, llm.WithRiskAssessorProviderRouting(cfg.AI.ProviderSort, nil))
		}
		riskAssessor = llm.NewRiskAssessor(cfg.AI.BaseURL, cfg.AI.Model, cfg.AI.APIKey, nil, raOpts...)
		log.Info().Msg("Risk assessor initialized (real LLM)")
	} else {
		riskAssessor = llm.NewNoOpRiskAssessor()
		log.Info().Msg("Risk assessor initialized (no-op — LLM disabled)")
	}
	revaluatorInterval := 5 * time.Minute
	svc.posRevaluator = positionmonitor.NewRevaluator(
		svc.posMonitor,
		riskAssessor,
		infra.eventBus,
		func(symbol string) (domain.IndicatorSnapshot, bool) {
			return svc.monitor.GetLastSnapshot(symbol)
		},
		nil,
		revaluatorInterval,
		"default",
		domain.EnvModePaper,
		log.With().Str("component", "position_revaluator").Logger(),
	)

	// 5a. Initialize notification adapters (gracefully no-op if tokens not set)
	var notifiers []ports.NotifierPort
	if cfg.Notification.TelegramBotToken != "" && cfg.Notification.TelegramChatID != "" {
		notifiers = append(notifiers, notification.NewTelegramNotifier(cfg.Notification.TelegramBotToken, cfg.Notification.TelegramChatID, nil))
		log.Info().Msg("Telegram notifier enabled")
	}
	if cfg.Notification.DiscordWebhookURL != "" {
		discordLog := log.With().Str("component", "discord").Logger()
		notifiers = append(notifiers, notification.NewDiscordNotifier(cfg.Notification.DiscordWebhookURL, nil, discordLog))
		log.Info().Msg("Discord notifier enabled")
	}
	// KakaoTalk notifier disabled — was generating persistent log noise with no token configured.
	// To re-enable, uncomment and ensure KAKAO_REST_API_KEY is set + OAuth token acquired.
	// if cfg.Notification.KakaoRestAPIKey != "" {
	// 	svc.kakaoNotifier = notification.NewKakaoNotifier(cfg.Notification.KakaoRestAPIKey, infra.tokenStore, nil)
	// 	notifiers = append(notifiers, svc.kakaoNotifier)
	// 	log.Info().Msg("KakaoTalk notifier enabled")
	// }
	multiNotifier := notification.NewMultiNotifier(notifiers...)
	notifyLog := log.With().Str("component", "notify").Logger()
	chartGen := charting.NewGonumChartGenerator()
	var notifyErr error
	svc.notifySvc, notifyErr = notify.NewService(infra.eventBus, multiNotifier, notifyLog,
		notify.WithChartGenerator(chartGen),
		notify.WithRepository(infra.repo),
	)
	if notifyErr != nil {
		log.Fatal().Err(notifyErr).Msg("failed to initialize notification service")
	}
	log.Info().Int("active", len(notifiers)).Msg("notification adapters initialized")

	svc.dnaApproval = dnaapproval.NewService(infra.dnaApprovalRepo, infra.eventBus, log.With().Str("component", "dnaapproval").Logger())
	svc.monitor.SetDNAGate(svc.dnaApproval, "orb_break_retest")

	return svc
}

func initStrategyPipeline(cfg *config.Config, infra *infraDeps, svc *appServices, log zerolog.Logger) {
	svc.useStrategyV2 = os.Getenv("STRATEGY_V2") == "true"

	// v1 (legacy) — always create so the /strategies/current endpoint works
	svc.dnaManager = strategy.NewDNAManager()
	svc.strategySvc = strategy.NewService(infra.eventBus)
	svc.strategySvc.SetAccountEquity(svc.accountEquity)
	// Load ALL strategy DNA TOML files from configs/strategies/
	svc.dnaPaths, _ = filepath.Glob("configs/strategies/*.toml")
	for _, p := range svc.dnaPaths {
		dna, err := svc.dnaManager.Load(p)
		if err != nil {
			log.Warn().Err(err).Str("path", p).Msg("failed to load strategy DNA")
			continue
		}
		log.Info().Str("strategy_id", dna.ID).Str("version", dna.Version).Str("path", p).Msg("strategy DNA loaded")
		// v1 backward compat: register ORB DNA with legacy services
		if dna.ID == "orb_break_retest" {
			svc.strategySvc.RegisterDNA(dna)
			svc.monitor.SetORBConfig(dna.Parameters)
		}
	}

	// Create AI advisor port — used by both v2 SignalDebateEnricher and v1 debate.Service.
	if cfg.AI.Enabled {
		var advisorOpts []llm.AdvisorOption
		if cfg.AI.ProviderSort != "" {
			advisorOpts = append(advisorOpts, llm.WithProviderRouting(cfg.AI.ProviderSort, nil))
		}
		svc.aiAdvisor = llm.NewAdvisor(cfg.AI.BaseURL, cfg.AI.Model, cfg.AI.APIKey, nil, advisorOpts...)
		log.Info().
			Str("base_url", cfg.AI.BaseURL).
			Str("model", cfg.AI.Model).
			Str("provider_sort", cfg.AI.ProviderSort).
			Msg("AI advisor initialized (real LLM)")
	} else {
		svc.aiAdvisor = llm.NewNoOpAdvisor()
		log.Info().Msg("AI advisor initialized (no-op — LLM disabled)")
	}

	if !svc.useStrategyV2 {
		return
	}

	// v2 — new StrategyRunner + RiskSizer + SignalDebateEnricher pipeline (feature-flagged)
	const specDir = "configs/strategies"
	svc.specStore = store_fs.NewStore(specDir, strategy.LoadSpecFile)
	svc.posMonitor.SetSpecStore(svc.specStore)

	// Register all builtin strategies in the in-memory registry.
	registry := strategy.NewMemRegistry()
	for _, s := range []start.Strategy{
		builtin.NewORBStrategy(),
		builtin.NewAVWAPStrategy(),
		builtin.NewAIScalperStrategy(),
	} {
		if err := registry.Register(s); err != nil {
			log.Fatal().Err(err).Str("strategy", s.Meta().ID.String()).Msg("strategy v2: failed to register builtin strategy")
		}
	}

	// Load all specs from the store.
	allSpecs, err := svc.specStore.List(context.Background(), nil)
	if err != nil {
		log.Fatal().Err(err).Msg("strategy v2: failed to list specs")
	}
	if len(allSpecs) == 0 {
		log.Fatal().Msg("strategy v2: no strategy specs found")
	}

	// Create the router and wire instances for each spec × symbol.
	svc.router = strategy.NewRouter()
	stratLog := slog.Default()
	allSymbols := make(map[string]struct{})
	totalInstances := 0

	for _, spec := range allSpecs {
		impl, err := registry.Get(spec.ID)
		if err != nil {
			log.Warn().Str("spec_id", spec.ID.String()).Msg("strategy v2: no builtin implementation for spec, skipping")
			continue
		}

		for _, sym := range spec.Routing.Symbols {
			instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", spec.ID, spec.Version, sym))
			inst := strategy.NewInstance(instanceID, impl, spec.Params, strategy.InstanceAssignment{
				Symbols:  []string{sym},
				Priority: spec.Routing.Priority,
			}, start.LifecycleLiveActive, stratLog)
			initCtx := strategy.NewContext(time.Now(), stratLog, nil)
			if err := inst.InitSymbol(initCtx, sym, nil); err != nil {
				log.Fatal().Err(err).
					Str("strategy", spec.ID.String()).
					Str("symbol", sym).
					Msg("strategy v2: failed to init symbol")
			}
			svc.router.Register(inst)
			allSymbols[sym] = struct{}{}
			totalInstances++
		}

		svc.symRouterSpecs = append(svc.symRouterSpecs, symbolrouter.StrategySpec{
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

	svc.strategyRunner = strategy.NewRunner(infra.eventBus, svc.router, "default", domain.EnvModePaper, stratLog)
	svc.strategyRunner.SetPositionLookup(svc.posMonitor.LookupPosition)
	svc.signalEnricher = strategy.NewSignalDebateEnricher(infra.eventBus, svc.aiAdvisor, stratLog,
		strategy.WithRepository(infra.repo),
		strategy.WithMarketDataProvider(svc.monitor.GetLastSnapshot),
		strategy.WithPositionLookup(svc.posMonitor.LookupPosition),
		strategy.WithDebateTimeout(30*time.Second),
	)
	svc.riskSizer = strategy.NewRiskSizer(infra.eventBus, svc.specStore, svc.accountEquity, stratLog)
	svc.lifecycleSvc = strategy.NewLifecycleService(svc.router, stratLog)

	// Also set ORB params on monitor for backward compatibility.
	orbID, _ := start.NewStrategyID("orb_break_retest")
	if orbSpec, err := svc.specStore.GetLatest(context.Background(), orbID); err == nil {
		svc.monitor.SetORBConfig(orbSpec.Params)
	}

	log.Info().
		Int("specs", len(allSpecs)).
		Int("instances", totalInstances).
		Bool("ai_enabled", cfg.AI.Enabled).
		Msg("strategy v2 pipeline initialized (runner → enricher → riskSizer)")

	svc.symRouter = symbolrouter.NewService(
		infra.eventBus,
		svc.symRouterSpecs,
		"default",
		domain.EnvModePaper,
		log.With().Str("component", "symbolrouter").Logger(),
	)

	// Deduplicate symbols for monitor base symbols.
	baseSymbols := make([]string, 0, len(allSymbols))
	for sym := range allSymbols {
		baseSymbols = append(baseSymbols, sym)
	}
	svc.monitor.SetBaseSymbols(baseSymbols)
}

func initMultiAccount(cfg *config.Config, infra *infraDeps, svc *appServices, log zerolog.Logger) {
	if !cfg.MultiAccount || !svc.useStrategyV2 {
		return
	}

	accounts, err := config.LoadAccounts("configs/accounts.toml")
	if err != nil {
		log.Fatal().Err(err).Msg("multi-account: failed to load accounts.toml")
	}
	shared := orchestrator.SharedDeps{
		EventBus:   infra.eventBus,
		Repo:       infra.repo,
		PnLRepo:    infra.pnlRepo,
		MarketData: infra.alpacaAdapter,
		SpecStore:  nil, // not used directly by orchestrator
		Metrics:    nil, // wired later after metrics.New()
		Log:        log.With().Str("component", "orchestrator").Logger(),
	}
	svc.orchestrator = orchestrator.New(shared)

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

		acctLedger := perf.NewLedgerWriter(infra.eventBus, infra.pnlRepo, acctAdapter, acctEquity, acctLog.With().Str("component", "ledger").Logger())
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
			infra.eventBus, acctAdapter, infra.repo,
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
			execution.WithOrderStream(acctAdapter),
		)

		// Per-account strategy pipeline reuses shared router + specStore
		acctStratLog := slog.Default()
		acctRunner := strategy.NewRunner(infra.eventBus, svc.router, acct.TenantID, domain.EnvModePaper, acctStratLog)
		acctRunner.SetPositionLookup(svc.posMonitor.LookupPosition)
		acctRiskSizer := strategy.NewRiskSizer(infra.eventBus, svc.specStore, acctEquity, acctStratLog)
		acctLifecycle := strategy.NewLifecycleService(svc.router, acctStratLog)
		acctSymRouter := symbolrouter.NewService(
			infra.eventBus, svc.symRouterSpecs, acct.TenantID, domain.EnvModePaper,
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
		if err := svc.orchestrator.Add(handle); err != nil {
			log.Fatal().Err(err).Str("tenant", acct.TenantID).Msg("multi-account: failed to add account")
		}
		acctLog.Info().Str("label", acct.Label).Msg("multi-account: account wired")
	}

	log.Info().Int("accounts", len(accounts)).Msg("multi-account orchestrator initialized")
}

func initDebateService(cfg *config.Config, infra *infraDeps, svc *appServices, log zerolog.Logger) {
	if !cfg.AI.Enabled {
		log.Info().Msg("AI debate service disabled (v1 path — set LLM_ENABLED=true to enable)")
		return
	}
	debateLog := log.With().Str("component", "debate").Logger()
	svc.debateSvc = debate.NewService(infra.eventBus, svc.aiAdvisor, infra.repo, cfg.AI.MinConfidence, debateLog)
	log.Info().
		Float64("min_confidence", cfg.AI.MinConfidence).
		Msg("AI debate service enabled (v1 path)")
}

func startServices(ctx context.Context, cfg *config.Config, infra *infraDeps, svc *appServices, log zerolog.Logger) {
	if err := svc.ingestion.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start ingestion")
	}
	if err := svc.monitor.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start monitor")
	}
	if err := svc.ledgerWriter.Start(ctx, "default", domain.EnvModePaper); err != nil {
		log.Fatal().Err(err).Msg("failed to start ledger writer")
	}
	if err := svc.signalTracker.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start signal tracker")
	}
	if err := svc.execution.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start execution")
	}
	if err := svc.priceCache.Start(ctx, infra.eventBus); err != nil {
		log.Fatal().Err(err).Msg("failed to start price cache")
	}
	if err := svc.posMonitor.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start position monitor")
	}
	if err := svc.posRevaluator.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start position revaluator")
	}
	if !svc.useStrategyV2 {
		if err := svc.strategySvc.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start strategy")
		}
	}
	if svc.debateSvc != nil {
		if err := svc.debateSvc.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start debate")
		}
	}
	if svc.useStrategyV2 {
		if err := svc.strategyRunner.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start strategy runner v2")
		}
		if err := svc.signalEnricher.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start signal debate enricher v2")
		}
		if err := svc.riskSizer.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start risk sizer v2")
		}
		log.Info().Msg("v2 pipeline started: runner → enricher → riskSizer")
	}
	if svc.symRouter != nil {
		if err := svc.symRouter.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start symbol router v2")
		}
	}
	if svc.orchestrator != nil {
		if err := svc.orchestrator.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start multi-account orchestrator")
		}
	}
	if err := svc.notifySvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start notification service")
	}
	if err := svc.dnaApproval.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start dna approval service")
	}

	screenerEnabled := os.Getenv("SCREENER_ENABLED") == "true"
	if screenerEnabled {
		screenerRepo := timescaledb.NewScreenerRepo(timescaledb.NewSqlDB(infra.sqlDB), log.With().Str("component", "screener_repo").Logger())
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
			infra.eventBus,
			infra.alpacaAdapter,
			infra.alpacaAdapter,
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
	if !svc.useStrategyV2 {
		for _, p := range svc.dnaPaths {
			watchPath := p // capture for goroutine
			go svc.dnaManager.Watch(ctx, watchPath, func(updated *strategy.StrategyDNA) {
				if updated.ID == "orb_break_retest" {
					svc.strategySvc.RegisterDNA(updated)
				}
				publishDNAVersionDetected(ctx, infra.eventBus, log, watchPath, updated.ID, svc.orchestrator != nil)
				log.Info().Str("strategy_id", updated.ID).Str("version", updated.Version).Msg("strategy DNA hot-reloaded")
			})
		}
	}
	log.Info().Msg("all services started")
	// 5c (continued): periodic account equity refresh every 5 minutes.
	// Skipped when multi-account is active — orchestrator handles per-account refresh.
	if svc.orchestrator == nil {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if eq, err := infra.alpacaAdapter.GetAccountEquity(ctx); err == nil {
						svc.execution.SetAccountEquity(eq)
						svc.ledgerWriter.SetAccountEquity(eq)
						svc.strategySvc.SetAccountEquity(eq)
						if svc.riskSizer != nil {
							svc.riskSizer.SetAccountEquity(eq)
						}
						log.Info().Float64("equity", eq).Msg("account equity refreshed")
					} else {
						log.Warn().Err(err).Msg("failed to refresh account equity")
					}
				}
			}
		}()
	}
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
