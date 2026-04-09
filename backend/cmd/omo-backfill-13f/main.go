package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/openfigi"
	"github.com/oh-my-opentrade/backend/internal/adapters/sec"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/whale13f"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

func main() {
	var (
		fromQuarter string
		toQuarter   string
		filersFlag  string
		resume      bool
		concurrency int
		batchSize   int
		userAgent   string
		configPath  string
		envPath     string
	)

	flag.StringVar(&fromQuarter, "from-quarter", "", "Start quarter, e.g. 2023Q1 (required)")
	flag.StringVar(&toQuarter, "to-quarter", "", "End quarter, e.g. 2025Q4 (default: most recent)")
	flag.StringVar(&filersFlag, "filers", "", "Comma-separated CIKs (default: built-in list)")
	flag.BoolVar(&resume, "resume", false, "Skip quarters already ingested per filer")
	flag.IntVar(&concurrency, "concurrency", 3, "Number of parallel filer workers")
	flag.IntVar(&batchSize, "batch-size", 500, "Database batch insert size")
	flag.StringVar(&userAgent, "user-agent", "", "SEC User-Agent (required, format: 'Company email@example.com')")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.Parse()

	log := logger.New(logger.Config{
		Level:  zerolog.InfoLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-backfill-13f").Logger()

	// User-agent is required by SEC.
	if userAgent == "" {
		userAgent = os.Getenv("SEC_USER_AGENT")
	}
	if userAgent == "" {
		log.Fatal().Msg("--user-agent is required (SEC policy), e.g. 'MyCompany admin@example.com'")
	}

	// Parse quarter range.
	if fromQuarter == "" {
		log.Fatal().Msg("--from-quarter is required, e.g. 2023Q1")
	}
	from, err := whale13f.ParseQuarterID(fromQuarter)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid --from-quarter")
	}
	var to whale13f.QuarterID
	if toQuarter != "" {
		to, err = whale13f.ParseQuarterID(toQuarter)
		if err != nil {
			log.Fatal().Err(err).Msg("invalid --to-quarter")
		}
	} else {
		to = whale13f.CurrentQuarter()
	}

	// Resolve filer list.
	var filers []sec.FilerConfig
	if filersFlag != "" {
		for _, cik := range strings.Split(filersFlag, ",") {
			cik = strings.TrimSpace(cik)
			if cik != "" {
				filers = append(filers, sec.FilerConfig{CIK: cik, Name: cik, Tier: 1})
			}
		}
	} else {
		filers = sec.DefaultFilers()
	}
	if len(filers) == 0 {
		log.Fatal().Msg("no filers specified")
	}

	// Load config for DB credentials.
	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Connect to TimescaleDB.
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
	whaleRepo := timescaledb.NewWhaleRepo(dbAdapter, log.With().Str("component", "whale_repo").Logger())
	cusipCache := timescaledb.NewCUSIPCacheRepo(dbAdapter, log.With().Str("component", "cusip_cache").Logger())

	// External API clients.
	edgarClient := sec.NewEdgarClient(userAgent, log.With().Str("component", "sec_edgar").Logger())
	figiClient := openfigi.NewClient(os.Getenv("OPENFIGI_API_KEY"), log.With().Str("component", "openfigi").Logger())

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

	svc := whale13f.NewBackfillService(edgarClient, figiClient, cusipCache, whaleRepo, whale13f.Config{
		Concurrency: concurrency,
		BatchSize:   batchSize,
		UserAgent:   userAgent,
	}, log)

	log.Info().
		Str("from", from.String()).
		Str("to", to.String()).
		Int("filers", len(filers)).
		Int("concurrency", concurrency).
		Msg("starting 13F backfill")

	if err := svc.RunQuarters(ctx, from, to, filers); err != nil {
		log.Fatal().Err(err).Msg("backfill failed")
	}

	log.Info().Msg("13F backfill finished")
}
