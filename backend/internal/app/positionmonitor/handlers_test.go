package positionmonitor

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// buildFillEvent returns a FillReceived event with the payload shape
// handleFillEvent expects (matches execution.Service.publishFillReceived).
func buildFillEvent(t *testing.T, symbol, side, idemKey string, filledAt time.Time) domain.Event {
	t.Helper()
	payload := map[string]any{
		"symbol":    symbol,
		"side":      side,
		"price":     float64(150.0),
		"quantity":  float64(10.0),
		"filled_at": filledAt,
		"strategy":  "copytrade_v1",
	}
	ev, err := domain.NewEvent(domain.EventFillReceived, "tenant-1", domain.EnvModePaper, idemKey, payload)
	require.NoError(t, err)
	return *ev
}

// TestHandleFillEvent_BacktestPath_InlineProcessFill: with disableTickLoop=true,
// the gated handler must process the fill inline so s.positions is populated by
// the time bus.Publish returns. s.fills channel stays empty (bypassed).
func TestHandleFillEvent_BacktestPath_InlineProcessFill(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 15, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithDisableTickLoop(),
		WithDisableReconcile(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	ev := buildFillEvent(t, "AAPL", "BUY", "fill-1", now)
	require.NoError(t, bus.Publish(ctx, ev))

	// Inline path: position registered before Publish returns. No Eventually.
	require.Equal(t, 1, svc.PositionCount(), "position must be registered synchronously in backtest mode")
	require.Len(t, svc.fills, 0, "s.fills channel must NOT be used when disableTickLoop=true")
}

// TestHandleFillEvent_BacktestPath_TwoFillsSameTick: catches the original race.
// Two FillReceived events for different contracts published back-to-back; both
// positions must be registered before either Publish returns.
func TestHandleFillEvent_BacktestPath_TwoFillsSameTick(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 15, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithDisableTickLoop(),
		WithDisableReconcile(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	ev1 := buildFillEvent(t, "AAPL", "BUY", "fill-aapl", now)
	require.NoError(t, bus.Publish(ctx, ev1))
	require.Equal(t, 1, svc.PositionCount(), "AAPL position must be visible before second Publish")

	ev2 := buildFillEvent(t, "MSFT", "BUY", "fill-msft", now)
	require.NoError(t, bus.Publish(ctx, ev2))
	require.Equal(t, 2, svc.PositionCount(), "both positions must be visible synchronously")
}

// TestHandleFillEvent_BacktestPath_BurstSameContractIdempotent extends the
// burst test to assert that two consecutive fills for the SAME contract do
// not panic and behave per scale-in semantics (positions map keyed by symbol,
// quantity is averaged). From risk register: "addPosition idempotency on
// burst FillReceived for same contract".
func TestHandleFillEvent_BacktestPath_BurstSameContractIdempotent(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 15, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithDisableTickLoop(),
		WithDisableReconcile(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	ev1 := buildFillEvent(t, "AAPL", "BUY", "fill-aapl-1", now)
	require.NoError(t, bus.Publish(ctx, ev1))
	ev2 := buildFillEvent(t, "AAPL", "BUY", "fill-aapl-2", now)
	require.NoError(t, bus.Publish(ctx, ev2))

	require.Equal(t, 1, svc.PositionCount(), "same-contract scale-in must not double-register")
	pos := svc.positions["tenant-1:Paper:AAPL"]
	require.NotNil(t, pos)
	require.InDelta(t, 20.0, pos.Quantity, 1e-9, "quantity must be summed across fills")
}

// TestHandleFillEvent_LivePath_ChannelEnqueue: with disableTickLoop=false,
// handleFillEvent pushes onto s.fills and returns; s.positions stays empty
// until the tick loop drains. Construct manually because in the test bus
// mockEventBus.SubscribeAsync == Subscribe, but the gate inside Start picks
// the right path based on s.disableTickLoop.
func TestHandleFillEvent_LivePath_ChannelEnqueue(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 15, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithTickInterval(1*time.Hour),
		WithDisableReconcile(),
	)

	// Note: NOT calling svc.Start(ctx) so the tick loop never drains.
	// Invoke the subscribed handler directly via bus.Publish path requires
	// Start; instead exercise handleFillEvent directly here.
	ev := buildFillEvent(t, "AAPL", "BUY", "fill-1", now)
	require.NoError(t, svc.handleFillEvent(context.Background(), ev))

	require.Equal(t, 0, svc.PositionCount(), "live mode: position must NOT be visible before tick processes")
	require.Len(t, svc.fills, 1, "live mode: fill must be enqueued on s.fills channel")
}

// TestHandleFillEvent_LivePath_TickProcesses: confirms the channel-drain path
// is still working. The runTickLoop processes the buffered fill on the next
// tick boundary.
func TestHandleFillEvent_LivePath_TickProcesses(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())
	pg := execution.NewPositionGate(&mockBroker{}, zerolog.Nop())

	now := time.Date(2026, 3, 6, 15, 0, 0, 0, time.UTC)
	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithNowFunc(func() time.Time { return now }),
		WithTickInterval(10*time.Millisecond),
		WithDisableReconcile(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	ev := buildFillEvent(t, "AAPL", "BUY", "fill-1", now)
	require.NoError(t, bus.Publish(ctx, ev))

	// Tick loop drains channel; position must appear within a few ticks.
	require.Eventually(t, func() bool { return svc.PositionCount() == 1 },
		500*time.Millisecond, 5*time.Millisecond,
		"live mode: tick loop must drain s.fills and register the position")
}

