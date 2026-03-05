package notify_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/notify"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockNotifier records all Notify calls for assertion.
type mockNotifier struct {
	mu       sync.Mutex
	messages []notifyCall
}

type notifyCall struct {
	TenantID string
	Message  string
}

func (m *mockNotifier) Notify(_ context.Context, tenantID, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, notifyCall{TenantID: tenantID, Message: message})
	return nil
}

func (m *mockNotifier) getMessages() []notifyCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]notifyCall, len(m.messages))
	copy(out, m.messages)
	return out
}

func TestService_SubscribesToOrderEvents(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc := notify.NewService(bus, notifier, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	// Publish an OrderSubmitted event
	intent := createTestOrderIntent(t)
	payload := domain.NewOrderIntentEventPayload(intent, domain.OrderIntentStatusSubmitted)
	ev, err := domain.NewEvent(domain.EventOrderSubmitted, "tenant-1", domain.EnvModePaper, "key-1", payload)
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.getMessages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "tenant-1", msgs[0].TenantID)
	assert.Contains(t, msgs[0].Message, "Order Submitted")
	assert.Contains(t, msgs[0].Message, "AAPL")
	assert.Contains(t, msgs[0].Message, "Strategy: test")
	assert.Contains(t, msgs[0].Message, "Rationale: test rationale")
	assert.Contains(t, msgs[0].Message, "80%")
}

func TestService_KillSwitchNotification(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc := notify.NewService(bus, notifier, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	ev, err := domain.NewEvent(domain.EventKillSwitchEngaged, "tenant-1", domain.EnvModePaper, "ks-1", nil)
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.getMessages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Message, "KILL SWITCH")
}

func TestService_CircuitBreakerNotification(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc := notify.NewService(bus, notifier, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	ev, err := domain.NewEvent(domain.EventCircuitBreakerTripped, "tenant-1", domain.EnvModePaper, "cb-1", "3 stops in 2 minutes")
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.getMessages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Message, "CIRCUIT BREAKER")
	assert.Contains(t, msgs[0].Message, "3 stops in 2 minutes")
}

func TestService_FillNotification(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc := notify.NewService(bus, notifier, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	fillPayload := map[string]any{
		"broker_order_id": "ord-123",
		"intent_id":       "intent-456",
		"symbol":          "AAPL",
		"side":            "buy",
		"quantity":        10.0,
		"price":           150.25,
	}
	ev, err := domain.NewEvent(domain.EventFillReceived, "tenant-1", domain.EnvModePaper, "fill-1", fillPayload)
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.getMessages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Message, "Fill")
	assert.Contains(t, msgs[0].Message, "AAPL")
	assert.Contains(t, msgs[0].Message, "150.25")
}

func TestService_IntentRejectedNotification(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc := notify.NewService(bus, notifier, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	intent := createTestOrderIntent(t)
	payload := domain.NewOrderIntentRejectedPayload(intent, "risk 850.00 exceeds maximum risk 620.00 (2.0% of 31000.00 equity)")
	ev, err := domain.NewEvent(domain.EventOrderIntentRejected, "tenant-1", domain.EnvModePaper, "rej-1", payload)
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.getMessages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Message, "Intent Rejected")
	assert.Contains(t, msgs[0].Message, "AAPL")
	assert.Contains(t, msgs[0].Message, "risk 850.00 exceeds maximum risk 620.00")
}

func TestService_DebateCompletedNotification(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc := notify.NewService(bus, notifier, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	decision := domain.AdvisoryDecision{
		Direction:      domain.DirectionLong,
		Confidence:     0.85,
		Rationale:      "Strong momentum with volume confirmation",
		BullArgument:   "RSI shows oversold bounce with increasing volume",
		BearArgument:   "Resistance at 160 could cap upside",
		JudgeReasoning: "Momentum outweighs resistance risk — go long",
	}
	ev, err := domain.NewEvent(domain.EventDebateCompleted, "tenant-1", domain.EnvModePaper, "debate-1", decision)
	require.NoError(t, err)

	err = bus.Publish(context.Background(), *ev)
	require.NoError(t, err)

	msgs := notifier.getMessages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Message, "AI Debate")
	assert.Contains(t, msgs[0].Message, "LONG")
	assert.Contains(t, msgs[0].Message, "85%")
	assert.Contains(t, msgs[0].Message, "Bull: RSI shows oversold bounce")
	assert.Contains(t, msgs[0].Message, "Bear: Resistance at 160")
	assert.Contains(t, msgs[0].Message, "Judge: Momentum outweighs resistance")
}

func TestService_MultipleEventsNotifyAll(t *testing.T) {
	bus := memory.NewBus()
	notifier := &mockNotifier{}
	svc := notify.NewService(bus, notifier, zerolog.Nop())

	err := svc.Start(context.Background())
	require.NoError(t, err)

	// Fire multiple event types
	events := []struct {
		eventType string
		payload   any
	}{
		{domain.EventKillSwitchEngaged, nil},
		{domain.EventCircuitBreakerTripped, "test"},
		{domain.EventDebateCompleted, nil},
	}

	for i, e := range events {
		ev, err := domain.NewEvent(e.eventType, "tenant-1", domain.EnvModePaper, fmt.Sprintf("key-%d", i), e.payload)
		require.NoError(t, err)
		err = bus.Publish(context.Background(), *ev)
		require.NoError(t, err)
	}

	msgs := notifier.getMessages()
	assert.Len(t, msgs, 3, "should receive notifications for all 3 event types")
}

func createTestOrderIntent(t *testing.T) domain.OrderIntent {
	t.Helper()
	intent, err := domain.NewOrderIntent(
		uuid.New(),
		"tenant-1",
		domain.EnvModePaper,
		"AAPL",
		domain.DirectionLong,
		150.0, // limit price
		145.0, // stop loss
		10,    // max slippage bps
		10.0,  // quantity
		"test",
		"test rationale",
		0.8,
		"idempotency-key-1",
	)
	require.NoError(t, err)
	return intent
}
