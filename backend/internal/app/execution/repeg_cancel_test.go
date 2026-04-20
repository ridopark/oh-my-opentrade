package execution

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopBroker is a minimal BrokerPort that returns zero values. Used by the
// suppression tests to satisfy cleanupPendingOrder's broker-touching
// branches (dust sweep calls GetPosition).
type noopBroker struct{}

func (b *noopBroker) SubmitOrder(_ context.Context, _ domain.OrderIntent) (string, error) {
	return "", nil
}
func (b *noopBroker) CancelOrder(_ context.Context, _ string) error { return nil }
func (b *noopBroker) CancelOpenOrders(_ context.Context, _ domain.Symbol, _ string) (int, error) {
	return 0, nil
}
func (b *noopBroker) GetOrderStatus(_ context.Context, _ string) (string, error) { return "", nil }
func (b *noopBroker) GetPositions(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}
func (b *noopBroker) GetPosition(_ context.Context, _ domain.Symbol) (float64, error) { return 0, nil }
func (b *noopBroker) CloseAtMarket(_ context.Context, _ domain.Symbol) (string, error) {
	return "", nil
}
func (b *noopBroker) GetOrderDetails(_ context.Context, _ string) (ports.OrderDetails, error) {
	return ports.OrderDetails{}, nil
}
func (b *noopBroker) CancelAllOpenOrders(_ context.Context) (int, error)   { return 0, nil }
func (b *noopBroker) GetOpenOrders(_ context.Context) ([]ports.OpenOrder, error) { return nil, nil }

// hasFailureRecord is a test-only helper since PositionGate doesn't expose
// the failure counter directly.
func (g *PositionGate) hasFailureRecord(tenantID string, envMode domain.EnvMode, symbol domain.Symbol) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.exitFails[inflightKey{TenantID: tenantID, EnvMode: envMode, Symbol: symbol}]
	return ok
}

// TestMarkRepegCancel_SuppressesFailureCount verifies that cleanupPendingOrder
// skips RecordExitFailure when MarkRepegCancel has tagged the pending order.
// This is the load-bearing guarantee that prevents the re-peg cycle from
// amplifying circuit-breaker failure counts 3-4x per exit attempt.
func TestMarkRepegCancel_SuppressesFailureCount(t *testing.T) {
	pg := NewPositionGate(&noopBroker{}, zerolog.Nop())
	bus := memory.NewBus()

	s := &Service{
		positionGate: pg,
		broker:       &noopBroker{},
		eventBus:     bus,
		log:          zerolog.Nop(),
	}

	// Use SCALE_OUT rationale to skip the dust-sweep branch (which needs
	// a live price port and async orchestration) and stay focused on the
	// RecordExitFailure decision.
	intent := mustExitIntent(t, "SCALE_OUT test")
	orderID := "broker-xyz"
	s.pendingOrders.Store(orderID, &pendingOrder{
		intent:      intent,
		tenantID:    intent.TenantID,
		envMode:     intent.EnvMode,
		submitStart: time.Now(),
	})

	// Baseline: without MarkRepegCancel, cleanup records the failure.
	s.cleanupPendingOrder(orderID)
	assert.True(t, pg.hasFailureRecord(intent.TenantID, intent.EnvMode, intent.Symbol),
		"baseline: cleanup must record a failure when not suppressed")
	pg.ResetExitFailures(intent.TenantID, intent.EnvMode, intent.Symbol)

	// Re-add and then suppress via MarkRepegCancel.
	s.pendingOrders.Store(orderID, &pendingOrder{
		intent:      intent,
		tenantID:    intent.TenantID,
		envMode:     intent.EnvMode,
		submitStart: time.Now(),
	})
	ok := s.MarkRepegCancel(orderID)
	require.True(t, ok, "MarkRepegCancel should find the pending order")

	s.cleanupPendingOrder(orderID)
	assert.False(t, pg.hasFailureRecord(intent.TenantID, intent.EnvMode, intent.Symbol),
		"suppressed cleanup must not record a failure")
}

