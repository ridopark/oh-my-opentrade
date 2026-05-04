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
	SaveMarketBars(ctx context.Context, bars []domain.MarketBar) (int, error)
	GetMarketBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
	GetMarketBarsMulti(ctx context.Context, symbols []domain.Symbol, timeframe domain.Timeframe, from, to time.Time) (map[string][]domain.MarketBar, error)
	SaveTrade(ctx context.Context, trade domain.Trade) error
	GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error)
	UpdateTradeThesis(ctx context.Context, tenantID string, envMode domain.EnvMode, symbol domain.Symbol, thesis json.RawMessage) error
	SaveStrategyDNA(ctx context.Context, dna domain.StrategyDNA) error
	GetLatestStrategyDNA(ctx context.Context, tenantID string, envMode domain.EnvMode) (*domain.StrategyDNA, error)
	SaveOrder(ctx context.Context, order domain.BrokerOrder) error
	UpdateOrderFill(ctx context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64) error

	// RecordFillPerExec persists ONE broker execution leg. Authoritative:
	// any prior aggregate (`agg:<broker_order_id>`) row is deleted first so
	// the trades ledger reflects only real exec rows once a per-exec stream
	// arrives. Caller sets trade.ExecutionID to the real broker exec ID.
	RecordFillPerExec(ctx context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64, trade domain.Trade) error

	// RecordFillAggregate persists an order-level fallback row only when no
	// per-exec row already exists for the same broker_order_id. Caller MUST
	// set trade.ExecutionID = "agg:<broker_order_id>" so re-fires collide on
	// idx_trades_execution_id and a later per-exec write can replace it.
	RecordFillAggregate(ctx context.Context, brokerOrderID string, filledAt time.Time, filledPrice, filledQty float64, trade domain.Trade) error

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

	// GetOrderByBrokerOrderID returns the order row for a given broker_order_id,
	// or (nil, nil) when no row exists. Used by boot-time backfill to detect
	// orders the DB has never recorded. Returns an error only on DB failure.
	GetOrderByBrokerOrderID(ctx context.Context, brokerOrderID string) (*domain.BrokerOrder, error)

	// GetRecordedFillQty returns the total recorded fill quantity for a symbol+side since a given time.
	// Used during startup fill reconciliation to determine how much has already been recorded.
	GetRecordedFillQty(ctx context.Context, tenantID string, envMode domain.EnvMode, symbol domain.Symbol, side string, since time.Time) (float64, error)

	// UpdateOrderStatus sets the status of an order by broker_order_id.
	// Used to mark orders as canceled/expired without a fill during reconciliation.
	UpdateOrderStatus(ctx context.Context, brokerOrderID string, status string) error

	// GetRecordedExecutionIDs returns the set of execution_ids already present
	// in trades for the given symbols. Used by boot fill-reconciliation to
	// cheaply diff broker fills against what the DB has, so we only INSERT the
	// missing legs. Returns an empty set (not nil) when nothing matches.
	GetRecordedExecutionIDs(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) (map[string]struct{}, error)

	// GetReconciledOrderIDs returns the set of broker_order_ids that have at
	// least one trade row in the window. Lets boot fill-reconciliation skip
	// orders whose live writes lacked an execution_id (the IBKR fastPoll
	// path), which GetRecordedExecutionIDs cannot see. Returns an empty set
	// (not nil) when nothing matches.
	GetReconciledOrderIDs(ctx context.Context, tenantID string, envMode domain.EnvMode, since time.Time) (map[string]struct{}, error)

	// GetNetPositions returns the net quantity per symbol from the trades table.
	// Only returns symbols with |net_qty| > epsilon (1e-10).
	// Used by global portfolio reconciliation to detect DB-vs-broker drift.
	GetNetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) (map[domain.Symbol]float64, error)

	// GetAvgEntryPrice returns the volume-weighted average BUY price for a symbol
	// over the last 30 days. Returns 0 if no BUY trades exist.
	// Used by global reconciliation to write zero-free reconciliation SELL trades.
	GetAvgEntryPrice(ctx context.Context, tenantID string, envMode domain.EnvMode, symbol domain.Symbol) (float64, error)

	// HasCanceledExitOrder returns true if the most recent SELL order for a symbol
	// has status canceled or expired. Used during bootstrap to restore ExitRetryCount
	// so after-hours EOD flatten retries survive restarts.
	HasCanceledExitOrder(ctx context.Context, tenantID string, envMode domain.EnvMode, symbol domain.Symbol) (bool, error)

	// HasTradeForBrokerOrderID reports whether at least one trade row already
	// exists for the given broker_order_id. Used by reconcileFilledOrder as
	// an idempotency check — when the WS-driven fill path has already
	// recorded the fill, the cancel-fill-race reconcile branch must NOT
	// write a duplicate trade row (the AAPL/PLTR/SMCI/SNOW/OXY/MRVL/TSLA
	// phantom-short pattern from 2026-04-30).
	HasTradeForBrokerOrderID(ctx context.Context, brokerOrderID string) (bool, error)

	// UpdateBarIndicators persists enriched indicator data (EMA, AVWAP) onto an
	// existing market_bars row identified by (symbol, timeframe, time).
	UpdateBarIndicators(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, t time.Time, ema9, ema21, ema50, ema200 float64, avwaps map[string]float64) error
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
