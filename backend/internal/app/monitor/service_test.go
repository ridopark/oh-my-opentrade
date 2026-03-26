package monitor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/rs/zerolog"
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

func TestService_SymbolFilter_AllowsAllWhenNoBaseSymbols(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var count int
	err = bus.Subscribe(context.Background(), domain.EventStateUpdated, func(ctx context.Context, ev domain.Event) error {
		count++
		return nil
	})
	require.NoError(t, err)

	sym, _ := domain.NewSymbol("MSFT")
	bar := createBar(t, sym, 100.0, 10.0)
	err = bus.Publish(context.Background(), createTestEvent(t, bar))
	require.NoError(t, err)

	assert.Equal(t, 1, count)
}

func TestService_SymbolFilter_BlocksNonBaseSymbol(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())
	svc.SetBaseSymbols([]string{"AAPL"})

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var count int
	err = bus.Subscribe(context.Background(), domain.EventStateUpdated, func(ctx context.Context, ev domain.Event) error {
		count++
		return nil
	})
	require.NoError(t, err)

	sym, _ := domain.NewSymbol("MSFT")
	bar := createBar(t, sym, 100.0, 10.0)
	err = bus.Publish(context.Background(), createTestEvent(t, bar))
	require.NoError(t, err)

	assert.Equal(t, 0, count)
}

func TestService_SymbolFilter_AllowsBaseSymbol(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())
	svc.SetBaseSymbols([]string{"AAPL"})
	svc.MarkReady("AAPL")

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var count int
	err = bus.Subscribe(context.Background(), domain.EventStateUpdated, func(ctx context.Context, ev domain.Event) error {
		count++
		return nil
	})
	require.NoError(t, err)

	sym, _ := domain.NewSymbol("AAPL")
	bar := createBar(t, sym, 100.0, 10.0)
	err = bus.Publish(context.Background(), createTestEvent(t, bar))
	require.NoError(t, err)

	assert.Equal(t, 1, count)
}

func TestService_EffectiveSymbolsUpdated_OverridesBase(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())
	svc.SetBaseSymbols([]string{"AAPL", "MSFT"})
	svc.MarkReady("AAPL", "MSFT", "TSLA")

	err := svc.Start(context.Background())
	require.NoError(t, err)

	payload := screener.EffectiveSymbolsUpdatedPayload{
		StrategyKey: "orb_break_retest",
		RunID:       "test-run",
		AsOf:        time.Now(),
		Mode:        "intersection",
		Source:      "intersection",
		Symbols:     []string{"TSLA"},
	}
	evt, err := domain.NewEvent(domain.EventEffectiveSymbolsUpdated, "tenant123", domain.EnvModePaper, "test-effective", payload)
	require.NoError(t, err)
	err = bus.Publish(context.Background(), *evt)
	require.NoError(t, err)

	var count int
	err = bus.Subscribe(context.Background(), domain.EventStateUpdated, func(ctx context.Context, ev domain.Event) error {
		count++
		return nil
	})
	require.NoError(t, err)

	symAAPL, _ := domain.NewSymbol("AAPL")
	symTSLA, _ := domain.NewSymbol("TSLA")
	err = bus.Publish(context.Background(), createTestEvent(t, createBar(t, symAAPL, 100.0, 10.0)))
	require.NoError(t, err)
	err = bus.Publish(context.Background(), createTestEvent(t, createBar(t, symTSLA, 100.0, 10.0)))
	require.NoError(t, err)

	assert.Equal(t, 1, count)
}

func TestService_ReadinessGate_BlocksUnreadySymbols(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())
	svc.SetBaseSymbols([]string{"AAPL", "MSFT"})

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var count int
	err = bus.Subscribe(context.Background(), domain.EventStateUpdated, func(ctx context.Context, ev domain.Event) error {
		count++
		return nil
	})
	require.NoError(t, err)

	symAAPL, _ := domain.NewSymbol("AAPL")
	err = bus.Publish(context.Background(), createTestEvent(t, createBar(t, symAAPL, 100.0, 10.0)))
	require.NoError(t, err)
	assert.Equal(t, 0, count, "unready symbol should be blocked")

	svc.MarkReady("AAPL")

	err = bus.Publish(context.Background(), createTestEvent(t, createBar(t, symAAPL, 101.0, 10.0)))
	require.NoError(t, err)
	assert.Equal(t, 1, count, "ready symbol should be processed")
}

