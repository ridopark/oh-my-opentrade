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

// TestRunner_TradingTheTrendSignal_TranslatesAndDispatches verifies the bus
// translation Phase 3a left out: handler publishes
// EventTradingTheTrendSignalReceived; runner subscribes, translates the
// payload to start.TradingTheTrendSignal, lazy-inits per-ticker state, and
// dispatches into Instance.OnEvent.
//
// This is the integration seam covered by Phase 3e — the unit tests in
// tradingthetrend_v1_test.go drive Instance.OnEvent directly and miss the
// runner layer entirely.
func TestRunner_TradingTheTrendSignal_TranslatesAndDispatches(t *testing.T) {
	bus := memory.NewSyncBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("Paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	var seenSignals atomic.Int32
	var seenTicker atomic.Value
	var seenStrike atomic.Value
	var seenTrigger atomic.Value

	fs := newFakeStrategy("tradingthetrend_v1", "1.0.0")
	fs.onEventFn = func(_ strat.Context, sym string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		if sig, ok := evt.(strat.TradingTheTrendSignal); ok {
			seenSignals.Add(1)
			seenTicker.Store(sig.Ticker)
			seenStrike.Store(sig.Strike)
			seenTrigger.Store(sig.Trigger)
			_ = sym
		}
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("tradingthetrend_v1:dynamic")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{}, // dynamic watchlist; lazy-init per ticker
		Priority: 90,
	}, strat.LifecyclePaperActive, nil)
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	postedAt := time.Date(2026, 5, 8, 14, 30, 0, 0, time.UTC)
	sigEvt, err := domain.NewEvent(
		domain.EventTradingTheTrendSignalReceived,
		"test-tenant",
		envMode,
		"tradingthetrend:msg-1:0",
		domain.TradingTheTrendSignalPayload{
			SignalID:  "tradingthetrend:msg-1:0",
			MessageID: "msg-1",
			Author:    "alice",
			PostedAt:  postedAt,
			Ticker:    "RKLB",
			Strike:    90,
			Right:     domain.OptionRightCall,
			Trigger:   88.0,
			RawLine:   "RKLB 90c > 88.00",
		},
	)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *sigEvt))

	assert.Equal(t, int32(1), seenSignals.Load(),
		"runner must translate one EventTradingTheTrendSignalReceived into one OnEvent dispatch")
	assert.Equal(t, "RKLB", seenTicker.Load())
	assert.Equal(t, 90.0, seenStrike.Load())
	assert.Equal(t, 88.0, seenTrigger.Load())
}

// TestRunner_TradingTheTrendSignal_NoActiveInstance silently no-ops when
// the strategy is not registered — backtests and deployments without TTT
// should not error on stray events from another tenant's sidecar.
func TestRunner_TradingTheTrendSignal_NoActiveInstance(t *testing.T) {
	bus := memory.NewSyncBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("Paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	sigEvt, err := domain.NewEvent(
		domain.EventTradingTheTrendSignalReceived,
		"test-tenant",
		envMode,
		"tradingthetrend:msg-1:0",
		domain.TradingTheTrendSignalPayload{
			SignalID: "tradingthetrend:msg-1:0",
			Ticker:   "RKLB",
			Strike:   90,
			Right:    domain.OptionRightCall,
			Trigger:  88.0,
		},
	)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *sigEvt), "publish must not error when no active TTT instance")
}

// TestRunner_TradingTheTrendSignal_LazyInitsPerTicker ensures different
// tickers reach OnEvent on distinct symbol buckets via the lazy-init path.
// Plan section 5a: "one active arm per ticker per day" — distinct tickers
// must not share state. We verify by counting OnEvent dispatches keyed by
// symbol arg.
func TestRunner_TradingTheTrendSignal_LazyInitsPerTicker(t *testing.T) {
	bus := memory.NewSyncBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("Paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	dispatched := make(map[string]int)
	var mu sync.Mutex

	fs := newFakeStrategy("tradingthetrend_v1", "1.0.0")
	fs.onEventFn = func(_ strat.Context, symbol string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
		if _, ok := evt.(strat.TradingTheTrendSignal); ok {
			mu.Lock()
			dispatched[symbol]++
			mu.Unlock()
		}
		return st, nil, nil
	}

	id, _ := strat.NewInstanceID("tradingthetrend_v1:dynamic")
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:  []string{},
		Priority: 90,
	}, strat.LifecyclePaperActive, nil)
	router.Register(inst)

	ctx := context.Background()
	require.NoError(t, runner.Start(ctx))

	publishTTT := func(ticker string) {
		evt, _ := domain.NewEvent(
			domain.EventTradingTheTrendSignalReceived,
			"test-tenant", envMode,
			"tradingthetrend:"+ticker+":0",
			domain.TradingTheTrendSignalPayload{
				SignalID: "tradingthetrend:" + ticker + ":0",
				Ticker:   domain.Symbol(ticker),
				Strike:   100, Right: domain.OptionRightCall, Trigger: 99,
			},
		)
		require.NoError(t, bus.Publish(ctx, *evt))
	}

	publishTTT("RKLB")
	publishTTT("MSFT")
	publishTTT("RKLB") // same ticker — must reach OnEvent under same symbol bucket

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, dispatched["RKLB"], "two RKLB signals must reach OnEvent under the RKLB bucket")
	assert.Equal(t, 1, dispatched["MSFT"], "one MSFT signal must reach OnEvent under the MSFT bucket")
}
