package timescaledb_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCorporateActionsRepo_ImplementsPort(t *testing.T) {
	var _ ports.CorporateActionsPort = (*timescaledb.CorporateActionsRepo)(nil)
}

func TestCorporateActionsRepo_Upsert_Idempotent(t *testing.T) {
	calls := 0
	db := &mockDB{
		execFunc: func(_ context.Context, query string, args ...any) (sql.Result, error) {
			calls++
			assert.True(t, strings.Contains(query, "INSERT INTO corporate_actions"))
			assert.True(t, strings.Contains(query, "ON CONFLICT"))
			require.GreaterOrEqual(t, len(args), 7)
			assert.Equal(t, "AAPL", args[0])
			assert.Equal(t, "split", args[1])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewCorporateActionsRepo(db, zerolog.Nop())
	ca := ports.CorporateAction{
		Symbol:           "AAPL",
		ActionType:       "split",
		EffectiveDate:    time.Date(2020, 8, 31, 0, 0, 0, 0, time.UTC),
		RatioNumerator:   4,
		RatioDenominator: 1,
		Source:           "alpaca",
	}
	require.NoError(t, repo.Upsert(context.Background(), ca))
	require.NoError(t, repo.Upsert(context.Background(), ca))
	assert.Equal(t, 2, calls, "both calls should reach the DB; ON CONFLICT handles idempotency server-side")
}

func TestCorporateActionsRepo_Upsert_RejectsEmptySymbol(t *testing.T) {
	repo := timescaledb.NewCorporateActionsRepo(&mockDB{}, zerolog.Nop())
	err := repo.Upsert(context.Background(), ports.CorporateAction{ActionType: "split", Source: "alpaca"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symbol required")
}

func TestCorporateActionsRepo_Between_ReturnsRows(t *testing.T) {
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	effDate := time.Date(2020, 8, 31, 0, 0, 0, 0, time.UTC)

	rows := &mockRows{
		data: [][]any{
			{"AAPL", "split", effDate, 4.0, 1.0, 0.0, "alpaca"},
		},
	}
	db := &mockDB{
		queryFunc: func(_ context.Context, query string, args ...any) (timescaledb.Rows, error) {
			assert.Contains(t, query, "FROM corporate_actions")
			assert.Equal(t, "AAPL", args[0])
			return rows, nil
		},
	}
	repo := timescaledb.NewCorporateActionsRepo(db, zerolog.Nop())
	out, err := repo.Between(context.Background(), "AAPL", from, to)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "split", out[0].ActionType)
	assert.Equal(t, 4.0, out[0].RatioNumerator)
}

func TestCorporateActionsRepo_Delisted_True(t *testing.T) {
	db := &mockDB{
		queryRowFunc: func(_ context.Context, _ string, _ ...any) timescaledb.Row {
			return &mockRow{scanFunc: func(dest ...any) error {
				if p, ok := dest[0].(*int); ok {
					*p = 1
				}
				return nil
			}}
		},
	}
	repo := timescaledb.NewCorporateActionsRepo(db, zerolog.Nop())
	ok, err := repo.Delisted(context.Background(), "XYZ", time.Now())
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestCorporateActionsRepo_Delisted_NoRow(t *testing.T) {
	db := &mockDB{
		queryRowFunc: func(_ context.Context, _ string, _ ...any) timescaledb.Row {
			return &mockRow{scanFunc: func(_ ...any) error { return sql.ErrNoRows }}
		},
	}
	repo := timescaledb.NewCorporateActionsRepo(db, zerolog.Nop())
	ok, err := repo.Delisted(context.Background(), "AAPL", time.Now())
	require.NoError(t, err)
	assert.False(t, ok)
}
