package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/coinbase"
	"github.com/oh-my-opentrade/backend/internal/adapters/dolthub"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/backfill"
	"github.com/oh-my-opentrade/backend/internal/app/optionsimport"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

func main() {
	var (
		symbolsFlag   string
		fromFlag      string
		toFlag        string
		timeframeFlag string
		resume        bool
		concurrency   int
		batchSize     int
		configPath    string
		envPath       string
	)

	var optionsSourceFlag string

	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols (default: active trading universe)")
	flag.StringVar(&fromFlag, "from", "", "Start date YYYY-MM-DD (required unless --resume)")
	flag.StringVar(&toFlag, "to", "", "End date YYYY-MM-DD (default: now)")
	flag.StringVar(&timeframeFlag, "timeframe", "1m", "Bar timeframe: 1m, 5m, 1h, 1d")
	flag.BoolVar(&resume, "resume", false, "Resume from last stored bar per symbol")
	flag.IntVar(&concurrency, "concurrency", 4, "Number of parallel symbol workers")
	flag.IntVar(&batchSize, "batch-size", 500, "Database batch insert size")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.StringVar(&optionsSourceFlag, "source", "dolthub", "Options chain source: dolthub | alpaca")
	flag.Parse()

	if optionsSourceFlag != "dolthub" && optionsSourceFlag != "alpaca" {
		fmt.Fprintf(os.Stderr, "invalid -source %q: must be 'dolthub' or 'alpaca'\n", optionsSourceFlag)
		os.Exit(2)
	}

	log := logger.New(logger.Config{
		Level:  zerolog.InfoLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-backfill").Logger()

	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Resolve symbols — default to config, override with --symbols.
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

	// Initialize adapters.
	alpacaAdapter, err := alpaca.NewAdapter(cfg.Alpaca, log.With().Str("component", "alpaca").Logger())
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Alpaca adapter")
	}

	coinbaseClient := coinbase.NewClient(cfg.Coinbase, log.With().Str("component", "coinbase").Logger())

	fetcher := &backfill.RoutingFetcher{
		Crypto: coinbaseClient,
		Equity: alpacaAdapter,
	}

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
	repo := timescaledb.NewRepositoryWithLogger(dbAdapter, log.With().Str("component", "timescaledb").Logger())

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
		Msg("starting backfill")

	// --- Step 1: Backfill 1m candles (all hours, RTH + pre/post market) ---
	log.Info().Msg("[1/2] backfilling 1m candles...")
	svc := backfill.NewService(fetcher, repo, backfill.Config{
		Symbols:         symbols,
		Timeframe:       domain.Timeframe(timeframeFlag),
		From:            fromTime,
		To:              toTime,
		Resume:          resume,
		Concurrency:     concurrency,
		BatchSize:       batchSize,
		ContinueOnError: true,
		MaxRetries:      5,
	}, log.With().Str("component", "backfill").Logger())

	if err := svc.Run(ctx); err != nil {
		log.Error().Err(err).Msg("candle backfill failed")
		os.Exit(1)
	}
	log.Info().Msg("[1/2] candle backfill complete")

	// --- Step 2: Import historical options data from DoltHub (skip for non-1m) ---
	if timeframeFlag != "1m" {
		log.Info().Str("timeframe", timeframeFlag).Msg("skipping options import for non-1m timeframe")
		log.Info().Msg("backfill finished successfully")
		return
	}
	log.Info().Str("source", optionsSourceFlag).Msg("[2/2] importing historical options data...")
	histOptRepo := timescaledb.NewHistoricalOptionsRepository(dbAdapter, log.With().Str("component", "hist_options").Logger())

	switch optionsSourceFlag {
	case "dolthub":
		dolthubClient := dolthub.NewClient(nil, log)
		importer := optionsimport.NewDoltHubService(dolthubClient, histOptRepo, log)

		const maxConcurrentImports = 4
		importSem := make(chan struct{}, maxConcurrentImports)
		var importWg sync.WaitGroup
		for _, sym := range symbols {
			importWg.Add(1)
			go func(sym domain.Symbol) {
				defer importWg.Done()
				importSem <- struct{}{}
				defer func() { <-importSem }()
				if importErr := importer.EnsureData(ctx, string(sym), fromTime, toTime); importErr != nil {
					log.Warn().Err(importErr).Str("symbol", string(sym)).Msg("DoltHub options import failed")
				}
			}(sym)
		}
		importWg.Wait()

	case "alpaca":
		// Pre-fetch the daily underlying bars for every symbol once,
		// build a map[date]close, and serve SpotLookup from that map.
		// Avoids per-(sym, date) Alpaca round-trips against the bars
		// endpoint and keeps the IV inversion path deterministic.
		spotByDate := make(map[domain.Symbol]map[string]float64, len(symbols))
		for _, sym := range symbols {
			bars, err := alpacaAdapter.GetHistoricalBars(ctx, sym, domain.Timeframe("1d"), fromTime, toTime)
			if err != nil {
				log.Warn().Err(err).Str("symbol", string(sym)).Msg("daily bar pre-fetch failed; spot lookups will return 0 and dates will skip")
				continue
			}
			byDate := make(map[string]float64, len(bars))
			for _, b := range bars {
				key := b.Time.UTC().Format("2006-01-02")
				byDate[key] = b.Close
			}
			spotByDate[sym] = byDate
		}
		spotLookup := func(_ context.Context, sym domain.Symbol, date time.Time) (float64, error) {
			if m, ok := spotByDate[sym]; ok {
				return m[date.UTC().Format("2006-01-02")], nil
			}
			return 0, nil
		}

		alpacaImporter := optionsimport.NewAlpacaService(
			alpacaAdapter.RESTClient(),
			alpacaAdapter.DataURL(),
			histOptRepo,
			spotLookup,
			optionsimport.AlpacaConfig{},
			log,
		)
		symStrs := make([]string, len(symbols))
		for i, s := range symbols {
			symStrs[i] = string(s)
		}
		if importErr := alpacaImporter.Run(ctx, symStrs, fromTime, toTime); importErr != nil {
			log.Error().Err(importErr).Msg("Alpaca historical options import failed")
			os.Exit(1)
		}
	}
	log.Info().Msg("[2/2] options import complete")

	log.Info().Msg("backfill finished successfully")
}
