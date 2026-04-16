package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCALookup is a minimal in-memory implementation of
// domain.CorporateActionsLookup for tests. It lets each test supply exactly
// the action list and delisted bit it wants without touching a DB.
type fakeCALookup struct {
	actions   []domain.CorporateActionView
	delisted  bool
	lastAsOf  time.Time
	lastQuery string
}

func (f *fakeCALookup) Between(_ context.Context, symbol string, _, _ time.Time) ([]domain.CorporateActionView, error) {
	f.lastQuery = symbol
	return f.actions, nil
}
func (f *fakeCALookup) Delisted(_ context.Context, _ string, asOf time.Time) (bool, error) {
	f.lastAsOf = asOf
	return f.delisted, nil
}

func TestApplyCorporateActions_ForwardSplitAdjustsPreSplitStrikes(t *testing.T) {
	effDate := time.Date(2020, 8, 31, 0, 0, 0, 0, time.UTC)
	pre := time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC)
	post := time.Date(2020, 9, 15, 0, 0, 0, 0, time.UTC)

	chain := domain.HistoricalOptionChain{
		{Date: pre, Symbol: "AAPL", Strike: 500, Right: domain.OptionRightCall},
		{Date: post, Symbol: "AAPL", Strike: 125, Right: domain.OptionRightCall},
	}

	lookup := &fakeCALookup{actions: []domain.CorporateActionView{
		{ActionType: "split", EffectiveDate: effDate, RatioNumerator: 4, RatioDenominator: 1},
	}}

	require.NoError(t, chain.ApplyCorporateActions(context.Background(), lookup))

	assert.InDelta(t, 125.0, chain[0].Strike, 1e-9, "pre-split 500 should become 125 after a 4-for-1 split")
	assert.InDelta(t, 125.0, chain[1].Strike, 1e-9, "post-split strikes stay put")
	assert.Equal(t, 100.0, chain[0].Multiplier)
	assert.Equal(t, 100.0, chain[1].Multiplier)
	assert.False(t, chain[0].IsPostDelisting())
}

func TestApplyCorporateActions_ReverseSplitMultipliesPreSplitStrikes(t *testing.T) {
	effDate := time.Date(2022, 1, 10, 0, 0, 0, 0, time.UTC)
	pre := time.Date(2021, 12, 1, 0, 0, 0, 0, time.UTC)
	post := time.Date(2022, 2, 1, 0, 0, 0, 0, time.UTC)

	// Pre-reverse-split price of $2 becomes $10 after a 1-for-5 reverse
	// split. A strike quoted in pre-split coords at $10 must become $50 in
	// post-split coords — i.e. divided by f=0.2.
	chain := domain.HistoricalOptionChain{
		{Date: pre, Symbol: "TEST", Strike: 10},
		{Date: post, Symbol: "TEST", Strike: 50},
	}
	lookup := &fakeCALookup{actions: []domain.CorporateActionView{
		{ActionType: "reverse_split", EffectiveDate: effDate, RatioNumerator: 1, RatioDenominator: 5},
	}}
	require.NoError(t, chain.ApplyCorporateActions(context.Background(), lookup))

	assert.InDelta(t, 50.0, chain[0].Strike, 1e-9, "pre-reverse-split strike should be multiplied by 5")
	assert.InDelta(t, 50.0, chain[1].Strike, 1e-9)
}

func TestApplyCorporateActions_DelistingFlagsPostRows(t *testing.T) {
	delist := time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)
	before := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	after := time.Date(2023, 7, 1, 0, 0, 0, 0, time.UTC)

	chain := domain.HistoricalOptionChain{
		{Date: before, Symbol: "BYE", Strike: 10},
		{Date: after, Symbol: "BYE", Strike: 10},
	}
	lookup := &fakeCALookup{
		delisted: true,
		actions: []domain.CorporateActionView{
			{ActionType: "delisting", EffectiveDate: delist},
		},
	}
	require.NoError(t, chain.ApplyCorporateActions(context.Background(), lookup))
	assert.False(t, chain[0].IsPostDelisting())
	assert.True(t, chain[1].IsPostDelisting())
}

func TestApplyCorporateActions_EmptyChainIsNoOp(t *testing.T) {
	var chain domain.HistoricalOptionChain
	require.NoError(t, chain.ApplyCorporateActions(context.Background(), &fakeCALookup{}))
}

func TestApplyCorporateActions_NilLookupIsNoOp(t *testing.T) {
	chain := domain.HistoricalOptionChain{{Date: time.Now(), Symbol: "A", Strike: 10}}
	require.NoError(t, chain.ApplyCorporateActions(context.Background(), nil))
	assert.Equal(t, 10.0, chain[0].Strike)
}
