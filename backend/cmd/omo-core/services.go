package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/charting"
	"github.com/oh-my-opentrade/backend/internal/adapters/llm"
	"github.com/oh-my-opentrade/backend/internal/adapters/notification"
	"github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/activation"
	"github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/app/bootstrap"
	"github.com/oh-my-opentrade/backend/internal/app/debate"
	"github.com/oh-my-opentrade/backend/internal/app/dnaapproval"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/gate"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/notify"
	"github.com/oh-my-opentrade/backend/internal/app/orchestrator"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	screenerapp "github.com/oh-my-opentrade/backend/internal/app/screener"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/app/strategywatchdog"
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
	notifier         ports.NotifierPort
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

	activationSvc     *activation.Service
	pipelineActivator *bootstrap.PipelineActivator

	orchestrator     *orchestrator.AccountOrchestrator
	strategyWatchdog *strategywatchdog.Service
	debateSvc        *debate.Service
	aiAdvisor    ports.AIAdvisorPort
	newsClient   *alpaca.NewsClient
	// kakaoNotifier *notification.KakaoNotifier — disabled

	accountEquity float64
	useStrategyV2 bool
}

func initCoreServices(cfg *config.Config, infra *infraDeps, log zerolog.Logger) *appServices {
	svc := &appServices{}

	// Ingestion (spike filter + bar writer + service)
	ingBundle, err := bootstrap.BuildIngestion(bootstrap.IngestionDeps{
		EventBus: infra.eventBus,
		Repo:     infra.repo,
		BarSaver: infra.repo,
		Logger:   log,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to build ingestion")
	}
	svc.spikeFilter = ingBundle.Filter
	svc.ingestion = ingBundle.Service
	svc.barWriter = ingBundle.BarWriter
	svc.barWriter.Start()

	// Monitor
	monitorSvc, err := bootstrap.BuildMonitor(bootstrap.MonitorDeps{
		EventBus: infra.eventBus,
		Repo:     infra.repo,
		Logger:   log,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to build monitor")
	}
	svc.monitor = monitorSvc

	// Account equity (must be fetched before building execution).
	// Use a goroutine + channel since IBKR's AccountSummary() can block indefinitely.
	svc.accountEquity = 100000.0 // fallback
	{
		type eqResult struct {
			equity float64
			err    error
		}
		ch := make(chan eqResult, 1)
		go func() {
			eq, err := infra.ibkrBroker.GetAccountEquity(context.Background())
			ch <- eqResult{eq, err}
		}()
		select {
		case res := <-ch:
			if res.err == nil {
				svc.accountEquity = res.equity
				log.Info().Float64("equity", res.equity).Msg("account equity fetched from broker")
			} else {
				log.Warn().Err(res.err).Float64("fallback_equity", svc.accountEquity).Msg("failed to fetch account equity, using fallback")
			}
		case <-time.After(10 * time.Second):
			log.Warn().Float64("fallback_equity", svc.accountEquity).Msg("IBKR account equity timed out, using fallback")
		}
	}

	// Execution guard chain (via shared bootstrap builder)
	var acctPort ports.AccountPort
	if os.Getenv("DTBP_FALLBACK") == "true" {
		acctPort = infra.ibkrBroker
		log.Info().Msg("DTBP fallback enabled — buying power guard active")
	}
	// Sprint 2 write-ahead journal — gated by cfg.OrderJournalEnabled (sourced
	// from OMO_ORDER_JOURNAL_ENABLED) so the default deploy remains byte-
	// identical to pre-Sprint-2 behavior. When the flag is unset, intentJournal
	// stays nil and the execution service skips the write-ahead/terminal
	// update calls entirely.
	var intentJournal ports.OrderIntentJournal
	if cfg.OrderJournalEnabled {
		intentJournal = infra.orderIntentRepo
		log.Info().Msg("order intent journal enabled — write-ahead audit active")
	}
	execBundle, err := bootstrap.BuildExecutionService(bootstrap.ExecutionDeps{
		EventBus:      infra.eventBus,
		Broker:        infra.ibkrBroker,
		Repo:          infra.repo,
		QuoteProvider: infra.ibkrBroker,
		AccountPort:   acctPort,
		PnLRepo:       infra.pnlRepo,
		TradeReader:   infra.repo,
		Clock:         time.Now,
		Config:        cfg,
		InitialEquity: svc.accountEquity,
		EnableOptions: true,
		BrokerName:    "ibkr",
		Logger:        log,
		IntentJournal: intentJournal,
		OptionsPrice:  infra.alpacaData,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to build execution service")
	}
	execution.WithOrderStream(infra.ibkrBroker)(execBundle.Service)
	svc.execution = execBundle.Service
	svc.ledgerWriter = execBundle.LedgerWriter
	svc.dailyLossBreaker = execBundle.DailyLossBreaker
	// Kill switch persistence — wire sink + load last persisted state so
	// operator halts survive restart. Absent row or ACTIVE = default ACTIVE.
	if infra.killSwitchRepo != nil && svc.dailyLossBreaker != nil {
		svc.dailyLossBreaker.SetSink(infra.killSwitchRepo)
		loadCtx, loadCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if row, err := infra.killSwitchRepo.LoadState(loadCtx); err != nil {
			log.Warn().Err(err).Msg("kill switch: failed to load persisted state, defaulting to ACTIVE")
		} else if row != nil && row.State != risk.KillSwitchActive {
			// Restore without re-emitting a transition event (would duplicate
			// the existing audit row). Use SetState with actor="system (restart)".
			svc.dailyLossBreaker.SetState(row.State, "restored from persisted state: "+row.Reason, "system (restart)")
		}
		loadCancel()
	}
	svc.signalTracker = perf.NewSignalTracker(infra.eventBus, infra.pnlRepo, log.With().Str("component", "signal_tracker").Logger())

	// Position monitor (price cache + exit rule evaluation, via shared bootstrap builder)
	posMonBundle, err := bootstrap.BuildPositionMonitor(bootstrap.PosMonitorDeps{
		EventBus:      infra.eventBus,
		PositionGate:  execBundle.PositionGate,
		Broker:        infra.ibkrBroker,
		Repo:          infra.repo,
		SnapshotFn:    svc.monitor.GetLastSnapshot,
		OptionsPrice:  infra.alpacaData,
		TenantID:      "default",
		EnvMode:       domain.EnvModePaper,
		Clock:         time.Now,
		IsBacktest:    false,
		Logger:        log,
		IntentJournal: intentJournal,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to build position monitor")
	}
	svc.priceCache = posMonBundle.PriceCache
	svc.posMonitor = posMonBundle.Service

	// Wire the re-peg suppression hook. Without this, cleanupPendingOrder
	// launches a dust sweep every time handleExitTimeout cancels a live
	// limit for re-peg — which is how the SOFI phantom short occurred on
	// 2026-04-16 (order 1604 re-peg cancel → dust sweep 1606 sold qty we
	// no longer owned because 1603 had filled in the cancel race).
	svc.posMonitor.SetRepegNotifier(svc.execution)

	// Wire the ATR-bucketed PREMIUM_TRAIL multiplier (2026-04-16 MRVL/SOXL
	// premature-exit fix). Default-on per quant; operators flip
	// [exits.atr_trail] enabled: false to kill-switch. Positions stamped
	// once at fill time; tick loop reads pos.CustomState["atr_trail_mult"].
	svc.posMonitor.SetATRTrailConfig(
		cfg.Exits.ATRTrail.Enabled,
		cfg.Exits.ATRTrail.ATRPeriod,
		cfg.Exits.ATRTrail.ATRLookbackDays,
		cfg.Exits.ATRTrail.ATRLookbackDaysCrypto,
		cfg.Exits.ATRTrail.MinHistoryDays,
		cfg.Exits.ATRTrail.TercileLowPctile,
		cfg.Exits.ATRTrail.TercileHighPctile,
		cfg.Exits.ATRTrail.InsufficientHistoryMultiplier,
		cfg.Exits.ATRTrail.TercileMultipliers,
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
	svc.notifier = multiNotifier
	// Wire the Discord log hook (constructed in initLogger) now that the
	// multi-notifier exists. Every ErrorLevel log from anywhere in the
	// process will fan out to Discord asynchronously.
	if discordLogHook != nil {
		discordLogHook.SetNotifier(multiNotifier)
	}
	// Position monitor was built earlier in this function (before the
	// notifier adapters existed), so wire the reconciliation notifier now
	// that the sink is available. Bootstrap reconciliation runs at Start()
	// time, well after this point, so the late-binding is safe.
	if svc.posMonitor != nil {
		svc.posMonitor.SetNotifier(multiNotifier)
	}
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

	// Wire IBKR reconnect escalation now that notifySvc exists. The ibkr
	// adapter uses the notifier to surface extended outages to Discord and
	// the fatal-halt callback to trip the global trading halt once reconnect
	// is exhausted. Both pieces live in main's process so that the adapter
	// itself stays free of execution/risk package imports.
	if infra.ibkrBroker != nil {
		infra.ibkrBroker.SetReconnectNotifier(svc.notifySvc)
		infra.ibkrBroker.SetReconnectFatalHalt(func(reason string) {
			log.Error().Str("reason", reason).Msg("ibkr: fatal halt — tripping kill switch HALTED")
			if svc.dailyLossBreaker != nil {
				svc.dailyLossBreaker.SetState(risk.KillSwitchHalted, "ibkr reconnect exhausted: "+reason, "system")
			}
		})
	}

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
		// Circuit breaker: after 5 consecutive failures skip calls for 60s.
		// Upstream LLM providers (OpenRouter) periodically return 402/5xx or
		// hang mid-stream — the breaker prevents per-bar retry storms and
		// protects callers from cumulative latency.
		advisorOpts = append(advisorOpts, llm.WithCircuitBreaker(5, 60*time.Second))
		svc.aiAdvisor = llm.NewAdvisor(cfg.AI.BaseURL, cfg.AI.Model, cfg.AI.APIKey, nil, advisorOpts...)
		svc.newsClient = alpaca.NewNewsClient(cfg.Alpaca.DataURL, cfg.Alpaca.APIKeyID, cfg.Alpaca.APISecretKey, nil)
		log.Info().
			Str("base_url", cfg.AI.BaseURL).
			Str("model", cfg.AI.Model).
			Str("provider_sort", cfg.AI.ProviderSort).
			Msg("AI advisor initialized (real LLM + news-gated)")
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

	var newsProvider strategy.NewsProvider
	if svc.newsClient != nil {
		nc := svc.newsClient
		newsProvider = func(ctx context.Context, symbol string) ([]domain.NewsItem, error) {
			return nc.GetRecentNews(ctx, symbol, 4*time.Hour)
		}
	}

	// Tide tracker for AVWAP SPY/QQQ telemetry (Phase 1, data-collection only).
	// Matches the backtest runner's warmup so live and backtest tag values are
	// derived from the same tracker configuration.
	tideTracker := gate.NewIndexTideTracker(30)

	pipeline, err := bootstrap.BuildStrategyPipeline(bootstrap.StrategyDeps{
		EventBus:        infra.eventBus,
		SpecStore:       svc.specStore,
		AIAdvisor:       svc.aiAdvisor,
		PositionLookup:  svc.posMonitor.LookupPosition,
		MarketDataFn:    svc.monitor.GetLastSnapshot,
		NewsProvider:    newsProvider,
		Repo:            infra.repo,
		StratPerf:       infra.stratPerfRepo,
		OptionsMarket:   infra.alpacaData,
		TenantID:        "default",
		EnvMode:         domain.EnvModePaper,
		Equity:          svc.accountEquity,
		Clock:           time.Now,
		DisableAI: false,
		Logger:          log,
		TideTracker:     tideTracker,
		// svc.notifier is the raw MultiNotifier (Telegram + Discord fan-out),
		// not the event-driven svc.notifySvc. Panic alerts must bypass the
		// batching pipeline and fire immediately, so we wire the raw sink.
		Notifier:       svc.notifier,
		PnLRepo:        infra.pnlRepo,
		GetPositionsFn: copytradeBrokerPositionsFn(infra.ibkrBroker, "default", domain.EnvModePaper),
	})
	if err != nil {
		log.Fatal().Err(err).Msg("strategy v2: failed to build pipeline")
	}
	svc.strategyRunner = pipeline.Runner
	svc.router = pipeline.Router
	svc.signalEnricher = pipeline.Enricher
	svc.riskSizer = pipeline.RiskSizer
	svc.lifecycleSvc = pipeline.LifecycleSvc
	svc.pipelineActivator = pipeline.Activator

	// Wire the per-position expected-loss cap (2026-04-16 MU incident fix).
	// Quant ships this enabled by default — operators flip [risk.position_cap]
	// enabled: false to kill-switch. Equity comes live from SetAccountEquity
	// calls that already run against this riskSizer, so no extra plumbing.
	svc.riskSizer.SetPositionRiskCap(cfg.Risk.PositionCap)

	// The static session resolver (session_open, pd_high, pd_low from DB) is
	// always wired. The AIAnchorResolver on top is gated by config: when
	// disabled, live takes the same resolveSessionAnchors path as backtest
	// with no_ai=true, avoiding divergence from candidate detectors and
	// fallbackRank ordering. See cfg.AI.AnchorResolverEnabled.
	loc, _ := time.LoadLocation("America/New_York")
	sessionResolver := backtest.NewSessionResolver(loc)
	svc.strategyRunner.SetAnchorResolver(sessionResolver.ResolveAnchors)
	svc.strategyRunner.SetKeyLevelPricesFn(sessionResolver.KeyLevelPrices)

	var aiAnchorResolver *strategy.AIAnchorResolver
	if cfg.AI.AnchorResolverEnabled {
		aiAnchorResolver = strategy.NewAIAnchorResolver(svc.aiAdvisor, nil, slog.Default())
		aiAnchorResolver.SetSessionResolver(sessionResolver.ResolveAnchors)
		svc.strategyRunner.SetAIAnchorResolver(aiAnchorResolver)
		log.Info().Msg("AI anchor resolver wired (cfg.ai.anchor_resolver_enabled = true)")
	} else {
		log.Info().Msg("AI anchor resolver disabled — live uses static session resolver (backtest-parity path)")
	}
	// Wire bar replay for AVWAP anchors. Returns 1m bars in [since, until)
	// so a mid-session restart can seed all anchors (including session_open)
	// with today's bars up to the current bar-time, matching the state that
	// bar-by-bar backtest accumulation produces at the same moment.
	// Shared by both the strategy runner and the monitor's standalone AVWAP.
	prevDayBarsFn := func(symbol string, since, until time.Time) []start.Bar {
		if since.IsZero() || !until.After(since) {
			return nil
		}
		rows, qErr := infra.sqlDB.QueryContext(context.Background(),
			`SELECT time, open, high, low, close, volume FROM market_bars
			 WHERE symbol = $1 AND timeframe = '1m' AND time >= $2 AND time < $3
			 ORDER BY time`, symbol, since, until)
		if qErr != nil {
			return nil
		}
		defer rows.Close()
		var bars []start.Bar
		for rows.Next() {
			var b start.Bar
			if scanErr := rows.Scan(&b.Time, &b.Open, &b.High, &b.Low, &b.Close, &b.Volume); scanErr != nil {
				continue
			}
			bars = append(bars, b)
		}
		return bars
	}
	svc.strategyRunner.SetPrevDayBarsFn(prevDayBarsFn)

	allSpecs, err := svc.specStore.List(context.Background(), nil)
	if err != nil {
		log.Fatal().Err(err).Msg("strategy v2: failed to list specs for symbol router")
	}

	anchorSymbols := make(map[string]bool)
	for _, spec := range allSpecs {
		for _, sym := range spec.Routing.Symbols {
			if !anchorSymbols[sym] {
				anchorSymbols[sym] = true
				if aiAnchorResolver != nil {
					isCrypto := strings.Contains(sym, "/") || strings.HasSuffix(sym, "USD") || strings.HasSuffix(sym, "USDT")
					aiAnchorResolver.RegisterSymbol(sym, isCrypto)
				}
			}
		}
	}

	// Pre-load session data (previous day high/low times) for AVWAP anchor resolution.
	// Cover the last 5 days to handle weekends/holidays.
	now := time.Now().In(loc)
	sessionFrom := now.AddDate(0, 0, -5)
	sessionTo := now.AddDate(0, 0, 1)
	for sym := range anchorSymbols {
		if loadErr := sessionResolver.Load(context.Background(), infra.sqlDB, domain.Symbol(sym), sessionFrom, sessionTo); loadErr != nil {
			log.Warn().Err(loadErr).Str("symbol", sym).Msg("failed to load session data for AVWAP anchors")
		}
	}

	for _, spec := range allSpecs {
		hookRef, hasHook := spec.Hooks["signals"]
		if !hasHook {
			continue
		}
		if _, err := start.NewStrategyID(hookRef.Name); err != nil {
			continue
		}

		if hookRef.Name == "copytrade_v1" {
			continue
		}

		svc.symRouterSpecs = append(svc.symRouterSpecs, symbolrouter.StrategySpec{
			Key:           spec.ID.String(),
			BaseSymbols:   spec.Routing.Symbols,
			WatchlistMode: spec.Routing.WatchlistMode,
		})
	}

	for _, spec := range allSpecs {
		svc.monitor.RegisterEMAConfig(spec.Routing.Symbols, spec.Routing.Timeframes, spec.Params)
	}

	orbID, _ := start.NewStrategyID("orb_break_retest")
	if orbSpec, err := svc.specStore.GetLatest(context.Background(), orbID); err == nil {
		svc.monitor.SetORBConfig(orbSpec.Params)
		if len(orbSpec.Routing.Timeframes) > 0 {
			svc.monitor.SetORBTimeframe(orbSpec.Routing.Timeframes[0])
		}
	}

	// Load VIX level from DB (pre-computed by omo-data service).
	{
		spySym, _ := domain.NewSymbol("SPY")
		rvFrom := time.Now().Add(-60 * 24 * time.Hour)
		spyDaily, spyErr := infra.repo.GetMarketBars(context.Background(), spySym, "1d", rvFrom, time.Now())
		if spyErr == nil && len(spyDaily) > 21 {
			rv := monitor.ComputeRealizedVol(spyDaily, 20)
			svc.monitor.SetVIXLevel(rv)
			log.Info().Float64("realized_vol", rv).Int("daily_bars", len(spyDaily)).Msg("VIX level set from DB")
		} else {
			log.Warn().Err(spyErr).Msg("no SPY daily bars in DB for VIX computation — VIX disabled")
		}
	}

	log.Info().
		Int("specs", len(allSpecs)).
		Int("symbols", len(pipeline.BaseSymbols)).
		Bool("ai_enabled", cfg.AI.Enabled).
		Msg("strategy v2 pipeline initialized (runner → enricher → riskSizer)")

	svc.symRouter = symbolrouter.NewService(
		infra.eventBus,
		svc.symRouterSpecs,
		"default",
		domain.EnvModePaper,
		log.With().Str("component", "symbolrouter").Logger(),
	)

	// Wire AVWAP function so monitor can include anchored VWAP values in enriched bar events.
	svc.monitor.SetAVWAPFn(svc.strategyRunner.GetAVWAPValues)

	// Wire standalone AVWAP computation in monitor for ALL streaming symbols.
	// This ensures newly rotated symbols have AVWAP values even before strategy assignment.
	svc.monitor.SetAnchorResolverFn(sessionResolver.ResolveAnchors)
	svc.monitor.SetPrevDayBarsFn(prevDayBarsFn)
	svc.monitor.SetAVWAPAnchors([]string{"session_open", "pd_high", "pd_low"})

	// Load session data for all base symbols (not just strategy-assigned ones)
	// so the monitor's standalone AVWAP can resolve anchors for any streaming symbol.
	for _, sym := range pipeline.BaseSymbols {
		if anchorSymbols[sym] {
			continue // already loaded above
		}
		if loadErr := sessionResolver.Load(context.Background(), infra.sqlDB, domain.Symbol(sym), sessionFrom, sessionTo); loadErr != nil {
			log.Warn().Err(loadErr).Str("symbol", sym).Msg("failed to load session data for monitor AVWAP")
		}
	}

	svc.monitor.SetBaseSymbols(pipeline.BaseSymbols)
}

// copytradeBrokerPositionsFn wraps BrokerPort.GetPositions into the narrow
// closure shape bootstrap.StrategyDeps expects. Returns nil when broker is
// absent — the bootstrap hook treats that as "skip" rather than "error".
func copytradeBrokerPositionsFn(broker ports.BrokerPort, tenantID string, envMode domain.EnvMode) func(context.Context) ([]domain.Trade, error) {
	if broker == nil {
		return nil
	}
	return func(ctx context.Context) ([]domain.Trade, error) {
		return broker.GetPositions(ctx, tenantID, envMode)
	}
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
		MarketData: infra.alpacaData,
		SpecStore:  nil, // not used directly by orchestrator
		Metrics:    nil, // wired later after metrics.New()
		Log:        log.With().Str("component", "orchestrator").Logger(),
	}
	svc.orchestrator = orchestrator.New(shared)

	// Multi-account currently requires Alpaca execution brokers per account.
	// This is incompatible with the IBKR-only broker model.
	log.Fatal().Msg("multi-account is not supported with IBKR-only broker — each account would need its own IBKR gateway")

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

		acctLedger := perf.NewLedgerWriter(infra.eventBus, infra.pnlRepo, acctAdapter, infra.repo, acctLog.With().Str("component", "ledger").Logger())
		acctLedger.SetDecayStats(infra.decayRepo)
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
			execution.WithPositionLookup(svc.posMonitor),
			execution.WithOptionsPricePort(infra.alpacaData),
		)

		// Per-account strategy pipeline reuses shared router + specStore
		acctStratLog := slog.Default()
		acctRunner := strategy.NewRunner(infra.eventBus, svc.router, acct.TenantID, domain.EnvModePaper, acctStratLog)
		acctRunner.SetPositionLookup(svc.posMonitor.LookupPosition)
		acctRiskSizer := strategy.NewRiskSizer(infra.eventBus, svc.specStore, acctEquity, acctStratLog)
		acctRiskSizer.SetPositionRiskCap(cfg.Risk.PositionCap)
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
	svc.debateSvc.SetEquity(svc.accountEquity)
	svc.debateSvc.SetSpecStore(svc.specStore)
	svc.debateSvc.SetOptionsMarket(infra.alpacaData)
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
	if err := svc.execution.Start(ctx, "default", domain.EnvModePaper); err != nil {
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
		// Subscribe enriched bar publisher AFTER the strategy runner so AVWAP values
		// are current when the handler reads them (event bus dispatches in order).
		if err := svc.monitor.StartEnrichedBarPublisher(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start enriched bar publisher")
		}

		// Persist enriched bar indicator data (EMA, AVWAP) to market_bars asynchronously
		// so /api/bars serves historical bars with full indicator data.
		_ = infra.eventBus.SubscribeAsync(ctx, domain.EventEnrichedBar, func(_ context.Context, evt domain.Event) error {
			payload, ok := evt.Payload.(domain.EnrichedBarPayload)
			if !ok {
				return nil
			}
			return infra.repo.UpdateBarIndicators(ctx,
				domain.Symbol(payload.Symbol),
				domain.Timeframe(payload.Timeframe),
				time.Unix(payload.Time, 0),
				payload.EMA9, payload.EMA21, payload.EMA50, payload.EMA200,
				payload.AVWAPs,
			)
		})
		log.Info().Msg("enriched bar persistence subscriber registered")

		// Persist aggregated HTF (5m, 15m, 30m, 1h) bars so backtests and charts
		// can read live-session HTF history back. 1m is already written by
		// ingestion, so skip it here to avoid redundant writes.
		_ = infra.eventBus.SubscribeAsync(ctx, domain.EventMarketBarSanitized, func(_ context.Context, evt domain.Event) error {
			bar, ok := evt.Payload.(domain.MarketBar)
			if !ok {
				return nil
			}
			if bar.Timeframe == "1m" {
				return nil
			}
			return infra.repo.SaveMarketBar(ctx, bar)
		})
		log.Info().Msg("HTF bar persistence subscriber registered")

		// Persist EntryGated events (blocked signals) asynchronously so the
		// dashboard's /signals page can surface "why didn't we trade" history
		// alongside the strategy-emitted lifecycle events. SubscribeAsync
		// keeps DB writes off the strategy runner's hot path; the parent ctx
		// unwinds the subscription on graceful shutdown.
		entryGatedWriter := strategy.NewEntryGatedWriter(infra.pnlRepo, log)
		if err := infra.eventBus.SubscribeAsync(ctx, domain.EventEntryGated, entryGatedWriter.Handle); err != nil {
			log.Warn().Err(err).Msg("failed to subscribe EntryGated writer — blocked signals will not persist")
		}
	}
	if svc.symRouter != nil {
		if err := svc.symRouter.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start symbol router v2")
		}
	}

	if svc.useStrategyV2 {
		actLog := log.With().Str("component", "activation").Logger()
		svc.activationSvc = activation.NewService(
			actLog,
			infra.eventBus,
			svc.monitor,
			infra.alpacaData,
			infra.ibkrBroker,
			svc.spikeFilter,
			svc.pipelineActivator,
			domain.Timeframe(cfg.Symbols.Timeframe),
		)
		// Pre-mark all configured symbols as warmed so the activation service
		// skips redundant warmup (which blocks the event bus and deadlocks startup).
		// warmupIndicators() already warmed these symbols before startServices().
		svc.activationSvc.MarkWarmed(cfg.Symbols.AllSymbols()...)
		if err := svc.activationSvc.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start activation service")
		}
		log.Info().Msg("activation service started")
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

	if svc.useStrategyV2 && svc.strategyRunner != nil {
		runner := svc.strategyRunner
		svc.strategyWatchdog = strategywatchdog.New(strategywatchdog.Deps{
			ListStrategies: func() []strategywatchdog.WatchedStrategy {
				infos := runner.ListStrategies()
				out := make([]strategywatchdog.WatchedStrategy, 0, len(infos))
				for _, info := range infos {
					out = append(out, strategywatchdog.WatchedStrategy{
						ID: info.ID, Symbols: info.Symbols, Active: info.Active,
					})
				}
				return out
			},
			LivenessFor: runner.Liveness,
			Notifier:    svc.notifier,
			Log:         log.With().Str("component", "strategy_watchdog").Logger(),
		}, strategywatchdog.Config{})
		svc.strategyWatchdog.Start(ctx)
		log.Info().Msg("strategy watchdog started")
	}

	// Seed initial DNA version detection for all loaded strategy TOMLs.
	// Without this, the DNA approval table stays empty until a file is
	// hot-reloaded, which means the DNA gate blocks strategies forever.
	for _, p := range svc.dnaPaths {
		dna, err := svc.dnaManager.Load(p)
		if err != nil {
			continue
		}
		publishDNAVersionDetected(ctx, infra.eventBus, log, p, dna.ID, svc.orchestrator != nil)
	}
	log.Info().Int("strategies", len(svc.dnaPaths)).Msg("initial DNA versions published for approval")

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
			infra.alpacaData,
			infra.alpacaData,
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

	// AI screener scheduling moved to omo-data. Bootstrap from DB to publish
	// EventAIScreenerCompleted for the symbol router.
	if cfg.AIScreener.Enabled && svc.useStrategyV2 {
		aiScreenerRepo := timescaledb.NewAIScreenerRepo(timescaledb.NewSqlDB(infra.sqlDB), log.With().Str("component", "ai_screener_repo").Logger())
		covered := screenerapp.BootstrapFromDB(
			ctx,
			aiScreenerRepo,
			svc.specStore,
			"default",
			string(domain.EnvModePaper),
			infra.eventBus,
			log.With().Str("component", "ai_screener").Logger(),
		)
		if svc.symRouter != nil {
			svc.symRouter.EmitFallbackForMissing(ctx, covered)
		}
	}

	// 13F whale accumulation is handled by omo-data service.

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
	// 5c (continued): periodic account equity refresh + equity_curve sampler.
	// Broker NetLiq is the authoritative equity source — write each sample
	// straight through to equity_curve so the dashboard reflects IBKR's own
	// account state (matching the statement number down to the penny, minus
	// drift between polls).
	// Skipped when multi-account is active — orchestrator handles per-account refresh.
	if svc.orchestrator == nil {
		go func() {
			ticker := time.NewTicker(1 * time.Minute)
			defer ticker.Stop()
			peak := svc.accountEquity
			sample := func(eq float64) {
				if eq > peak {
					peak = eq
				}
				drawdown := 0.0
				if peak > 0 {
					drawdown = (peak - eq) / peak
				}
				if infra.pnlRepo != nil {
					pt := domain.EquityPoint{
						Time:     time.Now().UTC(),
						TenantID: "default",
						EnvMode:  domain.EnvModePaper,
						Equity:   eq,
						Cash:     eq,
						Drawdown: drawdown,
					}
					if err := infra.pnlRepo.SaveEquityPoint(ctx, pt); err != nil {
						log.Warn().Err(err).Msg("failed to persist equity_curve point")
					}
				}
			}
			// Seed immediately with the startup equity so the chart has a point
			// before the first tick.
			sample(svc.accountEquity)
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if eq, err := infra.ibkrBroker.GetAccountEquity(ctx); err == nil {
						svc.execution.SetAccountEquity(eq)
						svc.strategySvc.SetAccountEquity(eq)
						if svc.riskSizer != nil {
							svc.riskSizer.SetAccountEquity(eq)
						}
						sample(eq)
						log.Info().Float64("equity", eq).Msg("account equity refreshed")
					} else {
						log.Warn().Err(err).Msg("failed to refresh account equity")
					}
				}
			}
		}()
	}

	// IV collection is now handled by omo-data service.

	// Persist auction imbalance snapshots (EventAuctionImbalance → auction_imbalances table).
	auctionRepo := timescaledb.NewAuctionImbalanceRepo(
		timescaledb.NewSqlDB(infra.sqlDB),
		log.With().Str("component", "auction_repo").Logger(),
	)
	_ = infra.eventBus.SubscribeAsync(ctx, domain.EventAuctionImbalance, func(_ context.Context, evt domain.Event) error {
		snap, ok := evt.Payload.(domain.AuctionImbalanceSnapshot)
		if !ok {
			return nil
		}
		if err := auctionRepo.SaveAuctionImbalance(ctx, snap); err != nil {
			log.Error().Err(err).
				Str("symbol", string(snap.Symbol)).
				Msg("failed to persist auction imbalance")
		}
		return nil
	})
	log.Info().Msg("auction imbalance persistence subscriber registered")
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
