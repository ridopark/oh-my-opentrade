package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/oh-my-opentrade/backend/internal/observability/tracing"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type startupReport struct {
	EMA50Succeeded  int
	EMA50Failed     []string
	EMA200Succeeded int
	EMA200Failed    []string
}

type infraDeps struct {
	eventBus        *memory.Bus
	ibkrBroker      *ibkr.Adapter
	alpacaData      *alpaca.Adapter
	sqlDB           *sql.DB
	repo            *timescaledb.Repository
	pnlRepo         *timescaledb.PnLRepository
	stratPerfRepo   *timescaledb.StrategyPerfRepo
	decayRepo       *timescaledb.DecayRepository
	dnaApprovalRepo *timescaledb.DNAApprovalRepo
	orderIntentRepo *timescaledb.OrderIntentRepo
	tokenStore      *timescaledb.TokenStore
	tracerProvider  *sdktrace.TracerProvider
	startup         startupReport
	ibkrPaperMode   bool
	streamingBroker ports.MarketDataPort
	streamingSource string
}

// discordLogHook is the package-level hook returned from initLogger so
// services.go can wire the Discord notifier once it has been built. Using
// a package variable (rather than threading the hook through multiple
// init functions) keeps the call sites unchanged.
var discordLogHook *logger.DiscordHook

func initLogger() zerolog.Logger {
	logLevel := zerolog.InfoLevel
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if parsed, err := zerolog.ParseLevel(lvl); err == nil {
			logLevel = parsed
		}
	}
	discordLogHook = logger.NewDiscordHook("default")
	log := logger.New(logger.Config{
		Level:  logLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
		Hooks:  []zerolog.Hook{discordLogHook},
	}).With().Str("service", "omo-core").Logger()

	log.Info().Msg("starting")
	return log
}

func initConfig(log zerolog.Logger) *config.Config {
	cfg, err := config.Load(".env", "configs/config.yaml")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	log.Info().Msg("config loaded")
	return cfg
}

func initInfra(cfg *config.Config, log zerolog.Logger) *infraDeps {
	tp, err := tracing.InitTracer("omo-core", "dev")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init tracer")
	}
	log.Info().Msg("OpenTelemetry tracer initialized")

	eventBus := memory.NewBus()
	log.Info().Msg("event bus initialized")

	// Alpaca adapter in REST-only mode (market data source).
	var alpacaData *alpaca.Adapter
	if err := retryWithBackoff(log, "alpaca_adapter_rest", 5, 2*time.Second, 30*time.Second, func() error {
		a, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger(), alpaca.WithNoStream())
		if err != nil {
			return err
		}
		alpacaData = a
		return nil
	}); err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter (REST mode) after retries")
	}

	// IBKR broker -- synchronous connection with retry. Fatal on failure.
	var ibkrBroker *ibkr.Adapter
	if err := retryWithBackoff(log, "ibkr_connect", 5, 2*time.Second, 30*time.Second, func() error {
		a, connectErr := ibkr.NewAdapter(cfg.IBKR, log.With().Str("component", "ibkr").Logger())
		if connectErr != nil {
			return connectErr
		}
		ibkrBroker = a
		return nil
	}); err != nil {
		log.Fatal().Err(err).
			Str("host", cfg.IBKR.Host).
			Int("port", cfg.IBKR.Port).
			Msg("IBKR Gateway not reachable — cannot start without IBKR")
	}
	log.Info().
		Str("host", cfg.IBKR.Host).
		Int("port", cfg.IBKR.Port).
		Msg("IBKR connected")

	// Publish IBKR connected event immediately.
	evt, evtErr := domain.NewEvent(
		domain.EventIBKRConnected, "system", domain.EnvModePaper,
		"ibkr-connected",
		domain.IBKRConnectedPayload{
			Host:           cfg.IBKR.Host,
			Port:           cfg.IBKR.Port,
			ClientID:       cfg.IBKR.ClientID,
			PaperMode:      cfg.IBKR.PaperMode,
			MarketDataType: cfg.IBKR.MarketDataType,
			AccountID:      cfg.IBKR.AccountID,
		},
	)
	if evtErr == nil {
		_ = eventBus.Publish(context.Background(), *evt)
	}

	// Streaming source — always create a second Alpaca adapter with streaming for bar data.
	var streamingBroker ports.MarketDataPort
	switch cfg.StreamingSource {
	case "alpaca":
		streamAlpaca, streamErr := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca_stream").Logger())
		if streamErr != nil {
			log.Fatal().Err(streamErr).Msg("failed to create Alpaca streaming adapter")
		}
		streamingBroker = streamAlpaca
		log.Info().Msg("streaming source: alpaca")
	default:
		streamingBroker = ibkrBroker
		log.Info().Str("source", cfg.StreamingSource).Msg("streaming source: " + cfg.StreamingSource)
	}

	// TimescaleDB repository
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	pgxCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse DB config")
	}
	sqlDB := stdlib.OpenDB(*pgxCfg)
	sqlDB.SetMaxOpenConns(cfg.Database.MaxPoolSize)
	sqlDB.SetMaxIdleConns(cfg.Database.MaxPoolSize)
	if err := retryWithBackoff(log, "timescaledb_ping", 5, 1*time.Second, 15*time.Second, func() error {
		return sqlDB.PingContext(context.Background())
	}); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to TimescaleDB after retries")
	}
	log.Info().Msg("TimescaleDB connected")

	repo := timescaledb.NewRepositoryWithLogger(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "timescaledb").Logger())
	pnlRepo := timescaledb.NewPnLRepository(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "pnl").Logger())
	stratPerfRepo := timescaledb.NewStrategyPerfRepo(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "strategy_perf").Logger())
	decayRepo := timescaledb.NewDecayRepository(sqlDB, log.With().Str("component", "decay").Logger())
	dnaApprovalRepo := timescaledb.NewDNAApprovalRepo(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "dna_approval_repo").Logger())
	orderIntentRepo := timescaledb.NewOrderIntentRepo(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "order_intent_repo").Logger())
	tokenStore := timescaledb.NewTokenStore(timescaledb.NewSqlDB(sqlDB))

	return &infraDeps{
		eventBus:        eventBus,
		ibkrBroker:      ibkrBroker,
		alpacaData:      alpacaData,
		sqlDB:           sqlDB,
		repo:            repo,
		pnlRepo:         pnlRepo,
		stratPerfRepo:   stratPerfRepo,
		decayRepo:       decayRepo,
		dnaApprovalRepo: dnaApprovalRepo,
		orderIntentRepo: orderIntentRepo,
		tokenStore:      tokenStore,
		tracerProvider:  tp,
		ibkrPaperMode:   cfg.IBKR.PaperMode,
		streamingBroker: streamingBroker,
		streamingSource: cfg.StreamingSource,
	}
}

// retryWithBackoff retries fn with exponential backoff. Returns nil on
// success, or the last error after maxAttempts exhausted.
func retryWithBackoff(log zerolog.Logger, desc string, maxAttempts int, initialDelay, maxDelay time.Duration, fn func() error) error {
	delay := initialDelay
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			if attempt == maxAttempts {
				break
			}
			log.Warn().Err(err).Str("operation", desc).Int("attempt", attempt).Dur("retry_in", delay).Msg("retrying after failure")
			time.Sleep(delay)
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
			continue
		}
		return nil
	}
	return lastErr
}
