package backtest

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
// stubHistOptRepo is a minimal HistoricalOptionsPort that returns empty
// results for everything, forcing the synthetic fallback path.
type stubHistOptRepo struct{}
func (stubHistOptRepo) GetHistoricalChain(_ context.Context, _ domain.Symbol, _ time.Time, _ domain.OptionRight, _, _ int) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (stubHistOptRepo) GetHistoricalChainBulk(_ context.Context, _ []domain.Symbol, _, _ time.Time) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (stubHistOptRepo) GetHistoricalContract(_ context.Context, _ domain.Symbol, _ time.Time, _ float64, _ time.Time, _ domain.OptionRight) (*domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (stubHistOptRepo) HasData(_ context.Context, _ domain.Symbol, _ time.Time) (bool, error) {
	return false, nil
}
func (stubHistOptRepo) SaveBatch(_ context.Context, _ []domain.HistoricalOptionChainRow) error {
	return nil
}
// nonEmptyHistOptRepo returns a single row IN the test's DTE window
// so the DB path short-circuits before synthetic generation. Callers
// in this file use minDTE=1, maxDTE=14; the 7-DTE expiry here lands in
// range. Predates the in-range predicate: the fixture used to return
// 30 DTE and pass by virtue of "any DB row" blocking synthetic; now
// only in-range rows block it.
type nonEmptyHistOptRepo struct{}
func (nonEmptyHistOptRepo) GetHistoricalChain(_ context.Context, sym domain.Symbol, asOf time.Time, right domain.OptionRight, _, _ int) ([]domain.HistoricalOptionChainRow, error) {
	return []domain.HistoricalOptionChainRow{{
		Symbol:     sym,
		Date:       asOf,
		Expiration: asOf.AddDate(0, 0, 7),
		Strike:     100.0,
		Right:      right,
		Bid:        1.0, Ask: 1.1, Delta: 0.5, IV: 0.30,
	}}, nil
}
// outOfRangeHistOptRepo returns a 30-DTE row that's OUTSIDE the test's
// [1,14] DTE window. Direct regression for the DoltHub-monthly-chain
// gap that motivated the generator: DB has data, all of it out-of-range,
// synthetic MUST fire to supply in-range weeklies.
type outOfRangeHistOptRepo struct{}
func (outOfRangeHistOptRepo) GetHistoricalChain(_ context.Context, sym domain.Symbol, asOf time.Time, right domain.OptionRight, _, _ int) ([]domain.HistoricalOptionChainRow, error) {
	return []domain.HistoricalOptionChainRow{{
		Symbol:     sym,
		Date:       asOf,
		Expiration: asOf.AddDate(0, 0, 30),
		Strike:     100.0,
		Right:      right,
		Bid:        1.0, Ask: 1.1, Delta: 0.5, IV: 0.30,
	}}, nil
}
func (outOfRangeHistOptRepo) GetHistoricalChainBulk(_ context.Context, _ []domain.Symbol, _, _ time.Time) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (outOfRangeHistOptRepo) GetHistoricalContract(_ context.Context, _ domain.Symbol, _ time.Time, _ float64, _ time.Time, _ domain.OptionRight) (*domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (outOfRangeHistOptRepo) HasData(_ context.Context, _ domain.Symbol, _ time.Time) (bool, error) {
	return true, nil
}
func (outOfRangeHistOptRepo) SaveBatch(_ context.Context, _ []domain.HistoricalOptionChainRow) error {
	return nil
}
func (nonEmptyHistOptRepo) GetHistoricalChainBulk(_ context.Context, _ []domain.Symbol, _, _ time.Time) ([]domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (nonEmptyHistOptRepo) GetHistoricalContract(_ context.Context, _ domain.Symbol, _ time.Time, _ float64, _ time.Time, _ domain.OptionRight) (*domain.HistoricalOptionChainRow, error) {
	return nil, nil
}
func (nonEmptyHistOptRepo) HasData(_ context.Context, _ domain.Symbol, _ time.Time) (bool, error) {
	return true, nil
}
func (nonEmptyHistOptRepo) SaveBatch(_ context.Context, _ []domain.HistoricalOptionChainRow) error {
	return nil
}
func TestAdapter_SyntheticFallback_UsedWhenDBEmpty(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(stubHistOptRepo{}, func() time.Time { return asOf })
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), constantSpot(100.0), constantIV(0.30),
	))
	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf.AddDate(0, 0, 7), domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	assert.NotEmpty(t, chain, "synthetic generator should fill the gap when DB is empty")
}
func TestAdapter_SyntheticFallback_SkippedWhenDBHasData(t *testing.T) {
	// nonEmptyHistOptRepo always returns one row, so synthetic path must
	// NOT be triggered. A generator with an always-panicking spot function
	// proves it - if synthetic ran, this test would panic.
	panicSpot := func(_ context.Context, _ domain.Symbol, _ time.Time) (float64, error) {
		panic("synthetic generator should not run when DB has data")
	}
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(nonEmptyHistOptRepo{}, func() time.Time { return asOf })
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), panicSpot, constantIV(0.30),
	))
	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf.AddDate(0, 0, 7), domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	assert.Len(t, chain, 1, "should return the single DB row without invoking synthetic")
}
func TestAdapter_SyntheticFallback_FiresWhenDBOutOfRange(t *testing.T) {
	// DB returns one row at 30 DTE, caller requests [1,14]. The synthetic
	// path must fire because the DB has no in-window rows. Direct
	// regression for the DoltHub monthly-chain gap where SOFI/MU/etc
	// returned non-empty chains but every contract was filter-rejected
	// on DTE alone.
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(outOfRangeHistOptRepo{}, func() time.Time { return asOf })
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), constantSpot(100.0), constantIV(0.30),
	))
	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf.AddDate(0, 0, 7), domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	require.NotEmpty(t, chain, "synthetic MUST fire when DB has rows but none in DTE window")
	for _, snap := range chain {
		dte := int(snap.OptionContract.Expiry.Sub(asOf).Hours() / 24)
		assert.GreaterOrEqual(t, dte, 1, "synthetic contract must be within requested DTE window")
		assert.LessOrEqual(t, dte, 14, "synthetic contract must be within requested DTE window")
	}
}
func TestAdapter_SyntheticFallback_NoOpWhenDisabled(t *testing.T) {
	// Adapter with no synthetic generator attached must behave exactly
	// as before: empty DB -> empty chain, no error.
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(stubHistOptRepo{}, func() time.Time { return asOf })
	chain, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf.AddDate(0, 0, 7), domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	assert.Empty(t, chain)
}
func TestAdapter_SyntheticFallback_CachesResult(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(stubHistOptRepo{}, func() time.Time { return asOf })
	calls := 0
	spotFn := func(_ context.Context, _ domain.Symbol, _ time.Time) (float64, error) {
		calls++
		return 100.0, nil
	}
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), spotFn, constantIV(0.30),
	))
	_, _ = adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	_, _ = adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	assert.Equal(t, 1, calls, "second call should hit the synthetic cache, not regenerate")
}

