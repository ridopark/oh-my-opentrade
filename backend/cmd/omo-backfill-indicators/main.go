package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

// barUpdate holds the computed indicator values for a single bar row.
type barUpdate struct {
	time   time.Time
	ema9   float64
	ema21  float64
	ema50  float64
	ema200 float64
	avwaps map[string]float64
}

func main() {
	var (
		symbolsFlag   string
		fromFlag      string
		toFlag        string
		timeframeFlag string
		configPath    string
		envPath       string
		batchSize     int
		dryRun        bool
	)

	flag.StringVar(&symbolsFlag, "symbols", "", "Comma-separated symbols (default: all symbols in market_bars)")
	flag.StringVar(&fromFlag, "from", "", "Start date YYYY-MM-DD (default: 30 days ago)")
	flag.StringVar(&toFlag, "to", "", "End date YYYY-MM-DD (default: today)")
	flag.StringVar(&timeframeFlag, "timeframe", "1m", "Timeframe to backfill (default: 1m)")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.IntVar(&batchSize, "batch-size", 500, "Batch UPDATE size")
	flag.BoolVar(&dryRun, "dry-run", false, "Compute but don't write to DB")
	flag.Parse()

	log := logger.New(logger.Config{
		Level:  zerolog.InfoLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-backfill-indicators").Logger()

	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Resolve time range.
	var fromTime, toTime time.Time
	if fromFlag != "" {
		fromTime, err = time.Parse("2006-01-02", fromFlag)
		if err != nil {
			log.Fatal().Err(err).Str("from", fromFlag).Msg("invalid --from date, expected YYYY-MM-DD")
		}
	} else {
		fromTime = time.Now().UTC().AddDate(0, 0, -30).Truncate(24 * time.Hour)
	}
	if toFlag != "" {
		toTime, err = time.Parse("2006-01-02", toFlag)
		if err != nil {
			log.Fatal().Err(err).Str("to", toFlag).Msg("invalid --to date, expected YYYY-MM-DD")
		}
	} else {
		toTime = time.Now().UTC().Truncate(24 * time.Hour)
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

	repo := timescaledb.NewRepositoryWithLogger(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "timescaledb").Logger())

	// Handle graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Warn().Str("signal", sig.String()).Msg("received signal, canceling...")
		cancel()
	}()

	// Resolve symbols.
	symbols, err := resolveSymbols(ctx, sqlDB, symbolsFlag, timeframeFlag)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to resolve symbols")
	}
	if len(symbols) == 0 {
		log.Fatal().Msg("no symbols found in market_bars")
	}
	log.Info().Strs("symbols", symbols).
		Time("from", fromTime).Time("to", toTime).
		Str("timeframe", timeframeFlag).
		Int("batch_size", batchSize).
		Bool("dry_run", dryRun).
		Msg("starting indicator backfill")

	nyLoc, err := time.LoadLocation("America/New_York")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load America/New_York timezone")
	}

	tf := domain.Timeframe(timeframeFlag)
	anchorNames := []string{"session_open", "pd_high", "pd_low"}

	for _, symStr := range symbols {
		if ctx.Err() != nil {
			break
		}

		sym := domain.Symbol(symStr)
		symLog := log.With().Str("symbol", symStr).Logger()

		// 1. Load session data via SessionResolver (from-5d to to+1d).
		sessionResolver := backtest.NewSessionResolver(nyLoc)
		sessionFrom := fromTime.AddDate(0, 0, -5)
		sessionTo := toTime.AddDate(0, 0, 1)
		if err := sessionResolver.Load(ctx, sqlDB, sym, sessionFrom, sessionTo); err != nil {
			symLog.Error().Err(err).Msg("failed to load session data, skipping symbol")
			continue
		}

		// 2. Load ALL bars with 200-bar warmup before from.
		bars, err := loadBars(ctx, sqlDB, symStr, timeframeFlag, fromTime, toTime)
		if err != nil {
			symLog.Error().Err(err).Msg("failed to load bars, skipping symbol")
			continue
		}
		if len(bars) == 0 {
			symLog.Warn().Msg("no bars found, skipping symbol")
			continue
		}

		// 3. Create IndicatorCalculator and process bars.
		indicatorCalc := monitor.NewIndicatorCalculator()
		var avwapCalc *start.AnchoredVWAPCalc
		var lastSessionDate string
		var updates []barUpdate
		totalBars := 0
		updatedBars := 0

		for _, bar := range bars {
			if ctx.Err() != nil {
				break
			}

			barET := bar.Time.In(nyLoc)
			barDate := barET.Format("2006-01-02")

			// Detect new session day — resolve AVWAP anchors.
			if barDate != lastSessionDate {
				lastSessionDate = barDate

				// Reset session VWAP in the indicator calculator.
				indicatorCalc.ResetSession(symStr, timeframeFlag)

				resolved := sessionResolver.ResolveAnchors(symStr, bar.Time, anchorNames)
				if len(resolved) > 0 {
					avwapCalc = start.NewAnchoredVWAPCalc()
					for name, t := range resolved {
						avwapCalc.AddAnchor(start.AnchorPoint{Name: name, AnchorTime: t})
					}

					// Replay prev-day bars for non-session_open anchors.
					sortedNames := make([]string, 0, len(resolved))
					for name := range resolved {
						if name != "session_open" {
							sortedNames = append(sortedNames, name)
						}
					}
					sort.Strings(sortedNames)
					for _, name := range sortedNames {
						anchorTime := resolved[name]
						prevBars := sessionResolver.GetBarsSince(ctx, sqlDB, symStr, anchorTime)
						for _, b := range prevBars {
							avwapCalc.UpdateSingleAnchor(name, b.Time, b.High, b.Low, b.Close, b.Volume)
						}
					}
				} else {
					avwapCalc = nil
				}
			}

			// Feed bar into indicator calculator.
			mb := domain.MarketBar{
				Time:      bar.Time,
				Symbol:    sym,
				Timeframe: tf,
				Open:      bar.Open,
				High:      bar.High,
				Low:       bar.Low,
				Close:     bar.Close,
				Volume:    bar.Volume,
			}
			snap := indicatorCalc.Update(mb)

			// Feed bar into AVWAP calculator.
			var avwaps map[string]float64
			if avwapCalc != nil {
				avwapCalc.Update(bar.Time, bar.High, bar.Low, bar.Close, bar.Volume)
				avwaps = avwapCalc.Values()
			}

			totalBars++

			// Only collect updates for bars within the requested [from, to] range (skip warmup).
			if !bar.Time.Before(fromTime) && bar.Time.Before(toTime.AddDate(0, 0, 1)) {
				updatedBars++
				updates = append(updates, barUpdate{
					time:   bar.Time,
					ema9:   snap.EMA9,
					ema21:  snap.EMA21,
					ema50:  snap.EMA50,
					ema200: snap.EMA200,
					avwaps: avwaps,
				})

				// Flush batch.
				if len(updates) >= batchSize {
					if !dryRun {
						if err := flushUpdates(ctx, repo, sym, tf, updates); err != nil {
							symLog.Error().Err(err).Int("batch_size", len(updates)).Msg("failed to flush batch")
						}
					}
					symLog.Info().
						Int("processed", totalBars).
						Int("updated", updatedBars).
						Int("total_bars", len(bars)).
						Msg("progress")
					updates = updates[:0]
				}
			}
		}

		// Flush remaining updates.
		if len(updates) > 0 && !dryRun {
			if err := flushUpdates(ctx, repo, sym, tf, updates); err != nil {
				symLog.Error().Err(err).Int("batch_size", len(updates)).Msg("failed to flush final batch")
			}
		}

		symLog.Info().
			Int("total_bars", len(bars)).
			Int("processed", totalBars).
			Int("updated", updatedBars).
			Msg("symbol backfill complete")
	}

	log.Info().Msg("indicator backfill finished")
}

