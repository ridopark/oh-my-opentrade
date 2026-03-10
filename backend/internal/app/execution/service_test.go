package execution_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestService(t *testing.T) (*execution.Service, *memory.Bus, *mockBroker, *mockQuoteProvider) {
	bus := memory.NewBus()
	broker := &mockBroker{}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{
		Bid: 49950.0,
		Ask: 50050.0,
	}

	riskEngine := execution.NewRiskEngine(0.02)
	slippageGuard := execution.NewSlippageGuard(quoteProvider)

	nowFunc := func() time.Time { return time.Now() }
	killSwitch := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc)

	svc := execution.NewService(bus, broker, repo, riskEngine, slippageGuard, killSwitch, nil, 100000.0, zerolog.Nop())

	return svc, bus, broker, quoteProvider
}

func TestService_StartSubscribes(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), intentEvt)
	assert.NoError(t, err)

	bus.Flush()
	assert.Equal(t, 1, broker.SubmitOrderCalls)
}

func TestService_FullPipelineSuccess(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentValidated,
		domain.EventOrderSubmitted,
	}, &emittedEvents)

	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), intentEvt)
	assert.NoError(t, err)
	bus.Flush()

	assert.Equal(t, 1, broker.SubmitOrderCalls)
	assert.Contains(t, emittedEvents, domain.EventOrderIntentValidated)
	assert.Contains(t, emittedEvents, domain.EventOrderSubmitted)
}

func TestService_ExitOrderPassesPipeline(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	// Mock broker returns an existing BTCUSD long position so exit can resolve quantity.
	broker.GetPositionsFunc = func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
		trade, _ := domain.NewTrade(
			time.Now(), tenantID, envMode, uuid.New(),
			"BTCUSD", "long", 1.0, 50000, 0, "FILLED", "strategy-1", "test",
		)
		return []domain.Trade{trade}, nil
	}
	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentValidated,
		domain.EventOrderSubmitted,
	}, &emittedEvents)

	// Publish an exit order (DirectionCloseLong).
	// This was previously rejected when exits used DirectionShort.
	intentEvt := createExitOrderIntentEvent(t, domain.DirectionCloseLong)
	err = bus.Publish(context.Background(), intentEvt)
	assert.NoError(t, err)
	bus.Flush()

	assert.Equal(t, 1, broker.SubmitOrderCalls, "exit order should reach broker")
	assert.Contains(t, emittedEvents, domain.EventOrderIntentValidated)
	assert.Contains(t, emittedEvents, domain.EventOrderSubmitted)
}

func TestService_NewShortEntryStillRejected(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentRejected,
	}, &emittedEvents)

	// Publish a new short entry (DirectionShort).
	// This should still be rejected by the SHORT direction guard.
	intentEvt := createOrderIntentEvent(t, domain.DirectionShort)
	err = bus.Publish(context.Background(), intentEvt)
	assert.NoError(t, err)
	bus.Flush()

	assert.Equal(t, 0, broker.SubmitOrderCalls, "new short entry should be rejected")
	assert.Contains(t, emittedEvents, domain.EventOrderIntentRejected)
}

func TestService_RiskRejection(t *testing.T) {
	// Create a service with very low equity so risk always exceeds 2%.
	// With LimitPrice=50000, StopLoss=49000, Qty=1: risk = 1000.
	// 2% of 100 = 2. 1000 > 2 → rejected.
	bus := memory.NewBus()
	broker := &mockBroker{}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{Bid: 49950.0, Ask: 50050.0}

	nowFunc := func() time.Time { return time.Now() }
	svc := execution.NewService(
		bus, broker, repo,
		execution.NewRiskEngine(0.02),
		execution.NewSlippageGuard(quoteProvider),
		execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc),
		nil,   // dailyLossBreaker
		100.0, // tiny equity → risk rejection
		zerolog.Nop(),
	)

	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentRejected,
	}, &emittedEvents)

	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), intentEvt)
	assert.NoError(t, err)
	bus.Flush()

	assert.Equal(t, 0, broker.SubmitOrderCalls)
	assert.Contains(t, emittedEvents, domain.EventOrderIntentRejected)
}

