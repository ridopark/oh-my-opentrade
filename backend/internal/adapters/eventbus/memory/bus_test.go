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

// 11. TestBus_SubscribeAsync_NonBlocking — Publish returns immediately; handler runs asynchronously
func TestBus_SubscribeAsync_NonBlocking(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()
	ctx := context.Background()

	started := make(chan struct{})
	block := make(chan struct{})

	handler := func(_ context.Context, _ domain.Event) error {
		close(started)
		<-block
		return nil
	}

	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, handler))

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	err = bus.Publish(ctx, *event)
	require.NoError(t, err)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("async handler was never started")
	}

	close(block)
}

// 12. TestBus_SubscribeAsync_PreservesOrder — events are processed in FIFO order within one handler
func TestBus_SubscribeAsync_PreservesOrder(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()
	ctx := context.Background()

	const n = 50
	received := make([]int, 0, n)
	var mu sync.Mutex
	done := make(chan struct{})

	handler := func(_ context.Context, e domain.Event) error {
		mu.Lock()
		received = append(received, e.Payload.(int))
		if len(received) == n {
			close(done)
		}
		mu.Unlock()
		return nil
	}

	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, handler))

	for i := 0; i < n; i++ {
		event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", i)
		require.NoError(t, err)
		require.NoError(t, bus.Publish(ctx, *event))
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for all events")
	}

	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < n; i++ {
		assert.Equal(t, i, received[i], "event %d out of order", i)
	}
}

// 13. TestBus_SubscribeAsync_Close_DrainsWorkers — Close waits for in-flight events to finish
func TestBus_SubscribeAsync_Close_DrainsWorkers(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	var processed int32

	handler := func(_ context.Context, _ domain.Event) error {
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&processed, 1)
		return nil
	}

	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, handler))

	for i := 0; i < 5; i++ {
		event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
		require.NoError(t, err)
		require.NoError(t, bus.Publish(ctx, *event))
	}

	bus.Close()

	assert.Equal(t, int32(5), atomic.LoadInt32(&processed))
}

// 14. TestBus_SubscribeAsync_Close_Idempotent — calling Close twice does not panic
func TestBus_SubscribeAsync_Close_Idempotent(t *testing.T) {
	bus := memory.NewBus()
	require.NoError(t, bus.SubscribeAsync(context.Background(), domain.EventMarketBarReceived,
		func(_ context.Context, _ domain.Event) error { return nil }))

	bus.Close()
	bus.Close()
}

// 15. TestBus_SubscribeAsync_RejectsAfterClose
func TestBus_SubscribeAsync_RejectsAfterClose(t *testing.T) {
	bus := memory.NewBus()
	bus.Close()

	err := bus.SubscribeAsync(context.Background(), domain.EventMarketBarReceived,
		func(_ context.Context, _ domain.Event) error { return nil })
	assert.Error(t, err)
}

// 16. TestBus_MixedSyncAsync — sync handlers execute synchronously, async handlers eventually
func TestBus_MixedSyncAsync(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()
	ctx := context.Background()

	var syncCalled int32
	var asyncCalled int32
	asyncDone := make(chan struct{})

	syncHandler := func(_ context.Context, _ domain.Event) error {
		atomic.AddInt32(&syncCalled, 1)
		return nil
	}

	asyncHandler := func(_ context.Context, _ domain.Event) error {
		atomic.AddInt32(&asyncCalled, 1)
		close(asyncDone)
		return nil
	}

	require.NoError(t, bus.Subscribe(ctx, domain.EventMarketBarReceived, syncHandler))
	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, asyncHandler))

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *event))

	assert.Equal(t, int32(1), atomic.LoadInt32(&syncCalled), "sync handler must fire inline")

	select {
	case <-asyncDone:
	case <-time.After(2 * time.Second):
		t.Fatal("async handler never fired")
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&asyncCalled))
}

// 17. TestBus_SubscribeAsync_DropOnFull — when channel is full, events are dropped (not blocking)
func TestBus_SubscribeAsync_DropOnFull(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()
	ctx := context.Background()

	block := make(chan struct{})

	handler := func(_ context.Context, _ domain.Event) error {
		<-block
		return nil
	}

	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, handler))

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	for i := 0; i < 200; i++ {
		err := bus.Publish(ctx, *event)
		require.NoError(t, err)
	}

	close(block)
}

