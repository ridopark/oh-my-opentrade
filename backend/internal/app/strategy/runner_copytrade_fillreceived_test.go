package strategy_test

import (
	"context"
	"sync"
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
		mu      sync.Mutex
		gotFC   []strat.FillConfirmation
		seenSym string
	)

	fs := newFakeStrategy("copytrade_v1", "1.0.0")
	fs.onEventFn = func(_ strat.Context, sym string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		if fc, ok := evt.(strat.FillConfirmation); ok {
			mu.Lock()
			gotFC = append(gotFC, fc)
			seenSym = sym
			mu.Unlock()
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
	assert.Equal(t, "__copytrade__", seenSym)
}

// TestRunner_HandleFill_EmitDomainEvent_ReachesBus verifies that handleFill
// builds an instanceContext with runner/ctx/tenantID/envMode wired through,
// so a strategy's EmitDomainEvent call from inside a FillConfirmation handler
// publishes to the bus. Without this wiring, EmitDomainEvent silently falls
// through to the context's no-op emit and drops the event.
//
// Today this fires for CopytradeOrphanFillPayload at copytrade_v1.go:574 (the
// emit is dead in the FillConfirmation path) and is a prerequisite for the
// queued-STC drain Stage A.2 depends on.
func TestRunner_HandleFill_EmitDomainEvent_ReachesBus(t *testing.T) {
	bus := memory.NewSyncBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("Paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	// Subscribe to the bus before publishing so we observe the emit.
	var (
		mu     sync.Mutex
		orphan []domain.CopytradeOrphanFillPayload
	)
	ctx := context.Background()
	require.NoError(t, bus.Subscribe(ctx, domain.EventCopytradeOrphanFill, func(_ context.Context, ev domain.Event) error {
		if p, ok := ev.Payload.(domain.CopytradeOrphanFillPayload); ok {
			mu.Lock()
			orphan = append(orphan, p)
			mu.Unlock()
		}
		return nil
	}))

	fs := newFakeStrategy("copytrade_v1", "1.0.0")
	fs.onEventFn = func(c strat.Context, _ string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		if _, ok := evt.(strat.FillConfirmation); ok {
			_ = c.EmitDomainEvent(domain.CopytradeOrphanFillPayload{
				StrategyID:     "copytrade_v1",
				ContractSymbol: "SPY260507C00724000",
				FillPrice:      2.475,
				Qty:            12.0,
				ObservedAt:     c.Now(),
			})
		}
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("copytrade_v1:1.0.0:__copytrade__")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"__copytrade__"},
		Priority: 90,
	}, strat.LifecyclePaperActive, nil)
	require.NoError(t, inst.InitSymbol(newTestCtx(), "__copytrade__", nil))
	router.Register(inst)

	require.NoError(t, runner.Start(ctx))

	fillEvt, err := domain.NewEvent(
		domain.EventFillReceived,
		"test-tenant",
		envMode,
		"5114",
		map[string]any{
			"symbol":    "SPY260507C00724000",
			"side":      "BUY",
			"quantity":  12.0,
			"price":     2.475,
			"filled_at": time.Date(2026, 5, 4, 14, 39, 12, 0, time.UTC),
			"strategy":  "copytrade_v1",
		},
	)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *fillEvt))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, orphan, 1, "EmitDomainEvent from FillConfirmation handler must reach the bus")
	assert.Equal(t, "SPY260507C00724000", orphan[0].ContractSymbol)
	assert.InDelta(t, 2.475, orphan[0].FillPrice, 1e-9)
	assert.InDelta(t, 12.0, orphan[0].Qty, 1e-9)
}
