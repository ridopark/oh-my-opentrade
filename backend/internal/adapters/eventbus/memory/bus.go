package memory

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

const flushSentinel domain.EventType = "__flush__"

// asyncTask bundles the context and event for async dispatch.
type asyncTask struct {
	ctx   context.Context
	event domain.Event
}

// asyncSub represents a single asynchronous subscription with its own
// buffered channel and worker goroutine.
type asyncSub struct {
	ch      chan asyncTask
	done    chan struct{}
	handler ports.EventHandler
}

// Bus is an in-memory implementation of ports.EventBusPort.
// It processes events synchronously in the publisher's goroutine context,
// unless the handler was registered via SubscribeAsync, in which case
// events are dispatched to a per-subscription buffered channel.
type Bus struct {
	mu        sync.RWMutex
	handlers  map[domain.EventType][]ports.EventHandler
	asyncSubs map[domain.EventType][]*asyncSub

	closeMu  sync.Mutex
	closed   bool
	syncMode bool // when true, SubscribeAsync behaves like Subscribe (for deterministic backtests)

	pending sync.WaitGroup

	// droppedTotal tracks the cumulative number of async events dropped due to full channels.
	droppedTotal atomic.Int64
	// droppedByType tracks per-event-type drop counts for granular observability.
	droppedByType sync.Map // domain.EventType -> *atomic.Int64

	// frozen is a lock-free snapshot of handlers, populated by FreezeHandlers.
	// Stored via atomic.Pointer so Publish can read it without acquiring any
	// lock. Writes are rare (FreezeHandlers is called once after startup).
	frozen atomic.Pointer[map[domain.EventType][]ports.EventHandler]
}

// NewBus creates a new in-memory event bus.
func NewBus() *Bus {
	return &Bus{
		handlers:  make(map[domain.EventType][]ports.EventHandler),
		asyncSubs: make(map[domain.EventType][]*asyncSub),
	}
}

// NewSyncBus creates an event bus where SubscribeAsync behaves like Subscribe.
// Use this for backtests to ensure deterministic event ordering.
func NewSyncBus() *Bus {
	return &Bus{
		handlers:  make(map[domain.EventType][]ports.EventHandler),
		asyncSubs: make(map[domain.EventType][]*asyncSub),
		syncMode:  true,
	}
}

