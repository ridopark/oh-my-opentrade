package strategy_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunner_CopytradeExitRejected_NoReentrantDeadlock reproduces the exact
// syncMode reentrancy stack that hung the 90d replay. While the runner is
// dispatching a CopytradeSignal into Instance.OnEvent (holding inst.mu and
// r.mu), the strategy publishes a payload that the sync bus routes back to
// Runner.handleCopytradeExitRejected on the same goroutine. Pre-fix, that
// handler would re-acquire inst.mu (via IsActive) and r.mu and deadlock.
// Post-fix: IsActive is lock-free and the nested dispatch is enqueued, so
// the outer OnEvent returns, the caller drains, and the enqueued callback
// runs without reentering any held mutex.
func TestRunner_CopytradeExitRejected_NoReentrantDeadlock(t *testing.T) {
	bus := memory.NewSyncBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("Paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	var innerOnEventCount atomic.Int32

	fs := newFakeStrategy("copytrade_v1", "1.0.0")
	fs.onEventFn = func(ctx strat.Context, _ string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		switch evt.(type) {
		case strat.CopytradeSignal:
			// Simulate the real handleSTC path: publish an exit-rejected
			// payload via the sync bus while still inside OnEvent. Pre-fix
			// this would deadlock on inst.mu.
			rejEvt, _ := domain.NewEvent(
				domain.EventCopytradeExitRejected,
				"test-tenant",
				envMode,
				"rej-1",
				domain.CopytradeExitRejectedPayload{
					ContractSymbol: "AAPL260425C00190000",
					Fraction:       0.5,
					Reason:         "exit_in_flight",
				},
			)
			_ = bus.Publish(context.Background(), *rejEvt)
		case strat.CopytradeExitRejection:
			innerOnEventCount.Add(1)
		}
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("copytrade_v1:__copytrade__")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"__copytrade__"},
		Priority: 90,
	}, strat.LifecyclePaperActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "__copytrade__", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	done := make(chan struct{})
	go func() {
		defer close(done)
		sigEvt, _ := domain.NewEvent(
			domain.EventCopytradeSignalReceived,
			"test-tenant",
			envMode,
			"sig-1",
			domain.CopytradeSignalPayload{
				SignalID:  "msg-1:0",
				MessageID: "msg-1",
				Author:    "alice",
				PostedAt:  time.Now(),
				Action:    domain.CopytradeActionSTC,
				Ticker:    "AAPL",
				Expiry:    time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
				Strike:    190,
				Right:     domain.OptionRightCall,
				Price:     1.50,
				Tail:      "half out",
			},
		)
		_ = bus.Publish(ctx, *sigEvt)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reentrant-dispatch deadlock: outer Publish did not return within 5s")
	}

	assert.Equal(t, int32(1), innerOnEventCount.Load(),
		"drained callback must have fired exactly once after outer OnEvent returned")
}
