package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// BrokerPort defines the interface for interacting with a broker.
type BrokerPort interface {
	SubmitOrder(ctx context.Context, intent domain.OrderIntent) (orderID string, err error)
	CancelOrder(ctx context.Context, orderID string) error
	GetOrderStatus(ctx context.Context, orderID string) (string, error)
	GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)
}
