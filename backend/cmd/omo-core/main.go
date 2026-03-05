package main

import (
	"context"
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
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/app/notify"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
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
	dailyLossBreaker := risk.NewDailyLossBreaker(cfg.Trading.MaxDailyLossPct/100.0, cfg.Trading.MaxDailyLossUSD, ledgerWriter, time.Now, log.With().Str("component", "daily_loss_breaker").Logger())
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

	// 5b. Initialize strategy pipeline
	useStrategyV2 := os.Getenv("STRATEGY_V2") == "true"

	// v1 (legacy) — always create so the /strategies/current endpoint works
	dnaManager := strategy.NewDNAManager()
	strategySvc := strategy.NewService(eventBus)
	strategySvc.SetAccountEquity(accountEquity)
	const dnaPath = "configs/strategies/orb_break_retest.toml"
	if dna, err := dnaManager.Load(dnaPath); err == nil {
		strategySvc.RegisterDNA(dna)
		log.Info().Str("strategy_id", dna.ID).Str("version", dna.Version).Msg("strategy DNA loaded")
	} else {
		log.Info().Err(err).Msg("no strategy DNA file found, using deterministic defaults")
	}

	// v2 — new StrategyRunner + RiskSizer pipeline (feature-flagged)
	var strategyRunner *strategy.Runner
	var riskSizer *strategy.RiskSizer
	var lifecycleSvc *strategy.LifecycleService
	if useStrategyV2 {
		const specDir = "configs/strategies"
		specStore := store_fs.NewStore(specDir, strategy.LoadSpecFile)
		orbID, _ := strat.NewStrategyID("orb_break_retest")
		spec, err := specStore.GetLatest(context.Background(), orbID)
		if err != nil {
			log.Fatal().Err(err).Msg("strategy v2: failed to load orb_break_retest spec")
		}

		// Register the builtin ORB strategy in the in-memory registry.
		registry := strategy.NewMemRegistry()
		orbStrategy := builtin.NewORBStrategy()
		if err := registry.Register(orbStrategy); err != nil {
			log.Fatal().Err(err).Msg("strategy v2: failed to register ORB strategy")
		}

		// Create the router and wire instances for each symbol.
		router := strategy.NewRouter()
		stratLog := slog.Default()
		for _, sym := range spec.Routing.Symbols {
			instanceID, _ := strat.NewInstanceID(fmt.Sprintf("%s:%s:%s", spec.ID, spec.Version, sym))
			inst := strategy.NewInstance(instanceID, orbStrategy, spec.Params, strategy.InstanceAssignment{
				Symbols:  []string{sym},
				Priority: spec.Routing.Priority,
			}, strat.LifecycleLiveActive, stratLog)
			initCtx := strategy.NewContext(time.Now(), stratLog, nil)
			if err := inst.InitSymbol(initCtx, sym, nil); err != nil {
				log.Fatal().Err(err).Str("symbol", sym).Msg("strategy v2: failed to init symbol")
			}
			router.Register(inst)
		}

		strategyRunner = strategy.NewRunner(eventBus, router, "default", domain.EnvModePaper, stratLog)
		riskSizer = strategy.NewRiskSizer(eventBus, specStore, accountEquity, stratLog)
		lifecycleSvc = strategy.NewLifecycleService(router, stratLog)

		log.Info().
			Int("symbols", len(spec.Routing.Symbols)).
			Str("spec_version", spec.Version.String()).
			Msg("strategy v2 pipeline initialized")
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
	if err := ledgerWriter.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start ledger writer")
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
	if err := notifySvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start notification service")
	}
	// 5b (continued): hot-reload DNA after all services are started
	if !useStrategyV2 {
		go dnaManager.Watch(ctx, dnaPath, func(updated *strategy.StrategyDNA) {
			strategySvc.RegisterDNA(updated)
			log.Info().Str("strategy_id", updated.ID).Str("version", updated.Version).Msg("strategy DNA hot-reloaded")
		})
	}
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
	if useStrategyV2 {
		lifecycleHandler := omhttp.NewLifecycleHandler(lifecycleSvc, httpLog)
		imux.Handle("/strategies/v2/", lifecycleHandler)
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
		pnlData, err := pnlRepo.GetDailyPnL(r.Context(), "default", envMode, from, to)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		type pnlJSON struct {
			Date      string  `json:"date"`
			Realized  float64 `json:"realized_pnl"`
			Unrealized float64 `json:"unrealized_pnl"`
			TradeCount int     `json:"trade_count"`
			MaxDrawdown float64 `json:"max_drawdown"`
		}
		var results []pnlJSON
		for _, p := range pnlData {
			results = append(results, pnlJSON{
				Date:       p.Date.Format("2006-01-02"),
				Realized:   p.RealizedPnL,
				Unrealized: p.UnrealizedPnL,
				TradeCount: p.TradeCount,
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
	for _, sym := range symbols {
		bars, err := alpacaAdapter.GetHistoricalBars(ctx, sym, timeframe, warmupFrom, warmupTo)
		if err != nil {
			warmupLog.Warn().Err(err).Str("symbol", string(sym)).Msg("warmup fetch failed, starting cold")
			continue
		}
		n := monitorSvc.WarmUp(bars)
		monitorSvc.ResetSessionIndicators(sym.String())
		warmupLog.Info().
			Str("symbol", string(sym)).
			Int("bars", n).
			Msg("indicator warmup complete")
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
			warmupLog.Info().
				Str("symbol", string(sym)).
				Int("bars", len(orbBars)).
				Msg("ORB warmup complete")
		}
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
