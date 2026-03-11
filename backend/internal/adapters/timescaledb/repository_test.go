package timescaledb_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockResult implements sql.Result
type mockResult struct {
	lastID   int64
	affected int64
}

func (m mockResult) LastInsertId() (int64, error) { return m.lastID, nil }
func (m mockResult) RowsAffected() (int64, error) { return m.affected, nil }

// mockRow implements Row
type mockRow struct {
	scanFunc func(dest ...any) error
}

func (m *mockRow) Scan(dest ...any) error { return m.scanFunc(dest...) }

// mockRows implements Rows
type mockRows struct {
	data    [][]any // each inner slice is one row's column values
	index   int
	closed  bool
	scanErr error
}

func (m *mockRows) Next() bool {
	if m.index < len(m.data) {
		m.index++
		return true
	}
	return false
}

func (m *mockRows) Scan(dest ...any) error {
	if m.scanErr != nil {
		return m.scanErr
	}
	row := m.data[m.index-1]
	// Copy values from row to dest via reflection or direct assignment
	for i, v := range row {
		if i < len(dest) {
			// Use type switch or reflect to assign
			switch d := dest[i].(type) {
			case *string:
				*d = v.(string)
			case *float64:
				*d = v.(float64)
			case *int:
				*d = v.(int)
			case *bool:
				*d = v.(bool)
			case *time.Time:
				*d = v.(time.Time)
			case *uuid.UUID:
				*d = v.(uuid.UUID)
			case *json.RawMessage:
				*d = v.(json.RawMessage)
			}
		}
	}
	return nil
}

func (m *mockRows) Close() error { m.closed = true; return nil }
func (m *mockRows) Err() error   { return nil }

// mockDB implements DBTX
type mockDB struct {
	execFunc     func(ctx context.Context, query string, args ...any) (sql.Result, error)
	queryFunc    func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error)
	queryRowFunc func(ctx context.Context, query string, args ...any) timescaledb.Row
}

func (m *mockDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return m.execFunc(ctx, query, args...)
}

func (m *mockDB) QueryContext(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
	return m.queryFunc(ctx, query, args...)
}

func (m *mockDB) QueryRowContext(ctx context.Context, query string, args ...any) timescaledb.Row {
	return m.queryRowFunc(ctx, query, args...)
}

func TestRepository_ImplementsRepositoryPort(t *testing.T) {
	var _ ports.RepositoryPort = (*timescaledb.Repository)(nil)
}

func TestNewRepository(t *testing.T) {
	db := &mockDB{}
	repo := timescaledb.NewRepository(db)
	assert.NotNil(t, repo)
}

func TestRepository_SaveMarketBar_Success(t *testing.T) {
	barTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	bar, err := domain.NewMarketBar(barTime, "AAPL", "1m", 150.0, 151.0, 149.0, 150.5, 1000)
	require.NoError(t, err)

	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			assert.True(t, strings.Contains(query, "INSERT INTO market_bars"), "query must contain table name")

			// Expected args: time, "", "Paper", symbol, timeframe, open, high, low, close, volume, suspect
			assert.Equal(t, bar.Time, args[0])
			assert.Equal(t, "", args[1])
			assert.Equal(t, string(domain.EnvModePaper), args[2])
			assert.Equal(t, string(bar.Symbol), args[3])
			assert.Equal(t, string(bar.Timeframe), args[4])
			assert.Equal(t, bar.Open, args[5])
			assert.Equal(t, bar.High, args[6])
			assert.Equal(t, bar.Low, args[7])
			assert.Equal(t, bar.Close, args[8])
			assert.Equal(t, bar.Volume, args[9])
			assert.Equal(t, bar.Suspect, args[10])

			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewRepository(db)

	err = repo.SaveMarketBar(context.Background(), bar)
	assert.NoError(t, err)
}

