package strategy_test

import (
	"context"
	"sync"
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

// TestRunner_CopytradeFillReceived_DispatchesFillConfirmation locks the cross-
// package contract between execution.runFillFinalization (publisher) and
// runner.handleFill (subscriber). Regressed once already on 2026-05-04: the
// fast-poll persist path skipped the publisher entirely, so copytrade ghost
// positions never confirmed and STC arrivals 90 minutes later were dropped
// as "no prior BTO".
//
// The payload shape used here mirrors what runFillFinalization emits for a
// copytrade BTO. Any drift in keys, types, or routing semantics between the
// two packages will break this test.
func TestRunner_CopytradeFillReceived_DispatchesFillConfirmation(t *testing.T) {
	bus := memory.NewSyncBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("Paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	var (
		mu        sync.Mutex
		gotFC     []strat.FillConfirmation
		seenSym   atomic.Pointer[string]
	)

	fs := newFakeStrategy("copytrade_v1", "1.0.0")
	fs.onEventFn = func(_ strat.Context, sym string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		if fc, ok := evt.(strat.FillConfirmation); ok {
			mu.Lock()
			gotFC = append(gotFC, fc)
			mu.Unlock()
			s := sym
			seenSym.Store(&s)
		}
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("copytrade_v1:1.0.0:__copytrade__")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"__copytrade__"},
		Priority: 90,
	}, strat.LifecyclePaperActive, nil)

	tctx := newTestCtx()
	require.NoError(t, inst.InitSymbol(tctx, "__copytrade__", nil))
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	// Payload identical to what execution.runFillFinalization emits for a
	// fast-poll fast-path copytrade BTO. The keys runner.handleFill reads:
	// symbol, strategy, side, quantity, price, filled_at.
	filledAt := time.Date(2026, 5, 4, 14, 39, 12, 0, time.UTC)
	fillEvt, err := domain.NewEvent(
		domain.EventFillReceived,
		"test-tenant",
		envMode,
		"5114",
		map[string]any{
			"broker_order_id": "5114",
			"symbol":          "SPY260507C00724000",
			"side":            "BUY",
			"direction":       "LONG",
			"quantity":        12.0,
			"price":           2.475,
			"filled_at":       filledAt,
			"strategy":        "copytrade_v1",
		},
	)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *fillEvt))

	// SyncBus guarantees handlers have run by the time Publish returns.
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, gotFC, 1, "copytrade strategy must receive exactly one FillConfirmation per FillReceived")
	assert.Equal(t, "SPY260507C00724000", gotFC[0].Symbol)
	assert.Equal(t, strat.SideBuy, gotFC[0].Side)
	assert.InDelta(t, 12.0, gotFC[0].Quantity, 1e-9)
	assert.InDelta(t, 2.475, gotFC[0].Price, 1e-9)

	// Routing should land on the __copytrade__ sentinel, not the OCC symbol,
	// because runner.handleFill resolves OCC -> underlying ("SPY") then
	// falls back to the copytrade-by-strategy lookup which dispatches under
	// the sentinel.
	gotSym := seenSym.Load()
	require.NotNil(t, gotSym)
	assert.Equal(t, "__copytrade__", *gotSym)
}
