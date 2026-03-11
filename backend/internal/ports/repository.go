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

	// GetLatestThesisForSymbol returns the most recent non-null thesis JSON from the
	// trades table for the given symbol. Used during bootstrap to retroactively restore
	// entry theses for positions that lost their thesis due to crash timing.
	// Returns (nil, nil) when no thesis exists.
	GetLatestThesisForSymbol(ctx context.Context, tenantID string, envMode domain.EnvMode, symbol domain.Symbol) (json.RawMessage, error)

	// SaveThoughtLog persists an AI debate thought log record.
	SaveThoughtLog(ctx context.Context, tl domain.ThoughtLog) error

	// GetThoughtLogsByIntentID retrieves thought logs linked to a specific order intent.
	GetThoughtLogsByIntentID(ctx context.Context, intentID string) ([]domain.ThoughtLog, error)

	// GetNonTerminalOrders returns all orders that haven't reached a terminal state
	// (filled/canceled/expired/rejected). Used at startup to reconcile pending orders.
	GetNonTerminalOrders(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.BrokerOrder, error)

	// GetRecordedFillQty returns the total recorded fill quantity for a symbol+side since a given time.
	// Used during startup fill reconciliation to determine how much has already been recorded.
	GetRecordedFillQty(ctx context.Context, tenantID string, envMode domain.EnvMode, symbol domain.Symbol, side string, since time.Time) (float64, error)

	// UpdateOrderStatus sets the status of an order by broker_order_id.
	// Used to mark orders as canceled/expired without a fill during reconciliation.
	UpdateOrderStatus(ctx context.Context, brokerOrderID string, status string) error

	// GetNetPositions returns the net quantity per symbol from the trades table.
	// Only returns symbols with |net_qty| > epsilon (1e-10).
	// Used by global portfolio reconciliation to detect DB-vs-broker drift.
	GetNetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) (map[domain.Symbol]float64, error)
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
