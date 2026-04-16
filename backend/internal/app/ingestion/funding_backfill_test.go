package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockFundingSource implements ports.FundingRatesPort for testing.
type mockFundingSource struct {
	latestFunc  func(ctx context.Context, venue domain.Venue, sym domain.Symbol) (ports.FundingRate, error)
	historyFunc func(ctx context.Context, venue domain.Venue, sym domain.Symbol, from, to time.Time) ([]ports.FundingRate, error)
	streamFunc  func(ctx context.Context, venue domain.Venue, sym domain.Symbol) (<-chan ports.FundingRate, error)
}

func (m *mockFundingSource) Latest(ctx context.Context, venue domain.Venue, sym domain.Symbol) (ports.FundingRate, error) {
	if m.latestFunc != nil {
		return m.latestFunc(ctx, venue, sym)
	}
	return ports.FundingRate{}, nil
}

func (m *mockFundingSource) History(ctx context.Context, venue domain.Venue, sym domain.Symbol, from, to time.Time) ([]ports.FundingRate, error) {
	if m.historyFunc != nil {
		return m.historyFunc(ctx, venue, sym, from, to)
	}
	return nil, nil
}

func (m *mockFundingSource) Stream(ctx context.Context, venue domain.Venue, sym domain.Symbol) (<-chan ports.FundingRate, error) {
	if m.streamFunc != nil {
		return m.streamFunc(ctx, venue, sym)
	}
	return nil, errors.New("stream not supported")
}

// mockFundingDB implements timescaledb.DBTX for testing the repo layer.
type mockFundingDB struct {
	execCalls int
	execFunc  func(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (m *mockFundingDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	m.execCalls++
	if m.execFunc != nil {
		return m.execFunc(ctx, query, args...)
	}
	return mockSQLResult{affected: 1}, nil
}

func (m *mockFundingDB) QueryContext(_ context.Context, _ string, _ ...any) (timescaledb.Rows, error) {
	return nil, nil
}

func (m *mockFundingDB) QueryRowContext(_ context.Context, _ string, _ ...any) timescaledb.Row {
	return nil
}

type mockSQLResult struct{ affected int64 }

func (m mockSQLResult) LastInsertId() (int64, error) { return 0, nil }
func (m mockSQLResult) RowsAffected() (int64, error) { return m.affected, nil }

func TestFundingBackfill_Run_ChunksDaily(t *testing.T) {
	historyCalls := 0
	source := &mockFundingSource{
		historyFunc: func(_ context.Context, venue domain.Venue, sym domain.Symbol, from, to time.Time) ([]ports.FundingRate, error) {
			historyCalls++
			// Return 3 rates per chunk.
			return []ports.FundingRate{
				{Venue: venue, Symbol: sym, Timestamp: from, Rate: 0.0001, IntervalHours: 8},
				{Venue: venue, Symbol: sym, Timestamp: from.Add(8 * time.Hour), Rate: 0.00012, IntervalHours: 8},
				{Venue: venue, Symbol: sym, Timestamp: from.Add(16 * time.Hour), Rate: 0.00015, IntervalHours: 8},
			}, nil
		},
	}

	db := &mockFundingDB{}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())
	bf := NewFundingBackfill(source, repo, zerolog.Nop())

	// 3-day range should produce 3 daily chunks.
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)

	err := bf.Run(context.Background(), domain.VenueBybit, []domain.Symbol{"BTC/USD"}, from, to)
	require.NoError(t, err)
	assert.Equal(t, 3, historyCalls, "should fetch 3 daily chunks")
	assert.Equal(t, 3, db.execCalls, "should insert 3 batches")
}

func TestFundingBackfill_Run_MultipleSymbols(t *testing.T) {
	symbolsSeen := map[domain.Symbol]int{}
	source := &mockFundingSource{
		historyFunc: func(_ context.Context, venue domain.Venue, sym domain.Symbol, _, _ time.Time) ([]ports.FundingRate, error) {
			symbolsSeen[sym]++
			return []ports.FundingRate{
				{Venue: venue, Symbol: sym, Timestamp: time.Now(), Rate: 0.0001, IntervalHours: 8},
			}, nil
		},
	}

	db := &mockFundingDB{}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())
	bf := NewFundingBackfill(source, repo, zerolog.Nop())

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	syms := []domain.Symbol{"BTC/USD", "ETH/USD", "SOL/USD"}
	err := bf.Run(context.Background(), domain.VenueBybit, syms, from, to)
	require.NoError(t, err)
	assert.Len(t, symbolsSeen, 3)
	for _, sym := range syms {
		assert.Equal(t, 1, symbolsSeen[sym])
	}
}

func TestFundingBackfill_Run_HistoryError(t *testing.T) {
	histErr := errors.New("connection refused")
	source := &mockFundingSource{
		historyFunc: func(_ context.Context, _ domain.Venue, _ domain.Symbol, _, _ time.Time) ([]ports.FundingRate, error) {
			return nil, histErr
		},
	}

	db := &mockFundingDB{}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())
	bf := NewFundingBackfill(source, repo, zerolog.Nop())

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	err := bf.Run(context.Background(), domain.VenueBybit, []domain.Symbol{"BTC/USD"}, from, to)
	require.Error(t, err)
	assert.ErrorIs(t, err, histErr)
}

func TestFundingBackfill_Run_EmptyHistory(t *testing.T) {
	source := &mockFundingSource{
		historyFunc: func(_ context.Context, _ domain.Venue, _ domain.Symbol, _, _ time.Time) ([]ports.FundingRate, error) {
			return nil, nil
		},
	}

	db := &mockFundingDB{}
	repo := timescaledb.NewFundingRepo(db, zerolog.Nop())
	bf := NewFundingBackfill(source, repo, zerolog.Nop())

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	err := bf.Run(context.Background(), domain.VenueBybit, []domain.Symbol{"BTC/USD"}, from, to)
	require.NoError(t, err)
	assert.Equal(t, 0, db.execCalls, "should not call DB when no rates returned")
}
