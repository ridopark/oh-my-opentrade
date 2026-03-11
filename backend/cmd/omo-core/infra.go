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
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/oh-my-opentrade/backend/internal/observability/tracing"
	"github.com/rs/zerolog"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type infraDeps struct {
	eventBus        *memory.Bus
	alpacaAdapter   *alpaca.Adapter
	sqlDB           *sql.DB
	repo            *timescaledb.Repository
	pnlRepo         *timescaledb.PnLRepository
	stratPerfRepo   *timescaledb.StrategyPerfRepo
	dnaApprovalRepo *timescaledb.DNAApprovalRepo
	tokenStore      *timescaledb.TokenStore
	tracerProvider  *sdktrace.TracerProvider
}

func initLogger() zerolog.Logger {
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

	// Alpaca adapter (MarketDataPort + BrokerPort + QuoteProvider)
	var alpacaAdapter *alpaca.Adapter
	if err := retryWithBackoff(log, "alpaca_adapter", 5, 2*time.Second, 30*time.Second, func() error {
		a, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger())
		if err != nil {
			return err
		}
		alpacaAdapter = a
		return nil
	}); err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter after retries")
	}
	log.Info().Msg("Alpaca adapter initialized")

	// TimescaleDB repository
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	pgxCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse DB config")
	}
	sqlDB := stdlib.OpenDB(*pgxCfg)
	if err := retryWithBackoff(log, "timescaledb_ping", 5, 1*time.Second, 15*time.Second, func() error {
		return sqlDB.PingContext(context.Background())
	}); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to TimescaleDB after retries")
	}
	log.Info().Msg("TimescaleDB connected")

	repo := timescaledb.NewRepositoryWithLogger(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "timescaledb").Logger())
	pnlRepo := timescaledb.NewPnLRepository(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "pnl").Logger())
	stratPerfRepo := timescaledb.NewStrategyPerfRepo(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "strategy_perf").Logger())
	dnaApprovalRepo := timescaledb.NewDNAApprovalRepo(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "dna_approval_repo").Logger())
	tokenStore := timescaledb.NewTokenStore(timescaledb.NewSqlDB(sqlDB))

	return &infraDeps{
		eventBus:        eventBus,
		alpacaAdapter:   alpacaAdapter,
		sqlDB:           sqlDB,
		repo:            repo,
		pnlRepo:         pnlRepo,
		stratPerfRepo:   stratPerfRepo,
		dnaApprovalRepo: dnaApprovalRepo,
		tokenStore:      tokenStore,
		tracerProvider:  tp,
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