func TestService_SlippageRejection(t *testing.T) {
	svc, bus, broker, quoteProvider := setupTestService(t)
	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	// Make quote provider return massive spread to fail slippage
	quoteProvider.Bid = 1000.0
	quoteProvider.Ask = 90000.0

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentRejected,
	}, &emittedEvents)

	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), intentEvt)
	assert.NoError(t, err)
	bus.Flush()

	assert.Equal(t, 0, broker.SubmitOrderCalls)
	assert.Contains(t, emittedEvents, domain.EventOrderIntentRejected)
}

func TestService_KillSwitchHalted(t *testing.T) {
	// Re-create service with a killswitch that's already halted
	bus := memory.NewBus()
	broker := &mockBroker{}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{Bid: 49950.0, Ask: 50050.0}

	nowFunc := func() time.Time { return time.Now() }
	killSwitch := execution.NewKillSwitch(1, 2*time.Minute, 15*time.Minute, nowFunc)
	_ = killSwitch.RecordStop("tenant-1", "BTCUSD") // trip it immediately

	svc := execution.NewService(bus, broker, repo, execution.NewRiskEngine(0.02), execution.NewSlippageGuard(quoteProvider), killSwitch, nil, 100000.0, zerolog.Nop())

	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventKillSwitchEngaged,
	}, &emittedEvents)

	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), intentEvt)
	assert.NoError(t, err)
	bus.Flush()

	assert.Equal(t, 0, broker.SubmitOrderCalls)
	assert.Contains(t, emittedEvents, domain.EventKillSwitchEngaged)
}

func TestService_KillSwitchDoesNotBlockExitIntents(t *testing.T) {
	bus := memory.NewBus()
	broker := &mockBroker{
		GetPositionsFunc: func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
			trade, _ := domain.NewTrade(
				time.Now(), tenantID, envMode, uuid.New(),
				"BTCUSD", "long", 1.0, 50000, 0, "FILLED", "strategy-1", "test",
			)
			return []domain.Trade{trade}, nil
		},
	}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{Bid: 49950.0, Ask: 50050.0}

	nowFunc := func() time.Time { return time.Now() }
	killSwitch := execution.NewKillSwitch(1, 2*time.Minute, 15*time.Minute, nowFunc)
	_ = killSwitch.RecordStop("tenant-1", "BTCUSD") // trip it immediately

	svc := execution.NewService(bus, broker, repo,
		execution.NewRiskEngine(0.02),
		execution.NewSlippageGuard(quoteProvider),
		killSwitch, nil, 100000.0, zerolog.Nop(),
	)

	require.NoError(t, svc.Start(context.Background(), "test", domain.EnvModePaper))

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderSubmitted,
		domain.EventKillSwitchEngaged,
	}, &emittedEvents)

	exitEvt := createExitOrderIntentEvent(t, domain.DirectionCloseLong)
	require.NoError(t, bus.Publish(context.Background(), exitEvt))
	bus.Flush()

	assert.Equal(t, 1, broker.SubmitOrderCalls, "exit should reach broker even with kill switch tripped")
	assert.Contains(t, emittedEvents, domain.EventOrderSubmitted)
	assert.NotContains(t, emittedEvents, domain.EventKillSwitchEngaged)
}

