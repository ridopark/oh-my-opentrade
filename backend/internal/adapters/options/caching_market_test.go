package options_test

import (
	"context"
	"testing"
	"time"

	optadapter "github.com/oh-my-opentrade/backend/internal/adapters/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingMarket records every call so the test can verify the
// caching wrapper's key shape: same (symbol, right) but different
// (minDTE, maxDTE) must NOT share a cached entry.
type countingMarket struct {
	calls []callRecord
	resp  []domain.OptionContractSnapshot
}

type callRecord struct {
	symbol         domain.Symbol
	right          domain.OptionRight
	minDTE, maxDTE int
}

func (m *countingMarket) GetOptionChain(
	_ context.Context,
	underlying domain.Symbol,
	_ time.Time,
	right domain.OptionRight,
	minDTE, maxDTE int,
) ([]domain.OptionContractSnapshot, error) {
	m.calls = append(m.calls, callRecord{symbol: underlying, right: right, minDTE: minDTE, maxDTE: maxDTE})
	return m.resp, nil
}

var _ ports.OptionsMarketDataPort = (*countingMarket)(nil)

// TestCachingMarket_KeyIncludesDTE guards the cache-key correctness fix:
// the Alpaca adapter computes expiryFrom/expiryTo from minDTE/maxDTE, so
// two calls for the same (symbol, right) but different DTE windows must
// each hit the underlying. Sharing a cache entry across DTE windows
// would silently return a chain filtered to the wrong expiries.
func TestCachingMarket_KeyIncludesDTE(t *testing.T) {
	inner := &countingMarket{
		resp: []domain.OptionContractSnapshot{{}},
	}
	wrapper := optadapter.NewCachingMarket(inner)

	ctx := context.Background()
	exp := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)

	_, err := wrapper.GetOptionChain(ctx, "AAPL", exp, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	_, err = wrapper.GetOptionChain(ctx, "AAPL", exp, domain.OptionRightCall, 30, 60)
	require.NoError(t, err)

	assert.Len(t, inner.calls, 2, "different DTE windows must each call underlying; cache must not share keys")
}

// TestCachingMarket_SameKeyHitsCache is the dual: identical
// (symbol, right, minDTE, maxDTE) MUST return the cached entry on the
// second call.
func TestCachingMarket_SameKeyHitsCache(t *testing.T) {
	inner := &countingMarket{
		resp: []domain.OptionContractSnapshot{{}},
	}
	wrapper := optadapter.NewCachingMarket(inner)

	ctx := context.Background()
	exp := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)

	_, err := wrapper.GetOptionChain(ctx, "AAPL", exp, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)
	_, err = wrapper.GetOptionChain(ctx, "AAPL", exp, domain.OptionRightCall, 1, 14)
	require.NoError(t, err)

	assert.Len(t, inner.calls, 1, "identical key must hit cache on second call")
}
