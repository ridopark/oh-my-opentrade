package backfill

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// RoutingFetcher dispatches GetHistoricalBars calls to Crypto or Equity based
// on whether the symbol is a crypto pair. It lets callers keep a single
// MarketDataFetcher handle while routing to the source with better coverage
// for each asset class (Coinbase for crypto, Alpaca for equities).
type RoutingFetcher struct {
	Crypto MarketDataFetcher
	Equity MarketDataFetcher
}

func (r *RoutingFetcher) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	if symbol.IsCryptoSymbol() {
		return r.Crypto.GetHistoricalBars(ctx, symbol, timeframe, from, to)
	}
	return r.Equity.GetHistoricalBars(ctx, symbol, timeframe, from, to)
}
