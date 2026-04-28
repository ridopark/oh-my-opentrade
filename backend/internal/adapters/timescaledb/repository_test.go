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
				if v != nil {
					*d = v.(string)
				}
			case *float64:
				if v != nil {
					*d = v.(float64)
				}
			case *int:
				if v != nil {
					*d = v.(int)
				}
			case *bool:
				if v != nil {
					*d = v.(bool)
				}
			case *time.Time:
				if v != nil {
					*d = v.(time.Time)
				}
			case *uuid.UUID:
				if v != nil {
					*d = v.(uuid.UUID)
				}
			case *json.RawMessage:
				if v != nil {
					*d = v.(json.RawMessage)
				}
			case *sql.NullString:
				if v == nil {
					*d = sql.NullString{}
				} else {
					*d = sql.NullString{String: v.(string), Valid: true}
				}
			case *sql.NullTime:
				if v == nil {
					*d = sql.NullTime{}
				} else {
					*d = sql.NullTime{Time: v.(time.Time), Valid: true}
				}
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

func (m *mockDB) BeginTx(_ context.Context, _ *sql.TxOptions) (*sql.Tx, error) {
	return nil, errors.New("mockDB: BeginTx not supported")
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

func TestRepository_SaveTrade_PropagatesBrokerOrderID(t *testing.T) {
	tradeTime := time.Date(2026, 4, 27, 16, 35, 0, 0, time.UTC)
	trade, err := domain.NewTrade(tradeTime, "tenant-1", domain.EnvModeLive, uuid.New(), "AAPL", "BUY", 4.0, 7.70, 0, "FILLED", "avwap_v4", "test")
	require.NoError(t, err)
	trade.BrokerOrderID = "3512"

	var captured []any
	db := &mockDB{
		execFunc: func(_ context.Context, query string, args ...any) (sql.Result, error) {
			assert.Contains(t, query, "broker_order_id")
			assert.Contains(t, query, "$24")
			captured = args
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewRepository(db)

	require.NoError(t, repo.SaveTrade(context.Background(), trade))
	require.Len(t, captured, 24)
	assert.Equal(t, "3512", captured[23])
}

func TestRepository_SaveTrade_NilBrokerOrderIDWhenEmpty(t *testing.T) {
	tradeTime := time.Date(2026, 4, 27, 16, 35, 0, 0, time.UTC)
	trade, err := domain.NewTrade(tradeTime, "tenant-1", domain.EnvModeLive, uuid.New(), "AAPL", "BUY", 4.0, 7.70, 0, "FILLED", "avwap_v4", "test")
	require.NoError(t, err)

	var captured []any
	db := &mockDB{
		execFunc: func(_ context.Context, _ string, args ...any) (sql.Result, error) {
			captured = args
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewRepository(db)

	require.NoError(t, repo.SaveTrade(context.Background(), trade))
	require.Len(t, captured, 24)
	assert.Nil(t, captured[23], "empty BrokerOrderID must arrive as NULL, not empty string")
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

func TestRepository_GetOrderByBrokerOrderID_LoadsOptionMetadata(t *testing.T) {
	orderTime := time.Date(2026, 4, 27, 16, 35, 0, 550879000, time.UTC)
	intentID := uuid.New()
	expiry := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	db := &mockDB{
		queryRowFunc: func(_ context.Context, query string, args ...any) timescaledb.Row {
			assert.Contains(t, query, "instrument_type")
			assert.Contains(t, query, "option_symbol")
			assert.Contains(t, query, "underlying")
			assert.Contains(t, query, "strike")
			assert.Contains(t, query, "expiry")
			assert.Contains(t, query, "option_right")
			assert.Equal(t, "3512", args[0])
			return &mockRow{
				scanFunc: func(dest ...any) error {
					require.Len(t, dest, 23)
					*dest[0].(*time.Time) = orderTime
					*dest[1].(*string) = "tenant-1"
					*dest[2].(*string) = string(domain.EnvModeLive)
					*dest[3].(*uuid.UUID) = intentID
					*dest[4].(*string) = "3512"
					*dest[5].(*string) = "NVDA260501C00207500"
					*dest[6].(*string) = "BUY"
					*dest[7].(*float64) = 4
					*dest[8].(*float64) = 7.70
					*dest[9].(*float64) = 0
					*dest[10].(*string) = "filled"
					*dest[11].(*time.Time) = orderTime
					*dest[12].(*float64) = 7.761
					*dest[13].(*float64) = 4
					*dest[14].(*string) = "avwap_v4"
					*dest[15].(*string) = "signal: entry buy"
					*dest[16].(*float64) = 0
					*dest[17].(*string) = string(domain.InstrumentTypeOption)
					*dest[18].(*string) = "NVDA260501C00207500"
					*dest[19].(*string) = "NVDA"
					*dest[20].(*float64) = 207.5
					*dest[21].(*time.Time) = expiry
					*dest[22].(*string) = "C"
					return nil
				},
			}
		},
	}
	repo := timescaledb.NewRepository(db)

	order, err := repo.GetOrderByBrokerOrderID(context.Background(), "3512")
	require.NoError(t, err)
	require.NotNil(t, order)
	assert.Equal(t, domain.InstrumentTypeOption, order.InstrumentType)
	assert.Equal(t, "NVDA260501C00207500", order.OptionSymbol)
	assert.Equal(t, "NVDA", order.Underlying)
	assert.Equal(t, 207.5, order.Strike)
	assert.Equal(t, expiry, order.Expiry)
	assert.Equal(t, "C", order.OptionRight)
}

func TestRepository_GetOrderByBrokerOrderID_NotFound(t *testing.T) {
	db := &mockDB{
		queryRowFunc: func(_ context.Context, _ string, _ ...any) timescaledb.Row {
			return &mockRow{scanFunc: func(_ ...any) error { return sql.ErrNoRows }}
		},
	}
	repo := timescaledb.NewRepository(db)

	order, err := repo.GetOrderByBrokerOrderID(context.Background(), "missing")
	assert.NoError(t, err)
	assert.Nil(t, order)
}
