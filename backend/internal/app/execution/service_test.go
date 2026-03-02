package execution_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
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

	svc := execution.NewService(bus, broker, repo, riskEngine, slippageGuard, killSwitch, 100000.0)

	return svc, bus, broker, quoteProvider
}

func TestService_StartSubscribes(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background())
	require.NoError(t, err)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), setupEvt)
	assert.NoError(t, err)

	// Since the stub handles the event and returns immediately (or fails),
	// wait a tiny bit to ensure async processing if any, though memory bus is sync in same goroutine usually.
	time.Sleep(10 * time.Millisecond)

	// With a full implementation, the service processes the event through the pipeline.
	assert.Equal(t, 1, broker.SubmitOrderCalls)
}

func TestService_FullPipelineSuccess(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background())
	require.NoError(t, err)

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentCreated,
		domain.EventOrderIntentValidated,
		domain.EventOrderSubmitted,
	}, &emittedEvents)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), setupEvt)
	assert.NoError(t, err)

	assert.Equal(t, 1, broker.SubmitOrderCalls)
	assert.Contains(t, emittedEvents, domain.EventOrderIntentCreated)
	assert.Contains(t, emittedEvents, domain.EventOrderIntentValidated)
	assert.Contains(t, emittedEvents, domain.EventOrderSubmitted)
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
		100.0, // tiny equity → risk rejection
	)

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentRejected,
	}, &emittedEvents)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), setupEvt)
	assert.NoError(t, err)

	assert.Equal(t, 0, broker.SubmitOrderCalls)
	assert.Contains(t, emittedEvents, domain.EventOrderIntentRejected)
}

func TestService_SlippageRejection(t *testing.T) {
	svc, bus, broker, quoteProvider := setupTestService(t)
	err := svc.Start(context.Background())
	require.NoError(t, err)

	// Make quote provider return massive spread to fail slippage
	quoteProvider.Bid = 1000.0
	quoteProvider.Ask = 90000.0

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentRejected,
	}, &emittedEvents)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), setupEvt)
	assert.NoError(t, err)

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

	svc := execution.NewService(bus, broker, repo, execution.NewRiskEngine(0.02), execution.NewSlippageGuard(quoteProvider), killSwitch, 100000.0)

	err := svc.Start(context.Background())
	require.NoError(t, err)

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventKillSwitchEngaged,
	}, &emittedEvents)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), setupEvt)
	assert.NoError(t, err)

	assert.Equal(t, 0, broker.SubmitOrderCalls)
	assert.Contains(t, emittedEvents, domain.EventKillSwitchEngaged)
}

func TestService_BrokerError(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background())
	require.NoError(t, err)

	broker.SubmitOrderFunc = func(ctx context.Context, intent domain.OrderIntent) (string, error) {
		return "", errors.New("broker unavailable")
	}

	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventOrderRejected,
	}, &emittedEvents)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), setupEvt)
	assert.NoError(t, err)

	assert.Equal(t, 1, broker.SubmitOrderCalls)
	assert.Contains(t, emittedEvents, domain.EventOrderRejected)
}

func TestService_InvalidPayload(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background())
	require.NoError(t, err)

	// Publish an event with a wrong payload type
	badEvt, _ := domain.NewEvent(domain.EventSetupDetected, "tenant-1", domain.EnvModePaper, "key", "invalid-payload-string")

	err = bus.Publish(context.Background(), *badEvt)
	assert.NoError(t, err) // publish succeeds, but handler should error internally

	// We can't easily assert the internal error, but we can ensure no broker calls and no success events
	assert.Equal(t, 0, broker.SubmitOrderCalls)
}

func TestService_EmitsCircuitBreakerOnKillSwitch(t *testing.T) {
	svc, bus, broker, _ := setupTestService(t)
	err := svc.Start(context.Background())
	require.NoError(t, err)

	// Note: Circuit breaker is tripped when a trade update/stop-loss comes in that trips the switch.
	// If the service pipeline also evaluates and trips it, it should emit CircuitBreakerTripped.
	var emittedEvents []string
	subscribeAll(t, bus, []string{
		domain.EventCircuitBreakerTripped,
	}, &emittedEvents)

	// Publish multiple to trigger the halt
	setupEvt := createSetupEvent(t, domain.DirectionLong)
	_ = bus.Publish(context.Background(), setupEvt)
	_ = bus.Publish(context.Background(), setupEvt)
	_ = bus.Publish(context.Background(), setupEvt)

	// Since stubs don't do anything, it will fail to find the event in emittedEvents.
	assert.Contains(t, emittedEvents, domain.EventCircuitBreakerTripped)
	// We also don't want it to submit the 3rd order if it tripped before submission,
	// but the specific behavior depends on implementation.
	// For RED phase, this is sufficient.
	assert.Equal(t, 2, broker.SubmitOrderCalls, "Should have stopped after 2 calls before tripping on 3rd")
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