// Publish sends an event to all handlers subscribed to its type.
// Synchronous handlers are executed in the publisher's goroutine.
// Async handlers receive the event via a buffered channel (non-blocking;
// events are dropped if the channel is full).
func (b *Bus) Publish(ctx context.Context, event domain.Event) error {
	// Check if context is already done
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Fast path: after FreezeHandlers() has been called (backtest/replay),
	// dispatch directly from the atomic snapshot with no locks and no slice
	// copies. Async subs are disallowed in frozen mode — SyncBus routes them
	// through Subscribe, so the snapshot is sufficient for every caller that
	// freezes. The slow path below still handles pre-freeze operation and any
	// caller that holds async subscriptions.
	if frozen := b.frozen.Load(); frozen != nil {
		handlers := (*frozen)[event.Type]
		if len(handlers) == 0 {
			return nil
		}
		var errs []error
		for _, handler := range handlers {
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

	b.mu.RLock()
	handlers, hExists := b.handlers[event.Type]
	asyncHandlers, aExists := b.asyncSubs[event.Type]

	if (!hExists || len(handlers) == 0) && (!aExists || len(asyncHandlers) == 0) {
		b.mu.RUnlock()
		return nil
	}

	// Copy slices to avoid holding the lock while executing handlers.
	var handlersCopy []ports.EventHandler
	if hExists && len(handlers) > 0 {
		handlersCopy = make([]ports.EventHandler, len(handlers))
		copy(handlersCopy, handlers)
	}
	var asyncCopy []*asyncSub
	if aExists && len(asyncHandlers) > 0 {
		asyncCopy = make([]*asyncSub, len(asyncHandlers))
		copy(asyncCopy, asyncHandlers)
	}
	b.mu.RUnlock()

	// Execute synchronous handlers.
	var errs []error
	for _, handler := range handlersCopy {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if err := handler(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}

	// Dispatch to async handlers (non-blocking).
	// Use context.WithoutCancel so async handlers aren't tied to publisher's
	// context lifecycle — they may outlive the publisher call.
	asyncCtx := context.WithoutCancel(ctx)
	for _, sub := range asyncCopy {
		b.pending.Add(1)
		select {
		case sub.ch <- asyncTask{ctx: asyncCtx, event: event}:
		default:
			b.pending.Done() // undo: event was dropped, not dispatched
			b.droppedTotal.Add(1)
			b.incrDroppedByType(event.Type)
			slog.Warn("async event bus: channel full, dropping event",
				"event_type", event.Type,
				"event_id", event.ID,
				"total_dropped", b.droppedTotal.Load(),
			)
		}
	}

	return errors.Join(errs...)
}

// Subscribe adds a synchronous handler for the specified event type.
func (b *Bus) Subscribe(ctx context.Context, eventType domain.EventType, handler ports.EventHandler) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.handlers[eventType] = append(b.handlers[eventType], handler)
	return nil
}

// SubscribeAsync adds a handler that receives events via a per-subscription
// buffered channel (size 64). A single worker goroutine processes events
// sequentially, preserving ordering within this handler. The publisher
// never blocks — if the channel is full the event is dropped with a warning.
func (b *Bus) SubscribeAsync(ctx context.Context, eventType domain.EventType, handler ports.EventHandler) error {
	if b.syncMode {
		return b.Subscribe(ctx, eventType, handler)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	b.closeMu.Lock()
	if b.closed {
		b.closeMu.Unlock()
		return errors.New("event bus is closed")
	}
	b.closeMu.Unlock()

	sub := &asyncSub{
		ch:      make(chan asyncTask, 256),
		done:    make(chan struct{}),
		handler: handler,
	}

	go func() {
		defer close(sub.done)
		for task := range sub.ch {
			if task.event.Type == flushSentinel {
				if ack, ok := task.event.Payload.(chan struct{}); ok {
					close(ack)
				}
				continue
			}
			func() {
				defer b.pending.Done()
				if err := sub.handler(task.ctx, task.event); err != nil {
					slog.Error("async event handler error",
						"event_type", task.event.Type,
						"event_id", task.event.ID,
						"error", err,
					)
				}
			}()
		}
	}()

	b.mu.Lock()
	defer b.mu.Unlock()
	b.asyncSubs[eventType] = append(b.asyncSubs[eventType], sub)
	return nil
}

// Flush blocks until all pending async tasks have been processed.
// Useful in tests to ensure async handlers have completed.
func (b *Bus) Flush() {
	b.mu.RLock()
	var allSubs []*asyncSub
	for _, subs := range b.asyncSubs {
		allSubs = append(allSubs, subs...)
	}
	b.mu.RUnlock()

	if len(allSubs) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, sub := range allSubs {
		wg.Add(1)
		ack := make(chan struct{})
		sub.ch <- asyncTask{
			ctx: context.Background(),
			event: domain.Event{
				Type:    flushSentinel,
				Payload: ack,
			},
		}
		go func() {
			defer wg.Done()
			<-ack
		}()
	}
	wg.Wait()
}

func (b *Bus) WaitPending() {
	b.pending.Wait()
}

// FreezeHandlers takes a snapshot of the current handler map so that
// PublishDirect can dispatch events without acquiring any locks.
// Must be called after all Subscribe/SubscribeAsync calls are complete.
func (b *Bus) FreezeHandlers() {
	b.mu.RLock()
	defer b.mu.RUnlock()

	frozen := make(map[domain.EventType][]ports.EventHandler, len(b.handlers))
	for et, hs := range b.handlers {
		cp := make([]ports.EventHandler, len(hs))
		copy(cp, hs)
		frozen[et] = cp
	}
	b.frozen.Store(&frozen)
}

// PublishDirect dispatches an event using the frozen handler snapshot,
// bypassing lock acquisition and slice allocation. Only valid after
// FreezeHandlers has been called; falls back to Publish otherwise.
func (b *Bus) PublishDirect(ctx context.Context, event domain.Event) error {
	frozen := b.frozen.Load()
	if frozen == nil {
		return b.Publish(ctx, event)
	}

	handlers := (*frozen)[event.Type]
	if len(handlers) == 0 {
		return nil
	}

	var errs []error
	for _, handler := range handlers {
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
			// Remove the handler maintaining insertion order.
			b.handlers[eventType] = append(handlers[:i], handlers[i+1:]...)
			return nil
		}
	}

	return nil
}

// Close gracefully shuts down all async subscriptions. It closes every
// worker channel and waits for workers to drain with a 10-second timeout.
// Idempotent — subsequent calls are no-ops.
func (b *Bus) Close() {
	b.closeMu.Lock()
	if b.closed {
		b.closeMu.Unlock()
		return
	}
	b.closed = true
	b.closeMu.Unlock()

	b.mu.RLock()
	var allSubs []*asyncSub
	for _, subs := range b.asyncSubs {
		allSubs = append(allSubs, subs...)
	}
	b.mu.RUnlock()

	if len(allSubs) == 0 {
		return
	}

	for _, sub := range allSubs {
		close(sub.ch)
	}

	done := make(chan struct{})
	go func() {
		for _, sub := range allSubs {
			<-sub.done
		}
		close(done)
	}()

	select {
	case <-done:
		slog.Info("event bus: all async workers drained")
	case <-time.After(10 * time.Second):
		slog.Warn("event bus: timed out waiting for async workers to drain")
	}
}

// DroppedEvents returns the total number of async events dropped due to full channels.
func (b *Bus) DroppedEvents() int64 {
	return b.droppedTotal.Load()
}

// DroppedEventsByType returns the number of dropped events for a specific event type.
// Returns 0 if no events of that type have been dropped.
func (b *Bus) DroppedEventsByType(eventType domain.EventType) int64 {
	if v, ok := b.droppedByType.Load(eventType); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

// incrDroppedByType atomically increments the per-event-type drop counter.
func (b *Bus) incrDroppedByType(eventType domain.EventType) {
	v, ok := b.droppedByType.Load(eventType)
	if !ok {
		v, _ = b.droppedByType.LoadOrStore(eventType, &atomic.Int64{})
	}
	v.(*atomic.Int64).Add(1)
}
