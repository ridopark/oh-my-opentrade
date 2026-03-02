package ports

import (
	"context"
)

// NotifierPort defines the interface for sending system notifications.
type NotifierPort interface {
	Notify(ctx context.Context, tenantID string, message string) error
}
