package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/dnaapproval"
)

// RepositoryPort defines the interface for data persistence operations.
type RepositoryPort interface {
	SaveMarketBar(ctx context.Context, bar domain.MarketBar) error
	GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
	SaveTrade(ctx context.Context, trade domain.Trade) error
	GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error)
	UpdateTradeThesis(ctx context.Context, tenantID string, envMode domain.EnvMode, symbol domain.Symbol, thesis json.RawMessage) error
	SaveStrategyDNA(ctx context.Context, dna domain.StrategyDNA) error
	GetLatestStrategyDNA(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error)
	SaveOrder(ctx context.Context, order domain.BrokerOrder) error
	UpdateOrderFill(ctx context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64) error

	// ListTrades retrieves trades with optional filters and keyset pagination.
	// cursor is the (time, trade_id) composite for keyset pagination.
	ListTrades(ctx context.Context, q TradeQuery) (TradePage, error)

	// ListOrders retrieves orders with optional filters and keyset pagination.
	ListOrders(ctx context.Context, q OrderQuery) (OrderPage, error)

	// GetMaxBarHighSince returns the maximum bar high price for a symbol since a given time.
	// Returns 0 if no bars exist in the range.
	GetMaxBarHighSince(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, since time.Time) (float64, error)

	// SaveThoughtLog persists an AI debate thought log record.
	SaveThoughtLog(ctx context.Context, tl domain.ThoughtLog) error

	// GetThoughtLogsByIntentID retrieves thought logs linked to a specific order intent.
	GetThoughtLogsByIntentID(ctx context.Context, intentID string) ([]domain.ThoughtLog, error)
}

// TradeQuery defines the filter and pagination parameters for listing trades.
type TradeQuery struct {
	TenantID   string
	EnvMode    domain.EnvMode
	From       time.Time
	To         time.Time
	Symbol     string     // optional filter
	Side       string     // optional filter: BUY or SELL
	Strategy   string     // optional filter
	Limit      int        // max rows to return
	CursorTime *time.Time // keyset cursor: trades before this time
	CursorID   string     // keyset cursor: trade_id at cursor time
}

// TradePage is a paginated result set of trades.
type TradePage struct {
	Items      []domain.Trade
	NextCursor string // opaque cursor for next page, empty if no more
}

// OrderQuery defines the filter and pagination parameters for listing orders.
type OrderQuery struct {
	TenantID   string
	EnvMode    domain.EnvMode
	From       time.Time
	To         time.Time
	Symbol     string     // optional filter
	Side       string     // optional filter: BUY or SELL
	Strategy   string     // optional filter
	Limit      int        // max rows to return
	CursorTime *time.Time // keyset cursor: orders before this time
	CursorID   string     // keyset cursor: intent_id at cursor time
}

// OrderPage is a paginated result set of orders.
type OrderPage struct {
	Items      []domain.BrokerOrder
	NextCursor string // opaque cursor for next page, empty if no more
}

type DNAApprovalRepoPort interface {
	SaveDNAVersion(ctx context.Context, v dnaapproval.DNAVersion) error
	GetDNAVersion(ctx context.Context, id string) (*dnaapproval.DNAVersion, error)
	GetDNAVersionByHash(ctx context.Context, strategyKey, contentHash string) (*dnaapproval.DNAVersion, error)
	SaveDNAApproval(ctx context.Context, a dnaapproval.DNAApproval) error
	UpdateDNAApproval(ctx context.Context, id string, status dnaapproval.DNAStatus, decidedBy string, comment string) error
	GetDNAApproval(ctx context.Context, id string) (*dnaapproval.DNAApproval, error)
	ListPendingApprovals(ctx context.Context) ([]dnaapproval.DNAApproval, error)
	GetActiveDNAVersion(ctx context.Context, strategyKey string) (*dnaapproval.DNAVersion, error)
}