func TestService_ReadinessGate_NoBaseSymbols_AllAllowed(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var count int
	err = bus.Subscribe(context.Background(), domain.EventStateUpdated, func(ctx context.Context, ev domain.Event) error {
		count++
		return nil
	})
	require.NoError(t, err)

	sym, _ := domain.NewSymbol("AAPL")
	err = bus.Publish(context.Background(), createTestEvent(t, createBar(t, sym, 100.0, 10.0)))
	require.NoError(t, err)
	assert.Equal(t, 1, count, "no base symbols configured = allow all (backward compat)")
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

	sym, _ := domain.NewSymbol("AAPL")

	base := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		bt := base.Add(time.Duration(i) * time.Minute)
		bar := createBarAtTime(t, sym, bt, 100, 101, 99, 100, 10)
		_ = bus.Publish(context.Background(), createTestEvent(t, bar))
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, post, 100, 101, 99, 100, 10)))
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, breakT, 100, 104, 100, 104, 50)))
	retestT := breakT.Add(time.Minute)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, retestT, 101, 104, 101, 103, 20)))

	// Wait to allow async events if any, but since memory bus is sync it should be immediate
	assert.Greater(t, setupCount, 0, "should have emitted SetupDetected event")
	assert.Equal(t, domain.EventSetupDetected, emitted.Type)

	setup, ok := emitted.Payload.(monitor.SetupCondition)
	require.True(t, ok)
	assert.Equal(t, sym, setup.Symbol)
	assert.NotEmpty(t, setup.Trigger)
	assert.Equal(t, "orb_break_retest", setup.Trigger)
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

func TestService_SettlingGuard_SuppressesSetupDetectionForFirstBars(t *testing.T) {
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

	sym, _ := domain.NewSymbol("AAPL")

	base := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		bt := base.Add(time.Duration(i) * time.Minute)
		bar := createBarAtTime(t, sym, bt, 100, 101, 99, 100, 10)
		_ = bus.Publish(context.Background(), createTestEvent(t, bar))
		if i < 5 {
			assert.Equal(t, 0, setupCount, "settling guard must suppress setup detection")
		}
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, post, 100, 101, 99, 100, 10)))
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, breakT, 100, 104, 100, 104, 50)))
	retestT := breakT.Add(time.Minute)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, retestT, 101, 104, 101, 103, 20)))

	assert.Greater(t, setupCount, 0, "should emit SetupDetected once settling complete")
}

func TestService_DNAGate_BlocksUnapprovedSetup(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	gate := &mockDNAGate{approved: false}
	svc.SetDNAGate(gate, "orb_break_retest")

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var setupCount int
	bus.Subscribe(context.Background(), domain.EventSetupDetected, func(ctx context.Context, ev domain.Event) error {
		setupCount++
		return nil
	})

	sym, _ := domain.NewSymbol("AAPL")

	// Same bar pattern that triggers setup in TestService_EmitsSetupDetected
	base := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		bt := base.Add(time.Duration(i) * time.Minute)
		bar := createBarAtTime(t, sym, bt, 100, 101, 99, 100, 10)
		_ = bus.Publish(context.Background(), createTestEvent(t, bar))
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, post, 100, 101, 99, 100, 10)))
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, breakT, 100, 104, 100, 104, 50)))
	retestT := breakT.Add(time.Minute)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, retestT, 101, 104, 101, 103, 20)))

	assert.Equal(t, 0, setupCount, "DNA gate must block SetupDetected when DNA not approved")
	assert.Greater(t, gate.calls, 0, "gate should have been called")
}

func TestService_DNAGate_AllowsApprovedSetup(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	gate := &mockDNAGate{approved: true}
	svc.SetDNAGate(gate, "orb_break_retest")

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var setupCount int
	bus.Subscribe(context.Background(), domain.EventSetupDetected, func(ctx context.Context, ev domain.Event) error {
		setupCount++
		return nil
	})

	sym, _ := domain.NewSymbol("AAPL")

	base := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		bt := base.Add(time.Duration(i) * time.Minute)
		bar := createBarAtTime(t, sym, bt, 100, 101, 99, 100, 10)
		_ = bus.Publish(context.Background(), createTestEvent(t, bar))
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, post, 100, 101, 99, 100, 10)))
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, breakT, 100, 104, 100, 104, 50)))
	retestT := breakT.Add(time.Minute)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, retestT, 101, 104, 101, 103, 20)))

	assert.Greater(t, setupCount, 0, "DNA gate must allow SetupDetected when DNA is approved")
}

func TestService_DNAGate_ErrorIsPermissive(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc := monitor.NewService(bus, repo, zerolog.Nop())

	gate := &mockDNAGate{approved: false, err: errors.New("db down")}
	svc.SetDNAGate(gate, "orb_break_retest")

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var setupCount int
	bus.Subscribe(context.Background(), domain.EventSetupDetected, func(ctx context.Context, ev domain.Event) error {
		setupCount++
		return nil
	})

	sym, _ := domain.NewSymbol("AAPL")

	base := time.Date(2025, 3, 4, 14, 30, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		bt := base.Add(time.Duration(i) * time.Minute)
		bar := createBarAtTime(t, sym, bt, 100, 101, 99, 100, 10)
		_ = bus.Publish(context.Background(), createTestEvent(t, bar))
	}
	post := time.Date(2025, 3, 4, 15, 0, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, post, 100, 101, 99, 100, 10)))
	breakT := time.Date(2025, 3, 4, 15, 1, 0, 0, time.UTC)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, breakT, 100, 104, 100, 104, 50)))
	retestT := breakT.Add(time.Minute)
	_ = bus.Publish(context.Background(), createTestEvent(t, createBarAtTime(t, sym, retestT, 101, 104, 101, 103, 20)))

	// Error in gate check should be permissive — setup still goes through
	assert.Greater(t, setupCount, 0, "gate error must be permissive (allow setup through)")
}
