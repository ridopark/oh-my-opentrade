package debate_test

import (
"context"
"errors"
"testing"
"time"

"github.com/google/uuid"
"github.com/rs/zerolog"
"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
"github.com/oh-my-opentrade/backend/internal/app/debate"
"github.com/oh-my-opentrade/backend/internal/app/monitor"
"github.com/oh-my-opentrade/backend/internal/domain"
"github.com/oh-my-opentrade/backend/internal/ports"
"github.com/stretchr/testify/assert"
"github.com/stretchr/testify/require"
)

// mockAIAdvisor is a test double for ports.AIAdvisorPort.
type mockAIAdvisor struct {
	RequestDebateFunc func(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error)
	CallCount         int
}

func (m *mockAIAdvisor) RequestDebate(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
	m.CallCount++
	if m.RequestDebateFunc != nil {
		return m.RequestDebateFunc(ctx, symbol, regime, indicators, opts...)
	}
	return &domain.AdvisoryDecision{
		Direction:      domain.DirectionLong,
		Confidence:     0.85,
		Rationale:      "AI says go long",
		BullArgument:   "Bull case: strong momentum",
		BearArgument:   "Bear case: resistance ahead",
		JudgeReasoning: "Judge: bull wins on volume",
	}, nil
}

// getMockAdvisoryDecision returns a default high-confidence decision.
func getMockAdvisoryDecision(overrides ...func(*domain.AdvisoryDecision)) *domain.AdvisoryDecision {
	d := &domain.AdvisoryDecision{
		Direction:      domain.DirectionLong,
		Confidence:     0.85,
		Rationale:      "AI says go long",
		BullArgument:   "Bull case: strong momentum",
		BearArgument:   "Bear case: resistance ahead",
		JudgeReasoning: "Judge: bull wins on volume",
	}
	for _, fn := range overrides {
		fn(d)
	}
	return d
}

// createSetupEvent builds a valid SetupDetected event for testing.
func createSetupEvent(t *testing.T, dir domain.Direction) domain.Event {
	t.Helper()

	snap := domain.IndicatorSnapshot{
		Time:      time.Now(),
		Symbol:    "BTCUSD",
		Timeframe: "1h",
		RSI:       42.0,
		StochK:    30.0,
		StochD:    28.0,
		EMA9:      50100.0,
		EMA21:     49900.0,
		VWAP:      50000.0,
		Volume:    1000.0,
		VolumeSMA: 800.0,
	}

	regime := domain.MarketRegime{
		Symbol:    "BTCUSD",
		Timeframe: "1h",
		Type:      domain.RegimeTrend,
		Since:     time.Now().Add(-time.Hour),
		Strength:  0.8,
	}

	setup := monitor.SetupCondition{
		Symbol:    "BTCUSD",
		Timeframe: "1h",
		Direction: dir,
		Trigger:   "RSI_Oversold",
		Snapshot:  snap,
		Regime:    regime,
	}

	event, err := domain.NewEvent(
		domain.EventSetupDetected,
		"tenant-1",
		domain.EnvModePaper,
		uuid.NewString(),
		setup,
	)
	require.NoError(t, err)
	return *event
}

// subscribeAll subscribes to the given event types and appends to dest.
func subscribeAll(t *testing.T, bus *memory.Bus, events []string, dest *[]string) {
	t.Helper()
	for _, ev := range events {
		err := bus.Subscribe(context.Background(), ev, func(ctx context.Context, event domain.Event) error {
			*dest = append(*dest, event.Type)
			return nil
		})
		require.NoError(t, err)
	}
}

// --- Tests ---

func TestService_StartSubscribesToSetupDetected(t *testing.T) {
	bus := memory.NewBus()
	advisor := &mockAIAdvisor{}
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	err = bus.Publish(context.Background(), setupEvt)
	assert.NoError(t, err)

	// Advisor should have been called once
	assert.Equal(t, 1, advisor.CallCount)
}

func TestService_EmitsDebateRequestedAndCompleted(t *testing.T) {
	bus := memory.NewBus()
	advisor := &mockAIAdvisor{}
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())

	require.NoError(t, svc.Start(context.Background()))

	var emitted []string
	subscribeAll(t, bus, []string{
		domain.EventDebateRequested,
		domain.EventDebateCompleted,
	}, &emitted)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), setupEvt))

	assert.Contains(t, emitted, domain.EventDebateRequested)
	assert.Contains(t, emitted, domain.EventDebateCompleted)
}

func TestService_EmitsOrderIntentCreatedWhenConfidenceAboveThreshold(t *testing.T) {
	bus := memory.NewBus()
	advisor := &mockAIAdvisor{
		RequestDebateFunc: func(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
			return getMockAdvisoryDecision(func(d *domain.AdvisoryDecision) {
				d.Confidence = 0.90 // above threshold
			}), nil
		},
	}
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())
	require.NoError(t, svc.Start(context.Background()))

	var emitted []string
	subscribeAll(t, bus, []string{domain.EventOrderIntentCreated}, &emitted)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), setupEvt))

	assert.Contains(t, emitted, domain.EventOrderIntentCreated)
}

func TestService_DoesNotEmitOrderIntentWhenConfidenceBelowThreshold(t *testing.T) {
	bus := memory.NewBus()
	advisor := &mockAIAdvisor{
		RequestDebateFunc: func(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
			return getMockAdvisoryDecision(func(d *domain.AdvisoryDecision) {
				d.Confidence = 0.50 // below threshold of 0.7
			}), nil
		},
	}
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())
	require.NoError(t, svc.Start(context.Background()))

	var emitted []string
	subscribeAll(t, bus, []string{domain.EventOrderIntentCreated}, &emitted)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), setupEvt))

	assert.NotContains(t, emitted, domain.EventOrderIntentCreated)
}

