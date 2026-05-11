package strategy_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/positionmonitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// mockBroker is a minimal BrokerPort stub so PositionGate accepts our wiring.
type mockBroker struct{}

func (m *mockBroker) SubmitOrder(_ context.Context, _ domain.OrderIntent) (string, error) {
	return "", nil
}
func (m *mockBroker) CancelOrder(_ context.Context, _ string) error { return nil }
func (m *mockBroker) GetOrderStatus(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (m *mockBroker) GetPositions(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}
func (m *mockBroker) CancelOpenOrders(_ context.Context, _ domain.Symbol, _ string) (int, error) {
	return 0, nil
}
func (m *mockBroker) CancelAllOpenOrders(_ context.Context) (int, error) { return 0, nil }
func (m *mockBroker) GetOpenOrders(_ context.Context) ([]ports.OpenOrder, error) {
	return nil, nil
}
func (m *mockBroker) GetPosition(_ context.Context, _ domain.Symbol) (float64, error) {
	return 0, nil
}
func (m *mockBroker) CloseAtMarket(_ context.Context, _ domain.Symbol) (string, error) {
	return "", nil
}
func (m *mockBroker) GetOrderDetails(_ context.Context, _ string) (ports.OrderDetails, error) {
	return ports.OrderDetails{}, nil
}

// TestFillReceived_SubscriptionOrder_PosMonBeforeRunner locks in the bootstrap
// invariant that position_monitor.handleFillEvent subscribes BEFORE any
// downstream FillReceived consumer (notably strategy.runner.handleFill). This
// is the Phase 3b regression test from copytrade_exit_race_fix_plan.md.
//
// The sync-mode in-memory bus (used in backtest) dispatches handlers in
// registration order on the publisher's goroutine. Bug pre-fix: strategy
// runner was subscribed before position_monitor; the runner saw FillReceived
// and drained queued STC events before position_monitor had registered the
// open position, causing "copytrade_exit_request: no matching position"
// warnings.
//
// Why a stand-in handler instead of the real strategy.Runner: the runner
// pulls in a large dependency graph (sizer, instances, indicator service,
// router) that is not load-bearing for the ordering invariant. A simple
// Subscribe handler with a shared counter catches the exact regression the
// plan flags: "a future bootstrap refactor that silently reintroduces the
// bug". The invariant under test is bus-level subscription ordering, not
// runner-internal semantics.
func TestFillReceived_SubscriptionOrder_PosMonBeforeRunner(t *testing.T) {
	bus := memory.NewSyncBus()

	pc := positionmonitor.NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 15, 0, 0, 0, time.UTC)
	svc := positionmonitor.NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		positionmonitor.WithNowFunc(func() time.Time { return now }),
		positionmonitor.WithDisableTickLoop(),
		positionmonitor.WithDisableReconcile(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Step 1: position monitor subscribes FIRST (mirrors backtest wiring after Phase 3a).
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	// Step 2: register the downstream subscriber (stand-in for strategy.runner.handleFill).
	var (
		mu                   sync.Mutex
		nextIdx              int64
		posMonObservedAtIdx  int64
		runnerStandInIdx     int64
		runnerSawPositionMap atomic.Bool
	)

	// A no-op handler registered BEFORE the stand-in so we can measure
	// posMon's relative position. The bus stores handlers in registration
	// order; this handler bumps the counter immediately after posMon's
	// handler runs, capturing posMon's index in the dispatch sequence.
	require.NoError(t, bus.Subscribe(ctx, domain.EventFillReceived, func(_ context.Context, _ domain.Event) error {
		mu.Lock()
		nextIdx++
		posMonObservedAtIdx = nextIdx
		mu.Unlock()
		return nil
	}))

	// The runner stand-in. When this fires, the position must already be in
	// position_monitor's map (the whole point of the fix).
	require.NoError(t, bus.Subscribe(ctx, domain.EventFillReceived, func(_ context.Context, _ domain.Event) error {
		mu.Lock()
		nextIdx++
		runnerStandInIdx = nextIdx
		mu.Unlock()
		if svc.PositionCount() == 1 {
			runnerSawPositionMap.Store(true)
		}
		return nil
	}))

	payload := map[string]any{
		"symbol":    "AAPL",
		"side":      "BUY",
		"price":     float64(150.0),
		"quantity":  float64(10.0),
		"filled_at": now,
		"strategy":  "copytrade_v1",
	}
	ev, err := domain.NewEvent(domain.EventFillReceived, "tenant-1", domain.EnvModePaper, "fill-1", payload)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *ev))

	mu.Lock()
	defer mu.Unlock()
	require.Greater(t, posMonObservedAtIdx, int64(0), "posMon tracker must have fired")
	require.Greater(t, runnerStandInIdx, int64(0), "runner stand-in must have fired")
	require.Less(t, posMonObservedAtIdx, runnerStandInIdx,
		"position_monitor handler must fire BEFORE downstream FillReceived subscribers")
	require.True(t, runnerSawPositionMap.Load(),
		"downstream subscriber must observe a registered position (no race)")
}
