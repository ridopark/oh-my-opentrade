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
	SubscribeAsync(ctx context.Context, eventType domain.EventType, handler EventHandler) error
	Unsubscribe(ctx context.Context, eventType domain.EventType, handler EventHandler) error
	Close()
}

// BacktestBus extends EventBusPort with synchronous flush for deterministic replay.
type BacktestBus interface {
	EventBusPort
	Flush()
	// FreezeHandlers snapshots the current handler map so that PublishDirect
	// can bypass locking. Call once after all Subscribe calls are done.
	FreezeHandlers()
	// PublishDirect dispatches an event using the frozen handler snapshot,
	// skipping lock acquisition and slice copies. Only valid after FreezeHandlers.
	PublishDirect(ctx context.Context, event domain.Event) error
}
