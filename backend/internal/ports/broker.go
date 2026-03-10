package ports

import (
	"context"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

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
	// ClosePosition liquidates any remaining position for a symbol via broker-native API.
	// Returns nil if the position was already fully closed (broker returns 404/422).
	ClosePosition(ctx context.Context, symbol domain.Symbol) error
}

// OrderUpdate represents a real-time order status change received from the
// broker's streaming API. It carries enough information for the execution
// service to correlate, persist, and emit fill events without additional
// REST calls.
type OrderUpdate struct {
	BrokerOrderID  string
	Event          string  // "fill", "partial_fill", "canceled", "canceled", "expired", "rejected", "new", "accepted"
	Qty            float64 // incremental: quantity filled in THIS specific fill
	Price          float64 // incremental: execution price for THIS specific fill
	FilledQty      float64 // cumulative: total quantity filled so far across all fills
	FilledAvgPrice float64 // cumulative: volume-weighted average price across all fills
	FilledAt       time.Time
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