func TestRepository_SaveMarketBar_DBError(t *testing.T) {
	barTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	bar, _ := domain.NewMarketBar(barTime, "AAPL", "1m", 150.0, 151.0, 149.0, 150.5, 1000)
	dbErr := errors.New("db connection error")

	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			return nil, dbErr
		},
	}
	repo := timescaledb.NewRepository(db)

	err := repo.SaveMarketBar(context.Background(), bar)
	assert.ErrorIs(t, err, dbErr)
}

func TestRepository_GetMarketBars_Success(t *testing.T) {
	from := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)
	barTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	db := &mockDB{
		queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
			assert.True(t, strings.Contains(query, "SELECT"), "query must be a SELECT")
			assert.True(t, strings.Contains(query, "FROM market_bars"), "query must use correct table")
			assert.True(t, strings.Contains(query, "WHERE"), "query must have WHERE clause")
			assert.True(t, strings.Contains(query, "ORDER BY time"), "query must be ordered")

			// time, symbol, timeframe, open, high, low, close, volume, suspect
			rows := &mockRows{
				data: [][]any{
					{barTime, "AAPL", "1m", 150.0, 151.0, 149.0, 150.5, 1000.0, false},
					{barTime.Add(time.Minute), "AAPL", "1m", 150.5, 152.0, 150.0, 151.5, 1200.0, false},
				},
			}
			return rows, nil
		},
	}
	repo := timescaledb.NewRepository(db)

	bars, err := repo.GetMarketBars(context.Background(), "AAPL", "1m", from, to)
	assert.NoError(t, err)
	require.Len(t, bars, 2)
	assert.Equal(t, barTime, bars[0].Time)
	assert.Equal(t, domain.Symbol("AAPL"), bars[0].Symbol)
	assert.Equal(t, domain.Timeframe("1m"), bars[0].Timeframe)
	assert.Equal(t, 150.0, bars[0].Open)
	assert.Equal(t, 151.0, bars[0].High)
	assert.Equal(t, 149.0, bars[0].Low)
	assert.Equal(t, 150.5, bars[0].Close)
	assert.Equal(t, 1000.0, bars[0].Volume)
	assert.False(t, bars[0].Suspect)

	assert.Equal(t, barTime.Add(time.Minute), bars[1].Time)
}

func TestRepository_GetMarketBars_Empty(t *testing.T) {
	from := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)

	db := &mockDB{
		queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
			return &mockRows{data: [][]any{}}, nil
		},
	}
	repo := timescaledb.NewRepository(db)

	bars, err := repo.GetMarketBars(context.Background(), "AAPL", "1m", from, to)
	assert.NoError(t, err)
	assert.Empty(t, bars)
}

func TestRepository_SaveTrade_Success(t *testing.T) {
	tradeTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	tradeID := uuid.New()
	trade, err := domain.NewTrade(tradeTime, "tenant-1", domain.EnvModePaper, tradeID, "AAPL", "BUY", 10.0, 150.0, 1.5, "FILLED", "", "")
	require.NoError(t, err)

	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			assert.True(t, strings.Contains(query, "INSERT INTO trades"), "query must contain table name")
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewRepository(db)

	err = repo.SaveTrade(context.Background(), trade)
	assert.NoError(t, err)
}

func TestRepository_SaveTrade_DBError(t *testing.T) {
	tradeTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	tradeID := uuid.New()
	trade, _ := domain.NewTrade(tradeTime, "tenant-1", domain.EnvModePaper, tradeID, "AAPL", "BUY", 10.0, 150.0, 1.5, "FILLED", "", "")
	dbErr := errors.New("db error")

	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			return nil, dbErr
		},
	}
	repo := timescaledb.NewRepository(db)

	err := repo.SaveTrade(context.Background(), trade)
	assert.ErrorIs(t, err, dbErr)
}

