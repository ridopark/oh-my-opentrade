//go:build integration

package ingestion_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn, ok := os.LookupEnv("TEST_DATABASE_URL")
	if !ok {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	if dsn == "" {
		dsn = "postgres://opentrade:opentrade@localhost:5432/opentrade_test?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	pgxCfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)

	db := stdlib.OpenDB(*pgxCfg)
	t.Cleanup(func() { _ = db.Close() })

	err = db.PingContext(ctx)
	if err != nil {
		t.Skipf("failed to connect to TimescaleDB (%s): %v", dsn, err)
	}

	return db
}

func truncateMarketBars(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	_, err := db.ExecContext(ctx, "TRUNCATE TABLE market_bars")
	require.NoError(t, err)
}

func mustSymbol(t *testing.T, raw string) domain.Symbol {
	t.Helper()
	s, err := domain.NewSymbol(raw)
	require.NoError(t, err)
	return s
}

func mustBar(t *testing.T, tm time.Time, sym domain.Symbol, tf domain.Timeframe, open, high, low, close, volume float64) domain.MarketBar {
	t.Helper()
	b, err := domain.NewMarketBar(tm, sym, tf, open, high, low, close, volume)
	require.NoError(t, err)
	return b
}

func mustEventMarketBarReceived(t *testing.T, idempotencyKey string, bar domain.MarketBar) domain.Event {
	t.Helper()
	ev, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant123", domain.EnvModePaper, idempotencyKey, bar)
	require.NoError(t, err)
	return *ev
}

func startIngestionService(t *testing.T, ctx context.Context, db *sql.DB) (*memory.Bus, *timescaledb.Repository) {
	t.Helper()

	log := zerolog.Nop()
	repo := timescaledb.NewRepositoryWithLogger(timescaledb.NewSqlDB(db), log)
	bus := memory.NewBus()
	filter := ingestion.NewZScoreFilter(5, 4.0)
	svc := ingestion.NewService(bus, repo, filter, log)

	require.NoError(t, svc.Start(ctx))
	return bus, repo
}

func TestIntegration_IngestionRoundTrip(t *testing.T) {
	db := openIntegrationDB(t)
	truncateMarketBars(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	bus, repo := startIngestionService(t, ctx, db)

	sym := mustSymbol(t, "AAPL")
	tf := domain.Timeframe("1m")
	barTime := time.Now().UTC().Truncate(time.Minute)
	bar := mustBar(t, barTime, sym, tf, 100.0, 101.0, 99.0, 100.5, 1234.0)

	require.NoError(t, bus.Publish(ctx, mustEventMarketBarReceived(t, "idempotency-roundtrip", bar)))

	from := barTime.Add(-1 * time.Minute)
	to := barTime.Add(1 * time.Minute)
	got, err := repo.GetMarketBars(ctx, sym, tf, from, to)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, sym, got[0].Symbol)
	assert.Equal(t, tf, got[0].Timeframe)
	assert.Equal(t, bar.Close, got[0].Close)
	assert.Equal(t, bar.Volume, got[0].Volume)
}

func TestIntegration_AnomalousBarNotSaved(t *testing.T) {
	db := openIntegrationDB(t)
	truncateMarketBars(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	bus, repo := startIngestionService(t, ctx, db)

	sym := mustSymbol(t, "AAPL")
	tf := domain.Timeframe("1m")
	base := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute)

	for i := 0; i < 5; i++ {
		tm := base.Add(time.Duration(i) * time.Minute)
		b := mustBar(t, tm, sym, tf, 100.0, 101.0, 99.0, 100.0, 10.0)
		require.NoError(t, bus.Publish(ctx, mustEventMarketBarReceived(t, "seed-"+tm.Format(time.RFC3339Nano), b)))
	}

	anomalousTime := base.Add(5 * time.Minute)
	anomalous := mustBar(t, anomalousTime, sym, tf, 100.0, 101.0, 99.0, 99999.0, 10.0)
	require.NoError(t, bus.Publish(ctx, mustEventMarketBarReceived(t, "anomalous-"+anomalousTime.Format(time.RFC3339Nano), anomalous)))

	from := base.Add(-1 * time.Minute)
	to := base.Add(10 * time.Minute)
	got, err := repo.GetMarketBars(ctx, sym, tf, from, to)
	require.NoError(t, err)

	assert.Len(t, got, 5)
	for _, b := range got {
		assert.NotEqual(t, anomalous.Close, b.Close)
	}
}
