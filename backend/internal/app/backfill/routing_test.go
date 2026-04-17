package backfill_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/app/backfill"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

type fakeFetcher struct {
	name  string
	calls int
	err   error
}

func (f *fakeFetcher) GetHistoricalBars(ctx context.Context, sym domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	sample, _ := domain.NewMarketBar(from, sym, tf, 1, 2, 0.5, 1.5, 10)
	return []domain.MarketBar{sample}, nil
}

func TestRoutingFetcher_DispatchesCryptoToCryptoFetcher(t *testing.T) {
	crypto := &fakeFetcher{name: "crypto"}
	equity := &fakeFetcher{name: "equity"}
	r := &backfill.RoutingFetcher{Crypto: crypto, Equity: equity}

	sym, err := domain.NewSymbol("BTC/USD")
	require.NoError(t, err)
	tf, err := domain.NewTimeframe("5m")
	require.NoError(t, err)

	bars, err := r.GetHistoricalBars(context.Background(), sym, tf, time.Unix(0, 0), time.Unix(60, 0))
	require.NoError(t, err)
	assert.Len(t, bars, 1)
	assert.Equal(t, 1, crypto.calls)
	assert.Equal(t, 0, equity.calls)
}

func TestRoutingFetcher_DispatchesEquityToEquityFetcher(t *testing.T) {
	crypto := &fakeFetcher{name: "crypto"}
	equity := &fakeFetcher{name: "equity"}
	r := &backfill.RoutingFetcher{Crypto: crypto, Equity: equity}

	sym, err := domain.NewSymbol("AAPL")
	require.NoError(t, err)
	tf, err := domain.NewTimeframe("5m")
	require.NoError(t, err)

	bars, err := r.GetHistoricalBars(context.Background(), sym, tf, time.Unix(0, 0), time.Unix(60, 0))
	require.NoError(t, err)
	assert.Len(t, bars, 1)
	assert.Equal(t, 0, crypto.calls)
	assert.Equal(t, 1, equity.calls)
}

func TestRoutingFetcher_PropagatesError(t *testing.T) {
	boom := errors.New("boom")
	r := &backfill.RoutingFetcher{
		Crypto: &fakeFetcher{err: boom},
		Equity: &fakeFetcher{},
	}

	sym, _ := domain.NewSymbol("ETH/USD")
	tf, _ := domain.NewTimeframe("1h")
	_, err := r.GetHistoricalBars(context.Background(), sym, tf, time.Unix(0, 0), time.Unix(60, 0))
	assert.ErrorIs(t, err, boom)
}

// Compile-time assertion that *RoutingFetcher satisfies the MarketDataFetcher
// interface so wiring in cmd/* binaries doesn't break if the signature drifts.
var _ backfill.MarketDataFetcher = (*backfill.RoutingFetcher)(nil)
