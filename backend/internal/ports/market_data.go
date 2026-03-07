package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// BarHandler is a callback function for processing incoming market bars.
type BarHandler func(ctx context.Context, bar domain.MarketBar) error

// TradeHandler is a callback for processing incoming trade ticks (for real-time chart only).
type TradeHandler func(ctx context.Context, trade domain.MarketTrade) error

// MarketDataPort defines the interface for interacting with market data providers.
type MarketDataPort interface {
	StreamBars(ctx context.Context, symbols []domain.Symbol, timeframe domain.Timeframe, handler BarHandler) error
	GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
	Close() error
}