// TestMarkRepegCancel_OrderGone reports false when no pending order matches.
// This is the no-op branch — position monitor should treat it as best-effort.
func TestMarkRepegCancel_OrderGone(t *testing.T) {
	s := &Service{log: zerolog.Nop()}
	ok := s.MarkRepegCancel("never-submitted")
	assert.False(t, ok)
}

// sweepProbeBroker reports dust-sweep launches by counting GetPosition
// calls (sweepDustPosition calls GetPosition as its first action). The
// SOFI phantom-short bug was caused by cleanupPendingOrder launching
// sweepDustPosition on a re-peg-canceled order; this mock makes that
// launch observable from a unit test.
type sweepProbeBroker struct {
	noopBroker
	getPositionCalls int32
}

func (b *sweepProbeBroker) GetPosition(_ context.Context, _ domain.Symbol) (float64, error) {
	atomic.AddInt32(&b.getPositionCalls, 1)
	// Return 0 so any launched sweep exits fast without submitting orders.
	return 0, nil
}

// TestMarkRepegCancel_SuppressesDustSweep verifies that after tagging a
// pending order via MarkRepegCancel, cleanupPendingOrder does NOT launch
// sweepDustPosition. Observable via GetPosition call count: sweep's first
// action is GetPosition, so a zero count means no sweep launched.
func TestMarkRepegCancel_SuppressesDustSweep(t *testing.T) {
	broker := &sweepProbeBroker{}
	pg := NewPositionGate(broker, zerolog.Nop())
	bus := memory.NewBus()

	s := &Service{
		positionGate: pg,
		broker:       broker,
		eventBus:     bus,
		log:          zerolog.Nop(),
	}

	// Full-exit rationale (no "SCALE_OUT") — this is the rationale that
	// normally triggers the dust-sweep launch.
	intent := mustExitIntent(t, "PREMIUM_TRAIL exit")
	orderID := "broker-1604"
	s.pendingOrders.Store(orderID, &pendingOrder{
		intent:      intent,
		tenantID:    intent.TenantID,
		envMode:     intent.EnvMode,
		submitStart: time.Now(),
	})

	require.True(t, s.MarkRepegCancel(orderID))

	s.cleanupPendingOrder(orderID)

	// Give any background goroutine a chance to fire before asserting.
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, int32(0), atomic.LoadInt32(&broker.getPositionCalls),
		"suppressed cleanup must NOT launch dust sweep (root cause of SOFI phantom short)")
}

// TestCleanupPendingOrder_LaunchesDustSweep_WhenNotSuppressed is the
// baseline complement to TestMarkRepegCancel_SuppressesDustSweep — a
// full exit WITHOUT suppression should launch the sweep.
func TestCleanupPendingOrder_LaunchesDustSweep_WhenNotSuppressed(t *testing.T) {
	broker := &sweepProbeBroker{}
	pg := NewPositionGate(broker, zerolog.Nop())
	bus := memory.NewBus()

	s := &Service{
		positionGate: pg,
		broker:       broker,
		eventBus:     bus,
		log:          zerolog.Nop(),
	}

	intent := mustExitIntent(t, "PREMIUM_TRAIL exit")
	orderID := "broker-1603"
	s.pendingOrders.Store(orderID, &pendingOrder{
		intent:      intent,
		tenantID:    intent.TenantID,
		envMode:     intent.EnvMode,
		submitStart: time.Now(),
	})

	s.cleanupPendingOrder(orderID)
	time.Sleep(50 * time.Millisecond)

	assert.GreaterOrEqual(t, atomic.LoadInt32(&broker.getPositionCalls), int32(1),
		"baseline: full-exit cleanup without suppression must launch dust sweep")
}

func mustExitIntent(t *testing.T, rationale string) domain.OrderIntent {
	t.Helper()
	intent, err := domain.NewOrderIntent(
		uuid.New(),
		"tenant-1",
		domain.EnvModePaper,
		"AAPL",
		domain.DirectionCloseLong,
		150.0,
		0,
		0,
		1.0,
		"test",
		rationale,
		1.0,
		uuid.New().String(),
	)
	require.NoError(t, err)
	return intent
}
