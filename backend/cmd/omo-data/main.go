package main

import (
	"context"
	"database/sql"
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
	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/oh-my-opentrade/backend/internal/app/datarefresh"
	"github.com/oh-my-opentrade/backend/internal/app/ivcollector"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

func main() {
	var (
		configPath  string
		envPath     string
		symbolsFlag string
		runOnce     bool
	)
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols (overrides config)")
	flag.BoolVar(&runOnce, "run-once", false, "Run all tasks once and exit")
	flag.Parse()

	// Logger (same pattern as omo-ingest)
	logLevel := zerolog.InfoLevel
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if parsed, err := zerolog.ParseLevel(lvl); err == nil {
			logLevel = parsed
		}
	}
	log := logger.New(logger.Config{
		Level:  logLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-data").Logger()

	log.Info().Msg("starting")

	// Config
	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Resolve symbols
	var symbols []string
	if symbolsFlag != "" {
		for _, s := range strings.Split(symbolsFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				symbols = append(symbols, s)
			}
		}
	} else {
		symbols = cfg.Symbols.AllSymbols()
	}
	if len(symbols) == 0 {
		log.Fatal().Msg("no symbols specified")
	}

	// Database (same initDB as omo-ingest)
	sqlDB := initDB(cfg, log)
	defer sqlDB.Close()
	repo := timescaledb.NewRepositoryWithLogger(
		timescaledb.NewSqlDB(sqlDB),
		log.With().Str("component", "timescaledb").Logger(),
	)

	// Alpaca REST adapter (no streaming)
	alpacaAdapter, err := alpaca.NewAdapter(
		cfg.Alpaca,
		log.With().Str("component", "alpaca").Logger(),
		alpaca.WithNoStream(),
	)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter")
	}

	// IBKR adapter (optional — for VIX index data)
	var ibkrData ports.MarketDataPort
	if cfg.IBKR.Host != "" {
		ibkrAdapter, ibkrErr := ibkr.NewAdapter(cfg.IBKR, log.With().Str("component", "ibkr").Logger())
		if ibkrErr != nil {
			log.Warn().Err(ibkrErr).Msg("IBKR connection failed — VIX will use SPY realized vol fallback")
		} else {
			ibkrData = ibkrAdapter
			log.Info().Str("host", cfg.IBKR.Host).Int("port", cfg.IBKR.Port).Msg("IBKR connected (for VIX)")
			defer ibkrAdapter.Close()
		}
	}

	// Context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Warn().Str("signal", sig.String()).Msg("received signal, shutting down...")
		cancel()
	}()

	// datarefresh service
	refreshSvc := datarefresh.NewService(datarefresh.Config{
		VIXSymbol:      "VIX",
		IndexSymbols:   []string{"SPY", "QQQ", "IWM"},
		TradingSymbols: symbols,
		RunAtHourET:    16,
		RunAtMinuteET:  15,
		LookbackDays:   90,
	}, ibkrData, alpacaAdapter, repo, noopVIXSetter{}, log)

	if runOnce {
		log.Info().Msg("run-once mode: executing all tasks")
		refreshSvc.RefreshAll(ctx)
		log.Info().Msg("run-once complete")
		return
	}

	// Start scheduled services
	if err := refreshSvc.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to start data refresh service")
	}

	// IV collector (uses Alpaca for options data)
	ivSymbols := cfg.Symbols.SymbolsByAssetClass("EQUITY")
	if len(ivSymbols) > 0 {
		ivRepo := timescaledb.NewIVRepository(
			timescaledb.NewSqlDB(sqlDB),
			log.With().Str("component", "iv_repo").Logger(),
		)
		ivSvc := ivcollector.NewService(
			ivcollector.Config{
				Symbols:       ivSymbols,
				TargetDTE:     30,
				RunAtHourET:   16,
				RunAtMinuteET: 15,
			},
			alpacaAdapter, alpacaAdapter, ivRepo,
			log.With().Str("component", "iv_collector").Logger(),
		)
		if err := ivSvc.Start(ctx); err != nil {
			log.Warn().Err(err).Msg("IV collector failed to start")
		}
	}

	log.Info().Int("symbols", len(symbols)).Msg("omo-data running")
	<-ctx.Done()
	log.Info().Msg("omo-data stopped")
}

// noopVIXSetter satisfies datarefresh.VIXSetter when no monitor is running.
type noopVIXSetter struct{}

func (noopVIXSetter) SetVIXLevel(float64) {}

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