// resolveSymbols returns the list of symbols to process. If the flag is empty,
// it queries all distinct symbols from market_bars for the given timeframe.
func resolveSymbols(ctx context.Context, db *sql.DB, symbolsFlag, timeframe string) ([]string, error) {
	if symbolsFlag != "" {
		var result []string
		for _, s := range strings.Split(symbolsFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				result = append(result, s)
			}
		}
		return result, nil
	}

	rows, err := db.QueryContext(ctx,
		"SELECT DISTINCT symbol FROM market_bars WHERE timeframe = $1 ORDER BY symbol", timeframe)
	if err != nil {
		return nil, fmt.Errorf("backfill-indicators: query distinct symbols: %w", err)
	}
	defer rows.Close()

	var symbols []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("backfill-indicators: scan symbol: %w", err)
		}
		symbols = append(symbols, s)
	}
	return symbols, rows.Err()
}

// rawBar is a lightweight bar struct for loading from DB without domain validation.
type rawBar struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// loadBars loads bars with a 200-bar warmup window before fromTime.
// Returns all bars from (warmup start) through toTime, ordered by time ascending.
func loadBars(ctx context.Context, db *sql.DB, symbol, timeframe string, fromTime, toTime time.Time) ([]rawBar, error) {
	// First, determine the warmup start: we need 200+ bars before fromTime.
	// Query the 200th bar before fromTime to find the warmup cutoff.
	var warmupStart time.Time
	err := db.QueryRowContext(ctx, `
		SELECT time FROM market_bars
		WHERE symbol = $1 AND timeframe = $2 AND time < $3
		ORDER BY time DESC
		OFFSET 199 LIMIT 1`,
		symbol, timeframe, fromTime).Scan(&warmupStart)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Not enough warmup bars — start from the earliest available bar.
			err = db.QueryRowContext(ctx, `
				SELECT MIN(time) FROM market_bars
				WHERE symbol = $1 AND timeframe = $2`,
				symbol, timeframe).Scan(&warmupStart)
			if err != nil {
				return nil, fmt.Errorf("backfill-indicators: find earliest bar: %w", err)
			}
		} else {
			return nil, fmt.Errorf("backfill-indicators: find warmup start: %w", err)
		}
	}

	// Load all bars from warmup start through toTime end-of-day.
	endTime := toTime.AddDate(0, 0, 1)
	rows, err := db.QueryContext(ctx, `
		SELECT time, open, high, low, close, volume
		FROM market_bars
		WHERE symbol = $1 AND timeframe = $2 AND time >= $3 AND time < $4
		ORDER BY time ASC`,
		symbol, timeframe, warmupStart, endTime)
	if err != nil {
		return nil, fmt.Errorf("backfill-indicators: load bars: %w", err)
	}
	defer rows.Close()

	var bars []rawBar
	for rows.Next() {
		var b rawBar
		if err := rows.Scan(&b.Time, &b.Open, &b.High, &b.Low, &b.Close, &b.Volume); err != nil {
			return nil, fmt.Errorf("backfill-indicators: scan bar: %w", err)
		}
		bars = append(bars, b)
	}
	return bars, rows.Err()
}

// flushUpdates writes a batch of indicator updates to the database.
func flushUpdates(ctx context.Context, repo *timescaledb.Repository, symbol domain.Symbol, tf domain.Timeframe, updates []barUpdate) error {
	for _, u := range updates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := repo.UpdateBarIndicators(ctx, symbol, tf, u.time, u.ema9, u.ema21, u.ema50, u.ema200, u.avwaps); err != nil {
			return fmt.Errorf("backfill-indicators: update bar at %v: %w", u.time, err)
		}
	}
	return nil
}
