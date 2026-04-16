// Package main provides a one-shot CLI tool to backfill historical funding
// rates from crypto perpetual exchanges. Supports Bybit and Hyperliquid venues.
//
// Usage: go run ./cmd/funding-backfill [flags]
//
//	-venue     Source venue: "bybit" or "hyperliquid" (default: hyperliquid)
//	-from      Start date YYYY-MM-DD (default: 2023-07-01)
//	-to        End date YYYY-MM-DD   (default: now)
//	-symbols   Comma-separated symbols (default: BTC/USD,ETH/USD,SOL/USD)
//	-config    Path to YAML config   (default: configs/config.yaml)
//	-env-file  Path to .env file     (default: .env)
//
// DB connection: set DATABASE_URL env var, or use -config/-env-file flags.
// Bybit is geo-blocked from the US; Hyperliquid works globally.
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

	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/adapters/bybit"
	"github.com/oh-my-opentrade/backend/internal/dbutil"
	"github.com/oh-my-opentrade/backend/internal/adapters/hyperliquid"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

func main() {
	var (
		venueFlag   string
		symbolsFlag string
		fromFlag    string
		toFlag      string
		configPath  string
		envPath     string
	)

	flag.StringVar(&venueFlag, "venue", "hyperliquid", "Source venue: bybit or hyperliquid")
	flag.StringVar(&symbolsFlag, "symbols", "BTC/USD,ETH/USD,SOL/USD", "Comma-separated symbols")
	flag.StringVar(&fromFlag, "from", "2023-07-01", "Start date YYYY-MM-DD")
	flag.StringVar(&toFlag, "to", "", "End date YYYY-MM-DD (default: now)")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.Parse()

	log := logger.New(logger.Config{
		Level:  zerolog.InfoLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "funding-backfill").Logger()

	// Parse time range.
	from, err := time.Parse("2006-01-02", fromFlag)
	if err != nil {
		log.Fatal().Err(err).Str("from", fromFlag).Msg("invalid --from date")
	}
	to := time.Now().UTC()
	if toFlag != "" {
		to, err = time.Parse("2006-01-02", toFlag)
		if err != nil {
			log.Fatal().Err(err).Str("to", toFlag).Msg("invalid --to date")
		}
	}

	// Parse symbols.
	var symbols []domain.Symbol
	for _, s := range strings.Split(symbolsFlag, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			symbols = append(symbols, domain.Symbol(s))
		}
	}

	// Resolve venue.
	var venue domain.Venue
	switch venueFlag {
	case "bybit":
		venue = domain.VenueBybit
	case "hyperliquid":
		venue = domain.VenueHyperliquid
	default:
		log.Fatal().Str("venue", venueFlag).Msg("unsupported venue; use 'bybit' or 'hyperliquid'")
	}

	// Connect to TimescaleDB via DATABASE_URL or config file.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		cfg, cfgErr := config.Load(envPath, configPath)
		if cfgErr != nil {
			log.Fatal().Err(cfgErr).Msg("failed to load config (set DATABASE_URL to bypass)")
		}
		dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
			cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	}
	sqlDB, err := dbutil.Open(dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to TimescaleDB")
	}
	defer sqlDB.Close()
	log.Info().Msg("TimescaleDB connected")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Create venue-specific funding adapter.
	var fundingAdapter ports.FundingRatesPort
	switch venue {
	case domain.VenueBybit:
		baseURL := os.Getenv("BYBIT_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.bybit.com"
		}
		bybitClient := bybit.NewClient(baseURL, log)
		fundingAdapter = bybit.NewFundingAdapter(bybitClient, log)

	case domain.VenueHyperliquid:
		hlCfg := config.HyperliquidConfig{Network: "mainnet"}
		hlClient, hlErr := hyperliquid.NewClient(hlCfg, log)
		if hlErr != nil {
			log.Fatal().Err(hlErr).Msg("failed to create Hyperliquid client")
		}
		rest := hyperliquid.NewRESTClient(hlClient)
		fundingAdapter = hyperliquid.NewPublicFundingAdapter(rest, log)
	}

	// Create funding repo.
	fundingRepo := timescaledb.NewFundingRepo(
		timescaledb.NewSqlDB(sqlDB),
		log.With().Str("component", "funding_repo").Logger(),
	)

	// Create and run the backfill. Hyperliquid returns up to 500 records
	// per call (hourly funding), so use 20-day chunks to maximize API
	// efficiency. A 1s delay between requests avoids rate-limit 429s.
	var opts []ingestion.FundingBackfillOption
	if venue == domain.VenueHyperliquid {
		opts = append(opts,
			ingestion.WithChunkSize(18*24*time.Hour),
			ingestion.WithRequestDelay(2*time.Second),
		)
	}
	backfiller := ingestion.NewFundingBackfill(fundingAdapter, fundingRepo, log, opts...)

	log.Info().
		Str("venue", string(venue)).
		Strs("symbols", func() []string {
			ss := make([]string, len(symbols))
			for i, s := range symbols {
				ss[i] = string(s)
			}
			return ss
		}()).
		Time("from", from).
		Time("to", to).
		Msg("starting funding rate backfill")

	if err := backfiller.Run(ctx, venue, symbols, from, to); err != nil {
		log.Fatal().Err(err).Msg("backfill failed")
	}

	log.Info().Msg("funding rate backfill complete")
}
