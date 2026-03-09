package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

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
	alpacaAdapter, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger())
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter")
	}
	if cfg.Alpaca.DataAPIKeyID != "" {
		log.Info().Msg("Alpaca adapter initialized (separate data credentials)")
	} else {
		log.Info().Msg("Alpaca adapter initialized")
	}

	// TimescaleDB repository
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
	tokenStore := timescaledb.NewTokenStore(timescaledb.NewSqlDB(sqlDB))

	return &infraDeps{
		eventBus:        eventBus,
		alpacaAdapter:   alpacaAdapter,
		sqlDB:           sqlDB,
		repo:            repo,
		pnlRepo:         pnlRepo,
		dnaApprovalRepo: dnaApprovalRepo,
		tokenStore:      tokenStore,
		tracerProvider:  tp,
	}
}