// TestAdapter_SyntheticFallback_DifferentDTEWindowsNotShared guards the cache
// key fix: a DTE 1..14 chain must not satisfy a later DTE 30..60 request.
// The generator must fire twice, producing different contract sets per window.
func TestAdapter_SyntheticFallback_DifferentDTEWindowsNotShared(t *testing.T) {
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	adapter := NewHistoricalOptionsAdapter(stubHistOptRepo{}, func() time.Time { return asOf })
	calls := 0
	spotFn := func(_ context.Context, _ domain.Symbol, _ time.Time) (float64, error) {
		calls++
		return 100.0, nil
	}
	adapter.SetSyntheticGenerator(NewSyntheticChainGenerator(
		defaultTestConfig(), spotFn, constantIV(0.30),
	))

	shortWin, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	longWin, err := adapter.GetOptionChain(context.Background(), "AAPL", asOf, domain.OptionRightCall, 30, 60)
	require.NoError(t, err)

	assert.Equal(t, 2, calls, "different DTE windows must each generate; no cache sharing")
	require.NotEmpty(t, shortWin)
	require.NotEmpty(t, longWin)

	// Expiries in the two windows should not overlap: short-window max
	// expiry < long-window min expiry. If the cache were shared, one call
	// would return the other's set and this would fail.
	var shortMaxExp, longMinExp time.Time
	for _, s := range shortWin {
		if shortMaxExp.IsZero() || s.OptionContract.Expiry.After(shortMaxExp) {
			shortMaxExp = s.OptionContract.Expiry
		}
	}
	for _, s := range longWin {
		if longMinExp.IsZero() || s.OptionContract.Expiry.Before(longMinExp) {
			longMinExp = s.OptionContract.Expiry
		}
	}
	assert.True(t, shortMaxExp.Before(longMinExp),
		"short-window max expiry %s must precede long-window min expiry %s",
		shortMaxExp.Format("2006-01-02"), longMinExp.Format("2006-01-02"))
}