// 17b. TestBus_DroppedEvents_Counter — DroppedEvents and DroppedEventsByType track drops
func TestBus_DroppedEvents_Counter(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()
	ctx := context.Background()

	assert.Equal(t, int64(0), bus.DroppedEvents(), "no drops initially")
	assert.Equal(t, int64(0), bus.DroppedEventsByType(domain.EventMarketBarReceived), "no per-type drops initially")

	// Block the handler so all channel slots fill up.
	block := make(chan struct{})
	handler := func(_ context.Context, _ domain.Event) error {
		<-block
		return nil
	}

	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, handler))

	const totalPublished = 512
	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	for i := 0; i < totalPublished; i++ {
		require.NoError(t, bus.Publish(ctx, *event))
	}

	dropped := bus.DroppedEvents()
	assert.True(t, dropped > 0, "expected some events to be dropped, got %d", dropped)
	assert.Equal(t, dropped, bus.DroppedEventsByType(domain.EventMarketBarReceived),
		"per-type count must match total for single-type scenario")

	// A type that was never dropped should return 0.
	assert.Equal(t, int64(0), bus.DroppedEventsByType(domain.EventOrderSubmitted))

	close(block)
}

// 17c. TestBus_DroppedEventsByType_MultipleTypes — per-type counters are independent
func TestBus_DroppedEventsByType_MultipleTypes(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()
	ctx := context.Background()

	block := make(chan struct{})
	handler := func(_ context.Context, _ domain.Event) error {
		<-block
		return nil
	}

	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, handler))
	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventOrderSubmitted, handler))

	barEvent, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)
	orderEvent, err := domain.NewEvent(domain.EventOrderSubmitted, "tenant-1", domain.EnvModePaper, "idem-2", nil)
	require.NoError(t, err)

	for i := 0; i < 512; i++ {
		_ = bus.Publish(ctx, *barEvent)
	}
	for i := 0; i < 512; i++ {
		_ = bus.Publish(ctx, *orderEvent)
	}

	totalDropped := bus.DroppedEvents()
	barDropped := bus.DroppedEventsByType(domain.EventMarketBarReceived)
	orderDropped := bus.DroppedEventsByType(domain.EventOrderSubmitted)

	assert.True(t, barDropped > 0, "expected bar drops")
	assert.True(t, orderDropped > 0, "expected order drops")
	assert.Equal(t, totalDropped, barDropped+orderDropped, "per-type drops must sum to total")

	close(block)
}

// 18. TestBus_SubscribeAsync_ConcurrentPublish — race detector safety check
func TestBus_SubscribeAsync_ConcurrentPublish(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()
	ctx := context.Background()

	var totalProcessed int32
	handler := func(_ context.Context, _ domain.Event) error {
		atomic.AddInt32(&totalProcessed, 1)
		return nil
	}

	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, handler))

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = bus.Publish(ctx, *event)
			}
		}()
	}

	wg.Wait()
	bus.Close()

	assert.True(t, atomic.LoadInt32(&totalProcessed) > 0)
}

// 19. TestWaitPending_DrainsAsyncHandlers
func TestWaitPending_DrainsAsyncHandlers(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()
	ctx := context.Background()

	var completed int32

	handler := func(_ context.Context, _ domain.Event) error {
		time.Sleep(50 * time.Millisecond)
		atomic.StoreInt32(&completed, 1)
		return nil
	}

	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, handler))

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *event))

	bus.WaitPending()

	assert.Equal(t, int32(1), atomic.LoadInt32(&completed))
}

// 20. TestWaitPending_NoOp
func TestWaitPending_NoOp(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()

	done := make(chan struct{})
	go func() {
		bus.WaitPending()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitPending blocked with no pending events")
	}
}

// 21. TestWaitPending_CascadingEvents
func TestWaitPending_CascadingEvents(t *testing.T) {
	bus := memory.NewBus()
	defer bus.Close()
	ctx := context.Background()

	var handlerACalled, handlerBCalled int32

	handlerB := func(_ context.Context, _ domain.Event) error {
		time.Sleep(20 * time.Millisecond)
		atomic.StoreInt32(&handlerBCalled, 1)
		return nil
	}
	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventOrderSubmitted, handlerB))

	handlerA := func(ctx2 context.Context, _ domain.Event) error {
		atomic.StoreInt32(&handlerACalled, 1)
		evt, err := domain.NewEvent(domain.EventOrderSubmitted, "tenant-1", domain.EnvModePaper, "idem-2", nil)
		if err != nil {
			return err
		}
		return bus.Publish(ctx2, *evt)
	}
	require.NoError(t, bus.SubscribeAsync(ctx, domain.EventMarketBarReceived, handlerA))

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant-1", domain.EnvModePaper, "idem-1", nil)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *event))

	bus.WaitPending()

	assert.Equal(t, int32(1), atomic.LoadInt32(&handlerACalled), "handler A should have completed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&handlerBCalled), "handler B should have completed")
}

