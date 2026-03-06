package strategy_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	stratports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestV2Pipeline_BarToOrderIntent(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	ctx := context.Background()

	envMode, err := domain.NewEnvMode("Paper")
	require.NoError(t, err)

	runner := strategy.NewRunner(bus, router, "t1", envMode, nil)

	fs := newFakeStrategy("test_strat", "1.0.0")
	instanceID, err := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	require.NoError(t, err)

	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		sig, err := strat.NewSignal(
			instanceID,
			symbol,
			strat.SignalEntry,
			strat.SideBuy,
			0.8,
			map[string]string{"ref_price": "150.0000"},
		)
		require.NoError(t, err)
		return st, []strat.Signal{sig}, nil
	}

	inst := strategy.NewInstance(instanceID, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	instCtx := strategy.NewContext(time.Now(), slog.Default(), nil)
	require.NoError(t, inst.InitSymbol(instCtx, "AAPL", nil))
	router.Register(inst)

	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(5),
		"stop_bps":           int64(25),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	advisor := &fakeAIAdvisor{err: errors.New("ai unavailable")}
	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)

	require.NoError(t, runner.Start(ctx))
	require.NoError(t, enricher.Start(ctx))
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	sym, err := domain.NewSymbol("AAPL")
	require.NoError(t, err)
	bar, err := domain.NewMarketBar(time.Now(), sym, "1m", 150, 151, 149, 150, 10)
	require.NoError(t, err)
	ev, err := domain.NewEvent(domain.EventMarketBarSanitized, "t1", envMode, "bar-1", bar)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *ev))

	evts := waitForEvents(t, received, 1)
	require.Equal(t, domain.EventOrderIntentCreated, evts[0].Type)

	intent, ok := evts[0].Payload.(domain.OrderIntent)
	require.True(t, ok)

	assert.Equal(t, domain.DirectionLong, intent.Direction)
	assert.Equal(t, domain.Symbol("AAPL"), intent.Symbol)
	assert.Equal(t, "test_strat", intent.Strategy)
	assert.InDelta(t, 150*(1+0.0005), intent.LimitPrice, 0.0000001)
	assert.InDelta(t, 150*(1-0.0025), intent.StopLoss, 0.0000001)
	assert.Greater(t, intent.Quantity, 0.0)
}

func TestV2Pipeline_NoSignal_NoOrderIntent(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	ctx := context.Background()

	envMode, err := domain.NewEnvMode("Paper")
	require.NoError(t, err)

	runner := strategy.NewRunner(bus, router, "t1", envMode, nil)

	fs := newFakeStrategy("test_strat", "1.0.0")
	instanceID, err := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	require.NoError(t, err)
	fs.onBarFunc = func(_ strat.Context, _ string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		return st, nil, nil
	}

	inst := strategy.NewInstance(instanceID, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	instCtx := strategy.NewContext(time.Now(), slog.Default(), nil)
	require.NoError(t, inst.InitSymbol(instCtx, "AAPL", nil))
	router.Register(inst)

	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(5),
		"stop_bps":           int64(25),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	advisor := &fakeAIAdvisor{err: errors.New("ai unavailable")}
	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)

	require.NoError(t, runner.Start(ctx))
	require.NoError(t, enricher.Start(ctx))
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	sym, err := domain.NewSymbol("AAPL")
	require.NoError(t, err)
	bar, err := domain.NewMarketBar(time.Now(), sym, "1m", 150, 151, 149, 150, 10)
	require.NoError(t, err)
	ev, err := domain.NewEvent(domain.EventMarketBarSanitized, "t1", envMode, "bar-1", bar)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *ev))

	select {
	case <-received:
		require.FailNow(t, "unexpected event")
	case <-time.After(100 * time.Millisecond):
		assert.True(t, true)
	}
}

func TestV2Pipeline_UnassignedSymbol_NoOrderIntent(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	ctx := context.Background()

	envMode, err := domain.NewEnvMode("Paper")
	require.NoError(t, err)

	runner := strategy.NewRunner(bus, router, "t1", envMode, nil)

	fs := newFakeStrategy("test_strat", "1.0.0")
	instanceID, err := strat.NewInstanceID("test_strat:1.0.0:AAPL")
	require.NoError(t, err)
	fs.onBarFunc = func(_ strat.Context, symbol string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
		sig, err := strat.NewSignal(
			instanceID,
			symbol,
			strat.SignalEntry,
			strat.SideBuy,
			0.8,
			map[string]string{"ref_price": "150.0000"},
		)
		require.NoError(t, err)
		return st, []strat.Signal{sig}, nil
	}

	inst := strategy.NewInstance(instanceID, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{"AAPL"},
		Priority: 100,
	}, strat.LifecycleLiveActive, nil)

	instCtx := strategy.NewContext(time.Now(), slog.Default(), nil)
	require.NoError(t, inst.InitSymbol(instCtx, "AAPL", nil))
	router.Register(inst)

	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(5),
		"stop_bps":           int64(25),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	advisor := &fakeAIAdvisor{err: errors.New("ai unavailable")}
	enricher := strategy.NewSignalDebateEnricher(bus, advisor, nil)

	require.NoError(t, runner.Start(ctx))
	require.NoError(t, enricher.Start(ctx))
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	sym, err := domain.NewSymbol("MSFT")
	require.NoError(t, err)
	bar, err := domain.NewMarketBar(time.Now(), sym, "1m", 150, 151, 149, 150, 10)
	require.NoError(t, err)
	ev, err := domain.NewEvent(domain.EventMarketBarSanitized, "t1", envMode, "bar-1", bar)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *ev))

	select {
	case <-received:
		require.FailNow(t, "unexpected event")
	case <-time.After(100 * time.Millisecond):
		assert.True(t, true)
	}
}
