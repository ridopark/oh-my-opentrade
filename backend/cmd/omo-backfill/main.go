package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/backfill"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

func main() {
	// CLI flags
	var (
		symbolsFlag     string
		timeframeFlag   string
		fromFlag        string
		toFlag          string
		resume          bool
		concurrency     int
		batchSize       int
		dryRun          bool
		continueOnError bool
		configPath      string
		envPath         string
	)

	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols to backfill (default: use config file symbols)")
	flag.StringVar(&timeframeFlag, "timeframe", "", "Timeframe: 1m, 5m, 15m, 1h, 1d (default: use config file)")
	flag.StringVar(&fromFlag, "from", "", "Start date in YYYY-MM-DD format (required unless --resume)")
	flag.StringVar(&toFlag, "to", "", "End date in YYYY-MM-DD format (default: now)")
	flag.BoolVar(&resume, "resume", false, "Resume from last stored bar per symbol")
	flag.IntVar(&concurrency, "concurrency", 2, "Number of parallel symbol workers")
	flag.IntVar(&batchSize, "batch-size", 500, "Database batch insert size")
	flag.BoolVar(&dryRun, "dry-run", false, "Fetch but do not write to database")
	flag.BoolVar(&continueOnError, "continue-on-error", false, "Continue backfilling other symbols on failure")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.Parse()

	// Logger
	log := logger.New(logger.Config{
		Level:  zerolog.InfoLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-backfill").Logger()

	// Load app config (for DB + Alpaca credentials)
	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Resolve symbols
	var symbols []domain.Symbol
	if symbolsFlag != "" {
		for _, s := range strings.Split(symbolsFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				symbols = append(symbols, domain.Symbol(s))
			}
		}
	} else {
		for _, s := range cfg.Symbols.Symbols {
			symbols = append(symbols, domain.Symbol(s))
		}
	}
	if len(symbols) == 0 {
		log.Fatal().Msg("no symbols specified — use --symbols or configure in config.yaml")
	}

	// Resolve timeframe
	tf := domain.Timeframe(cfg.Symbols.Timeframe)
	if timeframeFlag != "" {
		tf = domain.Timeframe(timeframeFlag)
	}
	// Validate timeframe
	if _, err := domain.NewTimeframe(string(tf)); err != nil {
		log.Fatal().Err(err).Str("timeframe", string(tf)).Msg("invalid timeframe")
	}

	// Resolve time range
	var fromTime, toTime time.Time
	if fromFlag != "" {
		fromTime, err = time.Parse("2006-01-02", fromFlag)
		if err != nil {
			log.Fatal().Err(err).Str("from", fromFlag).Msg("invalid --from date, expected YYYY-MM-DD")
		}
	} else if !resume {
		log.Fatal().Msg("--from is required unless --resume is set")
	}
	if toFlag != "" {
		toTime, err = time.Parse("2006-01-02", toFlag)
		if err != nil {
			log.Fatal().Err(err).Str("to", toFlag).Msg("invalid --to date, expected YYYY-MM-DD")
		}
	} else {
		toTime = time.Now().UTC()
	}

	// Initialize Alpaca adapter
	alpacaAdapter, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger())
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter")
	}

	// Initialize TimescaleDB
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

	// Build backfill config
	backfillCfg := backfill.Config{
		Symbols:         symbols,
		Timeframe:       tf,
		From:            fromTime,
		To:              toTime,
		Resume:          resume,
		Concurrency:     concurrency,
		BatchSize:       batchSize,
		DryRun:          dryRun,
		ContinueOnError: continueOnError,
		MaxRetries:      5,
	}

	// Create and run service
	svc := backfill.NewService(alpacaAdapter, repo, backfillCfg, log.With().Str("component", "backfill").Logger())

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Warn().Str("signal", sig.String()).Msg("received signal, cancelling backfill...")
		cancel()
	}()

	if err := svc.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("backfill failed")
	}
	log.Info().Msg("backfill finished successfully")
}
