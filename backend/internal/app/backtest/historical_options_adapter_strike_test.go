package backtest

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bulkStubRepo struct {
	rows []domain.HistoricalOptionChainRow
}

func (s bulkStubRepo) GetHistoricalChainBulk(_ context.Context, _ []domain.Symbol, _, _ time.Time) ([]domain.HistoricalOptionChainRow, error) {
	return s.rows, nil
}
func (s bulkStubRepo) GetHistoricalChain(_ context.Context, _ domain.Symbol, _ time.Time, _ domain.OptionRight, _, _ int) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (s bulkStubRepo) GetHistoricalContract(_ context.Context, _ domain.Symbol, _ time.Time, _ float64, _ time.Time, _ domain.OptionRight) (*domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (s bulkStubRepo) HasData(_ context.Context, _ domain.Symbol, _ time.Time) (bool, error) {
	return true, nil
}
func (s bulkStubRepo) SaveBatch(_ context.Context, _ []domain.HistoricalOptionChainRow) error {
	return nil
}

func makeRow(sym string, strike float64, expiry time.Time, date time.Time) domain.HistoricalOptionChainRow {
	return domain.HistoricalOptionChainRow{
		Symbol:     domain.Symbol(sym),
		Date:       date,
		Expiration: expiry,
		Strike:     strike,
		Right:      domain.OptionRightCall,
		Bid:        1.0,
		Ask:        1.1,
		Delta:      0.5,
		IV:         0.30,
	}
}

// TestGetHistoricalContract_RelativeTolerance_LargeStock guards the regression
// where a flat $2 tolerance missed every listed strike for $500 names. With
// the relative 2% floor, a $497.5 strike must match a $500 request.
func TestGetHistoricalContract_RelativeTolerance_LargeStock(t *testing.T) {
	date := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	expiry := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	repo := bulkStubRepo{rows: []domain.HistoricalOptionChainRow{
		makeRow("NVDA", 490.0, expiry, date),
		makeRow("NVDA", 497.5, expiry, date),
		makeRow("NVDA", 510.0, expiry, date),
	}}
	adapter := NewHistoricalOptionsAdapter(repo, func() time.Time { return date })
	require.NoError(t, adapter.PreLoad(context.Background(), []domain.Symbol{"NVDA"}, date, date.AddDate(0, 0, 1)))

	got, err := adapter.GetHistoricalContract(context.Background(), "NVDA", date, 500.0, expiry, domain.OptionRightCall)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 497.5, got.Strike, "497.5 is within 2% of 500 so should match")
}

// TestGetHistoricalContract_AbsoluteFloor_SmallStock guards that the $2 floor
// still works for low-priced names where 2% would be unhelpfully tight.
func TestGetHistoricalContract_AbsoluteFloor_SmallStock(t *testing.T) {
	date := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	expiry := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	repo := bulkStubRepo{rows: []domain.HistoricalOptionChainRow{
		makeRow("SOFI", 4.5, expiry, date),
		makeRow("SOFI", 5.0, expiry, date),
		makeRow("SOFI", 5.5, expiry, date),
	}}
	adapter := NewHistoricalOptionsAdapter(repo, func() time.Time { return date })
	require.NoError(t, adapter.PreLoad(context.Background(), []domain.Symbol{"SOFI"}, date, date.AddDate(0, 0, 1)))

	// Requesting strike=5 should find 5.0 exactly; 4.5 and 5.5 are both within
	// the $2 absolute floor so they're valid candidates but 5.0 wins on distance.
	got, err := adapter.GetHistoricalContract(context.Background(), "SOFI", date, 5.0, expiry, domain.OptionRightCall)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 5.0, got.Strike)
}

// TestGetHistoricalContract_TieBreakByExpiry verifies that when two rows have
// identical strike distance, the closer expiry wins.
func TestGetHistoricalContract_TieBreakByExpiry(t *testing.T) {
	date := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	wantExpiry := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	nearExpiry := wantExpiry.AddDate(0, 0, 2)
	farExpiry := wantExpiry.AddDate(0, 0, 5)
	repo := bulkStubRepo{rows: []domain.HistoricalOptionChainRow{
		makeRow("AAPL", 150.0, farExpiry, date),
		makeRow("AAPL", 150.0, nearExpiry, date),
	}}
	adapter := NewHistoricalOptionsAdapter(repo, func() time.Time { return date })
	require.NoError(t, adapter.PreLoad(context.Background(), []domain.Symbol{"AAPL"}, date, date.AddDate(0, 0, 1)))

	got, err := adapter.GetHistoricalContract(context.Background(), "AAPL", date, 150.0, wantExpiry, domain.OptionRightCall)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, nearExpiry, got.Expiration, "closer expiry must win when strikes tie")
}

func TestPreLoad_MarksSyntheticLast(t *testing.T) {
	date := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	expiry := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	repo := bulkStubRepo{rows: []domain.HistoricalOptionChainRow{
		makeRow("AAPL", 150.0, expiry, date),
	}}
	adapter := NewHistoricalOptionsAdapter(repo, func() time.Time { return date })
	require.NoError(t, adapter.PreLoad(context.Background(), []domain.Symbol{"AAPL"}, date, date.AddDate(0, 0, 1)))

	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", expiry, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	require.NotEmpty(t, chain)
	for _, snap := range chain {
		assert.True(t, snap.IsSyntheticLast, "DoltHub rows have no trade prints; Last must be flagged synthetic")
	}
}
