package monitor_test

import (
"context"
"testing"
"time"

"github.com/rs/zerolog"
"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
"github.com/oh-my-opentrade/backend/internal/app/monitor"
"github.com/oh-my-opentrade/backend/internal/domain"
"github.com/stretchr/testify/assert"
"github.com/stretchr/testify/require"
)

func TestService_StartSubscribes(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	// Publish an invalid payload and check if handler picks it up and returns error
	err = bus.Publish(context.Background(), createTestEvent(t, "invalid payload"))
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "payload is not a MarketBar")
	}
}

func TestService_EmitsStateUpdated(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var emitted domain.Event
	bus.Subscribe(context.Background(), domain.EventStateUpdated, func(ctx context.Context, ev domain.Event) error {
		emitted = ev
		return nil
	})

	sym, _ := domain.NewSymbol("BTC/USD")
	bar := createBar(t, sym, 100.0, 10.0)

	err = bus.Publish(context.Background(), createTestEvent(t, bar))
	require.NoError(t, err)

	// verify EventStateUpdated was emitted with IndicatorSnapshot
	assert.Equal(t, domain.EventStateUpdated, emitted.Type)
	snap, ok := emitted.Payload.(domain.IndicatorSnapshot)
	require.True(t, ok)
	assert.Equal(t, sym, snap.Symbol)
}

func TestService_EmitsRegimeShifted(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var emitted domain.Event
	var shiftCount int
	bus.Subscribe(context.Background(), domain.EventRegimeShifted, func(ctx context.Context, ev domain.Event) error {
		emitted = ev
		shiftCount++
		return nil
	})

	sym, _ := domain.NewSymbol("BTC/USD")

	// Publish enough bars to trigger an initial regime detection
	for i := 0; i < 25; i++ {
		bar := createBar(t, sym, 100.0+float64(i), 10.0)
		err = bus.Publish(context.Background(), createTestEvent(t, bar))
		require.NoError(t, err)
	}

	assert.Greater(t, shiftCount, 0, "should have emitted at least one RegimeShifted event")
	assert.Equal(t, domain.EventRegimeShifted, emitted.Type)

	regime, ok := emitted.Payload.(domain.MarketRegime)
	require.True(t, ok)
	assert.Equal(t, sym, regime.Symbol)
	// It should probably be TREND since we gave it an ascending price
	assert.Equal(t, domain.RegimeTrend, regime.Type)
}

func TestService_EmitsSetupDetected(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var emitted domain.Event
	var setupCount int
	bus.Subscribe(context.Background(), domain.EventSetupDetected, func(ctx context.Context, ev domain.Event) error {
		emitted = ev
		setupCount++
		return nil
	})

	sym, _ := domain.NewSymbol("BTC/USD")

	// Simulate condition for a setup (e.g., RSI oversold + bullish EMA crossover)
	// First push price down to get RSI oversold
	for i := 0; i < 15; i++ {
		bar := createBar(t, sym, 100.0-float64(i*2), 10.0)
		_ = bus.Publish(context.Background(), createTestEvent(t, bar))
	}

	// Then push price up rapidly to cross EMAs
	for i := 0; i < 5; i++ {
		bar := createBar(t, sym, 70.0+float64(i*10), 20.0)
		_ = bus.Publish(context.Background(), createTestEvent(t, bar))
	}

	// Wait to allow async events if any, but since memory bus is sync it should be immediate
	assert.Greater(t, setupCount, 0, "should have emitted SetupDetected event")
	assert.Equal(t, domain.EventSetupDetected, emitted.Type)

	setup, ok := emitted.Payload.(monitor.SetupCondition)
	require.True(t, ok)
	assert.Equal(t, sym, setup.Symbol)
	assert.NotEmpty(t, setup.Trigger)
}

