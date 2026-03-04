package main

import (
	"context"
	"fmt"
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
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/debate"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/app/notify"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/oh-my-opentrade/backend/internal/ports"
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
	executionSvc := execution.NewService(
		eventBus,
		alpacaAdapter, // BrokerPort
		repo,
		riskEngine,
		slippageGuard,
		killSwitch,
		accountEquity,
		executionLog,
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

	// 5b. Initialize strategy DNA engine
	dnaManager := strategy.NewDNAManager()
	strategySvc := strategy.NewService(eventBus)
	strategySvc.SetAccountEquity(accountEquity) // seed initial equity for position sizing
	const dnaPath = "configs/strategies/orb_break_retest.toml"
	if dna, err := dnaManager.Load(dnaPath); err == nil {
		strategySvc.RegisterDNA(dna)
		log.Info().Str("strategy_id", dna.ID).Int("version", dna.Version).Msg("strategy DNA loaded")
	} else {
		log.Info().Err(err).Msg("no strategy DNA file found, using deterministic defaults")
	}

	// 5c. Initialize AI debate service (only if enabled in config)
	var debateSvc *debate.Service
	if cfg.AI.Enabled {
		debateLog := log.With().Str("component", "debate").Logger()
		aiAdvisor := llm.NewAdvisor(cfg.AI.BaseURL, cfg.AI.Model, cfg.AI.APIKey, nil)
		debateSvc = debate.NewService(eventBus, aiAdvisor, cfg.AI.MinConfidence, debateLog)
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
	if err := executionSvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start execution")
	}
	if err := strategySvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start strategy")
	}
	if debateSvc != nil {
		if err := debateSvc.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to start debate")
		}
	}
	if err := notifySvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start notification service")
	}
	// 5b (continued): hot-reload DNA after all services are started
	go dnaManager.Watch(ctx, dnaPath, func(updated *strategy.StrategyDNA) {
		strategySvc.RegisterDNA(updated)
		log.Info().Str("strategy_id", updated.ID).Int("version", updated.Version).Msg("strategy DNA hot-reloaded")
	})
	log.Info().Msg("all services started")
	// 5c (continued): periodic account equity refresh every 5 minutes.
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
					strategySvc.SetAccountEquity(eq)
					log.Info().Float64("equity", eq).Msg("account equity refreshed")
				} else {
					log.Warn().Err(err).Msg("failed to refresh account equity")
				}
			}
		}
	}()

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
	mux := http.NewServeMux()
	mux.Handle("/bars", omhttp.NewBarsHandler(repo, alpacaAdapter, httpLog))
	mux.Handle("/events", sseHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
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
	)
	mux.Handle("/healthz/services", healthHandler)

	const strategyBasePath = "configs/strategies"
	strategyHandler := omhttp.NewStrategyHandler(dnaManager, strategyBasePath, httpLog)
	mux.Handle("/strategies/", strategyHandler)
	httpServer := &http.Server{
		Addr:         ":8080",
		Handler:      middleware.AccessLog(httpLog)(mux),
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

	// 6c. Indicator warmup — fetch last 30 bars per symbol and seed the monitor
	// calculator before streaming begins, so all indicators are live immediately.
	symbols := make([]domain.Symbol, len(cfg.Symbols.Symbols))
	for i, s := range cfg.Symbols.Symbols {
		symbols[i] = domain.Symbol(s)
	}
	timeframe := domain.Timeframe(cfg.Symbols.Timeframe)

	warmupLog := log.With().Str("component", "warmup").Logger()
	warmupFrom := time.Now().Add(-2 * time.Hour)
	warmupTo := time.Now()
	for _, sym := range symbols {
		bars, err := alpacaAdapter.GetHistoricalBars(ctx, sym, timeframe, warmupFrom, warmupTo)
		if err != nil {
			warmupLog.Warn().Err(err).Str("symbol", string(sym)).Msg("warmup fetch failed, starting cold")
			continue
		}
		n := monitorSvc.WarmUp(bars)
		warmupLog.Info().
			Str("symbol", string(sym)).
			Int("bars", n).
			Msg("indicator warmup complete")
	}

	log.Info().
		Strs("symbols", symbolStrings(symbols)).
		Str("timeframe", string(timeframe)).
		Msg("starting WebSocket stream")
	go func() {
		barHandler := func(bCtx context.Context, bar domain.MarketBar) error {
			evt, err := domain.NewEvent(domain.EventMarketBarReceived, "default", domain.EnvModePaper, bar.Time.String()+string(bar.Symbol), bar)
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

// symbolStrings converts []domain.Symbol to []string for log fields.
func symbolStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = string(s)
	}
	return out
}