func TestRepository_GetTrades_Success(t *testing.T) {
	from := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)
	tradeTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	tradeID := uuid.New()

	db := &mockDB{
		queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
			assert.True(t, strings.Contains(query, "SELECT"), "query must be a SELECT")
			assert.True(t, strings.Contains(query, "FROM trades"), "query must use correct table")
			assert.True(t, strings.Contains(query, "WHERE"), "query must have WHERE clause")
			assert.True(t, strings.Contains(query, "ORDER BY time"), "query must be ordered")

			// time, trade_id, execution_id, symbol, side, quantity, price, commission, status, strategy, rationale, thesis
			rows := &mockRows{
				data: [][]any{
					{tradeTime, tradeID, "", "AAPL", "BUY", 10.0, 150.0, 1.5, "FILLED", "", "", json.RawMessage(nil)},
				},
			}
			return rows, nil
		},
	}
	repo := timescaledb.NewRepository(db)

	trades, err := repo.GetTrades(context.Background(), "tenant-1", domain.EnvModePaper, from, to)
	assert.NoError(t, err)
	require.Len(t, trades, 1)
	assert.Equal(t, tradeTime, trades[0].Time)
	assert.Equal(t, tradeID, trades[0].TradeID)
	assert.Equal(t, domain.Symbol("AAPL"), trades[0].Symbol)
	assert.Equal(t, "BUY", trades[0].Side)
	assert.Equal(t, 10.0, trades[0].Quantity)
	assert.Equal(t, 150.0, trades[0].Price)
	assert.Equal(t, 1.5, trades[0].Commission)
	assert.Equal(t, "FILLED", trades[0].Status)
}

func TestRepository_SaveStrategyDNA_Success(t *testing.T) {
	id := uuid.New()
	dna, err := domain.NewStrategyDNA(id, "tenant-1", domain.EnvModePaper, 1, map[string]any{"p1": "v1"}, map[string]float64{"m1": 1.0})
	require.NoError(t, err)

	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			assert.True(t, strings.Contains(query, "INSERT INTO strategy_dna_history"), "query must contain table name")
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewRepository(db)

	err = repo.SaveStrategyDNA(context.Background(), dna)
	assert.NoError(t, err)
}

func TestRepository_GetLatestStrategyDNA_Success(t *testing.T) {
	dnaTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	id := uuid.New()

	paramsJSON := json.RawMessage(`{"p1":"v1"}`)
	metricsJSON := json.RawMessage(`{"m1":1.0}`)

	db := &mockDB{
		queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
			assert.True(t, strings.Contains(query, "SELECT"), "query must be a SELECT")
			assert.True(t, strings.Contains(query, "FROM strategy_dna_history"), "query must use correct table")
			assert.True(t, strings.Contains(query, "ORDER BY time DESC"), "query must be ordered")
			assert.True(t, strings.Contains(query, "LIMIT 1"), "query must be limited")

			return &mockRow{
				scanFunc: func(dest ...any) error {
					// time, strategy_id, version, parameters, performance
					*dest[0].(*time.Time) = dnaTime
					*dest[1].(*uuid.UUID) = id
					*dest[2].(*int) = 1
					*dest[3].(*json.RawMessage) = paramsJSON
					*dest[4].(*json.RawMessage) = metricsJSON
					return nil
				},
			}
		},
	}
	repo := timescaledb.NewRepository(db)

	dna, err := repo.GetLatestStrategyDNA(context.Background(), "tenant-1", domain.EnvModePaper)
	assert.NoError(t, err)
	require.NotNil(t, dna)
	assert.Equal(t, id, dna.ID)
	assert.Equal(t, 1, dna.Version)
	assert.Equal(t, map[string]any{"p1": "v1"}, dna.Parameters)
	assert.Equal(t, map[string]float64{"m1": 1.0}, dna.PerformanceMetrics)
}

func TestRepository_GetLatestStrategyDNA_NotFound(t *testing.T) {
	db := &mockDB{
		queryRowFunc: func(ctx context.Context, query string, args ...any) timescaledb.Row {
			return &mockRow{
				scanFunc: func(dest ...any) error {
					return sql.ErrNoRows
				},
			}
		},
	}
	repo := timescaledb.NewRepository(db)

	dna, err := repo.GetLatestStrategyDNA(context.Background(), "tenant-1", domain.EnvModePaper)
	assert.NoError(t, err)
	assert.Nil(t, dna)
}