func TestService_ExitIntentsDoNotTripKillSwitch(t *testing.T) {
	bus := memory.NewBus()
	broker := &mockBroker{
		GetPositionsFunc: func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
			trade, _ := domain.NewTrade(
				time.Now(), tenantID, envMode, uuid.New(),
				"BTCUSD", "long", 1.0, 50000, 0, "FILLED", "strategy-1", "test",
			)
			return []domain.Trade{trade}, nil
		},
	}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{Bid: 49950.0, Ask: 50050.0}

	nowFunc := func() time.Time { return time.Now() }
	killSwitch := execution.NewKillSwitch(1, 2*time.Minute, 15*time.Minute, nowFunc)

	svc := execution.NewService(bus, broker, repo,
		execution.NewRiskEngine(0.02),
		execution.NewSlippageGuard(quoteProvider),
		killSwitch, nil, 100000.0, zerolog.Nop(),
	)

	require.NoError(t, svc.Start(context.Background(), "test", domain.EnvModePaper))

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventCircuitBreakerTripped,
		domain.EventOrderSubmitted,
	}, &emittedEvents)

	// Submit 3 exit intents — none should trip the kill switch (maxStops=1).
	for i := 0; i < 3; i++ {
		exitEvt := createExitOrderIntentEvent(t, domain.DirectionCloseLong)
		require.NoError(t, bus.Publish(context.Background(), exitEvt))
	}
	bus.Flush()

	assert.NotContains(t, emittedEvents, domain.EventCircuitBreakerTripped)
	assert.False(t, killSwitch.IsHalted("tenant-1", "BTCUSD"), "exits should not trip the kill switch")
}

func TestService_BrokerError(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	broker.SubmitOrderFunc = func(ctx context.Context, intent domain.OrderIntent) (string, error) {
		return "", errors.New("broker unavailable")
	}

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderRejected,
	}, &emittedEvents)

	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), intentEvt)
	assert.NoError(t, err)
	bus.Flush()

	assert.Equal(t, 1, broker.SubmitOrderCalls)
	assert.Contains(t, emittedEvents, domain.EventOrderRejected)
}

func TestService_InvalidPayload(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	// Publish an event with a wrong payload type
	badEvt, _ := domain.NewEvent(domain.EventOrderIntentCreated, "tenant-1", domain.EnvModePaper, "key", "invalid-payload-string")

	err = bus.Publish(context.Background(), *badEvt)
	assert.NoError(t, err)
	bus.Flush()

	assert.Equal(t, 0, broker.SubmitOrderCalls)
}

func TestService_EmitsCircuitBreakerOnKillSwitch(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background(), "test", domain.EnvModePaper)
	require.NoError(t, err)

	// Note: Circuit breaker is tripped when RecordStop is called for the Nth time.
	// KillSwitch is configured with maxStops=3, so 3rd RecordStop trips it.
	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventCircuitBreakerTripped,
	}, &emittedEvents)

	// Publish 3 intents: first 2 succeed (RecordStop ok), 3rd trips circuit breaker
	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	_ = bus.Publish(context.Background(), intentEvt)
	bus.Flush()
	_ = bus.Publish(context.Background(), intentEvt)
	bus.Flush()
	_ = bus.Publish(context.Background(), intentEvt)
	bus.Flush()

	assert.Contains(t, emittedEvents, domain.EventCircuitBreakerTripped)
	assert.Equal(t, 2, broker.SubmitOrderCalls, "Should have stopped after 2 calls before tripping on 3rd")
}

func TestService_DynamicMetricLabels(t *testing.T) {
	bus := memory.NewBus()
	broker := &mockBroker{}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{Bid: 49950.0, Ask: 50050.0}

	riskEngine := execution.NewRiskEngine(0.02)
	slippageGuard := execution.NewSlippageGuard(quoteProvider)
	nowFunc := func() time.Time { return time.Now() }
	killSwitch := execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc)

	svc := execution.NewService(bus, broker, repo, riskEngine, slippageGuard, killSwitch, nil, 100000.0, zerolog.Nop())
	m := metrics.New("test", "test", "test", false)
	svc.SetMetrics(m)

	require.NoError(t, svc.Start(context.Background(), "test", domain.EnvModePaper))

	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), intentEvt))
	bus.Flush()

	assert.Equal(t, 1, broker.SubmitOrderCalls)

	placed := counterValue(t, m.Reg, "omo_orders_total", map[string]string{
		"venue":      "alpaca",
		"strategy":   "strategy-1",
		"side":       "buy",
		"order_type": "limit",
		"result":     "placed",
	})
	assert.Equal(t, float64(1), placed)

	obs := histogramSampleCount(t, m.Reg, "omo_order_submit_latency_seconds", map[string]string{
		"venue":      "alpaca",
		"strategy":   "strategy-1",
		"order_type": "limit",
	})
	require.Equal(t, uint64(1), obs)
}

