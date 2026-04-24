package ports

import (
	"context"
	"errors"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// ErrOrderNotFound is returned by GetOrderDetails when the broker has no record
// of the order (e.g. it was canceled when the previous session disconnected).
var ErrOrderNotFound = errors.New("order not found at broker")

// BrokerPort defines the interface for interacting with a broker.
type BrokerPort interface {
	SubmitOrder(ctx context.Context, intent domain.OrderIntent) (orderID string, err error)
	CancelOrder(ctx context.Context, orderID string) error
	CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error)
	GetOrderStatus(ctx context.Context, orderID string) (string, error)
	GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)
	// GetPosition returns the current quantity held for a single symbol.
	// Returns (0, nil) if no position exists — this is not an error.
	GetPosition(ctx context.Context, symbol domain.Symbol) (qty float64, err error)
	// CloseAtMarket liquidates any remaining position for a symbol via a
	// broker-native MKT order. Guaranteed-fill primitive; pays the spread.
	// Strategy exits and dust-sweeps use priced paths instead — this is the
	// last-resort/emergency primitive for operator-initiated liquidation.
	// Returns ("", nil) if the position was already fully closed (broker returns 404/422).
	CloseAtMarket(ctx context.Context, symbol domain.Symbol) (orderID string, err error)
	// GetOrderDetails returns full order details from the broker including cumulative fill info.
	GetOrderDetails(ctx context.Context, orderID string) (OrderDetails, error)
	// CancelAllOpenOrders cancels every open order on the broker account.
	// Used at startup to clear stale orders from a prior session.
	// Returns the number of orders for which cancellation was requested —
	// NOT the number confirmed terminal. Some brokers (IBKR ReqGlobalCancel)
	// issue the cancel asynchronously; callers should reconcile via
	// GetOpenOrders if confirmation is required.
	CancelAllOpenOrders(ctx context.Context) (int, error)
	// GetOpenOrders returns the broker's view of every currently-working
	// order on the account. Used by startup reconciliation to cross-reference
	// broker state against the intent journal (Sprint 2 Phase B).
	// Implementations MUST filter to working states only (Submitted, Accepted,
	// PreSubmitted, Working) — terminal orders are not interesting here.
	GetOpenOrders(ctx context.Context) ([]OpenOrder, error)
}

// OpenOrder is the broker's view of a working order that existed before
// this process started. It carries just enough information for startup
// reconciliation to match it against the intent journal.
type OpenOrder struct {
	BrokerOrderID string
	Symbol        string
	Side          string // "buy" / "sell"
	Quantity      float64
	OrderType     string // "limit" / "market" / "stop" / "stop_limit"
	LimitPrice    float64
	StopPrice     float64
	Status        string // "submitted" / "accepted" / "working"
	CreatedAt     time.Time
}

// FilledOrder is the broker's view of a terminal (filled) order from the
// current session's trade list. Used by startup backfill to restore orders
// the DB never recorded (session crashed before the orders row was written).
type FilledOrder struct {
	BrokerOrderID  string
	Symbol         string
	Side           string  // "BUY" / "SELL" (as reported by broker)
	Quantity       float64 // original order quantity
	FilledQty      float64 // cumulative filled quantity
	FilledAvgPrice float64
	FilledAt       time.Time
	Status         string // should be "filled"
}

// FilledOrderLister is an optional broker capability for backfill-on-boot.
// Brokers that can enumerate filled orders from the current session implement
// this; others (simbroker, alpaca stub) skip the backfill path. Kept off
// BrokerPort so adding broker adapters stays cheap.
type FilledOrderLister interface {
	// GetFilledOrders returns every terminal (filled) order currently visible
	// on the broker session. Implementations MUST filter to status=="filled"
	// with FilledQty>0; open/canceled/rejected orders belong elsewhere.
	GetFilledOrders(ctx context.Context) ([]FilledOrder, error)
}

// FillRecord is one execution leg as reported by the broker. Multi-fill
// orders produce multiple FillRecords sharing the same BrokerOrderID but
// carrying distinct ExecutionIDs. CumQty/AvgPrice are the running totals
// reported by the broker as of THIS exec; treating them as authoritative
// (vs. recomputing locally) makes the fill recorder race-free under
// out-of-order delivery.
type FillRecord struct {
	BrokerOrderID string
	ExecutionID   string
	Symbol        string
	Side          string
	Qty           float64
	Price         float64
	CumQty        float64
	AvgPrice      float64
	FilledAt      time.Time
}

// FillLister is an optional broker capability used by boot reconciliation
// to recover fills missed during crashes, disconnects, or stream gaps. The
// returned slice MUST include every fill the broker holds for the current
// session; the caller dedups against trades.execution_id before inserting.
type FillLister interface {
	GetAllFills(ctx context.Context) ([]FillRecord, error)
}

// OrderDetails contains full order information from the broker, including
// cumulative fill data needed for fill reconciliation.
type OrderDetails struct {
	BrokerOrderID  string
	Status         string
	FilledQty      float64
	FilledAvgPrice float64
	FilledAt       time.Time
	Symbol         string
	Side           string
	Qty            float64
}

// OrderUpdate represents a real-time order status change received from the
// broker's streaming API. It carries enough information for the execution
// service to correlate, persist, and emit fill events without additional
// REST calls.
type OrderUpdate struct {
	BrokerOrderID  string
	ExecutionID    string  // unique fill execution ID from broker (Alpaca WS "execution_id")
	Event          string  // "fill", "partial_fill", "canceled", "canceled", "expired", "rejected", "new", "accepted"
	Qty            float64 // incremental: quantity filled in THIS specific fill
	Price          float64 // incremental: execution price for THIS specific fill
	FilledQty      float64 // cumulative: total quantity filled so far across all fills
	FilledAvgPrice float64 // cumulative: volume-weighted average price across all fills
	FilledAt       time.Time
	// Commission, regulatory, and exchange fees attributed to THIS fill.
	// Populated by adapters that price fees internally (e.g. simbroker with a
	// FeeSchedule). Live adapters that receive fees out-of-band from the
	// broker leave these at 0.
	Commission     float64
	RegulatoryFee  float64
	ExchangeFee    float64
	FeesTotal      float64
}

// OrderStreamPort defines a push-based interface for receiving real-time
// order updates from the broker. Implementations must handle connection
// lifecycle (auth, reconnect) internally and deliver events on the returned
// channel until ctx is canceled.
type OrderStreamPort interface {
	// SubscribeOrderUpdates returns a channel that receives order status
	// changes in real time. The channel is closed when ctx is canceled
	// or the stream terminates. Callers should range over the channel.
	SubscribeOrderUpdates(ctx context.Context) (<-chan OrderUpdate, error)
}
