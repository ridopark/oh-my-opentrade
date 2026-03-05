package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// RepositoryPort defines the interface for data persistence operations.
type RepositoryPort interface {
	SaveMarketBar(ctx context.Context, bar domain.MarketBar) error
	GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
	SaveTrade(ctx context.Context, trade domain.Trade) error
	GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error)
	SaveStrategyDNA(ctx context.Context, dna domain.StrategyDNA) error
	GetLatestStrategyDNA(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error)
	SaveOrder(ctx context.Context, order domain.BrokerOrder) error
	UpdateOrderFill(ctx context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64) error

	// ListTrades retrieves trades with optional filters and keyset pagination.
	// cursor is the (time, trade_id) composite for keyset pagination.
	ListTrades(ctx context.Context, q TradeQuery) (TradePage, error)
}

// TradeQuery defines the filter and pagination parameters for listing trades.
type TradeQuery struct {
	TenantID string
	EnvMode  domain.EnvMode
	From     time.Time
	To       time.Time
	Symbol   string // optional filter
	Side     string // optional filter: BUY or SELL
	Limit    int    // max rows to return
	CursorTime *time.Time // keyset cursor: trades before this time
	CursorID   string     // keyset cursor: trade_id at cursor time
}

// TradePage is a paginated result set of trades.
type TradePage struct {
	Items      []domain.Trade
	NextCursor string // opaque cursor for next page, empty if no more
}