func histogramSampleCount(t *testing.T, reg *prometheus.Registry, metricName string, wantLabels map[string]string) uint64 {
	t.Helper()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m.GetLabel(), wantLabels) {
				h := m.GetHistogram()
				require.NotNil(t, h)
				return h.GetSampleCount()
			}
		}
	}

	require.FailNow(t, "histogram metric not found", "%s labels=%v", metricName, wantLabels)
	return 0
}

func counterValue(t *testing.T, reg *prometheus.Registry, metricName string, wantLabels map[string]string) float64 {
	t.Helper()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m.GetLabel(), wantLabels) {
				c := m.GetCounter()
				require.NotNil(t, c)
				return c.GetValue()
			}
		}
	}

	require.FailNow(t, "counter metric not found", "%s labels=%v", metricName, wantLabels)
	return 0
}

func labelsMatch(got []*dto.LabelPair, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}

	gotMap := make(map[string]string, len(got))
	for _, lp := range got {
		gotMap[lp.GetName()] = lp.GetValue()
	}
	for k, v := range want {
		if gotMap[k] != v {
			return false
		}
	}
	return true
}

func subscribeAll(t *testing.T, bus *memory.Bus, events []string, dest *[]string) {
	for _, ev := range events {
		err := bus.Subscribe(context.Background(), ev, func(ctx context.Context, event domain.Event) error {
			*dest = append(*dest, event.Type)
			return nil
		})
		require.NoError(t, err)
	}
}

// ---------- Position gate integration tests ----------

func TestService_PositionGate_RejectsDuplicateEntry(t *testing.T) {
	bus := memory.NewBus()
	broker := &mockBroker{
		GetPositionsFunc: func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
			// Simulate an existing long position in BTCUSD.
			trade, _ := domain.NewTrade(time.Now(), tenantID, envMode, [16]byte{}, "BTCUSD", "BUY", 1.0, 50000, 0, "FILLED", "strategy-1", "test")
			return []domain.Trade{trade}, nil
		},
	}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{Bid: 49950.0, Ask: 50050.0}

	nowFunc := func() time.Time { return time.Now() }
	posGate := execution.NewPositionGate(broker, zerolog.Nop())
	svc := execution.NewService(bus, broker, repo,
		execution.NewRiskEngine(0.02),
		execution.NewSlippageGuard(quoteProvider),
		execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc),
		nil,
		100000.0,
		zerolog.Nop(),
		execution.WithPositionGate(posGate),
	)

	require.NoError(t, svc.Start(context.Background(), "test", domain.EnvModePaper))

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentRejected,
	}, &emittedEvents)

	// Try to enter LONG on BTCUSD when we already have a long position.
	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), intentEvt))
	bus.Flush()

	assert.Equal(t, 0, broker.SubmitOrderCalls, "should not submit when position already exists")
	assert.Contains(t, emittedEvents, domain.EventOrderIntentRejected)
}

func TestService_PositionGate_AllowsEntryWhenFlat(t *testing.T) {
	bus := memory.NewBus()
	broker := &mockBroker{
		GetPositionsFunc: func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{}, nil // no positions
		},
	}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{Bid: 49950.0, Ask: 50050.0}

	nowFunc := func() time.Time { return time.Now() }
	posGate := execution.NewPositionGate(broker, zerolog.Nop())
	svc := execution.NewService(bus, broker, repo,
		execution.NewRiskEngine(0.02),
		execution.NewSlippageGuard(quoteProvider),
		execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc),
		nil,
		100000.0,
		zerolog.Nop(),
		execution.WithPositionGate(posGate),
	)

	require.NoError(t, svc.Start(context.Background(), "test", domain.EnvModePaper))

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentValidated,
		domain.EventOrderSubmitted,
	}, &emittedEvents)

	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), intentEvt))
	bus.Flush()

	assert.Equal(t, 1, broker.SubmitOrderCalls, "should submit when flat")
	assert.Contains(t, emittedEvents, domain.EventOrderSubmitted)
}

