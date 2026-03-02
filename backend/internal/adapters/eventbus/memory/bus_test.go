package memory_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// 1. TestNewBus
func TestNewBus(t *testing.T) {
	bus := memory.NewBus()
	require.NotNil(t, bus)
}

// 2. TestBus_ImplementsEventBusPort
func TestBus_ImplementsEventBusPort(t *testing.T) {
	var _ ports.EventBusPort = (*memory.Bus)(nil)
}

// 3. TestBus_PublishWithNoSubscribers
func TestBus_PublishWithNoSubscribers(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	err = bus.Publish(ctx, *event)
	assert.NoError(t, err)
}

// 4. TestBus_SubscribeAndPublish
func TestBus_SubscribeAndPublish(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", "test-payload")
	require.NoError(t, err)

	var receivedEvent domain.Event
	var called int32

	handler := func(ctx context.Context, e domain.Event) error {
		atomic.AddInt32(&called, 1)
		receivedEvent = e
		return nil
	}

	err = bus.Subscribe(ctx, domain.EventMarketBarReceived, handler)
	require.NoError(t, err)

	err = bus.Publish(ctx, *event)
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&called))
	assert.Equal(t, event.ID, receivedEvent.ID)
	assert.Equal(t, "test-payload", receivedEvent.Payload)
}

// 5. TestBus_MultipleSubscribers
func TestBus_MultipleSubscribers(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	var called1, called2 int32

	handler1 := func(ctx context.Context, e domain.Event) error {
		atomic.AddInt32(&called1, 1)
		return nil
	}
	handler2 := func(ctx context.Context, e domain.Event) error {
		atomic.AddInt32(&called2, 1)
		return nil
	}

	err = bus.Subscribe(ctx, domain.EventMarketBarReceived, handler1)
	require.NoError(t, err)
	err = bus.Subscribe(ctx, domain.EventMarketBarReceived, handler2)
	require.NoError(t, err)

	err = bus.Publish(ctx, *event)
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&called1))
	assert.Equal(t, int32(1), atomic.LoadInt32(&called2))
}

// 6. TestBus_DifferentEventTypes
func TestBus_DifferentEventTypes(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	var called int32

	handler := func(ctx context.Context, e domain.Event) error {
		atomic.AddInt32(&called, 1)
		return nil
	}

	err := bus.Subscribe(ctx, domain.EventOrderSubmitted, handler)
	require.NoError(t, err)

	// Publish a different event type
	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	err = bus.Publish(ctx, *event)
	require.NoError(t, err)

	assert.Equal(t, int32(0), atomic.LoadInt32(&called))
}

// 7. TestBus_Unsubscribe
func TestBus_Unsubscribe(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	var called int32

	handler := func(ctx context.Context, e domain.Event) error {
		atomic.AddInt32(&called, 1)
		return nil
	}

	err := bus.Subscribe(ctx, domain.EventMarketBarReceived, handler)
	require.NoError(t, err)

	err = bus.Unsubscribe(ctx, domain.EventMarketBarReceived, handler)
	require.NoError(t, err)

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	err = bus.Publish(ctx, *event)
	require.NoError(t, err)

	assert.Equal(t, int32(0), atomic.LoadInt32(&called))
}

// 8. TestBus_PublishCallsHandlersSynchronously
func TestBus_PublishCallsHandlersSynchronously(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	var handlerCompleted bool

	handler := func(ctx context.Context, e domain.Event) error {
		// Sleep briefly to ensure async execution would fail the test
		time.Sleep(10 * time.Millisecond)
		handlerCompleted = true
		return nil
	}

	err := bus.Subscribe(ctx, domain.EventMarketBarReceived, handler)
	require.NoError(t, err)

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	err = bus.Publish(ctx, *event)
	require.NoError(t, err)

	// Because Publish is synchronous, handlerCompleted must be true when Publish returns
	assert.True(t, handlerCompleted)
}

// 9. TestBus_HandlerError
func TestBus_HandlerError(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	var called1, called2 int32
	expectedErr := errors.New("handler error")

	handler1 := func(ctx context.Context, e domain.Event) error {
		atomic.AddInt32(&called1, 1)
		return expectedErr
	}
	handler2 := func(ctx context.Context, e domain.Event) error {
		atomic.AddInt32(&called2, 1)
		return nil
	}

	err := bus.Subscribe(ctx, domain.EventMarketBarReceived, handler1)
	require.NoError(t, err)
	err = bus.Subscribe(ctx, domain.EventMarketBarReceived, handler2)
	require.NoError(t, err)

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	err = bus.Publish(ctx, *event)
	// Expect the first error to be returned, but both handlers should be called
	assert.ErrorIs(t, err, expectedErr)
	assert.Equal(t, int32(1), atomic.LoadInt32(&called1))
	assert.Equal(t, int32(1), atomic.LoadInt32(&called2))
}

// 10. TestBus_ConcurrentPublish
func TestBus_ConcurrentPublish(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	var totalProcessed int32
	handler := func(ctx context.Context, e domain.Event) error {
		atomic.AddInt32(&totalProcessed, 1)
		return nil
	}

	err := bus.Subscribe(ctx, domain.EventMarketBarReceived, handler)
	require.NoError(t, err)

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	var wg sync.WaitGroup
	publishers := 10
	publishesPerWorker := 100

	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < publishesPerWorker; j++ {
				_ = bus.Publish(ctx, *event)
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, int32(publishers*publishesPerWorker), atomic.LoadInt32(&totalProcessed))
}
