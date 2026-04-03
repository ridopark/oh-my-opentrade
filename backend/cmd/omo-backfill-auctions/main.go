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

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

// defaultSymbols is the 34-symbol active trading universe.
const defaultSymbols = "AAPL,AFRM,AMD,AMZN,AVGO,BA,COIN,CRM,GOOGL,HIMS,HOOD,IWM,JPM,LLY,META,MRNA,MRVL,MSFT,MU,NET,NFLX,NVDA,OXY,PLTR,QQQ,RBLX,RIVN,SMCI,SNOW,SOFI,SOXL,SPY,TSLA,XOM"

func main() {
	var (
		symbolsFlag string
		fromFlag    string
		toFlag      string
		configPath  string
		envPath     string
		batchSize   int
	)

	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols (default: 34-symbol universe)")
	flag.StringVar(&fromFlag, "from", "2025-06-01", "Start date YYYY-MM-DD")
	flag.StringVar(&toFlag, "to", "2026-03-27", "End date YYYY-MM-DD")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.IntVar(&batchSize, "batch-size", 500, "Database batch insert size")
	flag.Parse()

	log := logger.New(logger.Config{
		Level:  zerolog.InfoLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-backfill-auctions").Logger()

	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Resolve symbols.
	var symbols []string
	if symbolsFlag != "" {
		for _, s := range strings.Split(symbolsFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				symbols = append(symbols, s)
			}
		}
	} else {
		symbols = strings.Split(defaultSymbols, ",")
	}

	// Parse date range.
	fromTime, err := time.Parse("2006-01-02", fromFlag)
	if err != nil {
		log.Fatal().Err(err).Str("from", fromFlag).Msg("invalid --from date")
	}
	toTime, err := time.Parse("2006-01-02", toFlag)
	if err != nil {
		log.Fatal().Err(err).Str("to", toFlag).Msg("invalid --to date")
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
	auctionRepo := timescaledb.NewAuctionImbalanceRepo(dbAdapter, log.With().Str("component", "auction_repo").Logger())

	// Initialize Alpaca marketdata client.
	apiKey := cfg.Alpaca.APIKeyID
	apiSecret := cfg.Alpaca.APISecretKey
	if apiKey == "" {
		apiKey = os.Getenv("APCA_API_KEY_ID")
	}
	if apiSecret == "" {
		apiSecret = os.Getenv("APCA_API_SECRET_KEY")
	}
	if apiKey == "" || apiSecret == "" {
		log.Fatal().Msg("Alpaca API credentials required (config or APCA_API_KEY_ID/APCA_API_SECRET_KEY env vars)")
	}

	mdClient := marketdata.NewClient(marketdata.ClientOpts{
		APIKey:    apiKey,
		APISecret: apiSecret,
	})

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
		Str("from", fromFlag).
		Str("to", toFlag).
		Msg("starting auction backfill")

	totalInserted := int64(0)

	// Process symbols one at a time with rate-limit awareness.
	// Alpaca free tier: 200 req/min. GetMultiAuctions counts as one request
	// per page but may page internally. Process in batches of 10 symbols
	// to stay well within limits.
	const symbolBatchSize = 10
	for i := 0; i < len(symbols); i += symbolBatchSize {
		if ctx.Err() != nil {
			break
		}

		end := i + symbolBatchSize
		if end > len(symbols) {
			end = len(symbols)
		}
		batch := symbols[i:end]

		log.Info().Strs("symbols", batch).Int("batch", i/symbolBatchSize+1).Msg("fetching auctions from Alpaca")

		auctionMap, err := mdClient.GetMultiAuctions(batch, marketdata.GetAuctionsRequest{
			Start: fromTime,
			End:   toTime,
		})
		if err != nil {
			log.Error().Err(err).Strs("symbols", batch).Msg("failed to fetch auctions")
			// Rate limit hit — wait and retry.
			if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "too many") {
				log.Warn().Msg("rate limited, waiting 60s before retry")
				select {
				case <-time.After(60 * time.Second):
					i -= symbolBatchSize // retry this batch
				case <-ctx.Done():
				}
			}
			continue
		}

		for sym, dailyAuctions := range auctionMap {
			if ctx.Err() != nil {
				break
			}

			var snaps []domain.AuctionImbalanceSnapshot
			for _, day := range dailyAuctions {
				for _, auction := range day.Closing {
					snap := domain.AuctionImbalanceSnapshot{
						Time:      auction.Timestamp,
						Symbol:    domain.Symbol(sym),
						Volume:    float64(auction.Size),
						Price:     auction.Price,
						Imbalance: 0, // Direction unknown from Alpaca; derived at backtest time.
					}
					snaps = append(snaps, snap)
				}
			}

			if len(snaps) == 0 {
				log.Info().Str("symbol", sym).Msg("no closing auctions found")
				continue
			}

			// Insert in batches.
			symbolInserted := int64(0)
			for j := 0; j < len(snaps); j += batchSize {
				if ctx.Err() != nil {
					break
				}
				end := j + batchSize
				if end > len(snaps) {
					end = len(snaps)
				}
				n, err := auctionRepo.SaveAuctionImbalanceBatch(ctx, snaps[j:end])
				if err != nil {
					log.Error().Err(err).Str("symbol", sym).Msg("batch insert failed")
					continue
				}
				symbolInserted += n
			}

			totalInserted += symbolInserted
			log.Info().
				Str("symbol", sym).
				Int("auctions_fetched", len(snaps)).
				Int64("rows_inserted", symbolInserted).
				Msg("symbol auction backfill complete")
		}

		// Brief pause between batches to respect rate limits.
		if end < len(symbols) {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
			}
		}
	}

	log.Info().Int64("total_inserted", totalInserted).Msg("auction backfill finished")
}
