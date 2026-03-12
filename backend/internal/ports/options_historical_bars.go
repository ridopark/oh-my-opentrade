package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// OptionsHistoricalBarsPort fetches historical OHLCV bars for option contracts.
// Used by omo-replay to inject option prices into the price cache during backtest
// so that price-dependent exit rules (MAX_LOSS, PROFIT_TARGET) can fire correctly.
type OptionsHistoricalBarsPort interface {
	GetHistoricalOptionBars(ctx context.Context, symbols []domain.Symbol, start, end time.Time) (map[domain.Symbol][]domain.MarketBar, error)
}
