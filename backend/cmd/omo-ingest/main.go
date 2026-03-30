package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/ingest"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

func main() {
	var (
		configPath string
		envPath    string
		symbolsFlag string
	)
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols (overrides config)")
	flag.Parse()

	// Logger
	logLevel := zerolog.InfoLevel
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if parsed, err := zerolog.ParseLevel(lvl); err == nil {
			logLevel = parsed
		}
	}
	log := logger.New(logger.Config{
		Level:  logLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-ingest").Logger()

	log.Info().Msg("starting")

	// Config
	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Override IBKR ClientID from env (default 3 for ingest)
	if cidStr := os.Getenv("IBKR_CLIENT_ID"); cidStr != "" {
		if cid, err := strconv.Atoi(cidStr); err == nil {
			cfg.IBKR.ClientID = cid
		}
	}
	if cfg.IBKR.ClientID == 0 {
		cfg.IBKR.ClientID = 3
	}

	// Resolve symbols
	var equitySymbols, cryptoSymbols []domain.Symbol
	if symbolsFlag != "" {
		for _, s := range strings.Split(symbolsFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				equitySymbols = append(equitySymbols, domain.Symbol(s))
			}
		}
	} else {
		for _, s := range cfg.Symbols.SymbolsByAssetClass("EQUITY") {
			equitySymbols = append(equitySymbols, domain.Symbol(s))
		}
		for _, s := range cfg.Symbols.SymbolsByAssetClass("CRYPTO") {
			cryptoSymbols = append(cryptoSymbols, domain.Symbol(s))
		}
	}
	allSymbols := make([]domain.Symbol, 0, len(equitySymbols)+len(cryptoSymbols))
	allSymbols = append(allSymbols, equitySymbols...)
	allSymbols = append(allSymbols, cryptoSymbols...)
	if len(allSymbols) == 0 {
		log.Fatal().Msg("no symbols specified — use --symbols or configure in config.yaml")
	}
	log.Info().
		Int("equity", len(equitySymbols)).
		Int("crypto", len(cryptoSymbols)).
		Strs("all", domainToStrings(allSymbols)).
		Msg("symbols resolved")

	// Database
	sqlDB := initDB(cfg, log)
	defer sqlDB.Close()
	repo := timescaledb.NewRepositoryWithLogger(
		timescaledb.NewSqlDB(sqlDB),
		log.With().Str("component", "timescaledb").Logger(),
	)

	// Alpaca adapter (REST only, for gap-fill historical data)
	alpacaAdapter, err := alpaca.NewAdapter(
		cfg.Alpaca,
		log.With().Str("component", "alpaca").Logger(),
		alpaca.WithNoStream(),
	)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter")
	}

	// Spike filter
	filter := ingestion.NewAdaptiveFilter(20, 3.0)
	for _, sym := range equitySymbols {
		filter.SetMaxDeviation(sym, 0.10)
	}
	for _, sym := range cryptoSymbols {
		filter.SetMaxDeviation(sym, 0.03)
	}

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Warn().Str("signal", sig.String()).Msg("received signal, shutting down...")
		cancel()
	}()

	// Gap-fill on startup (also seeds filter)
	log.Info().Msg("running gap-fill...")
	if err := ingest.GapFill(ctx, ingest.GapFillConfig{
		Symbols:     allSymbols,
		Timeframe:   "1m",
		MaxLookback: 7 * 24 * time.Hour,
		Concurrency: 2,
		BatchSize:   500,
	}, alpacaAdapter, repo, filter, log); err != nil {
		log.Error().Err(err).Msg("gap-fill failed (continuing with streaming)")
	}

	if ctx.Err() != nil {
		log.Info().Msg("shutdown requested during gap-fill")
		return
	}

	// Async bar writer
	writer := ingestion.NewAsyncBarWriter(
		repo,
		log,
		ingestion.WithBatchSize(50),
		ingestion.WithFlushInterval(5*time.Second),
	)
	writer.Start()
	defer writer.Close()

	// Pipeline
	sessionOpen := todaySessionOpen()
	pipeline := ingest.NewPipeline(filter, writer, equitySymbols, cryptoSymbols, sessionOpen, log)

	// Session reset goroutine: resets equity aggregators at each NYSE open
	go sessionResetLoop(ctx, pipeline, equitySymbols, log)

	// IBKR streaming
	log.Info().
		Int("client_id", cfg.IBKR.ClientID).
		Str("host", cfg.IBKR.Host).
		Int("port", cfg.IBKR.Port).
		Msg("connecting to IBKR for streaming...")

	ibkrAdapter, err := ibkr.NewAdapter(cfg.IBKR, log.With().Str("component", "ibkr").Logger())
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to IBKR")
	}

	log.Info().Int("symbols", len(allSymbols)).Msg("starting bar stream")
	if err := ibkrAdapter.StreamBars(ctx, allSymbols, "1m", pipeline.HandleBar); err != nil {
		if ctx.Err() == nil {
			log.Error().Err(err).Msg("stream error")
		}
	}

	log.Info().Msg("omo-ingest stopped")
}

func initDB(cfg *config.Config, log zerolog.Logger) *sql.DB {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	pgxCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse DB config")
	}
	sqlDB := stdlib.OpenDB(*pgxCfg)
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)

	// Retry DB connection
	delay := time.Second
	for attempt := 1; attempt <= 10; attempt++ {
		if err := sqlDB.PingContext(context.Background()); err == nil {
			log.Info().Msg("TimescaleDB connected")
			return sqlDB
		}
		log.Warn().Int("attempt", attempt).Dur("retry_in", delay).Msg("DB not ready, retrying")
		time.Sleep(delay)
		delay *= 2
		if delay > 15*time.Second {
			delay = 15 * time.Second
		}
	}
	log.Fatal().Msg("failed to connect to TimescaleDB after retries")
	return nil
}

// todaySessionOpen returns today's NYSE RTH open (09:30 ET).
func todaySessionOpen() time.Time {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("ET", -5*3600)
	}
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, loc).UTC()
}

// sessionResetLoop fires daily at 09:30 ET to reset equity aggregators.
func sessionResetLoop(ctx context.Context, pipeline *ingest.Pipeline, equitySymbols []domain.Symbol, log zerolog.Logger) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.FixedZone("ET", -5*3600)
	}

	for {
		now := time.Now().In(loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, loc)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		timer := time.NewTimer(time.Until(next))

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			sessionOpen := next.UTC()
			log.Info().Time("session_open", sessionOpen).Msg("daily session reset")
			pipeline.ResetSession(equitySymbols, sessionOpen)
		}
	}
}

func domainToStrings(syms []domain.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = string(s)
	}
	return out
}
