package memory

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// Bus is an in-memory implementation of ports.EventBusPort.
// It processes events synchronously in the publisher's goroutine context.
type Bus struct {
	mu       sync.RWMutex
	handlers map[domain.EventType][]ports.EventHandler
}

// NewBus creates a new in-memory event bus.
func NewBus() *Bus {
	return &Bus{
		handlers: make(map[domain.EventType][]ports.EventHandler),
	}
}

// Publish sends an event to all handlers subscribed to its type.
// It executes handlers synchronously. If a handler returns an error,
// it continues calling remaining handlers but returns the first error encountered.
func (b *Bus) Publish(ctx context.Context, event domain.Event) error {
	// Check if context is already done
	if ctx.Err() != nil {
		return ctx.Err()
	}

	b.mu.RLock()
	handlers, exists := b.handlers[event.Type]
	if !exists || len(handlers) == 0 {
		b.mu.RUnlock()
		return nil
	}

	// Make a copy of the handlers slice to avoid holding the lock
	// while executing user-provided handlers, which could cause deadlocks.
	handlersCopy := make([]ports.EventHandler, len(handlers))
	copy(handlersCopy, handlers)
	b.mu.RUnlock()

	var errs []error
	for _, handler := range handlersCopy {
		// Check context before each handler invocation
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}

		if err := handler(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// Subscribe adds a handler for the specified event type.
func (b *Bus) Subscribe(ctx context.Context, eventType domain.EventType, handler ports.EventHandler) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.handlers[eventType] = append(b.handlers[eventType], handler)
	return nil
}

// Unsubscribe removes a handler for the specified event type.
// Note: This uses reflect.ValueOf(handler).Pointer() for comparison.
// Closures created in a loop might share the same pointer, making them indistinguishable.
func (b *Bus) Unsubscribe(ctx context.Context, eventType domain.EventType, handler ports.EventHandler) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	handlers, exists := b.handlers[eventType]
	if !exists {
		return nil
	}

	handlerPtr := reflect.ValueOf(handler).Pointer()
	for i, h := range handlers {
		if reflect.ValueOf(h).Pointer() == handlerPtr {
			// Remove the handler
			// Fast path for removal if order doesn't matter:
			// handlers[i] = handlers[len(handlers)-1]
			// handlers = handlers[:len(handlers)-1]
			// However, to maintain insertion order:
			b.handlers[eventType] = append(handlers[:i], handlers[i+1:]...)
			return nil
		}
	}

	return nil
}