func TestService_PositionGate_InflightBlocksSecondEntry(t *testing.T) {
	bus := memory.NewBus()
	// GetPositions returns flat (no positions) so the gate won't reject on position grounds.
	// GetOrderStatus blocks forever so pollForFill never clears inflight.
	broker := &mockBroker{
		GetPositionsFunc: func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{}, nil
		},
		GetOrderStatusFunc: func(ctx context.Context, orderID string) (string, error) {
			// Block forever — simulate slow fill so inflight is never cleared.
			select {}
		},
	}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{Bid: 49950.0, Ask: 50050.0}

	nowFunc := func() time.Time { return time.Now() }
	posGate := execution.NewPositionGate(broker, zerolog.Nop())
	svc := execution.NewService(bus, broker, repo,
		execution.NewRiskEngine(0.02),
		execution.NewSlippageGuard(quoteProvider),
		execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc),
		nil,
		100000.0,
		zerolog.Nop(),
		execution.WithPositionGate(posGate),
	)

	require.NoError(t, svc.Start(context.Background(), "test", domain.EnvModePaper))

	var rejectedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentRejected,
	}, &rejectedEvents)

	// First intent: flat + no inflight → should be submitted and mark inflight.
	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), intentEvt))
	bus.Flush()
	assert.Equal(t, 1, broker.SubmitOrderCalls, "first entry should be submitted")

	time.Sleep(20 * time.Millisecond)

	intentEvt2 := createOrderIntentEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), intentEvt2))
	bus.Flush()
	assert.Equal(t, 1, broker.SubmitOrderCalls, "second entry should be blocked by inflight lock")
	assert.Contains(t, rejectedEvents, domain.EventOrderIntentRejected)
}

func TestService_PositionGate_InflightClearedAfterFill(t *testing.T) {
	bus := memory.NewBus()
	fillCh := make(chan struct{})
	broker := &mockBroker{
		GetPositionsFunc: func(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
			return []domain.Trade{}, nil
		},
		GetOrderStatusFunc: func(ctx context.Context, orderID string) (string, error) {
			return "filled", nil
		},
	}
	repo := &mockRepository{}
	quoteProvider := &mockQuoteProvider{Bid: 49950.0, Ask: 50050.0}

	nowFunc := func() time.Time { return time.Now() }
	posGate := execution.NewPositionGate(broker, zerolog.Nop())
	svc := execution.NewService(bus, broker, repo,
		execution.NewRiskEngine(0.02),
		execution.NewSlippageGuard(quoteProvider),
		execution.NewKillSwitch(3, 2*time.Minute, 15*time.Minute, nowFunc),
		nil,
		100000.0,
		zerolog.Nop(),
		execution.WithPositionGate(posGate),
	)

	require.NoError(t, svc.Start(context.Background(), "test", domain.EnvModePaper))

	// Subscribe to FillReceived so we know when the fill is processed.
	_ = bus.Subscribe(context.Background(), domain.EventFillReceived, func(ctx context.Context, event domain.Event) error {
		close(fillCh)
		return nil
	})

	// First entry — submitted, inflight marked, then pollForFill returns "filled" immediately.
	intentEvt := createOrderIntentEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), intentEvt))
	bus.Flush()
	assert.Equal(t, 1, broker.SubmitOrderCalls)

	// Wait for the fill event (pollForFill runs in a goroutine with 5s ticker, but
	// first tick is immediate after 5s — we need to wait for it).
	// Actually pollForFill uses time.NewTicker(5s) so first tick is after 5s.
	// That's too slow for a test. We'll wait up to 10 seconds.
	select {
	case <-fillCh:
		// Fill processed, inflight should be cleared.
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for fill event")
	}

	// Now that inflight is cleared, a second entry for the same symbol should succeed.
	// (GetPositions returns flat, so no position-based rejection either.)
	intentEvt2 := createOrderIntentEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), intentEvt2))
	bus.Flush()
	assert.Equal(t, 2, broker.SubmitOrderCalls, "second entry should be allowed after fill clears inflight")
}