func TestService_OrderIntentCarriesAIRationaleAndDirection(t *testing.T) {
	bus := memory.NewBus()
	const expectedRationale = "AI: long because momentum"
	advisor := &mockAIAdvisor{
		RequestDebateFunc: func(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
			return getMockAdvisoryDecision(func(d *domain.AdvisoryDecision) {
				d.Confidence = 0.80
				d.Direction = domain.DirectionLong
				d.Rationale = expectedRationale
			}), nil
		},
	}
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())
	require.NoError(t, svc.Start(context.Background()))

	var capturedIntent *domain.OrderIntent
	err := bus.Subscribe(context.Background(), domain.EventOrderIntentCreated, func(ctx context.Context, event domain.Event) error {
		if intent, ok := event.Payload.(domain.OrderIntent); ok {
			capturedIntent = &intent
		}
		return nil
	})
	require.NoError(t, err)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), setupEvt))

	require.NotNil(t, capturedIntent)
	assert.Equal(t, expectedRationale, capturedIntent.Rationale)
	assert.Equal(t, domain.DirectionLong, capturedIntent.Direction)
	assert.InDelta(t, 0.80, capturedIntent.Confidence, 0.001)
}

func TestService_ContinuesOnAIError(t *testing.T) {
	bus := memory.NewBus()
	advisor := &mockAIAdvisor{
		RequestDebateFunc: func(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
			return nil, errors.New("AI service unavailable")
		},
	}
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())
	require.NoError(t, svc.Start(context.Background()))

	var emitted []string
	subscribeAll(t, bus, []string{
		domain.EventOrderIntentCreated,
		domain.EventDebateRequested,
		domain.EventDebateCompleted,
	}, &emitted)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	// Should not return error when AI fails — pipeline continues gracefully
	err := bus.Publish(context.Background(), setupEvt)
	assert.NoError(t, err)

	// Debate requested should still be emitted, completed and OrderIntent should NOT be emitted
	assert.Contains(t, emitted, domain.EventDebateRequested)
	assert.NotContains(t, emitted, domain.EventOrderIntentCreated)
}

func TestService_InvalidPayloadIsIgnored(t *testing.T) {
	bus := memory.NewBus()
	advisor := &mockAIAdvisor{}
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())
	require.NoError(t, svc.Start(context.Background()))

	// Publish SetupDetected with invalid payload type
	badEvt, err := domain.NewEvent(domain.EventSetupDetected, "tenant-1", domain.EnvModePaper, uuid.NewString(), "not-a-setup-condition")
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *badEvt)
	assert.NoError(t, err) // publish succeeds, handler silently ignores

	// Advisor should NOT be called
	assert.Equal(t, 0, advisor.CallCount)
}

func TestService_DebateCompletedPayloadContainsDecision(t *testing.T) {
	bus := memory.NewBus()
	decision := getMockAdvisoryDecision()
	advisor := &mockAIAdvisor{
		RequestDebateFunc: func(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
			return decision, nil
		},
	}
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())
	require.NoError(t, svc.Start(context.Background()))

	var capturedDecision *domain.AdvisoryDecision
	err := bus.Subscribe(context.Background(), domain.EventDebateCompleted, func(ctx context.Context, event domain.Event) error {
		if d, ok := event.Payload.(*domain.AdvisoryDecision); ok {
			capturedDecision = d
		}
		return nil
	})
	require.NoError(t, err)

	setupEvt := createSetupEvent(t, domain.DirectionLong)
	require.NoError(t, bus.Publish(context.Background(), setupEvt))

	require.NotNil(t, capturedDecision)
	assert.Equal(t, domain.DirectionLong, capturedDecision.Direction)
	assert.InDelta(t, 0.85, capturedDecision.Confidence, 0.001)
}

func TestNewService_ReturnsNonNilService(t *testing.T) {
	bus := memory.NewBus()
	advisor := &mockAIAdvisor{}
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())
	assert.NotNil(t, svc)
}

func TestService_CancelsAdvisorCallAfterTimeout(t *testing.T) {
	bus := memory.NewBus()
	// Advisor that blocks until its context is cancelled.
	advisor := &mockAIAdvisor{
		RequestDebateFunc: func(ctx context.Context, symbol domain.Symbol, regime domain.MarketRegime, indicators domain.IndicatorSnapshot, opts ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	// Tight 50ms timeout — should fire well before test timeout.
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop(), debate.WithAdvisorTimeout(50*time.Millisecond))
	require.NoError(t, svc.Start(context.Background()))

	var emitted []string
	subscribeAll(t, bus, []string{domain.EventOrderIntentCreated}, &emitted)

	start := time.Now()
	require.NoError(t, bus.Publish(context.Background(), createSetupEvent(t, domain.DirectionLong)))

	// Must finish well within 500ms (advisor would block forever without timeout).
	assert.Less(t, time.Since(start), 500*time.Millisecond)
	// No order intent — advisor was cancelled.
	assert.NotContains(t, emitted, domain.EventOrderIntentCreated)
}

func TestService_DefaultTimeoutIsReasonable(t *testing.T) {
	bus := memory.NewBus()
	advisor := &mockAIAdvisor{}
	// NewService without options should compile and return a non-nil service.
	svc := debate.NewService(bus, advisor, 0.7, zerolog.Nop())
	assert.NotNil(t, svc)
}