func TestService_WarmUp_SeedsIndicators(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	sym, _ := domain.NewSymbol("AAPL")

	// Build 21 ascending bars — enough for EMA21 and RSI to initialise
	bars := make([]domain.MarketBar, 21)
	for i := 0; i < 21; i++ {
		bars[i] = createBar(t, sym, 100.0+float64(i), 10.0)
	}

	n := svc.WarmUp(bars)
	assert.Equal(t, 21, n, "WarmUp should return count of bars processed")

	// After warmup, the next live bar should produce a non-zero RSI and EMA21.
	var snap domain.IndicatorSnapshot
	bus.Subscribe(context.Background(), domain.EventStateUpdated, func(ctx context.Context, ev domain.Event) error {
		snap = ev.Payload.(domain.IndicatorSnapshot)
		return nil
	})

	liveBar := createBar(t, sym, 125.0, 10.0)
	evt, err := domain.NewEvent(domain.EventMarketBarSanitized, "t", domain.EnvModePaper, "warm-live", liveBar)
	require.NoError(t, err)
	err = bus.Publish(context.Background(), *evt)
	require.NoError(t, err)

	assert.Greater(t, snap.RSI, 0.0, "RSI should be live after warmup")
	assert.Greater(t, snap.EMA21, 0.0, "EMA21 should be live after warmup")
}

func TestService_WarmUp_EmitsNoEvents(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	sym, _ := domain.NewSymbol("AAPL")

	var eventCount int
	for _, typ := range []domain.EventType{
		domain.EventStateUpdated,
		domain.EventRegimeShifted,
		domain.EventSetupDetected,
	} {
		typ := typ
		bus.Subscribe(context.Background(), typ, func(ctx context.Context, ev domain.Event) error {
			_ = typ
			eventCount++
			return nil
		})
	}

	bars := make([]domain.MarketBar, 30)
	for i := 0; i < 30; i++ {
		bars[i] = createBar(t, sym, 100.0+float64(i), 10.0)
	}

	svc.WarmUp(bars)

	assert.Equal(t, 0, eventCount, "WarmUp must not emit any events")
}

func TestService_NoSetupWhenNoConditionMet(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var setupCount int
	bus.Subscribe(context.Background(), domain.EventSetupDetected, func(ctx context.Context, ev domain.Event) error {
		setupCount++
		return nil
	})

	sym, _ := domain.NewSymbol("BTC/USD")

	// Just send choppy/flat data that shouldn't trigger any setup
	for i := 0; i < 30; i++ {
		price := 100.0
		if i%2 == 0 {
			price = 101.0
		}
		bar := createBar(t, sym, price, 10.0)
		_ = bus.Publish(context.Background(), createTestEvent(t, bar))
	}

	assert.Equal(t, 0, setupCount, "should not emit SetupDetected for flat market")
}

func TestService_InvalidPayload(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.HandleMarketBar(context.Background(), createTestEvent(t, "not a bar"))
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "payload is not a MarketBar")
	}
}

func TestService_MaintainsStatePerSymbol(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var lastSnapBTC domain.IndicatorSnapshot
	var lastSnapETH domain.IndicatorSnapshot

	bus.Subscribe(context.Background(), domain.EventStateUpdated, func(ctx context.Context, ev domain.Event) error {
		snap := ev.Payload.(domain.IndicatorSnapshot)
		if snap.Symbol.String() == "BTC/USD" {
			lastSnapBTC = snap
		} else if snap.Symbol.String() == "ETH/USD" {
			lastSnapETH = snap
		}
		return nil
	})

	symBTC, _ := domain.NewSymbol("BTC/USD")
	symETH, _ := domain.NewSymbol("ETH/USD")

	// Send 15 UP bars for BTC
	for i := 0; i < 15; i++ {
		_ = bus.Publish(context.Background(), createTestEvent(t, createBar(t, symBTC, 100.0+float64(i), 10.0)))
	}

	// Send 15 DOWN bars for ETH
	for i := 0; i < 15; i++ {
		_ = bus.Publish(context.Background(), createTestEvent(t, createBar(t, symETH, 100.0-float64(i), 10.0)))
	}

	// Wait briefly (memory bus is sync, but just in case)
	time.Sleep(10 * time.Millisecond)

	// BTC RSI should be near 100, ETH RSI should be near 0
	assert.Greater(t, lastSnapBTC.RSI, 90.0, "BTC RSI should be high")
	assert.Less(t, lastSnapETH.RSI, 10.0, "ETH RSI should be low")
}
