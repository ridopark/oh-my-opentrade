package ports

import (
	"context"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// EventHandler is a callback function for processing incoming events.
type EventHandler func(ctx context.Context, event domain.Event) error

// EventBusPort defines the interface for publishing and subscribing to domain events.
type EventBusPort interface {
	Publish(ctx context.Context, event domain.Event) error
	Subscribe(ctx context.Context, eventType domain.EventType, handler EventHandler) error
	Unsubscribe(ctx context.Context, eventType domain.EventType, handler EventHandler) error
}
