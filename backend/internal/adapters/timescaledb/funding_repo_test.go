package timescaledb_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFundingRepo_Insert_Success(t *testing.T) {
	var capturedQuery string
	var capturedArgs []any
	db := &mockDB{
		execFunc: func(_ context.Context, query string, args ...any) (sql.Result, error) {
			capturedQuery = query
			capturedArgs = args
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())

	ts := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	rates := []ports.FundingRate{
		{
			Venue:         domain.VenueBybit,
			Symbol:        domain.Symbol("BTC/USD"),
			Timestamp:     ts,
			Rate:          0.0001,
			IntervalHours: 8,
			MarkPrice:     65000.0,
		},
	}

	err := repo.Insert(context.Background(), rates)
	require.NoError(t, err)
	assert.Contains(t, capturedQuery, "INSERT INTO funding_rates")
	assert.Contains(t, capturedQuery, "ON CONFLICT")
	require.Len(t, capturedArgs, 6)
	assert.Equal(t, "bybit", capturedArgs[0])
	assert.Equal(t, "BTC/USD", capturedArgs[1])
}

func TestFundingRepo_Insert_Empty(t *testing.T) {
	repo := timescaledb.NewFundingRepo(&mockDB{}, zerolog.Nop())
	err := repo.Insert(context.Background(), nil)
	require.NoError(t, err)
}

func TestFundingRepo_Insert_DBError(t *testing.T) {
	dbErr := sql.ErrConnDone
	db := &mockDB{
		execFunc: func(_ context.Context, _ string, _ ...any) (sql.Result, error) {
			return nil, dbErr
		},
	}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())

	rates := []ports.FundingRate{
		{Venue: domain.VenueBybit, Symbol: "BTC/USD", Timestamp: time.Now(), Rate: 0.0001, IntervalHours: 8},
	}
	err := repo.Insert(context.Background(), rates)
	require.Error(t, err)
	assert.ErrorIs(t, err, dbErr)
}

func TestFundingRepo_Query_Success(t *testing.T) {
	ts1 := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 4, 15, 8, 0, 0, 0, time.UTC)

	rows := &mockRows{
		data: [][]any{
			{"bybit", "BTC/USD", ts1, 0.0001, 8, 65000.0},
			{"bybit", "BTC/USD", ts2, 0.00015, 8, 65500.0},
		},
	}
	db := &mockDB{
		queryFunc: func(_ context.Context, query string, args ...any) (timescaledb.Rows, error) {
			assert.Contains(t, query, "FROM funding_rates")
			assert.Contains(t, query, "ORDER BY timestamp ASC")
			assert.Equal(t, "bybit", args[0])
			assert.Equal(t, "BTC/USD", args[1])
			return rows, nil
		},
	}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())

	from := ts1.Add(-time.Hour)
	to := ts2.Add(time.Hour)
	result, err := repo.Query(context.Background(), domain.VenueBybit, domain.Symbol("BTC/USD"), from, to)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, domain.VenueBybit, result[0].Venue)
	assert.Equal(t, domain.Symbol("BTC/USD"), result[0].Symbol)
	assert.InDelta(t, 0.0001, result[0].Rate, 1e-10)
	assert.InDelta(t, 65000.0, result[0].MarkPrice, 0.01)
	assert.InDelta(t, 0.00015, result[1].Rate, 1e-10)
}

func TestFundingRepo_Query_Empty(t *testing.T) {
	db := &mockDB{
		queryFunc: func(_ context.Context, _ string, _ ...any) (timescaledb.Rows, error) {
			return &mockRows{data: [][]any{}}, nil
		},
	}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())
	result, err := repo.Query(context.Background(), domain.VenueBybit, "BTC/USD", time.Now(), time.Now())
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestFundingRepo_Latest_Success(t *testing.T) {
	ts := time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC)
	db := &mockDB{
		queryRowFunc: func(_ context.Context, query string, args ...any) timescaledb.Row {
			assert.Contains(t, query, "ORDER BY timestamp DESC")
			assert.Contains(t, query, "LIMIT 1")
			return &mockRow{scanFunc: func(dest ...any) error {
				*dest[0].(*string) = "bybit"
				*dest[1].(*string) = "BTC/USD"
				*dest[2].(*time.Time) = ts
				*dest[3].(*float64) = 0.00012
				*dest[4].(*int) = 8
				*dest[5].(*float64) = 66000.0
				return nil
			}}
		},
	}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())

	fr, err := repo.Latest(context.Background(), domain.VenueBybit, domain.Symbol("BTC/USD"))
	require.NoError(t, err)
	assert.Equal(t, domain.VenueBybit, fr.Venue)
	assert.Equal(t, domain.Symbol("BTC/USD"), fr.Symbol)
	assert.InDelta(t, 0.00012, fr.Rate, 1e-10)
	assert.InDelta(t, 66000.0, fr.MarkPrice, 0.01)
}

func TestFundingRepo_Latest_NotFound(t *testing.T) {
	db := &mockDB{
		queryRowFunc: func(_ context.Context, _ string, _ ...any) timescaledb.Row {
			return &mockRow{scanFunc: func(_ ...any) error { return sql.ErrNoRows }}
		},
	}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())

	_, err := repo.Latest(context.Background(), domain.VenueBybit, "BTC/USD")
	require.Error(t, err)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestFundingRepo_Insert_BatchSplit(t *testing.T) {
	// Verify that large batches get split into chunks.
	callCount := 0
	db := &mockDB{
		execFunc: func(_ context.Context, query string, args ...any) (sql.Result, error) {
			callCount++
			assert.True(t, strings.Contains(query, "INSERT INTO funding_rates"))
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())

	// Create 5001 rates to trigger the split at 5000.
	rates := make([]ports.FundingRate, 5001)
	for i := range rates {
		rates[i] = ports.FundingRate{
			Venue:         domain.VenueBybit,
			Symbol:        "BTC/USD",
			Timestamp:     time.Now().Add(time.Duration(i) * time.Second),
			Rate:          0.0001,
			IntervalHours: 8,
		}
	}

	err := repo.Insert(context.Background(), rates)
	require.NoError(t, err)
	assert.Equal(t, 2, callCount, "5001 rates should be split into 2 DB calls")
}