// TestBus_FreezeHandlers_PostFreezeSubscribesAreInvisible documents and locks
// in a critical gotcha: any Subscribe call made AFTER FreezeHandlers() does
// NOT receive events from subsequent Publish calls (which use the frozen
// snapshot fast-path).
//
// This was the root cause of a dual-pipeline state divergence bug in the
// backtest engine: the sharded pipeline's runners called Subscribe AFTER
// freeze, so FillReceived events bypassed them entirely, leaving strategies
// stuck in PendingEntry state on daily timeframe. Fix: freeze AFTER all
// pipeline runners have started.
//
// If this test ever fails, FreezeHandlers semantics changed and the backtest
// runner's freeze ordering may need re-examination.
func TestBus_FreezeHandlers_PostFreezeSubscribesAreInvisible(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	var preFreezeCalled, postFreezeCalled int32

	preFreezeHandler := func(_ context.Context, _ domain.Event) error {
		atomic.AddInt32(&preFreezeCalled, 1)
		return nil
	}
	postFreezeHandler := func(_ context.Context, _ domain.Event) error {
		atomic.AddInt32(&postFreezeCalled, 1)
		return nil
	}

	// Pre-freeze subscription
	require.NoError(t, bus.Subscribe(ctx, domain.EventMarketBarReceived, preFreezeHandler))

	// Freeze the snapshot
	bus.FreezeHandlers()

	// Post-freeze subscription — invisible to fast-path Publish
	require.NoError(t, bus.Subscribe(ctx, domain.EventMarketBarReceived, postFreezeHandler))

	event, err := domain.NewEvent(domain.EventMarketBarReceived, "tenant", domain.EnvModePaper, "idem", nil)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *event))
	bus.WaitPending()

	assert.Equal(t, int32(1), atomic.LoadInt32(&preFreezeCalled),
		"pre-freeze handler should receive the event via frozen snapshot")
	assert.Equal(t, int32(0), atomic.LoadInt32(&postFreezeCalled),
		"post-freeze handler must NOT receive the event — Subscribe-after-freeze "+
			"is invisible to the publish fast-path. This is intentional and the "+
			"backtest runner must freeze AFTER all pipelines start (see "+
			"backtest/runner.go FreezeHandlers callsites).")
}

// TestBus_FreezeHandlers_AfterAllSubscribesDeliversToAll documents the
// CORRECT ordering: when FreezeHandlers is called AFTER all Subscribes, every
// handler receives published events. This is the ordering the backtest runner
// must follow (post-Tier-2 fix in 2026-04-14).
func TestBus_FreezeHandlers_AfterAllSubscribesDeliversToAll(t *testing.T) {
	bus := memory.NewBus()
	ctx := context.Background()

	var legacyCalled, shardCalled int32

	// Simulate two pipelines (legacy + sharded) both subscribing to the same
	// event type before the freeze.
	require.NoError(t, bus.Subscribe(ctx, domain.EventFillReceived, func(_ context.Context, _ domain.Event) error {
		atomic.AddInt32(&legacyCalled, 1)
		return nil
	}))
	require.NoError(t, bus.Subscribe(ctx, domain.EventFillReceived, func(_ context.Context, _ domain.Event) error {
		atomic.AddInt32(&shardCalled, 1)
		return nil
	}))

	// Freeze AFTER both subscribes — correct ordering.
	bus.FreezeHandlers()

	event, err := domain.NewEvent(domain.EventFillReceived, "tenant", domain.EnvModePaper, "idem", nil)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *event))
	bus.WaitPending()

	assert.Equal(t, int32(1), atomic.LoadInt32(&legacyCalled),
		"legacy handler must receive the event")
	assert.Equal(t, int32(1), atomic.LoadInt32(&shardCalled),
		"shard handler must also receive the event when Subscribe happened pre-freeze")
}
