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
	var (
		symbolsFlag string
		fromFlag    string
		toFlag      string
		resume      bool
		concurrency int
		batchSize   int
		configPath  string
		envPath     string
	)

	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols (default: active trading universe)")
	flag.StringVar(&fromFlag, "from", "", "Start date YYYY-MM-DD (required unless --resume)")
	flag.StringVar(&toFlag, "to", "", "End date YYYY-MM-DD (default: now)")
	flag.BoolVar(&resume, "resume", false, "Resume from last stored dark pool bar per symbol")
	flag.IntVar(&concurrency, "concurrency", 2, "Number of parallel symbol workers")
	flag.IntVar(&batchSize, "batch-size", 500, "Database batch insert size")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.Parse()

	log := logger.New(logger.Config{
		Level:  zerolog.InfoLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-backfill-darkpool").Logger()

	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Resolve symbols.
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

	// Resolve time range.
	var fromTime, toTime time.Time
	if fromFlag != "" {
		fromTime, err = time.Parse("2006-01-02", fromFlag)
		if err != nil {
			log.Fatal().Err(err).Str("from", fromFlag).Msg("invalid --from date")
		}
	} else if !resume {
		log.Fatal().Msg("--from is required unless --resume is set")
	}
	if toFlag != "" {
		toTime, err = time.Parse("2006-01-02", toFlag)
		if err != nil {
			log.Fatal().Err(err).Str("to", toFlag).Msg("invalid --to date")
		}
	} else {
		toTime = time.Now().UTC()
	}

	// Initialize Alpaca adapter (no streaming needed for backfill).
	alpacaAdapter, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger(), alpaca.WithNoStream())
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter")
	}

	// Initialize TimescaleDB.
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

	dbAdapter := timescaledb.NewSqlDB(sqlDB)
	dpRepo := timescaledb.NewDarkPoolRepo(dbAdapter, log.With().Str("component", "darkpool_repo").Logger())

	// Graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Warn().Str("signal", sig.String()).Msg("received signal, canceling...")
		cancel()
	}()

	log.Info().
		Int("symbols", len(symbols)).
		Time("from", fromTime).
		Time("to", toTime).
		Bool("resume", resume).
		Int("concurrency", concurrency).
		Msg("starting dark pool backfill")

	// Create trade fetcher adapter that bridges Alpaca → backfill.TradeTick.
	fetcher := &alpacaTradeFetcher{adapter: alpacaAdapter}

	svc := backfill.NewDarkPoolService(fetcher, dpRepo, backfill.DarkPoolConfig{
		Symbols:         symbols,
		From:            fromTime,
		To:              toTime,
		Resume:          resume,
		Concurrency:     concurrency,
		BatchSize:       batchSize,
		ContinueOnError: true,
		MaxRetries:      5,
	}, log.With().Str("component", "darkpool_backfill").Logger())

	if err := svc.Run(ctx); err != nil {
		log.Error().Err(err).Msg("dark pool backfill failed")
		os.Exit(1)
	}

	log.Info().Msg("dark pool backfill finished successfully")
}

// alpacaTradeFetcher adapts the Alpaca adapter to the backfill.TradeFetcher interface.
type alpacaTradeFetcher struct {
	adapter *alpaca.Adapter
}

func (f *alpacaTradeFetcher) GetHistoricalTrades(ctx context.Context, symbol domain.Symbol, from, to time.Time, handler func(backfill.TradeTick)) error {
	return f.adapter.GetHistoricalTrades(ctx, symbol, from, to, func(t alpaca.HistoricalTrade) {
		handler(backfill.TradeTick{
			T: t.T,
			X: t.X,
			P: t.P,
			S: t.S,
		})
	})
}
