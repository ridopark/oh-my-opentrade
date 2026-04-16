// Package main provides a one-shot CLI tool to backfill Deribit DVOL
// (30-day ATM implied volatility) for BTC and ETH into the crypto_iv_surface
// table. DVOL percentages are converted to fractions (55.2% -> 0.552).
//
// Usage: go run ./cmd/dvol-backfill [flags]
//
//	-from     Start date YYYY-MM-DD (default: 2021-01-01)
//	-to       End date YYYY-MM-DD   (default: now)
//	-assets   Comma-separated assets (default: BTC,ETH)
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/oh-my-opentrade/backend/internal/dbutil"
	"github.com/rs/zerolog"
)

const (
	deribitBaseURL = "https://www.deribit.com/api/v2/public/get_volatility_index_data"
	resolutionSec  = 3600 // hourly
	requestDelay   = 100 * time.Millisecond
	batchSize      = 500
)

type deribitResponse struct {
	Result struct {
		Data         [][]float64 `json:"data"`
		Continuation *float64    `json:"continuation"`
	} `json:"result"`
}

// dailyRow holds one day's DVOL close for an asset.
type dailyRow struct {
	asset    string
	day      time.Time
	atmIV30d float64 // fraction, e.g. 0.552
}

func main() {
	var (
		assetsFlag string
		fromFlag   string
		toFlag     string
	)

	flag.StringVar(&assetsFlag, "assets", "BTC,ETH", "Comma-separated assets")
	flag.StringVar(&fromFlag, "from", "2021-01-01", "Start date YYYY-MM-DD")
	flag.StringVar(&toFlag, "to", "", "End date YYYY-MM-DD (default: now)")
	flag.Parse()

	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).
		With().Timestamp().Str("service", "dvol-backfill").Logger()

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

	assets := parseAssets(assetsFlag)

	// Connect to DB.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://opentrade@localhost:5432/opentrade?sslmode=disable"
	}
	db, err := dbutil.Open(dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to TimescaleDB")
	}
	defer db.Close()
	log.Info().Msg("TimescaleDB connected")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client := &http.Client{Timeout: 30 * time.Second}

	for _, asset := range assets {
		if err := backfillAsset(ctx, log, client, db, asset, from, to); err != nil {
			log.Error().Err(err).Str("asset", asset).Msg("backfill failed")
		}
	}

	log.Info().Msg("DVOL backfill complete")
}

func parseAssets(s string) []string {
	var out []string
	for _, a := range strings.Split(s, ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			out = append(out, strings.ToUpper(a))
		}
	}
	return out
}

func backfillAsset(ctx context.Context, log zerolog.Logger, client *http.Client, db *sql.DB, asset string, from, to time.Time) error {
	log = log.With().Str("asset", asset).Logger()
	log.Info().Time("from", from).Time("to", to).Msg("starting DVOL fetch")

	// Deribit uses millisecond timestamps.
	startMs := from.UnixMilli()
	endMs := to.UnixMilli()

	var allRows []dailyRow
	requestCount := 0

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("dvol-backfill: context canceled: %w", err)
		}

		url := fmt.Sprintf("%s?currency=%s&start_timestamp=%d&end_timestamp=%d&resolution=%d",
			deribitBaseURL, asset, startMs, endMs, resolutionSec)

		resp, err := fetchDVOL(ctx, client, url)
		if err != nil {
			return fmt.Errorf("dvol-backfill: fetch: %w", err)
		}
		requestCount++

		rows := downsampleToDaily(asset, resp.Result.Data)
		allRows = append(allRows, rows...)

		log.Info().
			Int("hourly_points", len(resp.Result.Data)).
			Int("daily_rows", len(rows)).
			Int("total_daily", len(allRows)).
			Int("requests", requestCount).
			Msg("fetched chunk")

		if resp.Result.Continuation == nil {
			break
		}

		// Pagination: use continuation as the new end_timestamp.
		endMs = int64(*resp.Result.Continuation)
		time.Sleep(requestDelay)
	}

	if len(allRows) == 0 {
		log.Warn().Msg("no data fetched")
		return nil
	}

	// Insert into DB.
	inserted, err := insertRows(ctx, db, allRows)
	if err != nil {
		return fmt.Errorf("dvol-backfill: insert: %w", err)
	}

	log.Info().
		Int("total_rows", len(allRows)).
		Int64("inserted", inserted).
		Time("min_date", allRows[0].day).
		Time("max_date", allRows[len(allRows)-1].day).
		Msg("backfill complete")

	return nil
}

func fetchDVOL(ctx context.Context, client *http.Client, url string) (*deribitResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("dvol-backfill: new request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dvol-backfill: http do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dvol-backfill: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result deribitResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("dvol-backfill: decode: %w", err)
	}
	return &result, nil
}

// downsampleToDaily takes hourly DVOL candles and returns one row per UTC day,
// using the close of the last hourly candle of each day.
// Data format: [timestamp_ms, open, high, low, close].
func downsampleToDaily(asset string, data [][]float64) []dailyRow {
	if len(data) == 0 {
		return nil
	}

	// Map: UTC date string -> last candle close seen for that day.
	dayMap := make(map[string]dailyRow)
	// Track ordering by keeping a list of day keys.
	var dayOrder []string

	for _, candle := range data {
		if len(candle) < 5 {
			continue
		}
		ts := time.UnixMilli(int64(candle[0])).UTC()
		dvolClose := candle[4] // percentage, e.g. 55.2

		dayKey := ts.Format("2006-01-02")
		day := time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)

		if _, exists := dayMap[dayKey]; !exists {
			dayOrder = append(dayOrder, dayKey)
		}
		// Overwrite: since data is chronological, the last write per day
		// is the last hourly candle of that day.
		dayMap[dayKey] = dailyRow{
			asset:    asset,
			day:      day,
			atmIV30d: dvolClose / 100.0, // percentage to fraction
		}
	}

	rows := make([]dailyRow, 0, len(dayOrder))
	for _, k := range dayOrder {
		rows = append(rows, dayMap[k])
	}
	return rows
}

func insertRows(ctx context.Context, db *sql.DB, rows []dailyRow) (int64, error) {
	const query = `INSERT INTO crypto_iv_surface (asset, timestamp, atm_iv_30d)
		VALUES ($1, $2, $3)
		ON CONFLICT (asset, timestamp) DO NOTHING`

	var totalInserted int64

	// Process in batches within a transaction.
	for i := 0; i < len(rows); i += batchSize {
		end := i + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return totalInserted, fmt.Errorf("dvol-backfill: begin tx: %w", err)
		}

		stmt, err := tx.PrepareContext(ctx, query)
		if err != nil {
			_ = tx.Rollback()
			return totalInserted, fmt.Errorf("dvol-backfill: prepare: %w", err)
		}

		for _, r := range batch {
			res, err := stmt.ExecContext(ctx, r.asset, r.day, r.atmIV30d)
			if err != nil {
				stmt.Close()
				_ = tx.Rollback()
				return totalInserted, fmt.Errorf("dvol-backfill: exec row %s %s: %w", r.asset, r.day.Format("2006-01-02"), err)
			}
			n, _ := res.RowsAffected()
			totalInserted += n
		}

		stmt.Close()
		if err := tx.Commit(); err != nil {
			return totalInserted, fmt.Errorf("dvol-backfill: commit: %w", err)
		}
	}

	return totalInserted, nil
}
